// These two tests are adapted from the Task 6 brief. Every assertion is unchanged; only the
// constructor name differs — newServer() became fakesources.NewServer() because the server had
// to move out of package main so Tasks 7-10 can import it (see the Task 6 report).
package fakesources_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gernotstarke/zorgscope/internal/fakesources"
)

func TestGraphQLReturnsIssues(t *testing.T) {
	srv := httptest.NewServer(fakesources.NewServer())
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/graphql", "application/json", strings.NewReader(`{"query":"{}"}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body struct {
		Data struct {
			Repository struct {
				Issues struct{ Nodes []struct{ Title string } }
			}
		}
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Data.Repository.Issues.Nodes) == 0 {
		t.Fatal("want at least one issue in the fixture")
	}
}

func TestFailControlMakesTheSourceFail(t *testing.T) {
	srv := httptest.NewServer(fakesources.NewServer())
	defer srv.Close()

	post(t, srv.URL+"/_control/fail?source=github&status=500")

	resp, err := http.Post(srv.URL+"/graphql", "application/json", strings.NewReader(`{"query":"{}"}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 — QS-1.4 needs an injectable failure", resp.StatusCode)
	}
}

// post posts an empty body to url and fails the test on error or a non-2xx status.
func post(t *testing.T, url string) {
	t.Helper()
	resp, err := http.Post(url, "application/json", nil)
	if err != nil {
		t.Fatalf("post %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		t.Fatalf("post %s: status = %d, want < 300", url, resp.StatusCode)
	}
}
