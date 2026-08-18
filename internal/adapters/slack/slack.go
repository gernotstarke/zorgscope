// Package slack implements ports.Notifier over a Slack incoming webhook: one message per newly
// first-seen item (FR-6.1). A webhook post is a single JSON body to a single URL, so this package
// is written against net/http and encoding/json rather than a client library (no new dependency).
//
// Two rules run through it:
//
//   - The webhook URL is the credential. Anyone holding it can post to the channel, and net/http
//     quotes the URL it dialled in every transport error, so every error leaving this package is
//     scrubbed at the boundary (QS-4.3). Nothing here logs; the runner does, from what it is
//     handed.
//   - A batch stops at the first failed post. The runner marks an item notified only when it was
//     actually sent, and ports.Notifier reports "sent" as a single error, so a batch that fails
//     partway is retried whole on the next run. Carrying on is then pure cost: the usual cause of
//     a failed post is the endpoint rather than the message, so the remaining posts fail too,
//     each burning a request and a slice of the run's 30-second budget on a Machine that must not
//     be held awake (QS-2.5).
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
func New(webhookURL string, hc *http.Client) *Notifier {
	if hc == nil {
		hc = &http.Client{Timeout: defaultTimeout}
	}
	return &Notifier{webhookURL: webhookURL, hc: hc, scrub: newScrubber(webhookURL)}
}

// Notify posts one message per item, in order, and stops at the first failure (see the package
// comment). The returned error names how far the batch got, never the webhook.
//
// Rate limiting is deliberately not handled here. Slack's incoming webhooks allow roughly one
// message per second per hook with a short burst allowance, and this notifier only ever sees items
// the store says have never been announced — on a personal dashboard that is a handful per refresh
// and nothing at all on most runs. The one run that could exceed it is the very first, when every
// stored item is unannounced; a 429 there fails the batch, nothing is marked, and the next run
// retries. Adding backoff would mean sleeping inside a request that keeps a scale-to-zero Machine
// awake, which is the worse trade.
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

// post sends one message. The response body is always closed, and drained on success so the
// connection can be reused for the next item in the batch. Every error returned from here is
// scrubbed by the caller.
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

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		reason, _ := io.ReadAll(io.LimitReader(resp.Body, maxReasonLen))
		return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, strings.TrimSpace(string(reason)))
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxReasonLen))
	return nil
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
		subject = "<" + escape(it.URL) + "|" + title + ">"
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
	case domain.KindTask:
		return "task"
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
func (s scrubber) clean(err error) error {
	if err == nil || s.r == nil {
		return err
	}
	msg := err.Error()
	cleaned := s.r.Replace(msg)
	if cleaned == msg {
		return err
	}
	return errors.New(cleaned)
}
