// Package refresh orchestrates one refresh cycle: it takes the refresh lease, records a run, asks
// every configured source for its current state, stores what came back, and reports what happened.
//
// zorgscope runs on a scale-to-zero Machine, so there is no scheduler and no process between
// requests: an external cron service calls POST /api/refresh, and that request is the only thing
// that ever writes upstream data. Runner is what that request drives.
//
// Two rules run through the whole package:
//
//   - A partial result is never stored. A fetcher may return the items it did get together with a
//     non-nil error (the GitHub adapter does exactly that, QS-1.4); Store.ReplaceItems deletes the
//     rows the incoming set omits, so storing a partial result would delete the failed
//     repository's items and light them up as NEW when it recovered. On any fetch error the run
//     records the error and leaves the stored data alone, which is also what FR-1.4 AC3 asks for:
//     a failing source shows its last-known items and an error.
//   - One failing source never stops the others (FR-5.1 AC4). Run returns a non-nil error only
//     when the lease or the run record fails; a source failure travels in Report.OK and the
//     per-source Err.
//
// Everything that needs the current time takes it from the injected ports.Clock; nothing here
// calls time.Now.
package refresh

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gernotstarke/zorgscope/internal/domain"
	"github.com/gernotstarke/zorgscope/internal/ports"
)

// ErrBusy is returned by Run when another refresh already holds the lease (QS-1.7). A caller
// answering an HTTP request maps it to 409 rather than to a failure.
var ErrBusy = errors.New("a refresh is already running")

// leaseTTL is how long a refresh holds the lease. It is far longer than QS-2.5's 30-second refresh
// budget, so a healthy run never has its lease expire underneath it, and short enough that a
// Machine killed mid-run unblocks the next cron trigger instead of locking refreshes out.
const leaseTTL = 5 * time.Minute

// Error and detail texts are stored and rendered, so they are clipped to sane lengths: an upstream
// that answers with a page of HTML must not fill the database or the dashboard.
const (
	maxErrLen    = 500
	maxDetailLen = 2000
)

// cleanupTimeout bounds each of the writes that close a run out — releasing the lease, finishing
// the run record, recording a source error. They are single small statements; five seconds is
// generous for one of them and short enough that all of them together stay well inside what a
// caller can afford to wait for after its own budget is gone.
const cleanupTimeout = 5 * time.Second

// notifyBudget bounds the whole announcement step. It is a third of QS-2.5's 30-second refresh
// budget: enough for the handful of messages a personal dashboard produces in a run, and little
// enough that a webhook which never answers costs the run a delay rather than the Machine an open
// request it cannot be stopped with.
const notifyBudget = 10 * time.Second

// cleanupCtx derives the context the run's closing writes use. Both halves are load-bearing and
// neither may be dropped:
//
//   - context.WithoutCancel, because a run that was cancelled or timed out must still free its
//     lease, close its run record and say why the remaining sources have no fresh data. On the
//     run's own context every one of those writes would fail, and a single cancellation would
//     lock refreshes out for the whole leaseTTL and leave the run "running" forever.
//   - context.WithTimeout, because WithoutCancel strips the deadline along with the cancellation.
//     Without it, the only writes that run after the time budget is blown are the ones that can
//     then block indefinitely: a half-open Turso would keep the POST /api/refresh request in
//     flight, and Fly does not stop a Machine with a request in flight — the run capped at 30
//     seconds would hold the Machine awake for as long as the database stayed unresponsive, which
//     is the half of QS-2.5 that says nothing may hold the Machine awake.
//
// The caller must call the returned cancel, as with any context.WithTimeout.
func cleanupCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
}

// SourceReport is what one source contributed to a run.
type SourceReport struct {
	// Source is the fetcher's name, e.g. "github" or "todoist".
	Source string
	// Stored is how many items the source now holds — the count ReplaceItems returned, which is
	// len(items), not the number that are new. It is zero when the source failed.
	Stored int
	// Err is the source's failure message, empty when the source succeeded.
	Err string
}

