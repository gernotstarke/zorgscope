// These tests are in package web for the same reason the auth tests are: the property that matters
// is "only the pages loadDocs published are reachable", and asserting it over a list a future
// author must remember to extend is not the same assertion. They read s.docs itself.
package web

import (
	"net/http"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// FR-7.1 AC1 — /docs lists requirements, decisions and concepts.
func TestDocsIndexListsTheThreeCategories(t *testing.T) {
	body := get(newTestServer(t).Handler(), "/docs").Body.String()
	for _, want := range []string{"Requirements", "Decisions", "Concepts"} {
		if !strings.Contains(body, want) {
			t.Errorf("index does not mention %q (FR-7.1 AC1)", want)
		}
	}
}

// The index is the only way in, so every document has to be on it and every link on it has to
// answer. This is what makes a document that is embedded but unlisted — or listed but unreachable
// — a failure rather than something nobody notices.
func TestDocsIndexLinksToEveryPublishedPage(t *testing.T) {
	s := newTestServer(t)
	h := s.Handler()
	body := get(h, "/docs").Body.String()

	linked := hrefs(body)
	var docLinks []string
	for _, href := range linked {
		if strings.HasPrefix(href, "/docs/") {
			docLinks = append(docLinks, href)
		}
	}

	if len(s.docs) == 0 {
		t.Fatal("no documentation was loaded at all")
	}
	if len(docLinks) != len(s.docs) {
		t.Errorf("index links to %d pages, %d are published", len(docLinks), len(s.docs))
	}
	for _, href := range docLinks {
		if _, ok := s.docs[href]; !ok {
			t.Errorf("index links to %s, which is not a published page", href)
		}
		if code := get(h, href).Code; code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", href, code)
		}
	}
	for url := range s.docs {
		if !contains(docLinks, url) {
			t.Errorf("%s is published but the index does not link to it", url)
		}
	}
}

// FR-7.1 AC2 — each page renders the Markdown from the repository.
func TestDocPageRendersMarkdown(t *testing.T) {
	body := get(newTestServer(t).Handler(), "/docs/requirements/01-goals").Body.String()
	if !strings.Contains(body, "<h1") || !strings.Contains(body, "Goals") {
		t.Error("markdown was not rendered (FR-7.1 AC2)")
	}
	if strings.Contains(body, "# 1. Goals") {
		t.Error("the page shows Markdown source rather than rendered HTML")
	}
}

// The documentation is written in GFM, so the parts of GFM it actually uses have to arrive:
// tables in the requirements, fenced code in the concepts, and heading ids so that a "#section"
// link lands somewhere.
func TestDocPagesRenderTablesAndCodeBlocks(t *testing.T) {
	h := newTestServer(t).Handler()

	stakeholders := get(h, "/docs/requirements/02-stakeholders").Body.String()
	for _, want := range []string{"<table>", "<thead>", "<th", "<td"} {
		if !strings.Contains(stakeholders, want) {
			t.Errorf("02-stakeholders is a GFM table but the output has no %s", want)
		}
	}
	if !strings.Contains(stakeholders, `id="`) {
		t.Error("headings carry no ids, so an in-page link cannot resolve")
	}

	security := get(h, "/docs/concepts/security-and-tokens").Body.String()
	if !strings.Contains(security, "<pre><code") {
		t.Error("security-and-tokens has fenced code blocks but the output has no <pre><code")
	}
}

// The tab title and the index entry come from the document's own first heading, not from its file
// name.
func TestDocPageTitleComesFromTheHeading(t *testing.T) {
	s := newTestServer(t)
	if got := s.docs["/docs/decisions/0004-turso-libsql"].Title; !strings.Contains(got, "Turso") {
		t.Errorf("title = %q, want the document's own h1", got)
	}
	body := get(s.Handler(), "/docs/decisions/0004-turso-libsql").Body.String()
	if !strings.Contains(body, "· zorgscope</title>") {
		t.Error("the page title is not joined to the site name")
	}
}

// FR-7.1 AC3 — the pages need no authentication, and asking for one must not start a session
// either.
func TestDocsNeedNoSession(t *testing.T) {
	h := newTestServer(t).Handler()
	for _, p := range []string{"/docs", "/docs/", "/docs/requirements/01-goals"} {
		t.Run(p, func(t *testing.T) {
			rec := get(h, p)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (FR-7.1 AC3)", rec.Code)
			}
			if got := rec.Header().Get("Set-Cookie"); got != "" {
				t.Errorf("an unauthenticated documentation request set a cookie: %q", got)
			}
			if got := rec.Header().Get("Location"); got != "" {
				t.Errorf("an unauthenticated documentation request redirected to %q", got)
			}
		})
	}
}

