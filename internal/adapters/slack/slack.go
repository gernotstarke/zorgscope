// Package slack implements ports.Notifier over a Slack incoming webhook: one message per newly
// first-seen item (FR-6.1). A webhook post is a single JSON body to a single URL, so this package
// is written against net/http and encoding/json rather than a client library (no new dependency).
//
// Three rules run through it:
//
//   - The webhook URL is the credential. Anyone holding it can post to the channel, and net/http
//     quotes the URL it dialled in every transport error, so every error leaving this package is
//     scrubbed at the boundary (QS-4.3). Nothing here logs; the runner does, from what it is
//     handed.
//   - A batch stops at the first failed post. Carrying on is pure cost: the usual cause of a
//     failed post is the endpoint rather than the message, so the remaining posts fail too, each
//     burning a request and a slice of the run's 30-second budget on a Machine that must not be
//     held awake (QS-2.5). The runner calls this with one item at a time and records each success
//     as it happens, so stopping early costs only the tail of one run and the next run carries on
//     where this one stopped.
//   - A rejection says whether retrying can help, and the axis is whether the failure is about
//     this message or about the endpoint. A payload Slack has looked at and refused — 400, 413,
//     422 — will be refused identically forever, so the error is marked permanent (see permanent)
//     and the runner records the item as announced rather than letting it lead the queue for
//     good. Everything else is retryable and leaves the item unannounced for the next run: a
//     revoked or moved hook (401, 403, 404, 410), a 429, a 5xx, a timeout, a refused redirect, a
//     200 whose body is not Slack's "ok". Those answer the same way for every message, so
//     swallowing one item per run would drain the whole backlog while an operator was still
//     working out that the URL needs rotating.
//
// A post is never allowed to be redirected: 301, 302 and 303 turn a POST into a GET and drop the
// body, so a followed redirect would answer 2xx for a message that was never delivered — and the
// runner would then mark it announced. See refuseRedirect.
package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/gernotstarke/zorgscope/internal/domain"
)

// redacted stands in for the webhook wherever it would otherwise have appeared.
const redacted = "[REDACTED WEBHOOK]"

// defaultTimeout bounds one post when the caller supplies no client of its own. A notifier with no
// timeout at all could hang on an unresponsive webhook, and Fly does not stop a Machine with a
// request in flight; the caller's context is the real bound, this is the backstop.
const defaultTimeout = 10 * time.Second

// maxReasonLen caps how much of a rejection body is quoted back in an error. Slack answers with a
// short code such as "no_service", but a proxy in between can answer with a page of HTML, and that
// error text is logged.
const maxReasonLen = 200

// Notifier posts one Slack message per item to an incoming webhook.
type Notifier struct {
	webhookURL string
	hc         *http.Client
	scrub      scrubber
}

// New builds a Notifier posting to webhookURL. hc supplies the transport (as in this package's
// tests, which pass an httptest server's client); a nil hc gets a client with defaultTimeout, so a
// caller that forgets one cannot hang forever.
//
// A supplied client is copied rather than used as it is, because this notifier owns its redirect
// policy and must not inherit one someone else set — the shared client the main wiring passes is
// also the one every fetcher uses. The copy shares the transport, so connection pooling is
// unaffected.
func New(webhookURL string, hc *http.Client) *Notifier {
	c := &http.Client{Timeout: defaultTimeout}
	if hc != nil {
		clone := *hc
		c = &clone
	}
	c.CheckRedirect = refuseRedirect
	return &Notifier{webhookURL: webhookURL, hc: c, scrub: newScrubber(webhookURL)}
}

// refuseRedirect stops net/http from following a redirect from the webhook.
//
// The default policy follows up to ten, and for 301, 302 and 303 it rewrites the POST to a GET and
// drops the body. The final response can then be a perfectly good 200 for a message that was never
// delivered — and the runner, seeing a nil error, marks the item announced and never sends it
// again. That is the one direction the ordering rule refuses, so a webhook that redirects is
// treated as a failure instead. It names no URL: the error is wrapped by net/http, which quotes the
// webhook, and everything from here is scrubbed anyway.
func refuseRedirect(*http.Request, []*http.Request) error {
	return errors.New("refusing to follow a redirect: the webhook must accept the POST itself")
}

