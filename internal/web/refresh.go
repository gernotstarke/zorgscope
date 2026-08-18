package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/gernotstarke/zorgscope/internal/refresh"
)

// refreshCeiling is the hard backstop on one refresh request, and it is not the same thing as
// QS-2.5's 30-second budget.
//
// QS-2.5 is an expected duration for the representative configuration, asserted where the
// requirement says to assert it: in the run record, against the fake sources. This is the ceiling
// that stops a hung upstream from holding a scale-to-zero Machine awake — Fly does not stop a
// Machine with a request in flight, so an upstream that accepts a connection and then never
// answers would keep this process billed and running for as long as it stayed silent.
//
// It is two minutes rather than thirty seconds because the runner's three closing writes — release
// the lease, finish the run record, record a source error — each carry their own five-second
// deadline, deliberately independent of this context so that a cancelled run can still record why
// it failed. A run can therefore legitimately linger some fifteen seconds past its own budget, and
// a ceiling sized to the budget would cut those writes off precisely when they matter most.
//
// # The invariant: refreshCeiling must stay strictly below internal/refresh's leaseTTL
//
// The two constants live in different packages and look unrelated, and they are not. A run holds
// the refresh lease for leaseTTL (5 minutes) and the store hands an expired lease to whoever asks
// next. Raise this ceiling past that TTL and a genuinely slow run reaches minute six still working,
// the next cron trigger acquires the lease that has expired underneath it, and two runs call
// ReplaceItems on the same sources at once — the exact thing QS-1.7 exists to prevent, with the
// first-seen invariant in the blast radius. Nothing would report it: both runs answer 200.
//
// TestTheCeilingStaysInsideTheLease pins the relationship against the TTL the runner actually
// asks the store for, not against a copy of it, so raising either constant past the other fails.
//
// # The value: what it has to cover
//
// Sources are fetched sequentially, each HTTP call is bounded by main.go's 20-second
// upstreamTimeout, and two of the fetchers fan out inside one Fetch. The worst case a run can take
// is therefore, in whole seconds:
//
//	20 × (2×repos + 2×sites + 1) + 10
//
// — GitHub's issue and build fetchers make one call per configured repository each, Plausible
// makes one per site per window and there are two windows (FR-3.1), Todoist makes one, and the
// announcement step is bounded by the runner's own 10-second notifyBudget. One repository and one
// site is 110 s, which is inside this ceiling by ten seconds. Two repositories, or a second site,
// is not: at 150 s the ceiling fires mid-run and every source it has not reached records
// "context deadline exceeded" — rendered on the dashboard as a broken source that was never broken
// (FR-1.4 AC3), which is the fabricated-error failure the WithoutCancel below exists to prevent,
// one layer up. TestTheCeilingFiringIsReportedAsSuchPerSource pins what that looks like.
//
// So this value is a decision about configuration size, and adding a repository or a site is a
// decision to revisit it. The headroom above the ceiling is bounded too: leaseTTL minus the
// ceiling has to leave room for the closing writes above, which run past it.
const refreshCeiling = 2 * time.Minute

// busyNotice is what a visitor is told when another refresh already holds the lease. The API says
// the same thing in JSON; see refresh.ErrBusy.
const busyNotice = "A refresh is already running. Give it a moment and try again."

// ---------------------------------------------------------------- the cron endpoint

// handleAPIRefresh is the endpoint cron-job.org calls, and on a Machine that scales to zero it is
// the only thing that ever writes upstream data: there is no scheduler and no process between
// requests.
//
// It answers 200 with the run's report as JSON — per source, how many items were stored and which
// failed (FR-5.1 AC3) — or 409 when another refresh already holds the lease (FR-5.2 AC2, QS-1.7).
// A failing source is not a failing request: the run stores every source that worked and reports
// the one that did not (FR-5.1 AC4), so the answer is still 200 with OK false.
//
// The bearer in front of it is the route table's, not this handler's, and a request that fails it
// never reaches here — which is what makes "a wrong secret fetches nothing" structural rather than
// a check this function could forget (FR-5.1 AC2, QS-4.1).
func (s *Server) handleAPIRefresh(w http.ResponseWriter, r *http.Request) {
	rep, err := s.runRefresh(r, "cron")
	switch {
	case errors.Is(err, refresh.ErrBusy):
		s.writeJSON(w, r, http.StatusConflict, apiError{Error: refresh.ErrBusy.Error()})
	case err != nil:
		// JSON, not s.fail's text/plain: this endpoint's whole contract is a body a machine
		// decodes, and a caller that meets a syntax error exactly when the run failed learns
		// nothing from the one response that had something to tell it.
		s.failJSON(w, r, "refreshing", err)
	default:
		s.writeJSON(w, r, http.StatusOK, s.apiReport(rep))
	}
}

