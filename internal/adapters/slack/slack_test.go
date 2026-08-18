// The tests live in the package itself: the scrubber is the QS-4.3 guarantee and is worth
// testing directly, not only through the errors that happen to reach it.
package slack

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gernotstarke/zorgscope/internal/domain"
	"github.com/gernotstarke/zorgscope/internal/ports"
)

// canary is shaped like the opaque tail of a Slack incoming webhook — the part that *is* the
// credential. It is not a real webhook and posts nowhere. The '+' and '=' are deliberate: they
// make the percent-encoded spelling differ from the raw one, so a test that only ever sees one
// spelling cannot pass by accident (QS-4.3).
const canary = "zs+canary=9f3c1a7b2e5d4c8a0b6f2e1d7c3a9b45"

// webhookPath is the credential-bearing path a webhook URL carries, shaped like Slack's.
const webhookPath = "/services/T0CANARY/B0CANARY/" + canary

// recorder is an httptest server that records every posted body and can be told to fail.
type recorder struct {
	srv *httptest.Server

	mu      sync.Mutex
	bodies  []string
	headers []http.Header

	// failFrom, when positive, makes the nth request and every one after it fail with
	// failStatus and failBody.
	failFrom   int
	failStatus int
	failBody   string
}

func newRecorder(t *testing.T) *recorder {
	t.Helper()
	r := &recorder{failStatus: http.StatusInternalServerError}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		r.bodies = append(r.bodies, string(body))
		r.headers = append(r.headers, req.Header.Clone())
		n := len(r.bodies)
		failFrom, status, failBody := r.failFrom, r.failStatus, r.failBody
		r.mu.Unlock()

		if failFrom > 0 && n >= failFrom {
			w.WriteHeader(status)
			_, _ = io.WriteString(w, failBody)
			return
		}
		_, _ = io.WriteString(w, "ok")
	}))
	t.Cleanup(r.srv.Close)
	return r
}

// webhook is the full webhook URL pointing at the recorder, credential tail and all.
func (r *recorder) webhook() string { return r.srv.URL + webhookPath }

func (r *recorder) posted() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.bodies...)
}

func (r *recorder) header(i int) http.Header {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.headers[i]
}

// text decodes the {"text": "..."} payload of the i-th recorded post.
func (r *recorder) text(t *testing.T, i int) string {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal([]byte(r.posted()[i]), &payload); err != nil {
		t.Fatalf("post %d is not JSON (%v): %s", i, err, r.posted()[i])
	}
	got, ok := payload["text"].(string)
	if !ok {
		t.Fatalf("post %d has no string \"text\" field: %s", i, r.posted()[i])
	}
	return got
}

func issue(id, title, itemURL string) domain.Item {
	return domain.Item{
		Source: "github", ExternalID: "github:" + id, Kind: domain.KindIssue,
		Repo: "org/repo", Number: 1, Title: title, URL: itemURL, State: "open",
	}
}

// FR-6.1 AC1: a refresh posts one message per newly first-seen item, and the message names the
// item and links to it — a notification the reader cannot act on is not a notification.
func TestNotifyPostsOneMessagePerItem(t *testing.T) {
	rec := newRecorder(t)
	n := New(rec.webhook(), rec.srv.Client())

	items := []domain.Item{
		issue("1", "Flaky test on main", "https://github.com/org/repo/issues/1"),
		{
			Source: "github", ExternalID: "github:2", Kind: domain.KindPR,
			Repo: "org/repo", Number: 2, Title: "Bump deps", URL: "https://github.com/org/repo/pull/2",
		},
	}
	if err := n.Notify(context.Background(), items); err != nil {
		t.Fatalf("Notify: %v", err)
	}

	posts := rec.posted()
	if len(posts) != len(items) {
		t.Fatalf("posted %d messages, want one per item (%d): %q", len(posts), len(items), posts)
	}
	for i, it := range items {
		got := rec.text(t, i)
		if !strings.Contains(got, it.Title) {
			t.Errorf("message %d = %q, want it to name the item title %q", i, got, it.Title)
		}
		if !strings.Contains(got, it.URL) {
			t.Errorf("message %d = %q, want it to carry the item URL %q", i, got, it.URL)
		}
	}
	if got := rec.header(0).Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	// The kind is what tells an issue from a pull request at a glance.
	if !strings.Contains(strings.ToLower(rec.text(t, 1)), "pull request") {
		t.Errorf("pull-request message = %q, want it to say what kind of item it is", rec.text(t, 1))
	}
}