// Notify posts one message per item, in order, and stops at the first failure (see the package
// comment). The returned error names how far the batch got, never the webhook.
//
// Rate limiting is deliberately not handled here. Slack's incoming webhooks allow roughly one
// message per second per hook with a short burst allowance, and this notifier only ever sees items
// the store says have never been announced — on a personal dashboard that is a handful per refresh
// and nothing at all on most runs. The one run that could exceed it is the very first, when every
// stored item is unannounced; a 429 there stops the run's announcements, and because the runner
// has recorded everything that did go out, the next run picks up where this one stopped instead of
// starting over. Adding backoff would mean sleeping inside a request that keeps a scale-to-zero
// Machine awake, which is the worse trade.
func (n *Notifier) Notify(ctx context.Context, items []domain.Item) error {
	for i, it := range items {
		if err := n.post(ctx, message(it)); err != nil {
			return n.scrub.clean(fmt.Errorf("slack: posting item %d of %d (%s): %w",
				i+1, len(items), it.ExternalID, err))
		}
	}
	return nil
}

// payload is Slack's incoming-webhook body in its simplest form.
type payload struct {
	Text string `json:"text"`
}

// okBody is what Slack answers a delivered message with. Anything else at 200 — a captive
// portal's login page, a proxy's HTML error — is not a delivery, and treating it as one would
// have the runner mark the item announced.
const okBody = "ok"

// post sends one message. The response body is always closed, and read up to maxReasonLen so the
// connection can be reused for the next item in the batch. Every error returned from here is
// scrubbed by the caller.
//
// A failure is classified as it is produced: see permanent for what that distinction buys.
func (n *Notifier) post(ctx context.Context, text string) error {
	body, err := json.Marshal(payload{Text: text})
	if err != nil {
		return fmt.Errorf("encoding message: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.webhookURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := n.hc.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	answer, _ := io.ReadAll(io.LimitReader(resp.Body, maxReasonLen))
	reason := strings.TrimSpace(string(answer))

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		rejected := fmt.Errorf("unexpected status %d: %s", resp.StatusCode, reason)
		if isPermanentStatus(resp.StatusCode) {
			return permanent{rejected}
		}
		return rejected
	}
	if !strings.EqualFold(reason, okBody) {
		// Retryable on purpose: whatever answered is not Slack, so the message may still be
		// deliverable once whatever sits in between is gone.
		return fmt.Errorf("status %d but the body was %q, not %q: the message was not accepted",
			resp.StatusCode, reason, okBody)
	}
	return nil
}

// isPermanentStatus reports whether a status says that retrying this message cannot help.
//
// The axis is whether the failure is about *this message* or about *the endpoint*, and it is not
// the same as 4xx versus 5xx:
//
//   - Message-level. Slack has looked at this payload and refused it, and it will refuse the
//     identical payload identically forever: 400 (invalid_payload), 413 (too large), 422. Nothing
//     a human does to the webhook changes the answer, so the runner records the item as announced
//     although it never went out — otherwise this one message stands at the head of the queue on
//     every run and suppresses every item behind it for good.
//   - Endpoint-level. The hook is revoked, disabled, moved, or the workspace is gone: 401, 403
//     (action_prohibited), 404 (no_service), 410. These answer the same way for *every* message,
//     so treating them as permanent would mark one more item as announced on every run and quietly
//     drain the whole backlog into nothing while the operator was still working out that the URL
//     needs rotating. They are retryable: the announcements block, visibly — the runner records
//     the failure in the run record's detail on every run — and when the URL is rotated the queue
//     is still there.
//
// Anything else in 4xx defaults to retryable, and so does everything outside it: 429, every 5xx,
// every transport error, timeout and cancellation, and a refused redirect. Between an
// undeliverable message blocking the queue until somebody looks and items being destroyed
// silently, blocking is the recoverable failure, so it is the right default for a code that
// cannot be classified confidently. A new code belongs in the permanent list only if Slack is
// rejecting the payload itself.
func isPermanentStatus(code int) bool {
	switch code {
	case http.StatusBadRequest, http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity:
		return true
	default:
		return false
	}
}

// permanent marks an error that retrying cannot fix.
//
// The runner has to tell the two apart to decide whether an item may be recorded as announced, but
// internal/refresh must not import an adapter to do so: it discovers the distinction through the
// anonymous interface this method satisfies, the way net's callers read Timeout(). The marker is
// preserved through scrubbing (see scrubber.clean), because the error the runner sees is always
// the scrubbed one.
type permanent struct{ err error }

func (e permanent) Error() string { return e.err.Error() }
func (e permanent) Unwrap() error { return e.err }

// PermanentNotifyFailure reports that this message will be rejected again on every retry.
func (e permanent) PermanentNotifyFailure() bool { return true }

// isPermanent reports whether err carries the permanent marker anywhere in its chain.
func isPermanent(err error) bool {
	var p interface{ PermanentNotifyFailure() bool }
	return errors.As(err, &p) && p.PermanentNotifyFailure()
}

// message renders one item as Slack mrkdwn: what it is, where it lives, and a link to it. An item
// with no URL is still announced — a notification without a link beats silence.
func message(it domain.Item) string {
	title := escape(it.Title)
	if title == "" {
		title = "(untitled)"
	}
	where := ""
	if it.Repo != "" {
		where = " in " + escape(it.Repo)
	}
	subject := title
	if it.URL != "" {
		subject = "<" + linkTarget(it.URL) + "|" + title + ">"
	}
	return fmt.Sprintf("New %s%s: %s", label(it.Kind), where, subject)
}

// label names an item's kind in words a reader recognises.
func label(k domain.Kind) string {
	switch k {
	case domain.KindIssue:
		return "issue"
	case domain.KindPR:
		return "pull request"
	default:
		return "item"
	}
}

// escape makes s safe to drop into Slack's mrkdwn. Slack asks for exactly these three characters
// to be escaped, and the ampersand has to go first or it would double-escape the two it produces.
// It is not cosmetic: a title of "<https://elsewhere|click here>" would otherwise render as a
// working link to somewhere the item is not.
func escape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	return strings.ReplaceAll(s, ">", "&gt;")
}