// ---------------------------------------------------------------- the dashboard's button

// handleRefresh is the dashboard's own "Refresh now". It runs the same runner under a different
// trigger, so the run record says who asked: "user" here, "cron" for the endpoint above.
//
// It answers 303 back to the dashboard rather than 204, because the control is a plain form post
// with no htmx on it (FR-1.3 AC3): a 204 would leave the browser sitting on the old page with
// nothing having visibly happened. On ErrBusy it re-renders the dashboard with a notice at 409
// rather than calling http.Error, for the same reason — a form post that ends in a bare error
// status shows the browser's own error page instead of the site.
func (s *Server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	_, err := s.runRefresh(r, "user")
	switch {
	case errors.Is(err, refresh.ErrBusy):
		s.renderBusy(w, r)
	case err != nil:
		s.fail(w, r, "refreshing", err)
	default:
		http.Redirect(w, r, "/", http.StatusSeeOther)
	}
}

// renderBusy re-renders the dashboard, with the busy notice, at 409.
func (s *Server) renderBusy(w http.ResponseWriter, r *http.Request) {
	d, err := s.dashboard(r.Context())
	if err != nil {
		// The page cannot be assembled, but the answer to "may I refresh?" is still no. The
		// visitor gets the notice as plain text; the reason the page failed goes to the log,
		// scrubbed, because it may quote the DSN and the DSN carries the Turso token (QS-4.3).
		s.log.Error("request failed",
			"doing", "rendering the busy dashboard",
			"method", r.Method,
			"path", r.URL.Path,
			"err", Redact(s.cfg.Secrets, err.Error()),
		)
		http.Error(w, busyNotice, http.StatusConflict)
		return
	}
	view := s.dashboardView(d)
	s.render(w, r, http.StatusConflict, "dashboard.html", pageData{
		NewCount:  d.NewTotal,
		Dashboard: &view,
		Error:     busyNotice,
	})
}

// ---------------------------------------------------------------- running

// runRefresh performs one run under the given trigger.
//
// The context it runs on pairs context.WithoutCancel with context.WithTimeout, and that pairing
// only looks redundant until you have thought about it. Both halves are load-bearing and neither
// may be dropped — the runner's own cleanupCtx has the same shape for a related reason:
//
//   - context.WithoutCancel, because a client hanging up carries no information about whether the
//     refresh should finish. cron-job.org gives up at its own timeout, around thirty seconds, which
//     is well inside a run that is merely slow rather than broken. On the request's own context
//     that disconnect would cancel the run, and every source not yet fetched would have "context
//     canceled" recorded as its failure — a fabricated error, rendered on the dashboard as a
//     failing source (FR-1.4 AC3) when the source was fine and only the HTTP client left. On a
//     Machine that scales to zero it is self-perpetuating too: the cron trigger is the only thing
//     that ever writes upstream data, so an abandoned run leaves the data stale until the next
//     trigger, which is cut off at the same point. The same holds for the dashboard's own button:
//     a visitor navigating away must not leave half-written data and invented errors behind.
//   - context.WithTimeout, because something still has to bound how long this process stays awake;
//     see refreshCeiling. Dropping the client's ability to cut the work short is not the same as
//     letting it run forever, and the deadline is what keeps the second from following the first.
//
// It never calls time.Now: every timestamp in the report comes from the injected clock, through
// the runner.
//
// A panicking source is caught here only long enough to close the run record out; the panic then
// carries on to recoverPanics, which is what turns it into a 500.
func (s *Server) runRefresh(r *http.Request, trigger string) (refresh.Report, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), s.ceiling)
	defer cancel()
	// Registered after cancel, so it runs before it: the context is still live in here.
	defer s.closeRunAfterPanic(ctx)
	return s.runner.Run(ctx, trigger)
}

// panicDetail is what the run record says about a run a panic ended. It is deliberately fixed: the
// detail is stored and rendered, and a panic value can carry anything the panicking code was
// holding (QS-4.3). The value and its stack go to the log instead.
const panicDetail = "the refresh was stopped by a panic; see the log"

// panicCleanupTimeout bounds the one write closeAbandonedRun makes, for the same reason
// internal/refresh bounds its own closing writes: nothing on the way out may be able to hold a
// scale-to-zero Machine awake indefinitely.
const panicCleanupTimeout = 5 * time.Second

// closeRunAfterPanic closes out the refresh_run row of a run a panic ended, and then re-panics.
//
// The lease looks after itself: Runner.Run releases it in a defer, which the unwinding stack runs.
// FinishRun does not — it is a plain call after the fetch loop — so without this the row stays
// "running" for good and the dashboard's last-refresh line never moves again, even after the
// process recovers.
//
// It re-panics because a panic is a bug and must not be turned into a quiet answer here: the 500
// and the logged stack are recoverPanics' job, and this is only the part that has to happen while
// the run's own context is still alive.
func (s *Server) closeRunAfterPanic(ctx context.Context) {
	p := recover()
	if p == nil {
		return
	}
	s.closeAbandonedRun(ctx)
	panic(p)
}

