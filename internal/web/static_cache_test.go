// How static assets are cached. This is the file that exists because the appearance switch
// appeared not to work: the document began carrying data-theme, the browser was still holding the
// stylesheet it had fetched an hour earlier, and that stylesheet had never heard of the attribute.
// Nothing was broken on the server — the page was new HTML wearing an old stylesheet.
package web

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// Every static URL the page links carries its file's content hash, so a changed file is a
// different URL and can never be served from a cache that predates the change.
func TestThePageLinksVersionedAssets(t *testing.T) {
	h := dashHandler(t, &fakeSource{items: representativeItems()})
	page := getAuthed(t, h, "/").Body.String()

	linked := regexp.MustCompile(`/static/[A-Za-z0-9._/-]+\?v=[0-9a-f]+`).FindAllString(page, -1)
	if len(linked) < 4 {
		t.Fatalf("found %d versioned assets on the page (%v); the stylesheet, htmx, the logo and "+
			"the favicon are all linked", len(linked), linked)
	}

	// Nothing is linked without a version: one unversioned link is one file that can go stale.
	for _, ref := range regexp.MustCompile(`/static/[A-Za-z0-9._/-]+`).FindAllString(page, -1) {
		if !strings.Contains(page, ref+"?v=") {
			t.Errorf("%s is linked without a version", ref)
		}
	}
}

// The version is the file's own content hash, not a build stamp or a counter: it changes when and
// only when the bytes change, which is what makes an immutable cache safe.
func TestTheVersionIsTheContentHash(t *testing.T) {
	s := newTestServer(t)
	a, ok := s.assets["app.css"]
	if !ok {
		t.Fatal("no stylesheet among the assets")
	}
	sum := fmt.Sprintf("%x", sha256.Sum256(a.stored))
	if a.version != sum[:assetVersionLen] {
		t.Errorf("version = %q, want %q", a.version, sum[:assetVersionLen])
	}
	if want := "/static/app.css?v=" + a.version; s.assetURL("app.css") != want {
		t.Errorf("assetURL = %q, want %q", s.assetURL("app.css"), want)
	}
}

// A versioned URL may be kept forever; an unversioned one must be revalidated. The second rule is
// the one that matters: an unversioned URL names a moving target, and any max-age on it is a
// window in which new HTML is rendered against an old stylesheet.
func TestCacheLifetimeDependsOnWhetherTheURLIsVersioned(t *testing.T) {
	s := newTestServer(t)
	h := s.Handler()
	version := s.assets["app.css"].version

	tests := []struct {
		name, url, want string
	}{
		{"versioned", "/static/app.css?v=" + version, "immutable"},
		{"unversioned", "/static/app.css", "no-cache"},
		{"a stale version", "/static/app.css?v=0000000", "no-cache"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := get(h, tc.url)
			if rec.Code != http.StatusOK {
				t.Fatalf("GET %s = %d, want 200", tc.url, rec.Code)
			}
			if got := rec.Header().Get("Cache-Control"); !strings.Contains(got, tc.want) {
				t.Errorf("Cache-Control = %q, want it to contain %q", got, tc.want)
			}
		})
	}
}

// Revalidation costs a 304 with no body rather than the file again.
func TestAnUnchangedAssetRevalidatesToNotModified(t *testing.T) {
	h := newTestServer(t).Handler()

	first := get(h, "/static/app.css")
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag, so an unversioned request can only ever be answered in full")
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/static/app.css", nil)
	req.Header.Set("If-None-Match", etag)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotModified {
		t.Errorf("status = %d, want 304", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("a 304 carried %d bytes of body", rec.Body.Len())
	}
	if rec.Header().Get("ETag") != etag {
		t.Error("the 304 does not repeat the entity tag")
	}
}

// The two encodings are two representations, and a shared cache keyed on Vary: Accept-Encoding
// stores them separately. One entity tag for both would let it answer a plain request with the
// compressed bytes.
func TestTheGzipEncodingHasItsOwnEntityTag(t *testing.T) {
	h := newTestServer(t).Handler()

	plain := get(h, "/static/app.css").Header().Get("ETag")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/static/app.css", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	h.ServeHTTP(rec, req)
	compressed := rec.Header().Get("ETag")

	if compressed == plain {
		t.Errorf("both encodings answer with %s", plain)
	}
	if !strings.HasSuffix(strings.TrimSuffix(compressed, `"`), "-gzip") {
		t.Errorf("the compressed entity tag %s does not name its encoding", compressed)
	}
	// And the client holding the compressed one is not told it is up to date about the plain one.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/static/app.css", nil)
	req.Header.Set("If-None-Match", compressed)
	h.ServeHTTP(rec, req)
	if rec.Code == http.StatusNotModified {
		t.Error("a plain request was told the compressed representation it holds is current")
	}
}

// A client that holds both encodings sends both tags, and the header is a list.
func TestAListOfEntityTagsIsUnderstood(t *testing.T) {
	h := newTestServer(t).Handler()
	etag := get(h, "/static/app.css").Header().Get("ETag")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/static/app.css", nil)
	req.Header.Set("If-None-Match", `"something-else", `+etag)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotModified {
		t.Errorf("status = %d, want 304 — the entity tag was in the list", rec.Code)
	}
}

// FR-1.9: the wait page's mark is a 512 px JPEG, served as such and never gzipped — a JPEG is
// already compressed, and a gzip wrapper would only add bytes. Its size is pinned so a regenerated
// file cannot quietly blow the wait page's budget (QS-2.3).
func TestLargeMarkIsServedAsAJPEGWithoutGzip(t *testing.T) {
	h := dashHandler(t, &fakeSource{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/static/logo-large.jpg", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /static/logo-large.jpg = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/jpeg" {
		t.Errorf("Content-Type = %q, want image/jpeg", ct)
	}
	if enc := rec.Header().Get("Content-Encoding"); enc != "" {
		t.Errorf("Content-Encoding = %q, want none: a JPEG is not gzipped", enc)
	}
	if n := rec.Body.Len(); n > 45*1024 {
		t.Errorf("logo-large.jpg is %d bytes, at most 45 kB is allowed", n)
	}
	if !bytes.HasPrefix(rec.Body.Bytes(), []byte{0xFF, 0xD8, 0xFF}) {
		t.Error("the body does not start with the JPEG magic bytes")
	}
}