// linkTarget escapes a URL for the target half of Slack's <url|label> link syntax.
//
// On top of the three mrkdwn characters, the pipe has to go: Slack splits the link at the *first*
// one, so a URL carrying a pipe — a search query parameter with a list in it — would have its
// target truncated at the pipe and the rest folded into the label, producing a link that goes
// somewhere else. Percent-encoding it is lossless: the server decodes %7C back to a pipe.
func linkTarget(u string) string {
	return strings.ReplaceAll(escape(u), "|", "%7C")
}

// scrubber removes the webhook from error text.
//
// This package's own error strings never contain it, but net/http's do: every transport error is a
// *url.Error, whose message is `Post "<the whole webhook>": …`, and %w carries that text out. From
// there the path is short — internal/refresh logs what Notify returns — and the log of a personal
// dashboard is not where a credential that lets anyone post to the channel belongs (QS-4.3).
// Nothing above this layer holds the webhook, so nothing above it can redact the webhook; this
// layer does.
//
// Every spelling the URL can travel in is replaced: the raw URL, its percent-encoded forms, and
// the same three for the path alone — the part that actually is the secret, and the part a
// rejection body is most likely to echo back on its own.
type scrubber struct{ r *strings.Replacer }

// newScrubber builds the scrubber for one webhook URL. The zero scrubber — for an empty URL — is a
// no-op, because there is nothing to hide.
func newScrubber(webhookURL string) scrubber {
	var forms []string
	add := func(s string) {
		if s == "" {
			return
		}
		for _, spelling := range []string{s, url.QueryEscape(s), url.PathEscape(s)} {
			if !slices.Contains(forms, spelling) {
				forms = append(forms, spelling)
			}
		}
	}
	add(webhookURL)
	// A path of "" or "/" carries no credential, and replacing "/" everywhere would mangle every
	// error this package ever returns.
	if u, err := url.Parse(webhookURL); err == nil && len(u.Path) > 1 {
		tail := u.Path
		if u.RawQuery != "" {
			tail += "?" + u.RawQuery
		}
		add(tail)
	}
	if len(forms) == 0 {
		return scrubber{}
	}

	// strings.Replacer matches its patterns in argument order, so the longest has to come first:
	// otherwise the path would be replaced inside the full URL and leave the host and scheme
	// stranded around a marker.
	sort.SliceStable(forms, func(i, j int) bool { return len(forms[i]) > len(forms[j]) })
	pairs := make([]string, 0, 2*len(forms))
	for _, f := range forms {
		pairs = append(pairs, f, redacted)
	}
	return scrubber{r: strings.NewReplacer(pairs...)}
}

// clean returns err with every spelling of the webhook replaced by redacted.
//
// When nothing had to be replaced — the ordinary case — the error comes back untouched, wrapping
// and all. When something did, the wrapping is deliberately dropped: an error whose text is clean
// but whose wrapped cause still spells the webhook out would hand the credential back to anyone
// who called errors.Unwrap. Nothing in this repository matches on a notifier error's cause.
//
// The one thing carried across that rewrite is the permanent marker: it is not information about
// the webhook but about the message, the runner reads it off the error it is handed, and a
// rejection that lost its marker on the way through the scrubber would quietly become retryable —
// so a poison message would again block everything behind it, but only for the errors that had
// something to hide.
func (s scrubber) clean(err error) error {
	if err == nil || s.r == nil {
		return err
	}
	msg := err.Error()
	cleaned := s.r.Replace(msg)
	if cleaned == msg {
		return err
	}
	if isPermanent(err) {
		return permanent{errors.New(cleaned)}
	}
	return errors.New(cleaned)
}