// closeAbandonedRun writes the end of a run record nobody else will close.
//
// The row is found through LastRun rather than by ID, because a panic unwinds past the ID the
// runner holds. The lease is already free by the time this runs, so a cron trigger arriving in
// that same instant could have opened a newer row; the FinishedAt check keeps this from closing a
// run that finished on its own, not from closing the wrong open one. That window is microseconds
// wide, needs a concurrent trigger to hit it, and costs a run record's end timestamp — against a
// row that is otherwise wrong forever.
func (s *Server) closeAbandonedRun(ctx context.Context) {
	// The run's own context may well be the reason for the panic, and is cancelled the moment
	// runRefresh returns; the closing write gets a live one of its own, as the runner's do.
	closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), panicCleanupTimeout)
	defer cancel()

	run, err := s.store.LastRun(closeCtx)
	if err != nil {
		s.log.Error("closing out the run a panic ended",
			"doing", "reading the last run", "err", Redact(s.cfg.Secrets, err.Error()))
		return
	}
	if run.ID == 0 || !run.FinishedAt.IsZero() {
		return // nothing was opened, or it closed itself before the panic
	}
	if err := s.store.FinishRun(closeCtx, run.ID, s.clock.Now(), false, panicDetail); err != nil {
		s.log.Error("closing out the run a panic ended",
			"run", run.ID, "err", Redact(s.cfg.Secrets, err.Error()))
	}
}

// ---------------------------------------------------------------- the JSON body

// apiSource is one source's outcome as the caller of the API sees it.
type apiSource struct {
	Source string `json:"source"`
	// Stored is how many items the source now holds, zero when it failed (FR-5.1 AC3).
	Stored int `json:"stored"`
	// Err is the source's failure message, scrubbed of every configured secret. It is the one
	// field on this response that carries text from a dependency, so it is the one that has to
	// go through Redact: an error from an upstream client may quote a URL that carries a token
	// (QS-4.3).
	Err string `json:"err,omitempty"`
}

// apiReport is a refresh.Report as the caller of the API sees it: the same outcome, with every
// per-source error scrubbed.
type apiReport struct {
	RunID   int64  `json:"run_id"`
	Trigger string `json:"trigger"`
	// OK is true only when every source succeeded.
	OK bool `json:"ok"`
	// StartedAt and EndedAt are the run record's own timestamps, both read from the injected
	// clock. They are what QS-2.5's budget is measured against.
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at"`
	// Sources holds one entry per configured source, in the order they ran. It is never null:
	// a caller reading "which sources failed" should find a list, empty or otherwise.
	Sources []apiSource `json:"sources"`
}

// apiError is the body of an API answer that is not a report.
type apiError struct {
	OK    bool   `json:"ok"`
	Error string `json:"error"`
}

// apiReport converts a run's report into the response body, scrubbing every per-source error.
func (s *Server) apiReport(rep refresh.Report) apiReport {
	out := apiReport{
		RunID:     rep.RunID,
		Trigger:   rep.Trigger,
		OK:        rep.OK,
		StartedAt: rep.StartedAt,
		EndedAt:   rep.EndedAt,
		Sources:   make([]apiSource, 0, len(rep.Sources)),
	}
	for _, sr := range rep.Sources {
		out.Sources = append(out.Sources, apiSource{
			Source: sr.Source,
			Stored: sr.Stored,
			Err:    Redact(s.cfg.Secrets, sr.Err),
		})
	}
	return out
}

// failJSON is fail for the endpoint that answers in JSON: the same scrubbed log line, and a body
// the caller can decode. The body carries the fixed notice rather than the error's own text, for
// the reason fail's does — an error from the libSQL driver quotes the DSN, and the DSN carries the
// Turso auth token (QS-4.3).
func (s *Server) failJSON(w http.ResponseWriter, r *http.Request, doing string, err error) {
	s.logFailure(r, doing, err)
	s.writeJSON(w, r, http.StatusInternalServerError, apiError{Error: internalErrorNotice})
}

// writeJSON encodes v into a buffer before writing anything, so an encoding that fails half way
// through cannot leave a truncated body behind a 200 — the same reason render buffers a page.
func (s *Server) writeJSON(w http.ResponseWriter, r *http.Request, status int, v any) {
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(v); err != nil {
		s.fail(w, r, "encoding the refresh report", err)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	// The report names what changed upstream; no cache, shared or otherwise, should keep it.
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}
