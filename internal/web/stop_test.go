// The stop control (FR-1.7): who may stop this process, when it refuses, and what it never lets
// happen. These live in package web because they reach for the unexported Server fields that make
// the shutdown observable without actually ending the test binary.
package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gernotstarke/zorgscope/internal/domain"
	"github.com/gernotstarke/zorgscope/internal/ports"
)

// stopRecorder stands in for the real shutdown: it records that it was asked rather than ending
// the process, and lets a test wait for the asking without sleeping for the grace period.
type stopRecorder struct {
	mu     sync.Mutex
	called int
	done   chan struct{}
	once   sync.Once
}

func newStopRecorder() *stopRecorder {
	return &stopRecorder{done: make(chan struct{})}
}

func (r *stopRecorder) stop() {
	r.mu.Lock()
	r.called++
	r.mu.Unlock()
	r.once.Do(func() { close(r.done) })
}

// waitCalled reports whether the stop was asked for within a generous deadline.
func (r *stopRecorder) waitCalled(t *testing.T) bool {
	t.Helper()
	select {
	case <-r.done:
		return true
	case <-time.After(2 * time.Second):
		return false
	}
}

// neverCalled gives a shutdown that should not happen enough time to happen, and reports whether
// it stayed away. It waits several grace periods, so a stop that was merely slow still fails it.
func (r *stopRecorder) neverCalled(t *testing.T) bool {
	t.Helper()
	select {
	case <-r.done:
		return false
	case <-time.After(200 * time.Millisecond):
		return true
	}
}

// stopHandler builds a signed-in-able handler whose shutdown is recorded rather than performed.
func stopHandler(t *testing.T, store ports.Store) (http.Handler, *stopRecorder) {
	t.Helper()
	rec := newStopRecorder()
	s := newTestServerWith(t, func(o *Options) {
		credentialAllSources(o)
		o.Store = store
		o.Stop = rec.stop
	})
	// The grace period is a courtesy to the browser, not part of what is being asserted here.
	s.stopGrace = time.Millisecond
	return s.Handler(), rec
}

// The happy path: a signed-in visitor presses stop, is told what is happening by the process that
// is doing it, and the process goes.
func TestStopShutsTheProcessDown(t *testing.T) {
	h, rec := stopHandler(t, representativeStore())
	c := signIn(t, h)

	res := postAs(h, "/stop", nil, c)
	if res.Code != http.StatusOK {
		t.Fatalf("POST /stop = %d, want 200", res.Code)
	}
	body := res.Body.String()
	if !strings.Contains(body, "zorgscope is stopping") {
		t.Errorf("the page does not say the process is stopping: %s", body)
	}
	// The visitor is told how to get it back, because on a Machine that scales to zero the answer
	// is "open it again" and that is not obvious from a stopped process.
	if !strings.Contains(body, "starts it back up") {
		t.Error("the page does not say how to start it again")
	}
	if !rec.waitCalled(t) {
		t.Fatal("the process was never asked to stop")
	}
}

// The response is written before the shutdown is triggered. A visitor who pressed stop must be
// told by this process, not by their browser's connection error.
func TestStopAnswersBeforeItShutsDown(t *testing.T) {
	h, rec := stopHandler(t, representativeStore())
	c := signIn(t, h)

	res := postAs(h, "/stop", nil, c)
	if res.Body.Len() == 0 {
		t.Fatal("the stop request was answered with nothing")
	}
	if res.Code != http.StatusOK {
		t.Fatalf("POST /stop = %d, want 200", res.Code)
	}
	if !rec.waitCalled(t) {
		t.Error("the process was never asked to stop")
	}
}

// QS-4.1: the credential this route declares is a session, and nothing else opens it. An
// anonymous POST is refused and — the part that matters — changes nothing.
func TestStopRefusesAnAnonymousRequest(t *testing.T) {
	h, rec := stopHandler(t, representativeStore())

	res := post(h, "/stop", nil)
	if res.Code != http.StatusUnauthorized {
		t.Errorf("anonymous POST /stop = %d, want 401", res.Code)
	}
	if !rec.neverCalled(t) {
		t.Fatal("an anonymous request stopped the process")
	}
}

