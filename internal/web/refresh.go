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
		s.fail(w, r, "refreshing", err)
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

// runRefresh performs one run under the given trigger, bounded by refreshCeiling.
//
// The ceiling hangs off the request's own context, so a caller that gives up also stops the work
// it asked for; the runner's closing writes are on contexts of their own and survive either
// ending, so the lease is freed and the run record closed out whichever way the run ends.
//
// It never calls time.Now: every timestamp in the report comes from the injected clock, through
// the runner.
func (s *Server) runRefresh(r *http.Request, trigger string) (refresh.Report, error) {
	if s.runner == nil {
		return refresh.Report{}, errors.New("no refresh runner is configured")
	}
	ctx, cancel := context.WithTimeout(r.Context(), refreshCeiling)
	defer cancel()
	return s.runner.Run(ctx, trigger)
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
