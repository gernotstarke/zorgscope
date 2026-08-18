package web

import (
	"bytes"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"

	zorgscope "github.com/gernotstarke/zorgscope"
)

// docsPrefix is the one URL prefix the documentation lives under. Both route-table entries —
// "/docs" and "/docs/" — reach handleDocs, and every page URL is built from this constant, so the
// mount point is written once.
const docsPrefix = "/docs"

// docsRoot is the directory inside DocsFS that the embed patterns rooted the files at. The
// embedded names are repository-relative, so a page's source is docs/<category>/<slug>.md.
const docsRoot = "docs"

// docCategories are the three published categories, in the order the index shows them and with
// the headings FR-7.1 AC1 asks for. A category not named here is not part of the site, whatever
// happens to be embedded.
var docCategories = []struct{ Dir, Title string }{
	{"requirements", "Requirements"},
	{"decisions", "Decisions"},
	{"concepts", "Concepts"},
}

// docPage is one documentation page, rendered once at start-up.
//
// Body is template.HTML, which means the rendering has to be trustworthy rather than merely
// plausible: see newDocMarkdown for why raw HTML in a source document is dropped instead of
// passed through.
type docPage struct {
	// URL is the page's path on this site, and simultaneously its key in Server.docs. A page is
	// reachable if and only if it is in that map (see handleDocs).
	URL string
	// Title is the text of the document's first level-one heading, or its file name when it has
	// none. It is the page heading, the tab title and the index link text.
	Title string
	// Body is the rendered Markdown.
	Body template.HTML
}

// docCategory is one group of the index.
type docCategory struct {
	Title string
	Pages []*docPage
}

// loadDocs renders every embedded document once, at start-up, and returns the lookup used by
// handleDocs together with the grouped index.
//
// Rendering here rather than per request suits a machine that scales to zero: the work happens
// once, while templates are being parsed anyway, on files that cannot change while the process
// runs. It also means a document that fails to render stops the process from starting, rather
// than turning into a 500 that a visitor discovers later.
//
// It runs in two passes because link rewriting needs to know the whole corpus before any of it is
// rendered: a link in the first file may point at the last one.
//
// There is no allow-list of documents anywhere in here, and that is the decision: docs/ is the
// single source, so a Markdown file added to one of the three published categories is published
// and linked from the index the moment it exists. What a visitor may read is therefore decided
// once, by the embed patterns in docsfs.go, rather than twice — in an allow-list that a new
// document has to be remembered into, and that would silently keep it off the site when it is not.
// TestAMarkdownFileInAPublishedCategoryIsPublishedWithoutBeingListed states that as a property,
// and the embed patterns are pinned by TestOnlyTheThreePublishedCategoriesAreEmbedded.
func loadDocs() (map[string]*docPage, []docCategory, error) {
	return loadDocsFrom(zorgscope.DocsFS)
}

// loadDocsFrom is loadDocs against a given corpus. The parameter exists for the tests: the
// published corpus is embedded at build time and cannot be varied, so a synthetic one is the only
// way to state what happens to a document nobody has written yet.
func loadDocsFrom(fsys fs.FS) (map[string]*docPage, []docCategory, error) {
	// sources maps a document's embedded path — "docs/decisions/0004-turso-libsql.md" — to its
	// URL on this site. It is what a rewritten link is resolved against, and it is the complete
	// list of documents this site publishes.
	sources := make(map[string]string)
	files := make(map[string][]string, len(docCategories))
	for _, c := range docCategories {
		dir := path.Join(docsRoot, c.Dir)
		entries, err := fs.ReadDir(fsys, dir)
		if err != nil {
			return nil, nil, err
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			file := path.Join(dir, e.Name())
			files[c.Dir] = append(files[c.Dir], file)
			sources[file] = docsPrefix + "/" + c.Dir + "/" + strings.TrimSuffix(e.Name(), ".md")
		}
		// A category's README is its own overview, so it comes first; everything else follows in
		// file-name order, which is the numbering the requirements and the decisions already use.
		group := files[c.Dir]
		sort.Slice(group, func(i, j int) bool {
			iReadme, jReadme := path.Base(group[i]) == "README.md", path.Base(group[j]) == "README.md"
			if iReadme != jReadme {
				return iReadme
			}
			return group[i] < group[j]
		})
	}

	md := newDocMarkdown()
	pages := make(map[string]*docPage, len(sources))
	index := make([]docCategory, 0, len(docCategories))
	for _, c := range docCategories {
		category := docCategory{Title: c.Title}
		for _, file := range files[c.Dir] {
			src, err := fs.ReadFile(fsys, file)
			if err != nil {
				return nil, nil, err
			}
			title, body, err := renderDoc(md, src, path.Dir(file), sources)
			if err != nil {
				return nil, nil, fmt.Errorf("rendering %s: %w", file, err)
			}
			if title == "" {
				title = path.Base(sources[file])
			}
			p := &docPage{URL: sources[file], Title: title, Body: body}
			pages[p.URL] = p
			category.Pages = append(category.Pages, p)
		}
		index = append(index, category)
	}
	return pages, index, nil
}