// REFRESH_SECRET exists so an external scheduler can trigger a refresh, and it must not also be a
// shutdown switch: the two credentials are independent and neither authorises the other's action.
// A leaked cron URL that could stop the Machine would be a one-request denial of service.
func TestTheRefreshSecretCannotStopTheProcess(t *testing.T) {
	h, rec := stopHandler(t, representativeStore())

	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/stop", nil)
	req.Header.Set("Authorization", "Bearer "+testSecret)
	h.ServeHTTP(res, req)

	if res.Code == http.StatusOK {
		t.Error("REFRESH_SECRET stopped the process")
	}
	if !rec.neverCalled(t) {
		t.Fatal("REFRESH_SECRET stopped the process")
	}
}

// A refresh run holds the refresh lease and writes as it goes. Stopping half way through leaves
// the lease held until it expires and the sources the run never reached with no record of why, so
// the request is refused and says when to try again (QS-1.7).
func TestStopRefusesWhileARefreshIsRunning(t *testing.T) {
	store := representativeStore()
	store.lastRun = domain.RefreshRun{ID: 7, StartedAt: testNow.Add(-30 * time.Second)}
	h, rec := stopHandler(t, store)
	c := signIn(t, h)

	res := postAs(h, "/stop", nil, c)
	if res.Code != http.StatusConflict {
		t.Errorf("POST /stop during a run = %d, want 409", res.Code)
	}
	if !strings.Contains(res.Body.String(), "A refresh is running") {
		t.Errorf("the refusal does not say why: %s", res.Body.String())
	}
	if !rec.neverCalled(t) {
		t.Fatal("the process stopped in the middle of a refresh run")
	}
}

// The other half of that rule. A run record left open by a process that died holding it stays open
// forever, and a stop control that refused for it would never work again. Past the ceiling on a
// run, nothing is still running.
func TestAnAbandonedRunDoesNotBlockStoppingForever(t *testing.T) {
	store := representativeStore()
	store.lastRun = domain.RefreshRun{ID: 7, StartedAt: testNow.Add(-24 * time.Hour)}
	h, rec := stopHandler(t, store)
	c := signIn(t, h)

	if res := postAs(h, "/stop", nil, c); res.Code != http.StatusOK {
		t.Fatalf("POST /stop with an abandoned run = %d, want 200", res.Code)
	}
	if !rec.waitCalled(t) {
		t.Fatal("an abandoned run left the process unable to stop")
	}
}

// A deployment that handed in no way to stop says so, rather than rendering a page claiming the
// process is going away while it carries on serving.
func TestStopSaysSoWhenThereIsNothingBehindIt(t *testing.T) {
	h := dashHandler(t, representativeStore()) // built without a Stop function
	c := signIn(t, h)

	res := postAs(h, "/stop", nil, c)
	if res.Code != http.StatusNotImplemented {
		t.Errorf("POST /stop with no stop function = %d, want 501", res.Code)
	}
	if strings.Contains(res.Body.String(), "zorgscope is stopping") {
		t.Error("the page claims the process is stopping when nothing can stop it")
	}
}

// The control is drawn only where it works, and it is a form post — so it needs no script
// (FR-1.3 AC3) and cannot be triggered by anything that merely follows links.
func TestTheStopControlIsDrawnOnlyWhereItWorks(t *testing.T) {
	withStop, _ := stopHandler(t, representativeStore())
	body := getAuthed(t, withStop, "/").Body.String()

	form := firstLineContaining(body, `action="/stop"`)
	if form == "" {
		t.Fatal("the header has no stop control")
	}
	if !strings.Contains(form, `method="post"`) {
		t.Errorf("the stop control is not a form post: %s", form)
	}

	without := getAuthed(t, dashHandler(t, representativeStore()), "/").Body.String()
	if strings.Contains(without, `action="/stop"`) {
		t.Error("a deployment that cannot stop still draws the stop control")
	}

	// And not to someone who could not use it. The appearance switch beside it is public on
	// purpose; this one answers 401, so offering it on the sign-in page would be a lie.
	if anon := get(withStop, "/login").Body.String(); strings.Contains(anon, `action="/stop"`) {
		t.Error("the sign-in page offers a stop control that would answer 401")
	}
}

// GET must not stop anything. A link that shut the process down would be followed by every
// prefetcher, crawler and accidental bookmark there is.
func TestStoppingIsNotReachableByGET(t *testing.T) {
	h, rec := stopHandler(t, representativeStore())
	c := signIn(t, h)

	if res := getAs(h, "/stop", c); res.Code == http.StatusOK {
		t.Error("GET /stop = 200; stopping must be a POST")
	}
	if !rec.neverCalled(t) {
		t.Fatal("a GET stopped the process")
	}
}
