package server

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/gernotstarke/zorgscope/internal/app"
)

var update = flag.Bool("update", false, "rewrite golden files")

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden.html")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("missing golden %s (run with -update): %v", path, err)
	}
	if !bytes.Equal(bytes.TrimSpace(want), bytes.TrimSpace(got)) {
		t.Fatalf("golden %s differs:\n--- want\n%s\n--- got\n%s", name, want, got)
	}
}

func TestGoldenTiles(t *testing.T) {
	tmpl, err := parseTemplates()
	if err != nil {
		t.Fatal(err)
	}
	rows := app.View{Tiles: []string{"attention", "repos"}}
	rows.Attention = app.AttentionView{Total: 3, Overflow: 1, Rows: []app.AttentionRow{
		{ID: "github:arc42/arc42-template|issues/240", Source: "arc42/arc42-template", SourceShort: "arc42-template", Number: "#240", Title: "Typo in section 8", URL: "https://github.com/arc42/arc42-template/issues/240", Author: "newcomer", Age: "2h", Bucket: "lt24h", Badge: "NEW", Level: "new", Kind: "issue", UpdatedAt: 1786968000},
		{ID: "github:arc42/arc42-template|pulls/237", Source: "arc42/arc42-template", SourceShort: "arc42-template", Number: "#237", Title: "Add example stakeholder table", URL: "https://github.com/arc42/arc42-template/pull/237", Author: "Sofeso", Age: "20d", Bucket: "lt30d", Badge: "UNANSWERED", Level: "unanswered", Kind: "pr", UpdatedAt: 1785400000},
	}}
	rows.Repos = []app.RepoCard{
		{Name: "arc42/arc42-template", ShortName: "arc42-template", URL: "https://github.com/arc42/arc42-template", OpenIssues: 3, OpenPRs: 1, New: 1, Unanswered: 2, Build: app.BuildView{State: "ok", Workflow: "build", URL: "https://github.com/arc42/arc42-template/actions/runs/1001", Age: "6h"}},
		{Name: "arc42/arc42.org-site", ShortName: "arc42.org-site", URL: "https://github.com/arc42/arc42.org-site", OpenIssues: 1, Build: app.BuildView{State: "failed", Workflow: "deploy", URL: "https://github.com/arc42/arc42.org-site/actions/runs/2002", Age: "1h"}},
		// Build.URL == "" exercises tile_repos.html's no-link <span> branch (the html/template
		// conditional-href escaper hazard) — this must not be dropped or the branch goes untested.
		{Name: "arc42/arc42-mini", ShortName: "arc42-mini", URL: "https://github.com/arc42/arc42-mini", OpenIssues: 0, Build: app.BuildView{State: "unknown"}},
	}
	data := pageData{View: rows, CSRF: "csrf-token", PollSeconds: 60}
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "tile_attention", data); err != nil {
		t.Fatal(err)
	}
	golden(t, "attention_rows", buf.Bytes())
	buf.Reset()
	if err := tmpl.ExecuteTemplate(&buf, "tile_repos", data); err != nil {
		t.Fatal(err)
	}
	golden(t, "repos", buf.Bytes())
	buf.Reset()
	empty := pageData{View: app.View{}, CSRF: "csrf-token", PollSeconds: 60}
	if err := tmpl.ExecuteTemplate(&buf, "tile_attention", empty); err != nil {
		t.Fatal(err)
	}
	golden(t, "attention_empty", buf.Bytes())
	// every template must execute with an empty view (no nil-pointer traps)
	for _, name := range []string{"layout", "tile_header", "tile_attention", "tile_repos", "unauthorized"} {
		if err := tmpl.ExecuteTemplate(&bytes.Buffer{}, name, empty); err != nil {
			t.Fatalf("%s with empty view: %v", name, err)
		}
	}
}