// FR-7.2 AC2 — links between documents resolve inside the rendered site. The concept pages link
// both sideways ("security-and-tokens.md") and across a category ("../decisions/0003-….md"), so
// both forms are exercised on real content.
func TestDocsInternalLinksAreRewritten(t *testing.T) {
	s := newTestServer(t)
	body := get(s.Handler(), "/docs/concepts/configuration").Body.String()

	for _, want := range []string{
		`href="/docs/concepts/security-and-tokens"`,
		`href="/docs/decisions/0003-fly-scale-to-zero-external-cron"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("configuration.md does not link to %s", want)
		}
	}
	if strings.Contains(body, `href="security-and-tokens.md"`) || strings.Contains(body, "../decisions/") {
		t.Error("a raw .md link survived rewriting")
	}
}

// The general form of the test above: across every published page, no href points at a Markdown
// file or climbs a relative path. One page passing is a coincidence; the whole corpus passing is
// the requirement.
func TestNoRenderedPageLinksToAMarkdownFile(t *testing.T) {
	s := newTestServer(t)
	h := s.Handler()
	for _, url := range sortedKeys(s.docs) {
		body := get(h, url).Body.String()
		for _, href := range hrefs(body) {
			if strings.HasPrefix(href, "http://") || strings.HasPrefix(href, "https://") {
				continue // an external link may well name a .md file on someone else's site
			}
			if strings.HasSuffix(href, ".md") || strings.Contains(href, ".md#") {
				t.Errorf("%s links to %q, a Markdown file (FR-7.2 AC2)", url, href)
			}
			if strings.HasPrefix(href, "../") || strings.HasPrefix(href, "./") {
				t.Errorf("%s has the relative link %q, which does not resolve on this site", url, href)
			}
		}
	}
}

// Every internal link has to answer, not merely look like a URL. A rewrite that produced a
// plausible-looking /docs/… for a document this site does not publish would pass the test above
// and hand the visitor a 404.
func TestEveryInternalLinkResolves(t *testing.T) {
	s := newTestServer(t)
	h := s.Handler()
	for _, url := range sortedKeys(s.docs) {
		for _, href := range hrefs(get(h, url).Body.String()) {
			target, _, _ := strings.Cut(href, "#")
			if !strings.HasPrefix(target, "/docs/") {
				continue
			}
			if code := get(h, target).Code; code != http.StatusOK {
				t.Errorf("%s links to %s, which answers %d", url, target, code)
			}
		}
	}
}

// docs/superpowers holds the working plans of the build, which several documents link to and this
// site deliberately does not publish. Such a link keeps its text and loses its anchor: it must not
// become a /docs URL that 404s, and it must not become a hint that there is more to fetch.
func TestALinkToAnUnpublishedDocumentIsNotALink(t *testing.T) {
	h := newTestServer(t).Handler()
	body := get(h, "/docs/concepts/security-and-tokens").Body.String()

	if !strings.Contains(body, "implementation plan") {
		t.Fatal("the sentence this test is about is no longer in the document")
	}
	for _, unwanted := range []string{"superpowers", ".md"} {
		for _, href := range hrefs(body) {
			if strings.Contains(href, unwanted) {
				t.Errorf("an unpublished document is still linked: %q", href)
			}
		}
	}
	if code := get(h, "/docs/superpowers/plans/HANDOVER").Code; code != http.StatusNotFound {
		t.Errorf("/docs/superpowers/plans/HANDOVER = %d, want 404", code)
	}
}

// A path that names no published page is a 404 — including the ones that try to leave /docs. The
// handler looks a page up by its exact URL in a map built at start-up, so there is no path to
// sanitise and no file system to reach; canonicalPath refuses the rest before the mux sees it.
func TestUnknownDocIs404NotAPathTraversal(t *testing.T) {
	h := newTestServer(t).Handler()
	for _, p := range []string{
		"/docs/requirements/nope",
		"/docs/../../etc/passwd",
		"/docs/requirements/../../../go.mod",
		"/docs/%2e%2e/%2e%2e/etc/passwd",
		"/docs/requirements/01-goals/../../../go.mod",
		"/docs//requirements/01-goals",
	} {
		t.Run(p, func(t *testing.T) {
			rec := get(h, p)
			if rec.Code != http.StatusNotFound {
				t.Errorf("%s = %d, want 404 (Location: %q)", p, rec.Code, rec.Header().Get("Location"))
			}
			if body := rec.Body.String(); strings.Contains(body, "passwd") || strings.Contains(body, "go.mod") {
				t.Errorf("the 404 body echoes the request: %q", body)
			}
		})
	}
}

// The stronger statement: a URL answers 200 if and only if loadDocs published it. These are the
// shapes a lookup that joined the path onto a directory would answer instead — the source file, a
// directory, the category on its own, a file that is in the repository but not in the corpus.
func TestOnlyPublishedPagesAreServed(t *testing.T) {
	s := newTestServer(t)
	h := s.Handler()
	for _, p := range []string{
		"/docs/requirements/01-goals.md",
		"/docs/requirements/",
		"/docs/requirements",
		"/docs/decisions/README.md",
		"/docs/logo/zorgscope-logo.jpeg",
		"/docs/go.mod",
		"/docs/../go.mod",
		"/docs/requirements/01-goals/",
	} {
		t.Run(p, func(t *testing.T) {
			if _, published := s.docs[p]; published {
				t.Skip("this path is a published page after all")
			}
			if code := get(h, p).Code; code != http.StatusNotFound {
				t.Errorf("%s = %d, want 404", p, code)
			}
		})
	}
}

// QS-4.4 — the pages are served under a Content-Security-Policy with no 'unsafe-inline', so
// rendered Markdown may not carry an inline style, an inline script or an event handler. goldmark
// emits none of its own; this asserts it over the corpus, which is where a future document would
// arrive.
func TestRenderedDocsCarryNoInlineStyleOrScript(t *testing.T) {
	s := newTestServer(t)
	h := s.Handler()
	onAttr := regexp.MustCompile(`(?i)<[a-z][^>]*\son[a-z]+\s*=`)
	for _, url := range sortedKeys(s.docs) {
		body := docBody(get(h, url).Body.String())
		for _, unwanted := range []string{"<script", "<style", "style=", "javascript:"} {
			if strings.Contains(strings.ToLower(body), unwanted) {
				t.Errorf("%s contains %q, which the CSP forbids", url, unwanted)
			}
		}
		if m := onAttr.FindString(body); m != "" {
			t.Errorf("%s has an inline event handler: %q", url, m)
		}
	}
}

// The renderer is treated as untrusted output even though the documents are ours: raw HTML in a
// source file is dropped rather than passed through, so a document can never introduce markup into
// an unauthenticated page. This exercises input the repository does not contain, which is the
// point — it is the guard for the document nobody has written yet.
func TestRawHTMLInMarkdownIsNotEmitted(t *testing.T) {
	src := []byte(strings.Join([]string{
		"# Title",
		"",
		"<script>alert(1)</script>",
		"",
		`<div style="background:red" onclick="steal()">block</div>`,
		"",
		`Inline <b onmouseover="x">bold</b> and <img src=x onerror=alert(1)>.`,
		"",
		"[bad](javascript:alert(1))",
		"",
	}, "\n"))

	_, body, err := renderDoc(newDocMarkdown(), src, "docs/concepts", map[string]string{})
	if err != nil {
		t.Fatalf("renderDoc() error = %v", err)
	}
	got := strings.ToLower(string(body))
	for _, unwanted := range []string{"<script", "<div", "<b ", "<img", "style=", "onclick", "onerror", "onmouseover", "javascript:"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("raw HTML reached the output: %q is present in\n%s", unwanted, body)
		}
	}
	if !strings.Contains(got, "<h1") {
		t.Error("the safe part of the document did not render")
	}
}

// A column alignment has to survive as an align attribute. goldmark's HTML5 default is an inline
// style attribute, which style-src 'self' without 'unsafe-inline' blocks outright — the column
// would quietly lose its alignment in every browser. The published documents happen to use no
// alignment today, so this states the rule on input of its own.
func TestTableAlignmentIsAnAttributeNotAnInlineStyle(t *testing.T) {
	src := []byte("| left | middle | right |\n|:-----|:------:|------:|\n| a | b | c |\n")

	_, body, err := renderDoc(newDocMarkdown(), src, "docs/concepts", map[string]string{})
	if err != nil {
		t.Fatalf("renderDoc() error = %v", err)
	}
	got := string(body)
	if strings.Contains(got, "style=") {
		t.Errorf("an aligned table emitted an inline style, which the CSP blocks:\n%s", got)
	}
	//nolint:misspell // "center" is HTML's spelling of the attribute value, not prose.
	if !strings.Contains(got, `align="center"`) || !strings.Contains(got, `align="right"`) {
		t.Errorf("alignment was not rendered as an attribute:\n%s", got)
	}
}

// QS-4.3 — /docs is unauthenticated, so whatever it renders is public. The risk on these pages is
// not a secret but a leaked internal: an error, a stack trace or a build path reaching the visitor
// instead of the log. Nothing the site returns under /docs may look like one.
//
// The documents themselves talk about module caches and library names, which is why this asks for
// the shapes only a runtime error produces — a Go source position, a goroutine dump, the checkout
// path inside the build container — rather than for keywords.
func TestDocsExposeNoErrorsOrBuildPaths(t *testing.T) {
	s := newTestServer(t)
	h := s.Handler()
	sourcePosition := regexp.MustCompile(`[a-z_]+\.go:[0-9]+`)

	pages := []string{"/docs", "/docs/", "/docs/nope", "/docs/../../etc/passwd"}
	pages = append(pages, sortedKeys(s.docs)...)
	for _, p := range pages {
		body := get(h, p).Body.String()
		if m := sourcePosition.FindString(body); m != "" {
			t.Errorf("%s shows a Go source position: %q", p, m)
		}
		for _, unwanted := range []string{"panic:", "goroutine 1 [", "/src/internal/", "/usr/local/go/"} {
			if strings.Contains(body, unwanted) {
				t.Errorf("%s shows %q", p, unwanted)
			}
		}
	}
}

// docLinkTarget is the whole of the rewriting rule, and it is worth stating as a table: the cases
// that should be left alone are as load-bearing as the ones that should change.
func TestDocLinkTarget(t *testing.T) {
	sources := map[string]string{
		"docs/requirements/01-goals.md":    "/docs/requirements/01-goals",
		"docs/decisions/0001-adr.md":       "/docs/decisions/0001-adr",
		"docs/concepts/security.md":        "/docs/concepts/security",
		"docs/concepts/data-storage.md":    "/docs/concepts/data-storage",
		"docs/requirements/06-glossary.md": "/docs/requirements/06-glossary",
	}
	tests := []struct {
		name, dir, dest, want string
		isDoc                 bool
	}{
		{"sibling", "docs/concepts", "security.md", "/docs/concepts/security", true},
		{"across categories", "docs/concepts", "../decisions/0001-adr.md", "/docs/decisions/0001-adr", true},
		{"with a fragment", "docs/concepts", "../requirements/01-goals.md#vision", "/docs/requirements/01-goals#vision", true},
		{"unpublished", "docs/concepts", "../superpowers/plans/plan.md", "", true},
		{"escaping the corpus", "docs/concepts", "../../../go.mod.md", "", true},
		{"external", "docs/concepts", "https://example.com/a.md", "", false},
		{"mailto", "docs/concepts", "mailto:someone@example.com", "", false},
		{"in-page anchor", "docs/concepts", "#vision", "", false},
		{"already absolute", "docs/concepts", "/docs/requirements/01-goals", "", false},
		{"not markdown", "docs/concepts", "../logo/logo.svg", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, isDoc := docLinkTarget(tc.dir, tc.dest, sources)
			if isDoc != tc.isDoc || got != tc.want {
				t.Errorf("docLinkTarget(%q, %q) = (%q, %v), want (%q, %v)",
					tc.dir, tc.dest, got, isDoc, tc.want, tc.isDoc)
			}
		})
	}
}

// canonicalPath is a property of every route, not only of /docs: a request for a path no client
// would have sent is answered with 404 rather than a redirect that echoes it back.
func TestNonCanonicalPathsAreRefusedEverywhere(t *testing.T) {
	h := newTestServer(t).Handler()
	for _, p := range []string{"/static/../go.mod", "/healthz/../go.mod", "//healthz", "/login/."} {
		t.Run(p, func(t *testing.T) {
			rec := get(h, p)
			if rec.Code != http.StatusNotFound {
				t.Errorf("%s = %d, want 404 (Location: %q)", p, rec.Code, rec.Header().Get("Location"))
			}
		})
	}
}

// ---------------------------------------------------------------- helpers

var hrefPattern = regexp.MustCompile(`href="([^"]*)"`)

// hrefs returns every href in a rendered page. The tests work on links rather than on the whole
// body because these documents talk about file names in prose and in code spans, and a substring
// search cannot tell that apart from a link.
func hrefs(body string) []string {
	var out []string
	for _, m := range hrefPattern.FindAllStringSubmatch(body, -1) {
		out = append(out, m[1])
	}
	return out
}

// docBody is the part of a page the Markdown produced, without the layout around it. The layout's
// own stylesheet link and script tag are not what these tests are asking about.
func docBody(page string) string {
	_, rest, ok := strings.Cut(page, `<article class="doc">`)
	if !ok {
		return page
	}
	body, _, _ := strings.Cut(rest, "</article>")
	return body
}

func sortedKeys(m map[string]*docPage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