// Nothing to announce means no request at all: a refresh that found nothing new must be silent.
func TestNotifyWithoutItemsPostsNothing(t *testing.T) {
	rec := newRecorder(t)
	n := New(rec.webhook(), rec.srv.Client())

	if err := n.Notify(context.Background(), nil); err != nil {
		t.Fatalf("Notify(nil): %v", err)
	}
	if got := len(rec.posted()); got != 0 {
		t.Errorf("posted %d messages for no items, want 0", got)
	}
}

// The judgment call this package makes about a batch that fails midway.
//
// The runner marks an item notified only when it was actually sent, and with a one-error
// interface "sent" can only mean "the whole batch went". So a batch that fails partway is retried
// whole on the next run — which makes carrying on after the first failure pure cost: the usual
// cause is the endpoint, not the message, so every remaining post fails too, each burning a
// request and a slice of the run's 30-second budget (QS-2.5) on a scale-to-zero Machine that must
// not be held awake. Stopping at the first failure costs one request to learn the webhook is
// down, and the next run re-announces everything.
func TestNotifyStopsAtTheFirstFailedPost(t *testing.T) {
	rec := newRecorder(t)
	rec.failFrom = 2
	n := New(rec.webhook(), rec.srv.Client())

	err := n.Notify(context.Background(), []domain.Item{
		issue("1", "first", "https://example.test/1"),
		issue("2", "second", "https://example.test/2"),
		issue("3", "third", "https://example.test/3"),
	})
	if err == nil {
		t.Fatal("Notify returned nil although the webhook rejected a post")
	}
	if got := len(rec.posted()); got != 2 {
		t.Errorf("attempted %d posts, want 2: the batch must stop at the first failure", got)
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error = %q, want it to name the status the webhook answered with", err)
	}
}

// A hung webhook must not outlive the caller's context: the run has a budget, and a request in
// flight keeps the Machine awake (QS-2.5).
func TestNotifyHonoursContextCancellation(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		<-release
	}))
	defer srv.Close()
	defer close(release)

	n := New(srv.URL+webhookPath, srv.Client())
	ctx, cancel := context.WithCancel(context.Background())

	errs := make(chan error, 1)
	go func() { errs <- n.Notify(ctx, []domain.Item{issue("1", "hangs", "https://example.test/1")}) }()
	cancel()

	select {
	case err := <-errs:
		if err == nil {
			t.Fatal("Notify returned nil for a cancelled context")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Notify did not return promptly after its context was cancelled")
	}
}

// QS-4.3: the webhook URL is the credential — anyone holding it can post to the channel — so it
// may never appear in an error, which is what the runner logs. Every way an error can be produced
// is exercised, because the leak only has to happen once.
func TestWebhookURLNeverAppearsInAnError(t *testing.T) {
	rec := newRecorder(t)
	// A webhook that answers 4xx by echoing what was called is not hypothetical: proxies do it,
	// and Slack's own "no_service" replies quote the request.
	rec.failFrom, rec.failStatus = 1, http.StatusNotFound
	rec.failBody = "no_service for " + rec.webhook()

	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := dead.URL + webhookPath
	dead.Close() // nothing listens there any more: net/http quotes the URL it failed to dial

	cases := map[string]struct{ webhook string }{
		"the webhook answers with an error quoting itself": {rec.webhook()},
		"the webhook cannot be reached":                    {deadURL},
		"the webhook URL cannot even be parsed":            {"://" + canary},
		"the webhook URL has an unsupported scheme":        {"gopher://host" + webhookPath},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			n := New(tc.webhook, rec.srv.Client())
			err := n.Notify(context.Background(), []domain.Item{issue("1", "t", "https://example.test/1")})
			if err == nil {
				t.Fatal("Notify returned nil, so this case proves nothing about its error")
			}
			assertNoWebhook(t, tc.webhook, err.Error())
			if !strings.Contains(err.Error(), "[REDACTED") && strings.Contains(name, "quoting itself") {
				t.Errorf("error = %q, want it to mark what it removed", err)
			}
		})
	}
}

// A successful Notify must not smuggle the URL out through a nil error either: the only value
// this package returns is an error, so an empty one is the whole story.
func TestSuccessfulNotifyReturnsNoError(t *testing.T) {
	rec := newRecorder(t)
	n := New(rec.webhook(), rec.srv.Client())
	if err := n.Notify(context.Background(), []domain.Item{issue("1", "t", "https://example.test/1")}); err != nil {
		assertNoWebhook(t, rec.webhook(), err.Error())
		t.Fatalf("Notify: %v", err)
	}
}

