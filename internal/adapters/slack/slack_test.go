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
// Carrying on after the first failure is pure cost: the usual cause is the endpoint, not the
// message, so every remaining post fails too, each burning a request and a slice of the run's
// 30-second budget (QS-2.5) on a scale-to-zero Machine that must not be held awake. Stopping
// costs one request to learn the webhook is down. It is affordable only because the runner sends
// one item per call and records each success as it happens, so what stops here is the tail of one
// run rather than every announcement the run owed — see Runner.notify.
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
		{Source: "github", ExternalID: "9", Kind: domain.KindIssue, Title: "call the plumber"},
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

// A followed redirect is the one failure that looks like a success. net/http follows up to ten by
// default and rewrites POST to GET for 301, 302 and 303 — dropping the body — so the final 200
// would report a message that was never delivered, and the runner would mark the item announced
// and never send it again.
func TestRedirectsAreRefused(t *testing.T) {
	final := newRecorder(t)
	away := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, final.srv.URL+webhookPath, http.StatusFound)
	}))
	t.Cleanup(away.Close)

	webhook := away.URL + webhookPath
	n := New(webhook, away.Client())
	err := n.Notify(context.Background(), []domain.Item{issue("1", "t", "https://example.test/1")})
	if err == nil {
		t.Fatal("Notify followed a redirect and reported success: the message was never delivered")
	}
	if got := len(final.posted()); got != 0 {
		t.Errorf("the redirect target received %d requests, want 0", got)
	}
	assertNoWebhook(t, webhook, err.Error())
	// A misconfigured or redirecting endpoint may recover; the item must stay unannounced.
	if isPermanent(err) {
		t.Error("a refused redirect was classified permanent; it must be retried, not swallowed")
	}
}

// Slack answers a delivered message with the body "ok". A 200 from anything else — a captive
// portal, a proxy's error page — is not a delivery, and counting it as one would mark the item
// announced and drop the notification for good.
func TestA200WithoutSlacksOkBodyIsAFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "<html>sign in to the guest network</html>")
	}))
	t.Cleanup(srv.Close)

	n := New(srv.URL+webhookPath, srv.Client())
	err := n.Notify(context.Background(), []domain.Item{issue("1", "t", "https://example.test/1")})
	if err == nil {
		t.Fatal("Notify accepted a 200 whose body was not Slack's \"ok\"")
	}
	if isPermanent(err) {
		t.Error("whatever answered is not Slack, so the message may still be deliverable: retryable")
	}
	assertNoWebhook(t, srv.URL+webhookPath, err.Error())
}

// A URL carrying a pipe must not truncate the link: Slack splits <url|label> at the first pipe, so
// an unescaped one would send the reader somewhere the item is not.
func TestAPipeInTheItemURLDoesNotTruncateTheLink(t *testing.T) {
	rec := newRecorder(t)
	n := New(rec.webhook(), rec.srv.Client())

	itemURL := "https://example.test/search?q=today%20|%20overdue"
	if err := n.Notify(context.Background(), []domain.Item{issue("1", "piped", itemURL)}); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	got := rec.text(t, 0)
	target, _, ok := strings.Cut(strings.TrimPrefix(got[strings.Index(got, "<"):], "<"), "|")
	if !ok {
		t.Fatalf("message = %q, want a <url|label> link", got)
	}
	if strings.Contains(target, "|") {
		t.Fatalf("link target = %q still contains a raw pipe", target)
	}
	if target != strings.ReplaceAll(itemURL, "|", "%7C") {
		t.Errorf("link target = %q, want the whole URL with its pipe percent-encoded", target)
	}
}

// The classification the runner reads to decide whether an item may be recorded as announced.
//
// The axis is message-level versus endpoint-level, not 4xx versus 5xx. Getting it wrong is
// expensive in both directions, and asymmetrically so: too retryable and one message Slack will
// never accept blocks every item behind it on every run, which a human can still fix; too
// permanent and a revoked hook quietly marks one more item announced per run until the whole
// backlog has been destroyed, which nobody can fix.
func TestRejectionsAreClassifiedRetryableOrPermanent(t *testing.T) {
	cases := []struct {
		status        int
		wantPermanent bool
		why           string
	}{
		// Message-level: Slack looked at this payload and refused it.
		{http.StatusBadRequest, true, "invalid_payload: this message, on every run"},
		{http.StatusRequestEntityTooLarge, true, "the message is too large and always will be"},
		{http.StatusUnprocessableEntity, true, "the payload cannot be processed"},
		// Endpoint-level: the hook is revoked, disabled or gone, which is true of every message.
		{http.StatusUnauthorized, false, "the webhook needs rotating, not the message rewriting"},
		{http.StatusForbidden, false, "action_prohibited: the hook, not the payload"},
		{http.StatusNotFound, false, "no_service: a rotated URL brings the queue back"},
		{http.StatusGone, false, "the channel or workspace is gone; the items are not"},
		{http.StatusTeapot, false, "an unknown 4xx blocks rather than destroys"},
		// Everything else.
		{http.StatusTooManyRequests, false, "rate limited: the message is fine"},
		{http.StatusInternalServerError, false, "Slack is having a bad day"},
		{http.StatusBadGateway, false, "something in between is having a bad day"},
		{http.StatusServiceUnavailable, false, "temporary by definition"},
	}
	for _, tc := range cases {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			rec := newRecorder(t)
			rec.failFrom, rec.failStatus, rec.failBody = 1, tc.status, "rejected"
			n := New(rec.webhook(), rec.srv.Client())

			err := n.Notify(context.Background(), []domain.Item{issue("1", "t", "https://example.test/1")})
			if err == nil {
				t.Fatalf("Notify returned nil for status %d", tc.status)
			}
			if got := isPermanent(err); got != tc.wantPermanent {
				t.Errorf("status %d classified permanent=%v, want %v: %s",
					tc.status, got, tc.wantPermanent, tc.why)
			}
		})
	}
}

// The marker has to survive scrubbing, because the runner only ever sees the scrubbed error: a
// rejection that lost it on the way out would silently become retryable and block the queue again.
func TestThePermanentMarkerSurvivesScrubbing(t *testing.T) {
	rec := newRecorder(t)
	// A webhook that echoes what was called is what forces the scrubber to rewrite the error.
	rec.failFrom, rec.failStatus = 1, http.StatusBadRequest
	rec.failBody = "invalid_payload for " + rec.webhook()
	n := New(rec.webhook(), rec.srv.Client())

	err := n.Notify(context.Background(), []domain.Item{issue("1", "t", "https://example.test/1")})
	if err == nil {
		t.Fatal("Notify returned nil for a 400")
	}
	assertNoWebhook(t, rec.webhook(), err.Error())
	if !strings.Contains(err.Error(), redacted) {
		t.Fatalf("error = %q was not rewritten, so this proves nothing about the marker", err)
	}
	if !isPermanent(err) {
		t.Error("the permanent marker was lost while the error was scrubbed")
	}
}

// The notifier owns its redirect policy, and the client it is handed is the one every fetcher
// shares: setting the policy on that client would change how every other adapter behaves.
func TestNewDoesNotMutateTheClientItIsGiven(t *testing.T) {
	shared := &http.Client{Timeout: time.Minute}
	_ = New("https://hooks.slack.test"+webhookPath, shared)
	if shared.CheckRedirect != nil {
		t.Error("New set a redirect policy on the caller's client")
	}
}
