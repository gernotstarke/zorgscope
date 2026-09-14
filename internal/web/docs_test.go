// These tests are in package web for the same reason the auth tests are: the property that matters
// is "only the pages loadDocs published are reachable", and asserting it over a list a future
// author must remember to extend is not the same assertion. They read s.docs itself.
package web

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path"
	"regexp"
	"sort"
	"strings"
	"testing"
	"testing/fstest"

	zorgscope "github.com/gernotstarke/zorgscope"
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

	linked := linkedURLs(body)
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
		for _, href := range linkedURLs(body) {
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
		for _, href := range linkedURLs(get(h, url).Body.String()) {
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
	// The decisions index is the page that names the design specs, which live under
	// docs/superpowers and are not published. The sentence naming them has to still be there, or
	// this test would pass by having nothing to prove.
	body := get(h, "/docs/decisions/README").Body.String()

	if !strings.Contains(body, "The records are kept current with the design specs") {
		t.Fatal("the sentence this test is about is no longer in the document")
	}
	for _, unwanted := range []string{"superpowers", ".md"} {
		for _, href := range linkedURLs(body) {
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
		// No page here reads a query — handleDocs looks a page up by its path alone — but the
		// rewrite used to drop one silently, which turns what the author wrote into something
		// else with nothing to show for it. It is carried over, ahead of the fragment.
		{"with a query", "docs/concepts", "security.md?print=1", "/docs/concepts/security?print=1", true},
		{"with a query and a fragment", "docs/concepts", "security.md?print=1#tokens", "/docs/concepts/security?print=1#tokens", true},
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

// ---------------------------------------------------------------- what is in the binary

// The narrowed embed in docsfs.go is a security decision, and until this test it was the only one
// with nothing holding it: every test above passes just as well against //go:embed all:docs, which
// would put docs/superpowers — this build's plans, briefs and handovers — and two megabytes of logo
// sources into the binary of an unauthenticated site. Widening the directive is exactly the edit
// that looks like tidying, so the set of embedded files is asserted here rather than assumed.
//
// It walks the file system rather than listing names: a file added to a published category is
// meant to ship (see the auto-publication test below), and a file anywhere else is not.
func TestOnlyTheThreePublishedCategoriesAreEmbedded(t *testing.T) {
	published := make(map[string]bool, len(docCategories))
	for _, c := range docCategories {
		published[path.Join(docsRoot, c.Dir)] = true
	}

	counted := make(map[string]int, len(published))
	err := fs.WalkDir(zorgscope.DocsFS, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		dir := path.Dir(p)
		if !published[dir] {
			t.Errorf("%s is embedded in the binary but belongs to no published category. /docs is "+
				"unauthenticated (FR-7.1 AC3), so everything the embed patterns in docsfs.go name "+
				"is public: widen them and the working notes ship with the site. If this file is "+
				"meant to be published, it needs a category in docCategories, not a wider pattern.", p)
			return nil
		}
		counted[dir]++
		return nil
	})
	if err != nil {
		t.Fatalf("walking the embedded documentation: %v", err)
	}

	// The other direction, so that a pattern narrowed to nothing fails too rather than passing
	// vacuously.
	for dir := range published {
		if counted[dir] == 0 {
			t.Errorf("nothing is embedded under %s, so that category cannot be served at all", dir)
		}
	}

	// The two directories the decision is actually about, named so the failure says what leaked.
	for _, unpublished := range []string{
		"docs/superpowers/plans/HANDOVER.md",
		"docs/logo/zorgscope-logo.jpeg",
	} {
		if _, err := fs.Stat(zorgscope.DocsFS, unpublished); err == nil {
			t.Errorf("%s is in the binary; it is deliberately not part of the published site", unpublished)
		}
	}
}

// The corpus side of the same statement: publication follows from the embed patterns and nothing
// else. Every Markdown file that is in the binary has a page, so there is no third place — an
// allow-list, a front-matter flag, a naming convention — where a document can be embedded and yet
// quietly unreachable.
func TestEveryEmbeddedMarkdownFileIsPublished(t *testing.T) {
	s := newTestServer(t)
	err := fs.WalkDir(zorgscope.DocsFS, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".md") {
			return err
		}
		url := docsPrefix + "/" + strings.TrimSuffix(strings.TrimPrefix(p, docsRoot+"/"), ".md")
		if _, ok := s.docs[url]; !ok {
			t.Errorf("%s is embedded but no page serves it (expected %s)", p, url)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the embedded documentation: %v", err)
	}
}

// Auto-publication is the choice, not an oversight: a .md file dropped into one of the three
// categories is served and linked from the index without being named anywhere in the code. docs/
// is the single source of the project's documentation, so the alternative — an allow-list — would
// be a second place to remember, and forgetting it means a document that exists in the repository
// and silently does not exist on the site. What a visitor may read is decided once, by the embed
// patterns, and the test above is what guards that decision.
//
// The corpus here is synthetic because the real one is fixed at build time: this is the only way
// to ask what happens to a document nobody has written yet.
func TestAMarkdownFileInAPublishedCategoryIsPublishedWithoutBeingListed(t *testing.T) {
	const newFile = "docs/concepts/a-note-nobody-listed.md"
	corpus := fstest.MapFS{
		"docs/requirements/README.md": &fstest.MapFile{Data: []byte("# Requirements\n")},
		"docs/decisions/README.md":    &fstest.MapFile{Data: []byte("# Decisions\n")},
		"docs/concepts/README.md":     &fstest.MapFile{Data: []byte("# Concepts\n")},
		newFile:                       &fstest.MapFile{Data: []byte("# A Note Nobody Listed\n\nA paragraph.\n")},
		// Only Markdown becomes a page; anything else in the directory is embedded and inert.
		"docs/concepts/notes.txt": &fstest.MapFile{Data: []byte("not markdown")},
	}

	pages, index, err := loadDocsFrom(corpus)
	if err != nil {
		t.Fatalf("loadDocsFrom() error = %v", err)
	}

	const want = "/docs/concepts/a-note-nobody-listed"
	p, ok := pages[want]
	if !ok {
		t.Fatalf("%s was not published; the site is meant to publish what docs/ contains, with no "+
			"allow-list to remember a new document into", newFile)
	}
	if p.Title != "A Note Nobody Listed" {
		t.Errorf("title = %q, want the document's own h1", p.Title)
	}
	// Published is not enough: the index is the only way in, so a new document has to be linked
	// as well as reachable.
	var linked bool
	for _, c := range index {
		for _, ip := range c.Pages {
			if ip.URL == want {
				linked = c.Title == "Concepts"
			}
		}
	}
	if !linked {
		t.Errorf("%s is published but does not appear under Concepts on the index", newFile)
	}
	for _, notAPage := range []string{"/docs/concepts/notes", "/docs/concepts/notes.txt"} {
		if _, ok := pages[notAPage]; ok {
			t.Errorf("%s is served, but only Markdown becomes a page", notAPage)
		}
	}
}

// ---------------------------------------------------------------- the index's order

// The index's order is a decision — categories as FR-7.1 AC1 names them, and inside a category the
// README first, because it is that category's own overview, then file-name order, which is the
// numbering the requirements and the decisions already carry. Every other test here compares sets,
// so the ordering was pinned by nothing at all and a rewrite of the sort would have gone unnoticed.
func TestTheIndexIsOrderedReadmeFirstThenByFileName(t *testing.T) {
	s := newTestServer(t)

	var gotCategories []string
	for _, c := range s.docIndex {
		gotCategories = append(gotCategories, c.Title)
	}
	if want := []string{"Requirements", "Decisions", "Concepts"}; !sameOrder(gotCategories, want) {
		t.Errorf("the index shows %v, want %v (FR-7.1 AC1)", gotCategories, want)
	}

	for i, c := range s.docIndex {
		var got []string
		for _, p := range c.Pages {
			got = append(got, p.URL)
		}
		want := expectedOrder(t, docCategories[i].Dir)
		if !sameOrder(got, want) {
			t.Errorf("%s is ordered\n  %v\nwant\n  %v", c.Title, got, want)
		}
	}

	// And the rendered page shows that same sequence, not merely the same pages: the order is only
	// a decision if it survives the template.
	var rendered []string
	for _, u := range linkedURLs(get(s.Handler(), "/docs").Body.String()) {
		if strings.HasPrefix(u, docsPrefix+"/") {
			rendered = append(rendered, u)
		}
	}
	var want []string
	for _, c := range s.docIndex {
		for _, p := range c.Pages {
			want = append(want, p.URL)
		}
	}
	if !sameOrder(rendered, want) {
		t.Errorf("the rendered index links\n  %v\nwant\n  %v", rendered, want)
	}
	// The headings are in that order too, so the groups cannot be drawn in one order and titled
	// in another.
	body := get(s.Handler(), "/docs").Body.String()
	if req, dec, con := strings.Index(body, "Requirements"), strings.Index(body, "Decisions"),
		strings.Index(body, "Concepts"); req > dec || dec > con {
		t.Errorf("the category headings appear at %d, %d, %d; want Requirements, Decisions, Concepts",
			req, dec, con)
	}
}

// expectedOrder states the ordering rule independently of the sort that implements it: the
// category's Markdown files, sorted by name, with the README moved to the front.
func expectedOrder(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := fs.ReadDir(zorgscope.DocsFS, path.Join(docsRoot, dir))
	if err != nil {
		t.Fatalf("reading docs/%s: %v", dir, err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for i, n := range names {
		if n == "README.md" {
			names = append([]string{n}, append(append([]string{}, names[:i]...), names[i+1:]...)...)
			break
		}
	}
	urls := make([]string, 0, len(names))
	for _, n := range names {
		urls = append(urls, docsPrefix+"/"+dir+"/"+strings.TrimSuffix(n, ".md"))
	}
	return urls
}

func sameOrder(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------- images

// FR-7.2 AC2 for the one node type the rewriting used to walk past. An image is not a link a
// reader can decline to follow: a destination that names nothing this site serves is a broken
// picture the moment the page opens, and the stylesheet's own .doc img rule invites documents to
// carry images. A relative destination can never resolve — /docs publishes rendered pages and no
// files at all — so it degrades to its alt text, the same answer a link to an unpublished document
// gets. Destinations this site or another one does serve are left exactly as written.
func TestAnImageThatCannotResolveDegradesToItsAltText(t *testing.T) {
	src := []byte(strings.Join([]string{
		"# Title",
		"",
		"![the project logo](../logo/zorgscope-logo.jpeg)",
		"",
		"![a diagram beside this file](diagram.png)",
		"",
		"![a served asset](/static/logo.png)",
		"",
		"![somebody else's picture](https://example.com/x.png)",
		"",
	}, "\n"))

	_, body, err := renderDoc(newDocMarkdown(), src, "docs/concepts", map[string]string{})
	if err != nil {
		t.Fatalf("renderDoc() error = %v", err)
	}
	got := string(body)

	for _, gone := range []string{"../logo/", "zorgscope-logo.jpeg", "diagram.png"} {
		if strings.Contains(got, gone) {
			t.Errorf("a relative image destination survived: %q is still in\n%s", gone, got)
		}
	}
	for _, text := range []string{"the project logo", "a diagram beside this file"} {
		if !strings.Contains(got, text) {
			t.Errorf("the alt text %q was dropped with the image; it is what the reader is left with", text)
		}
	}
	for _, kept := range []string{`src="/static/logo.png"`, `src="https://example.com/x.png"`} {
		if !strings.Contains(got, kept) {
			t.Errorf("%s was rewritten; a destination this site serves, or somebody else's, is "+
				"left as written:\n%s", kept, got)
		}
	}
}

// ---------------------------------------------------------------- the two edges of canonicalPath

// canonicalPath answers a non-canonical path with 404 instead of a redirect, and after it the
// application has exactly one redirect left: http.ServeMux's own trailing-slash redirect for the
// subtree pattern "GET /static/". "/static" is a canonical path, so canonicalPath passes it
// through, and the mux redirects. It is a 307 rather than the 301 the older mux sent — the pattern
// carries a method, so the redirect preserves it.
//
// That is behaviour, not a defect. The Location is the fixed string "/static/" — it echoes nothing
// the client sent, which is the property canonicalPath exists for — and where it lands is a 404,
// because /static/ names no asset. It is pinned here because it is the one place a redirect can
// still come from, so a change to it should be a decision rather than a surprise.
func TestTheOnlyRedirectLeftIsTheMuxsTrailingSlashOnStatic(t *testing.T) {
	h := newTestServer(t).Handler()

	rec := get(h, "/static")
	if rec.Code != http.StatusTemporaryRedirect {
		t.Fatalf("GET /static = %d, want 307", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/static/" {
		t.Errorf("Location = %q, want %q: a redirect must not echo the request back", got, "/static/")
	}
	// It is still a response of this application's, so it carries the headers every response does.
	if got := rec.Header().Get("Content-Security-Policy"); got != contentSecurityPolicy {
		t.Errorf("the redirect's Content-Security-Policy = %q, want the site's (QS-4.4)", got)
	}
	// And it leads nowhere: the directory itself is not an asset.
	if code := get(h, "/static/").Code; code != http.StatusNotFound {
		t.Errorf("GET /static/ = %d, want 404: the subtree root is not a listing", code)
	}
}

// The other edge: a request whose path is empty. "OPTIONS *" is the one request-target in HTTP that
// is not a path, and cleanPath turns anything that is not already canonical into a 404 — so such a
// request is refused before the mux, where net/http would previously have redirected it.
//
// That is undocumented behaviour rather than a defect: no route in the table answers OPTIONS, so
// the request has nowhere to go either way, and 404 tells that truth without a Location header
// naming a path the client never asked for. It is recorded here because "canonicalPath never
// redirects" is a claim about every request, including the ones that are not paths.
func TestARequestWithNoPathIsRefusedRatherThanRedirected(t *testing.T) {
	h := newTestServer(t).Handler()

	for _, tc := range []struct {
		name string
		req  func() *http.Request
	}{
		{"OPTIONS *", func() *http.Request { return httptest.NewRequest(http.MethodOptions, "*", nil) }},
		{"an empty path", func() *http.Request {
			r := httptest.NewRequest(http.MethodOptions, "/", nil)
			r.URL.Path = ""
			return r
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, tc.req())
			if rec.Code != http.StatusNotFound {
				t.Errorf("status = %d, want 404", rec.Code)
			}
			if got := rec.Header().Get("Location"); got != "" {
				t.Errorf("Location = %q, want none: canonicalPath answers, it does not redirect", got)
			}
		})
	}
}

// ---------------------------------------------------------------- the footer

// FR-7.2 AC1 — the link to the documentation is in the footer of every page, so /docs is reachable
// from anywhere on the site including the sign-in page, which is the only page an anonymous visitor
// sees. It lives in layout.html, which every page wraps itself in; this asserts it on the rendered
// pages rather than on the template, because a page that stopped using the layout would still pass
// a test that read the file.
func TestEveryPageCarriesTheFooterLinkToTheDocs(t *testing.T) {
	s := newTestServer(t)
	h := s.Handler()

	for _, tc := range []struct{ name, body string }{
		{"the sign-in page", get(h, "/login").Body.String()},
		{"the documentation index", get(h, docsPrefix).Body.String()},
		{"a documentation page", get(h, "/docs/requirements/01-goals").Body.String()},
		{"the dashboard", getAuthed(t, h, "/").Body.String()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			footer := footerOf(tc.body)
			if footer == "" {
				t.Fatal("the page has no footer at all (FR-7.2 AC1)")
			}
			if !strings.Contains(footer, `href="`+docsPrefix+`"`) {
				t.Errorf("the footer does not link to %s (FR-7.2 AC1):\n%s", docsPrefix, footer)
			}
		})
	}
}

// footerOf is the page's footer element, or "" when it has none.
func footerOf(page string) string {
	_, rest, ok := strings.Cut(page, "<footer")
	if !ok {
		return ""
	}
	footer, _, ok := strings.Cut(rest, "</footer>")
	if !ok {
		return ""
	}
	return footer
}

// ---------------------------------------------------------------- helpers

var linkedURLPattern = regexp.MustCompile(`(?:href|src)="([^"]*)"`)

// linkedURLs returns every URL a rendered page points at: hrefs and srcs alike. The tests work on
// these rather than on the whole body because these documents talk about file names in prose and
// in code spans, and a substring search cannot tell that apart from a link.
//
// src is in here because href alone was a blind spot: an image is the one thing a document can
// write that fetches a URL without being a link, so every corpus-wide assertion below — no .md
// destination, no relative path, everything internal resolves — used to stop at the <img> tag.
func linkedURLs(body string) []string {
	var out []string
	for _, m := range linkedURLPattern.FindAllStringSubmatch(body, -1) {
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
