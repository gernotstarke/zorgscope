// These tests are adapted from the Task 5 brief verbatim; only the four small helpers below are
// this file's own, written because the package's existing "post" helper (server_test.go) posts to
// a full URL over a real listener rather than to a path on a bare handler, so it is not an
// equivalent of what these tests need.
package fakesources_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gernotstarke/zorgscope/internal/fakesources"
)

// With no redirect_uri on the request — the shape of the backend's own request (design 2026-09-14
// §2) — the fake falls back to its one fixed default callback, matching the local OAuth App's
// registered callback: the fake's local interactive sign-in (spec §7) runs the backend on
// localhost:8080, and without this the offline flow has nowhere to land.
func TestAuthorizeRedirectsToTheDefaultCallbackWithTheState(t *testing.T) {
	rec := get(t, fakesources.NewServer(), "/login/oauth/authorize?client_id=abc&state=xyz")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("code = %d", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "http://localhost:8080/auth/callback?code=fake-code&state=xyz" {
		t.Fatalf("Location = %q", got)
	}
}

// A caller that does pass its own redirect_uri — not the shape of zorgscope's own request, but a
// real GitHub App would still honour one if a client sent it — is redirected there instead of the
// default.
func TestAuthorizeHonoursAnExplicitRedirectURI(t *testing.T) {
	rec := get(t, fakesources.NewServer(), "/login/oauth/authorize?client_id=abc&state=xyz&redirect_uri=http://app.test/auth/callback")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("code = %d", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "http://app.test/auth/callback?code=fake-code&state=xyz" {
		t.Fatalf("Location = %q", got)
	}
}

func TestTokenExchangeAcceptsOnlyTheFakeCode(t *testing.T) {
	h := fakesources.NewServer()
	ok := postForm(t, h, "/login/oauth/access_token", url.Values{"code": {"fake-code"}, "client_id": {"abc"}, "client_secret": {"s"}})
	if ok.Code != http.StatusOK || !strings.Contains(ok.Body.String(), `"access_token":"fake-token"`) {
		t.Fatalf("exchange = %d %s", ok.Code, ok.Body.String())
	}
	bad := postForm(t, h, "/login/oauth/access_token", url.Values{"code": {"stale"}})
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("bad code = %d", bad.Code)
	}
}

// TestTokenExchangeAcceptsAJSONBodyToo proves the JSON branch of handleAccessToken, which
// TestTokenExchangeAcceptsOnlyTheFakeCode above never exercises — it only ever posts a form body.
// GitHub's real endpoint accepts either encoding depending on the request's own Content-Type, and
// the backend (Task 6) is free to use either, so both must work here.
func TestTokenExchangeAcceptsAJSONBodyToo(t *testing.T) {
	h := fakesources.NewServer()
	ok := postJSON(t, h, "/login/oauth/access_token", `{"code":"fake-code","client_id":"abc","client_secret":"s"}`)
	if ok.Code != http.StatusOK || !strings.Contains(ok.Body.String(), `"access_token":"fake-token"`) {
		t.Fatalf("exchange = %d %s", ok.Code, ok.Body.String())
	}
	bad := postJSON(t, h, "/login/oauth/access_token", `{"code":"stale"}`)
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("bad code = %d", bad.Code)
	}
}

func TestRepositoryPermissionsFollowTheControl(t *testing.T) {
	h := fakesources.NewServer()
	for perm, want := range map[string]string{
		"admin": `"push":true`, "maintain": `"push":true`, "push": `"push":true`,
		"pull": `"push":false`, "none": `"pull":false`,
	} {
		post(t, h, "/_control/oauth-user?permission="+perm)
		rec := getWithBearer(t, h, "/repos/gernotstarke/zorgscope", "fake-token")
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), want) {
			t.Errorf("%s: %d %s", perm, rec.Code, rec.Body.String())
		}
	}
	post(t, h, "/_control/oauth-user?permission=absent")
	if body := getWithBearer(t, h, "/repos/gernotstarke/zorgscope", "fake-token").Body.String(); strings.Contains(body, "permissions") {
		t.Fatalf("absent still has a permissions block: %s", body)
	}
}

func TestRepositoryNeedsTheFakeToken(t *testing.T) {
	h := fakesources.NewServer()
	if rec := getWithBearer(t, h, "/repos/gernotstarke/zorgscope", "other"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d", rec.Code)
	}
}

// A freshly built server starts with the default sign-in permission (push), which is what every
// other test in this package relies on without setting it itself.
func TestNewServerStartsWithPushPermission(t *testing.T) {
	h := fakesources.NewServer()
	if rec := getWithBearer(t, h, "/repos/o/r", "fake-token"); !strings.Contains(rec.Body.String(), `"push":true`) {
		t.Fatal("a fresh server does not start with push permission")
	}
}

// get performs a GET against path on h and returns the recorded response, for a caller that wants
// to inspect the status code or body of both success and failure cases itself.
func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

// post posts an empty body to path on h and fails the test unless the response is a success — the
// control routes it is used against always answer 200, so a failure here means the request itself
// was wrong, not something a caller needs to branch on.
func post(t *testing.T, h http.Handler, path string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, nil))
	if rec.Code >= 300 {
		t.Fatalf("POST %s: status = %d, want < 300", path, rec.Code)
	}
}

// postForm posts an application/x-www-form-urlencoded body to path on h and returns the recorded
// response without asserting on its status, since callers exercise both the accepted and the
// rejected case.
func postForm(t *testing.T, h http.Handler, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	h.ServeHTTP(rec, req)
	return rec
}

// getWithBearer performs a GET against path on h carrying an Authorization: Bearer token header,
// and returns the recorded response.
func getWithBearer(t *testing.T, h http.Handler, path, token string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	h.ServeHTTP(rec, req)
	return rec
}

// postJSON posts body as an application/json request to path on h and returns the recorded
// response without asserting on its status, since callers exercise both the accepted and the
// rejected case.
func postJSON(t *testing.T, h http.Handler, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rec, req)
	return rec
}