// newDocMarkdown builds the renderer used for every page.
//
// Two of its settings are security decisions rather than taste.
//
// Raw HTML stays disabled, which is goldmark's default: a source document's own <script>, <style>
// or style="…" would land in a page served under a Content-Security-Policy that allows none of
// them (QS-4.4), so the browser would refuse it and the page would be quietly wrong. Enabling it
// would also make every future Markdown file a place where markup reaches an unauthenticated page
// — a bigger promise than "we render our own documentation" needs to make. goldmark replaces such
// input with an HTML comment; the three published categories contain no raw HTML, only angle
// brackets inside code spans, which are escaped as text either way.
//
// Table alignment is rendered as an align attribute, not the style attribute goldmark otherwise
// emits for HTML5. An inline style is exactly what style-src 'self' without 'unsafe-inline'
// blocks, so an aligned column would silently lose its alignment in the browser.
//
// The extensions are GFM's four, listed one by one rather than as extension.GFM, because the
// table one has to carry that option.
func newDocMarkdown() goldmark.Markdown {
	return goldmark.New(
		goldmark.WithExtensions(
			extension.NewTable(
				extension.WithTableCellAlignMethod(extension.TableCellAlignAttribute),
			),
			extension.Strikethrough,
			extension.Linkify,
			extension.TaskList,
		),
		// Heading ids let a "…#section" link land where it says it does. They are also the only
		// renderer option this needs: the html renderer's defaults — escaped text, no raw HTML,
		// dangerous URL schemes dropped — are the ones the policy above wants.
		goldmark.WithParserOptions(parser.WithAutoHeadingID()),
	)
}

// renderDoc parses one document, rewrites its links to other documents, and renders it. dir is
// the document's own directory inside the embedded file system, which is what a relative link is
// resolved against.
func renderDoc(md goldmark.Markdown, src []byte, dir string, sources map[string]string) (string, template.HTML, error) {
	doc := md.Parser().Parse(text.NewReader(src))
	rewriteDocLinks(doc, dir, sources)

	var buf bytes.Buffer
	if err := md.Renderer().Render(&buf, src, doc); err != nil {
		return "", "", err
	}
	// The conversion to template.HTML is what newDocMarkdown's settings pay for: this is
	// goldmark's own output, with raw HTML disabled and dangerous URL schemes dropped, so nothing
	// a source document wrote reaches the page as markup.
	//
	// gosec flags every template.HTML conversion (G203), and it is right to: the conversion turns
	// off html/template's escaping, so it is only ever as safe as the thing that produced the
	// string. Here that holds for two reasons, and the annotation below is valid only while both
	// of them do:
	//
	//   - The input is trusted by construction. src comes from zorgscope.DocsFS — the three
	//     directories embedded from docs/ at build time by the //go:embed patterns in docsfs.go.
	//     It is repository content, fixed when the binary was linked. No request, no header, no
	//     form field and no file system reaches this function; there is no path by which a visitor
	//     can influence a byte of it. TestOnlyTheThreePublishedCategoriesAreEmbedded pins that set.
	//   - The output carries no markup of the document's own. newDocMarkdown leaves goldmark's raw
	//     HTML disabled, so a <script>, a <style> or a style="…" written into a source document is
	//     dropped rather than passed through, and dangerous URL schemes are dropped with it. Every
	//     tag in buf is one goldmark emitted itself. TestRawHTMLInMarkdownIsNotEmitted is the
	//     guard, on input the repository does not contain.
	//
	// Both are load-bearing, and either can be taken away from a distance. Adding
	// html.WithUnsafe() to newDocMarkdown — or html.WithXHTML, or any renderer option that lets a
	// source document's own markup through — invalidates this annotation, and so does rendering
	// Markdown here that did not come from the embedded set: a README fetched over HTTP, a
	// document uploaded by a visitor, anything read from disk at run time. In either case the
	// conversion becomes an XSS hole on an unauthenticated page (/docs needs no session, FR-7.1
	// AC3), and this #nosec is no longer telling the truth. Take it out and sanitise instead.
	return firstHeading(doc, src), template.HTML(buf.String()), nil // #nosec G203 -- see above
}

