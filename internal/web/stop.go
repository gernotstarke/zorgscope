package web

import (
	"net/http"
	"time"
)

// defaultStopGrace is how long the process keeps serving after answering the stop request.
//
// It exists because http.Server.Shutdown stops accepting connections and closes idle ones as soon
// as it is called: trigger the shutdown from inside the handler and the confirmation page can
// reach the browser while the stylesheet it asks for next cannot. A second is enough for the
// response to land and the page to finish fetching what it needs over the connection it already
// has. It is a courtesy, not a guarantee — nothing is lost if it expires early, because by then
// the only thing left to draw is a page saying the process is going away.
const defaultStopGrace = time.Second

// handleStop shuts this process down at the visitor's request (FR-1.7).
//
// On a Machine that scales to zero this is not a destructive act: the Machine stops, stops being
// billed, and the next request starts it again. That is the whole reason the control exists — the
// idle timeout is minutes long and there is no reason to wait it out once you are finished
// reading.
//
// Two things it refuses to do. It will not stop a process that is in the middle of a refresh run:
// a run holds the refresh lease and writes as it goes, and killing it half way leaves the lease
// held until it expires and the sources it never reached with no record of why (QS-1.7). And it
// will not pretend when there is nothing behind it: a deployment that handed in no stop function
// gets 501, not a page claiming the process is going away.
func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
	if s.stop == nil {
		s.render(w, r, http.StatusNotImplemented, "stopping.html", pageData{
			Title: "Cannot stop",
			Error: "This deployment cannot stop itself from the browser.",
		})
		return
	}

	if s.refreshInFlight(r) {
		s.render(w, r, http.StatusConflict, "stopping.html", pageData{
			Title: "Still refreshing",
			Error: busyStopNotice,
		})
		return
	}

	// The page is written before anything is stopped, so the visitor is told what is happening by
	// the process that is doing it rather than by their browser's connection error.
	s.render(w, r, http.StatusOK, "stopping.html", pageData{Title: "Stopping"})

	s.log.Info("stopping on request", "method", r.Method, "path", r.URL.Path)

	stop, grace := s.stop, s.stopGrace
	go func() {
		time.Sleep(grace)
		stop()
	}()
}

// busyStopNotice is what a stop request gets while a refresh is running. It says what to do about
// it, because "try again" is only useful with an idea of when.
const busyStopNotice = "A refresh is running. Stopping now would leave it half done, so it has " +
	"to finish first — refreshes are bounded, so this clears itself."

// refreshInFlight reports whether a refresh run is under way right now.
//
// It asks the store the same question the header asks, so the two can never disagree about
// whether something is running. "Under way" is deliberately bounded by the ceiling on a run: a run
// record that is still open long after no run could still be going belongs to a process that died
// holding it, and refusing to stop for it would mean the control never worked again. A store that
// cannot be read at all answers no — a database that is down is not a refresh in progress, and it
// is the state in which being able to stop the process matters most.
func (s *Server) refreshInFlight(r *http.Request) bool {
	run, err := s.store.LastRun(r.Context())
	if err != nil {
		return false
	}
	return run.Running() && s.clock.Now().Sub(run.StartedAt) < s.ceiling
}
