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
	// Source is the fetcher's name, e.g. "github" or "github-builds".
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
	// NotifyErr is why the announcement step stopped, empty when it did not stop.
	//
	// It deliberately does not touch OK: announcing is a courtesy and never fails a refresh
	// (FR-6.1 AC3). But "recorded" has to mean more than a log line — on a Machine that scales to
	// zero nobody reads the logs, and a revoked webhook would otherwise stop the notifications for
	// weeks while every dashboard signal said the system was healthy. It therefore travels into
	// the run record's detail (see detail).
	NotifyErr string
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
// deletes the rows the incoming set omits. A fetcher that returns only builds therefore calls
// ReplaceItems with no items, which deletes every item of that source name. Give
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
		sr := r.runSource(ctx, f, &fresh)
		if sr.Err != "" {
			rep.OK = false
		}
		rep.Sources = append(rep.Sources, sr)
	}

	rep.NotifyErr = r.notify(ctx, fresh)

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
//
// # Which writes an empty result still performs
//
// The two writes answer the question "does an empty slice mean anything?" differently, and the
// difference is not a style: it follows from whether the write deletes, and from what it is keyed
// by.
//
//   - ReplaceItems is called unconditionally. It is scoped by source and it deletes the rows the
//     incoming set omits, so a source that legitimately returns nothing has to clear its own rows
//     — a closed issue disappears when the last one does, and no other source is touched.
//   - UpsertBuilds is called whenever the result claims the builds table (FetchResult.OwnsBuilds),
//     empty or not, and never otherwise. It deletes like ReplaceItems but is *not* scoped by
//     source: the builds table is one flat set. Guarding it on len(res.Builds) > 0 skipped exactly
//     the case its complement delete exists for — every watched repository losing CI at once, or
//     the last one leaving the configuration — and left rows on the tile that nothing would ever
//     refresh or remove. Guarding it on nothing at all would be worse: a second source's empty
//     slice would wipe GitHub's rows on every refresh.
//
// # Why the clock is read here and not at the start of the run
//
// The time this function passes to the store becomes the items' first_seen_at (the store stamps
// the INSERT with it), and first_seen_at is one half of the comparison the NEW badge is: an item
// is new while first_seen_at > last_visit_at (FR-1.2 AC1, QS-1.2). The other half is written by
// POST /seen at the wall-clock instant of the click.
//
// Reading the clock once for the whole run made the two halves incomparable. A run starting at
// 12:00:00 stamped every item it stored with 12:00:00, however long the fetch took; a visitor
// pressing "mark all seen" at 12:00:30 moved the watermark past that stamp; and every item the
// run stored afterwards was born already-seen — not until the next visit, but for good, because
// first_seen_at is deliberately never updated (FR-5.3 AC2). Nothing reported it: the item simply
// never carried a badge.
//
// Reading it after the fetch returned makes "first seen" mean what the column says — the instant
// zorgscope first held the item — and shrinks the window in which a click can still overtake a
// store from the length of the whole run to the length of one source's writes. FR-5.3 AC1's "the
// time of that refresh run" is still satisfied: this reading lies inside the run it belongs to.
//
// The residual window is not zero, and cannot be closed here: a click landing between this line
// and ReplaceItems' commit still stamps an item just behind the watermark. Closing it entirely
// needs the watermark and the stamp to be taken under one lock — a store change, not a runner one.
func (r *Runner) runSource(ctx context.Context, f ports.SourceFetcher, fresh *[]domain.Item) SourceReport {
	source := f.Name()

	res, err := f.Fetch(ctx)
	now := r.clock.Now()
	if err != nil {
		// Deliberately not storing res: a non-nil error means the result may be partial.
		return r.sourceFailed(ctx, source, now, fmt.Errorf("fetch: %w", err))
	}

	stored, err := r.store.ReplaceItems(ctx, source, res.Items, now)
	if err != nil {
		return r.sourceFailed(ctx, source, now, fmt.Errorf("store items: %w", err))
	}
	// Stored on the strength of OwnsBuilds and never of len(res.Builds): see the comment above
	// this function for why an empty result from the builds fetcher is a fact to store and an
	// empty one from anybody else is nothing at all.
	if res.OwnsBuilds {
		if err := r.store.UpsertBuilds(ctx, res.Builds, now); err != nil {
			return r.sourceFailed(ctx, source, now, fmt.Errorf("store builds: %w", err))
		}
	}
	if err := r.store.RecordSourceOK(ctx, source, now, stored); err != nil {
		return r.sourceFailed(ctx, source, now, fmt.Errorf("record success: %w", err))
	}

	*fresh = append(*fresh, res.Items...)
	r.log.Info("source refreshed", "source", source, "stored", stored, "builds", len(res.Builds))
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
// no-op while there is no notifier. It returns why it stopped, empty when it did not — see
// Report.NotifyErr.
//
// The three steps happen in exactly this order, and the order is the guarantee:
//
//  1. Store.UnnotifiedKeys filters the run's keys down to the ones that have never been sent.
//     This is what makes an item announced at most once across restarts and repeated runs
//     (FR-6.1 AC2) — the bookkeeping is a table, not a field of a process that dies between
//     requests.
//  2. Notifier.Notify sends them, one item per call, stopping at the first failure.
//  3. Store.MarkNotified records the ones that actually went out, and only those.
//
// Marking first would be the cheaper-looking order and it is the wrong one: a Machine that dies
// between marking and sending swallows the announcement for good, while one that dies between
// sending and marking repeats it. A duplicate Slack message is an annoyance; a silently dropped
// notification is the feature not working.
//
// One item per call is what makes a failed announcement converge, and it is worth spelling out
// why. Sending the whole set in one call and marking nothing when it failed looked like the
// strictest reading of "mark only what was sent" — but the two failures a batch actually meets
// are deterministic: Slack's incoming webhooks allow about one message a second, and the whole
// step has notifyBudget to work in. A first run over a populated database would therefore fail at
// roughly the same position every time, re-post the same prefix on every cron tick forever, and
// never reach the items behind it. Sending item by item makes "mark only what was sent" literally
// true per item and shortens the unannounced prefix on every run, so the same repeated failure
// now converges. It costs nothing extra on the failing path either: the batch still stops at the
// first failure, so a dead webhook is still one failed POST per run, not one per item.
//
// A rejection that retrying cannot fix is marked as announced all the same (see the notifier's
// permanent marker). It is the one deliberate exception to "never mark what was not sent", and
// the alternative is worse: with a fail-fast batch, one message Slack will never accept would
// lead the queue on every run and suppress every item behind it for good.
//
// Everything here is logged and dropped. Announcing is a courtesy and must never turn a good
// refresh into a bad one (FR-6.1 AC3): Run still returns nil and Report.OK is untouched — but the
// reason is reported, because a failure only a log knows about is a feature that stops working
// silently.
//
// The items offered are the runner's fresh slice, which holds only what a *successful* source
// stored. A partial result is never stored (see the package comment) and must never be announced
// either: announcing it would tell the user about items that are not in the database and mark
// them notified, suppressing the real announcement when they finally arrive.
func (r *Runner) notify(ctx context.Context, items []domain.Item) string {
	if r.notifier == nil || len(items) == 0 {
		return ""
	}

	keys := make([]string, 0, len(items))
	byKey := make(map[string]domain.Item, len(items))
	for _, it := range items {
		if !announceable(it) {
			continue
		}
		k := notifyKey(it)
		if _, dup := byKey[k]; dup {
			continue // one message per item, even if a source hands the same one over twice
		}
		byKey[k] = it
		keys = append(keys, k)
	}
	if len(keys) == 0 {
		return ""
	}

	unsent, err := r.store.UnnotifiedKeys(ctx, keys)
	if err != nil {
		// Without the filter there is no way to tell a new item from one announced last week, and
		// announcing everything again is worse than announcing nothing.
		r.log.Error("select unnotified items", "err", err)
		return clip("select unnotified items: "+err.Error(), maxErrLen)
	}

	// The announcement gets a budget of its own. It runs inside the refresh's 30-second budget
	// (QS-2.5), and a webhook that accepts the connection and then says nothing would otherwise
	// hold the POST /api/refresh request in flight — and Fly does not stop a Machine with a
	// request in flight. It is derived from the run's context rather than detached from it, so it
	// is bounded by the run's own ceiling and cannot outlive the run: SIGTERM and the refresh
	// budget both still stop the posting, which is right, because posting to Slack after the
	// process has been told to stop is worse than not posting at all.
	notifyCtx, cancel := context.WithTimeout(ctx, notifyBudget)
	defer cancel()

	sent := make([]string, 0, len(unsent))
	note := ""
	for _, k := range unsent {
		it, ok := byKey[k]
		if !ok {
			// A key this run never asked about. It cannot be sent — there is no item behind it —
			// so it must not be marked either: marking it would swallow the announcement the day
			// the item does turn up. The keys marked below are exactly the ones sent, never the
			// ones the store returned.
			r.log.Warn("unnotified key not in this run", "key", k)
			continue
		}
		if err := r.notifier.Notify(notifyCtx, []domain.Item{it}); err != nil {
			r.log.Error("notify", "key", k, "sent", len(sent), "err", err)
			note = clip(err.Error(), maxErrLen)
			if permanentlyRejected(err) {
				// Retrying cannot change the answer, and leaving it unmarked would park it at the
				// head of the queue for good.
				r.log.Warn("announcement permanently rejected; recording it as sent", "key", k)
				sent = append(sent, k)
			}
			break
		}
		sent = append(sent, k)
	}
	if len(sent) == 0 {
		return note
	}

	// Recorded on a cleanup context: the messages are already out, and a deadline that expires
	// between the last post and this write would re-announce every one of them on the next run.
	// It is a closing write like FinishRun, and needs the same both-halves treatment (see
	// cleanupCtx) — outliving cancellation, but never able to hang.
	mark, cancelMark := cleanupCtx(ctx)
	defer cancelMark()
	if err := r.store.MarkNotified(mark, sent, r.clock.Now()); err != nil {
		// The messages are out; only the bookkeeping failed, so the next run announces them
		// again. That is the direction this order was chosen for.
		r.log.Error("mark notified", "items", len(sent), "err", err)
		if note == "" {
			note = clip("mark notified: "+err.Error(), maxErrLen)
		}
	}
	return note
}

// announceable reports whether an item is one FR-6.1 AC1 asks to announce: "one message per newly
// first-seen GitHub item", i.e. an issue or a pull request.
//
// It is a kind rather than a source name because no source name is hard-coded in this package
// (see New) — and because the kind is what the requirement is really about: a future source that
// contributes something other than an issue or a pull request is stored and shown on the
// dashboard like everything else, just not posted, without this function needing to know its name.
func announceable(it domain.Item) bool {
	return it.Kind == domain.KindIssue || it.Kind == domain.KindPR
}

// permanentlyRejected reports whether a notifier said that retrying this message cannot help.
//
// The check is an anonymous interface rather than an error value from the notifier's package,
// because this package talks to notifiers through ports.Notifier and must not import an adapter
// to interpret one. A notifier that does not classify its failures simply has none of them
// treated as permanent, which is the safe direction: the item stays unannounced and is retried.
func permanentlyRejected(err error) bool {
	var p interface{ PermanentNotifyFailure() bool }
	return errors.As(err, &p) && p.PermanentNotifyFailure()
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

// detail renders the per-source outcome for the run record: "github: 12; github-builds: fetch: …".
//
// A failed announcement is appended to it. The run itself was fine and stays OK (FR-6.1 AC3), but
// the failure has to be recorded somewhere a person actually looks — the run record is that place,
// and it is where the notifier error's scrubbing (QS-4.3) earns its keep, because this text is
// rendered on the dashboard.
func detail(rep Report) string {
	parts := make([]string, 0, len(rep.Sources)+1)
	for _, sr := range rep.Sources {
		if sr.Err != "" {
			parts = append(parts, sr.Source+": "+sr.Err)
			continue
		}
		parts = append(parts, fmt.Sprintf("%s: %d", sr.Source, sr.Stored))
	}
	if rep.NotifyErr != "" {
		parts = append(parts, domain.NotifyDetailPrefix+rep.NotifyErr)
	}
	return clip(strings.Join(parts, domain.DetailSeparator), maxDetailLen)
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