// rewriteDocLinks implements FR-7.2 AC2: a link to another document has to work inside the
// rendered site, not only in a Markdown viewer.
//
// It walks the parsed document rather than substituting strings in the rendered HTML, because
// only the tree knows what is a link: "0001-go-modular-monolith.md" appears in these documents
// inside code spans and prose as well, and a text replacement would rewrite those too.
//
// A destination that resolves to a published document becomes that document's URL. A destination
// that ends in .md but is not published — the working plans under docs/superpowers, which this
// site deliberately does not serve — loses its link and keeps its text, so no page offers a
// visitor a URL that answers 404. Everything else, including absolute and external links, is left
// exactly as written.
//
// Images are walked too, under docImageResolves' stricter rule. An image is not a link the reader
// can decline to follow: a destination that names nothing this site serves is a broken picture the
// moment the page is opened, so it degrades to its alt text here rather than shipping. Skipping
// *ast.Image would leave the one node type that can point at a file with no rule at all.
func rewriteDocLinks(doc ast.Node, dir string, sources map[string]string) {
	// The nodes are collected before any of them is changed: unlinking splices a node out of its
	// parent, and doing that to the node the walk is standing on cuts the walk short.
	var links []*ast.Link
	var images []*ast.Image
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if entering {
			// *ast.Image embeds ast.Link by value, so it is not an *ast.Link and has to be named
			// in its own case; the order matters only in that both cases exist.
			switch node := n.(type) {
			case *ast.Image:
				images = append(images, node)
			case *ast.Link:
				links = append(links, node)
			}
		}
		return ast.WalkContinue, nil
	})

	for _, l := range links {
		target, isDoc := docLinkTarget(dir, string(l.Destination), sources)
		switch {
		case !isDoc:
		case target == "":
			unlink(l)
		default:
			l.Destination = []byte(target)
		}
	}
	for _, img := range images {
		if !docImageResolves(string(img.Destination)) {
			unlink(img)
		}
	}
}

// docLinkTarget resolves a Markdown link destination against dir. isDoc says whether the
// destination is a relative link to another Markdown document at all; when it is, target is the
// URL that document has on this site, or empty when this site does not publish it.
func docLinkTarget(dir, dest string, sources map[string]string) (target string, isDoc bool) {
	u, err := url.Parse(dest)
	// Anything absolute, external, or without a path of its own — "#section", "mailto:…",
	// "https://…", "/docs/…" — is somebody else's link and stays as written.
	if err != nil || u.Scheme != "" || u.Host != "" || u.Path == "" || strings.HasPrefix(u.Path, "/") {
		return "", false
	}
	if !strings.HasSuffix(u.Path, ".md") {
		return "", false
	}
	// path.Join cleans the result, so "../decisions/0004-turso-libsql.md" from docs/concepts
	// becomes docs/decisions/0004-turso-libsql.md. A destination that climbs out of docs/
	// entirely simply fails the lookup below; it cannot name a file, because the lookup is
	// against the list of published documents rather than against a file system.
	published, ok := sources[path.Join(dir, u.Path)]
	if !ok {
		return "", true
	}
	// Everything the author wrote after the path is carried over, in the order a URL has it. No
	// page here reads a query — handleDocs looks a page up by r.URL.Path alone — but a rewrite
	// that silently dropped one would change what the author wrote into something else and give
	// no sign of it; a link that arrives with a query the site ignores is the smaller surprise,
	// and the one a Markdown viewer already shows.
	if u.RawQuery != "" || u.ForceQuery {
		published += "?" + u.RawQuery
	}
	if u.Fragment != "" {
		published += "#" + u.EscapedFragment()
	}
	return published, true
}

