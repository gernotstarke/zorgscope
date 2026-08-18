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
func New(store ports.Store, fetchers []ports.SourceFetcher, clock ports.Clock, n ports.Notifier, log *slog.Logger) *Runner {
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
	// context.WithoutCancel matters here: a cancelled or timed-out run must still free its lease,
	// or a single cancellation locks every refresh out for the whole leaseTTL.
	defer func() {
		if err := r.store.ReleaseRefreshLease(context.WithoutCancel(ctx), holder); err != nil {
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
	// Finished with an uncancellable context for the same reason as the lease: a run that was
	// cancelled mid-flight must still be closed out, or it stays "running" in the history forever.
	if err := r.store.FinishRun(context.WithoutCancel(ctx), runID, rep.EndedAt, rep.OK, detail(rep)); err != nil {
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
// The failure is recorded with an uncancellable context: a run cancelled between two sources must
// still be able to say why the later ones have no fresh data (FR-1.4 AC2), and the write is a
// single small statement.
//
// Only the error's message is stored and logged. Every adapter keeps credentials out of its error
// text — the libSQL adapter never names its DSN, the HTTP adapters never echo a token — because
// this message reaches both the log and the dashboard (QS-4.3).
func (r *Runner) sourceFailed(ctx context.Context, source string, now time.Time, cause error) SourceReport {
	msg := clip(cause.Error(), maxErrLen)
	if err := r.store.RecordSourceError(context.WithoutCancel(ctx), source, now, msg); err != nil {
		r.log.Error("record source error", "source", source, "err", err)
	}
	r.log.Warn("source failed", "source", source, "err", msg)
	return SourceReport{Source: source, Err: msg}
}

// notify offers the refreshed items to the notifier, and is a no-op while there is none — Task 17
// fills that in. A notification that fails is logged and nothing more: announcing is a courtesy,
// and it must never turn a good refresh into a bad one (FR-6.1 AC3).
func (r *Runner) notify(ctx context.Context, items []domain.Item) {
	if r.notifier == nil || len(items) == 0 {
		return
	}
	if err := r.notifier.Notify(ctx, items); err != nil {
		r.log.Error("notify", "err", err)
	}
}

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