// Report is the outcome of one refresh run, both for the caller of Run and for the run record.
type Report struct {
	// RunID identifies the refresh_run row this report describes.
	RunID int64
	// StartedAt and EndedAt bracket the run, both read from the Runner's clock.
	StartedAt, EndedAt time.Time
	// Trigger is what asked for the refresh, e.g. "cron" or "user".
	Trigger string
	// OK is true only when every source succeeded.
	OK bool
	// Sources holds one entry per configured fetcher, in the order they ran.
	Sources []SourceReport
}

// Runner performs refresh runs. Its fields are set once by New and never mutated, so a single
// Runner is safe to use from several goroutines; concurrent runs exclude each other through the
// store's refresh lease, not through the Runner.
type Runner struct {
	store    ports.Store
	fetchers []ports.SourceFetcher
	clock    ports.Clock
	notifier ports.Notifier
	log      *slog.Logger
}

// New builds a Runner over the given store, fetchers and clock. The notifier may be nil, in which
// case nothing is announced; log may be nil, in which case log output is discarded.
//
// The fetchers are used in the order given and are identified by their own Name; no source name is
// hard-coded here.
//
// No two fetchers may share a Name, and New panics if two do. This is a precondition, not a
// preference, and it protects the first-seen invariant: a source owns its rows, and ReplaceItems
// deletes the rows the incoming set omits. A fetcher that returns only builds or only metrics
// therefore calls ReplaceItems with no items, which deletes every item of that source name. Give
// GitHub's build fetcher the name "github" instead of "github-builds" and each refresh deletes
// every GitHub issue and pull request; the issue fetcher re-inserts them on the next run with a
// fresh FirstSeenAt, so the whole dashboard lights up NEW on every single refresh (QS-1.2).
//
// It is a panic rather than an error because it can only be violated by a code change where the
// sources are wired, is fully determined at that point, and is caught on the first startup — and
// because a Runner built over a colliding set has no safe behaviour to fall back on.
func New(store ports.Store, fetchers []ports.SourceFetcher, clock ports.Clock, n ports.Notifier, log *slog.Logger) *Runner {
	seen := make(map[string]struct{}, len(fetchers))
	for _, f := range fetchers {
		name := f.Name()
		if _, dup := seen[name]; dup {
			panic("refresh.New: two fetchers share the source name " + name +
				": one fetcher per source name, or each refresh deletes the other's items")
		}
		seen[name] = struct{}{}
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Runner{store: store, fetchers: fetchers, clock: clock, notifier: n, log: log}
}

// Run performs one refresh cycle and reports what happened.
//
// It first takes the refresh lease, returning ErrBusy when another run holds it: one refresh at a
// time is enforced in the database, so the guarantee survives a Machine restart (QS-1.7). It then
// records the start of the run, fetches and stores every source in turn, and records the outcome.
//
// A failing source does not fail the run: Run returns a nil error and reports the failure through
// Report.OK and SourceReport.Err (FR-5.1 AC4). A non-nil error means the lease or the run record
// failed, i.e. nothing was refreshed.
func (r *Runner) Run(ctx context.Context, trigger string) (Report, error) {
	started := r.clock.Now()
	holder := leaseHolder(trigger, started)

	ok, err := r.store.AcquireRefreshLease(ctx, holder, started, leaseTTL)
	if err != nil {
		return Report{}, fmt.Errorf("acquire refresh lease: %w", err)
	}
	if !ok {
		return Report{}, ErrBusy
	}
	// A cancelled or timed-out run must still free its lease, or a single cancellation locks every
	// refresh out for the whole leaseTTL — and it must not be able to hang doing so; see
	// cleanupCtx for why both halves are needed.
	defer func() {
		release, cancel := cleanupCtx(ctx)
		defer cancel()
		if err := r.store.ReleaseRefreshLease(release, holder); err != nil {
			r.log.Error("release refresh lease", "err", err)
		}
	}()

	runID, err := r.store.StartRun(ctx, trigger, started)
	if err != nil {
		return Report{}, fmt.Errorf("start run: %w", err)
	}

	rep := Report{RunID: runID, StartedAt: started, Trigger: trigger, OK: true}
	var fresh []domain.Item

	for _, f := range r.fetchers {
		sr := r.runSource(ctx, f, started, &fresh)
		if sr.Err != "" {
			rep.OK = false
		}
		rep.Sources = append(rep.Sources, sr)
	}

	r.notify(ctx, fresh)

	rep.EndedAt = r.clock.Now()
	// Finished on a cleanup context for the same reason as the lease: a run that was cancelled
	// mid-flight must still be closed out, or it stays "running" in the history forever — under a
	// deadline of its own, so closing out cannot outlast the run it closes (see cleanupCtx).
	finish, cancelFinish := cleanupCtx(ctx)
	defer cancelFinish()
	if err := r.store.FinishRun(finish, runID, rep.EndedAt, rep.OK, detail(rep)); err != nil {
		r.log.Error("finish run", "run", runID, "err", err)
	}
	r.log.Info("refresh finished",
		"run", runID, "trigger", trigger, "ok", rep.OK,
		"sources", len(rep.Sources), "duration", rep.EndedAt.Sub(rep.StartedAt))
	return rep, nil
}

// runSource fetches one source and stores what it returned.
//
// On any error from Fetch the stored data is left exactly as it was: the result may be partial,
// and ReplaceItems would delete the rows it omits (see the package comment). Otherwise every write
// is its own transaction, so a Machine that dies mid-run leaves the sources that already finished
// intact (FR-5.5 AC1).
//
// The items of a successful source are appended to fresh, which is what the notifier is offered.
func (r *Runner) runSource(ctx context.Context, f ports.SourceFetcher, now time.Time, fresh *[]domain.Item) SourceReport {
	source := f.Name()

	res, err := f.Fetch(ctx)
	if err != nil {
		// Deliberately not storing res: a non-nil error means the result may be partial.
		return r.sourceFailed(ctx, source, now, fmt.Errorf("fetch: %w", err))
	}

	stored, err := r.store.ReplaceItems(ctx, source, res.Items, now)
	if err != nil {
		return r.sourceFailed(ctx, source, now, fmt.Errorf("store items: %w", err))
	}
	if len(res.Builds) > 0 {
		if err := r.store.UpsertBuilds(ctx, res.Builds, now); err != nil {
			return r.sourceFailed(ctx, source, now, fmt.Errorf("store builds: %w", err))
		}
	}
	if len(res.Metrics) > 0 {
		if err := r.store.UpsertMetrics(ctx, res.Metrics, now); err != nil {
			return r.sourceFailed(ctx, source, now, fmt.Errorf("store metrics: %w", err))
		}
	}
	if err := r.store.RecordSourceOK(ctx, source, now, stored); err != nil {
		return r.sourceFailed(ctx, source, now, fmt.Errorf("record success: %w", err))
	}

	*fresh = append(*fresh, res.Items...)
	r.log.Info("source refreshed", "source", source, "stored", stored,
		"builds", len(res.Builds), "metrics", len(res.Metrics))
	return SourceReport{Source: source, Stored: stored}
}

// sourceFailed records a source's failure and reports it.
//
// The failure is recorded on a cleanup context: a run cancelled between two sources must still be
// able to say why the later ones have no fresh data (FR-1.4 AC2), and the write is a single small
// statement — but it is exactly the write a dead database would make hang, so it is given a
// deadline of its own (see cleanupCtx).
//
// Only the error's message is stored and logged. Every adapter keeps credentials out of its error
// text — the libSQL adapter never names its DSN, the HTTP adapters never echo a token — because
// this message reaches both the log and the dashboard (QS-4.3).
func (r *Runner) sourceFailed(ctx context.Context, source string, now time.Time, cause error) SourceReport {
	msg := clip(cause.Error(), maxErrLen)
	record, cancel := cleanupCtx(ctx)
	defer cancel()
	if err := r.store.RecordSourceError(record, source, now, msg); err != nil {
		r.log.Error("record source error", "source", source, "err", err)
	}
	r.log.Warn("source failed", "source", source, "err", msg)
	return SourceReport{Source: source, Err: msg}
}

// notify announces the items this run stored that have never been announced before, and is a
// no-op while there is no notifier.
//
// The three steps happen in exactly this order, and the order is the guarantee:
//
//  1. Store.UnnotifiedKeys filters the run's keys down to the ones that have never been sent.
//     This is what makes an item announced at most once across restarts and repeated runs
//     (FR-6.1 AC2) — the bookkeeping is a table, not a field of a process that dies between
//     requests.
//  2. Notifier.Notify sends those and only those.
//  3. Store.MarkNotified records them, and only once they have actually been sent.
//
// Marking first would be the cheaper-looking order and it is the wrong one: a Machine that dies
// between marking and sending swallows the announcement for good, while one that dies between
// sending and marking repeats it. A duplicate Slack message is an annoyance; a silently dropped
// notification is the feature not working.
//
// Notify's error is all-or-nothing by the shape of ports.Notifier, so a failed batch marks
// nothing. Some of it may already have gone out — the next run re-announces those, which is the
// same trade in the same direction.
//
// Everything here is logged and dropped. Announcing is a courtesy and must never turn a good
// refresh into a bad one (FR-6.1 AC3): Run still returns nil and Report.OK is untouched.
//
// The items offered are the runner's fresh slice, which holds only what a *successful* source
// stored. A partial result is never stored (see the package comment) and must never be announced
// either: announcing it would tell the user about items that are not in the database and mark
// them notified, suppressing the real announcement when they finally arrive.
func (r *Runner) notify(ctx context.Context, items []domain.Item) {
	if r.notifier == nil || len(items) == 0 {
		return
	}

	keys := make([]string, 0, len(items))
	byKey := make(map[string]domain.Item, len(items))
	for _, it := range items {
		k := notifyKey(it)
		if _, dup := byKey[k]; dup {
			continue // one message per item, even if a source hands the same one over twice
		}
		byKey[k] = it
		keys = append(keys, k)
	}

	unsent, err := r.store.UnnotifiedKeys(ctx, keys)
	if err != nil {
		// Without the filter there is no way to tell a new item from one announced last week, and
		// announcing everything again is worse than announcing nothing.
		r.log.Error("select unnotified items", "err", err)
		return
	}
	if len(unsent) == 0 {
		return
	}

	fresh := make([]domain.Item, 0, len(unsent))
	for _, k := range unsent {
		if it, ok := byKey[k]; ok {
			fresh = append(fresh, it)
		}
	}

	// The announcement gets a budget of its own. It runs inside the refresh's 30-second budget
	// (QS-2.5), and a webhook that accepts the connection and then says nothing would otherwise
	// hold the POST /api/refresh request in flight — and Fly does not stop a Machine with a
	// request in flight. Derived from the run's context rather than detached from it: a caller
	// that hung up is not owed notifications.
	notifyCtx, cancel := context.WithTimeout(ctx, notifyBudget)
	defer cancel()
	if err := r.notifier.Notify(notifyCtx, fresh); err != nil {
		r.log.Error("notify", "items", len(fresh), "err", err)
		return
	}

	if err := r.store.MarkNotified(ctx, unsent, r.clock.Now()); err != nil {
		// The messages are out; only the bookkeeping failed, so the next run announces them
		// again. That is the direction this order was chosen for.
		r.log.Error("mark notified", "items", len(unsent), "err", err)
	}
}

// notifyKey is the identity the notified table records, "source|external_id" — the same pair that
// identifies an item everywhere else in the store.
func notifyKey(it domain.Item) string { return it.Source + "|" + it.ExternalID }

// leaseHolder builds the identity this run holds the lease under. It has to be unique per run:
// AcquireRefreshLease lets the current holder re-acquire its own lease, so two runs sharing a
// holder name would both believe they won. The trigger and start time make it readable in the
// database, and the random suffix makes it unique even for two runs of the same trigger reading
// the same clock. The store rejects a holder containing '|', its own field separator, so a trigger
// carrying one is folded to '-'.
func leaseHolder(trigger string, started time.Time) string {
	return fmt.Sprintf("%s-%d-%s", strings.ReplaceAll(trigger, "|", "-"), started.UnixNano(), rand.Text())
}

// detail renders the per-source outcome for the run record: "github: 12; todoist: fetch: …".
func detail(rep Report) string {
	parts := make([]string, 0, len(rep.Sources))
	for _, sr := range rep.Sources {
		if sr.Err != "" {
			parts = append(parts, sr.Source+": "+sr.Err)
			continue
		}
		parts = append(parts, fmt.Sprintf("%s: %d", sr.Source, sr.Stored))
	}
	return clip(strings.Join(parts, "; "), maxDetailLen)
}

// clip shortens s to at most n bytes, marking that it was shortened. It backs up to a rune
// boundary first, so the result is still valid UTF-8.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}