// docImageResolves reports whether an image's destination can name something this site actually
// serves, and it is deliberately stricter than the rule for links.
//
// A relative destination cannot: /docs publishes rendered pages and nothing else — no file under
// docs/ is ever served as itself — so "![diagram](../logo/zorgscope-logo.jpeg)" would be a broken
// image on a public page, with the alt text hidden behind a browser's own placeholder. The .doc img
// rule in the stylesheet invites exactly that, so this is the case worth being explicit about.
// rewriteDocLinks answers it the way it answers a link to an unpublished document: the markup goes
// and the text stays, so the reader sees the alt text as prose rather than a broken picture.
//
// Anything rooted at this site ("/static/logo.png"), and anything with a scheme or a host — an
// external URL, a data: URI, both of which the img-src policy allows — is somebody else's
// destination and is left exactly as written.
func docImageResolves(dest string) bool {
	u, err := url.Parse(dest)
	if err != nil {
		return false
	}
	return u.Scheme != "" || u.Host != "" || strings.HasPrefix(u.Path, "/")
}

// unlink replaces a link or an image with its own children, so the text survives and the markup
// does not: a link keeps its text without the anchor, an image keeps its alt text without the tag.
func unlink(n ast.Node) {
	parent := n.Parent()
	if parent == nil {
		return
	}
	for c := n.FirstChild(); c != nil; c = n.FirstChild() {
		n.RemoveChild(n, c)
		parent.InsertBefore(parent, n, c)
	}
	parent.RemoveChild(parent, n)
}

// firstHeading returns the text of the document's first level-one heading.
func firstHeading(doc ast.Node, src []byte) string {
	for n := doc.FirstChild(); n != nil; n = n.NextSibling() {
		h, ok := n.(*ast.Heading)
		if !ok || h.Level != 1 {
			continue
		}
		var b strings.Builder
		_ = ast.Walk(h, func(c ast.Node, entering bool) (ast.WalkStatus, error) {
			if entering {
				switch t := c.(type) {
				case *ast.Text:
					b.Write(t.Segment.Value(src))
				case *ast.String:
					b.Write(t.Value)
				}
			}
			return ast.WalkContinue, nil
		})
		return strings.TrimSpace(b.String())
	}
	return ""
}

// handleDocs serves the documentation (FR-7.1). It needs no session (AC3): its route-table entries
// say authPublic, and everything it can return is a file that is already in the repository.
//
// A page is found by looking its exact URL up in the map built at start-up. That is what makes a
// path traversal impossible by construction rather than by inspection: there is no path to
// sanitise, no file system to reach, and no way to name a document that loadDocs did not publish.
// Requests whose path is not canonical never arrive here at all — canonicalPath answers those with
// 404 before the mux sees them.
func (s *Server) handleDocs(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == docsPrefix || r.URL.Path == docsPrefix+"/" {
		s.render(w, r, http.StatusOK, "docs_index.html", pageData{
			Title: "Documentation",
			Docs:  s.docIndex,
		})
		return
	}
	p, ok := s.docs[r.URL.Path]
	if !ok {
		http.NotFound(w, r)
		return
	}
	s.render(w, r, http.StatusOK, "docs.html", pageData{Title: p.Title, Doc: p})
}