// assertNoWebhook fails when text names the webhook, or its credential tail, in any spelling it
// could travel in: raw, query-encoded and path-encoded.
func assertNoWebhook(t *testing.T, webhook, text string) {
	t.Helper()
	secrets := map[string]string{
		"raw webhook":            webhook,
		"query-encoded webhook":  url.QueryEscape(webhook),
		"path-encoded webhook":   url.PathEscape(webhook),
		"raw credential":         canary,
		"query-encoded creden.":  url.QueryEscape(canary),
		"path-encoded credenti.": url.PathEscape(canary),
	}
	for label, form := range secrets {
		if form == "" {
			continue
		}
		if strings.Contains(text, form) {
			t.Errorf("the %s leaked into an error (QS-4.3): %s", label, text)
		}
	}
}

// A title carrying Slack's own markup characters must not be able to rewrite the message around
// it — a title of "<https://evil|click>" would otherwise become a working link.
func TestMessageEscapesSlackMarkup(t *testing.T) {
	rec := newRecorder(t)
	n := New(rec.webhook(), rec.srv.Client())

	if err := n.Notify(context.Background(), []domain.Item{
		issue("1", "fix <script> & \"quotes\"", "https://example.test/a?x=1&y=2"),
	}); err != nil {
		t.Fatalf("Notify: %v", err)
	}

	got := rec.text(t, 0)
	if strings.Contains(got, "<script>") {
		t.Errorf("message = %q, want the title's angle brackets escaped", got)
	}
	if !strings.Contains(got, "&lt;script&gt;") {
		t.Errorf("message = %q, want the escaped title", got)
	}
	// The link target must survive escaping in a form Slack resolves back to the real URL.
	if !strings.Contains(got, "https://example.test/a?x=1&amp;y=2") {
		t.Errorf("message = %q, want the item URL with its ampersand escaped", got)
	}
}

// An item with no URL still gets announced: a notification without a link beats silence.
func TestItemWithoutURLIsStillAnnounced(t *testing.T) {
	rec := newRecorder(t)
	n := New(rec.webhook(), rec.srv.Client())

	if err := n.Notify(context.Background(), []domain.Item{
		{Source: "todoist", ExternalID: "todoist:9", Kind: domain.KindTask, Title: "call the plumber"},
	}); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if got := rec.text(t, 0); !strings.Contains(got, "call the plumber") {
		t.Errorf("message = %q, want the item title", got)
	}
}

// New must be usable without a client of its own, and must not then hang forever on a webhook
// that never answers: a caller that forgets the client would otherwise hold the Machine awake.
func TestNewWithoutAClientStillHasATimeout(t *testing.T) {
	n := New("https://example.invalid"+webhookPath, nil)
	err := n.Notify(context.Background(), []domain.Item{issue("1", "t", "https://example.test/1")})
	if err == nil {
		t.Fatal("Notify to an unresolvable host returned nil")
	}
	assertNoWebhook(t, "https://example.invalid"+webhookPath, err.Error())
}

// The compile-time proof that this adapter is what the runner takes.
var _ ports.Notifier = (*Notifier)(nil)

// The scrubber is tested head-on as well as through Notify: it is the one thing standing between
// the credential and the log, and every spelling it misses is a leak.
func TestScrubberReplacesEverySpellingOfTheWebhook(t *testing.T) {
	webhook := "https://hooks.slack.test" + webhookPath
	scrub := newScrubber(webhook)

	text := "raw " + webhook + " query " + url.QueryEscape(webhook) +
		" tail " + webhookPath + " tail-query " + url.QueryEscape(webhookPath)
	cleaned := scrub.clean(errors.New(text))
	assertNoWebhook(t, webhook, cleaned.Error())
	if !strings.Contains(cleaned.Error(), redacted) {
		t.Errorf("scrubbed text %q does not mark what it removed", cleaned)
	}

	// An error with nothing to hide keeps its wrapping, so errors.Is still works for callers.
	sentinel := errors.New("nothing secret here")
	if got := scrub.clean(sentinel); !errors.Is(got, sentinel) {
		t.Errorf("clean rewrote an error that needed no scrubbing: %v", got)
	}
	// One that did have to be rewritten deliberately drops its cause: an Unwrap still spelling the
	// webhook out would hand the credential straight back.
	leaky := fmt.Errorf("post %s: refused", webhook)
	if got := scrub.clean(leaky); errors.Is(got, leaky) {
		t.Error("clean kept a cause whose own text names the webhook")
	}
	if scrub.clean(nil) != nil {
		t.Error("clean(nil) is not nil")
	}
	// No webhook, nothing to scrub, nothing changed. A path of "/" carries no credential either,
	// and scrubbing it would replace every slash in every error.
	if got := newScrubber("").clean(sentinel); !errors.Is(got, sentinel) {
		t.Errorf("the empty scrubber rewrote %v", got)
	}
	if got := newScrubber("https://hooks.slack.test/").clean(errors.New("a/b")); got.Error() != "a/b" {
		t.Errorf("a webhook with no credential tail scrubbed %q", got)
	}
}
