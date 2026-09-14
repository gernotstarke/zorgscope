package libsql

import (
	"context"
	"os"
	"testing"
	"time"
)

// openTestStore is migrate_test.go's own copy of the store tests' newStore helper (store_test.go,
// package libsql_test): this file needs direct access to the unexported db field to recreate
// 0001's old shape by hand, so it lives in package libsql rather than libsql_test.
func openTestStore(t *testing.T) *Store {
	t.Helper()
	url := os.Getenv("TEST_TURSO_URL")
	if url == "" {
		t.Skip("TEST_TURSO_URL not set; run via `make test`")
	}
	s, err := Open(url, "")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if err := s.TruncateAll(context.Background()); err != nil {
		t.Fatalf("TruncateAll: %v", err)
	}
	return s
}

// mustExec runs one statement directly against the store's connection, failing the test on error.
// It exists so this file can recreate 0001's old table shape by hand — the migrated store has
// already dropped metrics and the two item columns 0005 removes.
func mustExec(t *testing.T, s *Store, query string, args ...any) {
	t.Helper()
	if _, err := s.db.ExecContext(context.Background(), query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

// 0005 is the migration that removes Plausible and Todoist from the schema (design 2026-09-14
// §3): the metrics table, Todoist's items and source_state and notified rows, and the two item
// columns only a task ever filled. GitHub's rows, first_seen_at included, must survive untouched.
func TestMigration0005RemovesTodoistAndMetricsAndKeepsGitHub(t *testing.T) {
	s := openTestStore(t) // migrates to the latest version and truncates
	ctx := context.Background()

	// Apply 0001..0004 only by re-creating the old shape by hand: the migrated store has no
	// metrics table any more, so the test recreates it and the old columns exactly as 0001 did,
	// seeds them, then applies 0005 again through the same code path Migrate uses.
	mustExec(t, s, `CREATE TABLE metrics (site TEXT, window_days INTEGER, visitors INTEGER, pageviews INTEGER, prev_visitors INTEGER, prev_pageviews INTEGER, fetched_at TEXT, PRIMARY KEY (site, window_days))`)
	mustExec(t, s, `INSERT INTO metrics VALUES ('arc42.org', 7, 1, 1, 1, 1, '2026-09-01T00:00:00Z')`)
	mustExec(t, s, `ALTER TABLE items ADD COLUMN due_at TEXT`)
	mustExec(t, s, `ALTER TABLE items ADD COLUMN priority INTEGER`)
	mustExec(t, s, `INSERT INTO items (source, external_id, kind, repo, title, first_seen_at, last_fetched_at) VALUES ('todoist','t1','task','Inbox','a task','2026-09-01T00:00:00Z','2026-09-01T00:00:00Z')`)
	mustExec(t, s, `INSERT INTO items (source, external_id, kind, repo, title, first_seen_at, last_fetched_at) VALUES ('github','g1','issue','org/repo','an issue','2026-08-01T00:00:00Z','2026-09-01T00:00:00Z')`)
	mustExec(t, s, `INSERT INTO source_state (source) VALUES ('plausible'), ('todoist'), ('github')`)
	mustExec(t, s, `DELETE FROM schema_migrations WHERE version = 5`)

	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	items, err := s.Items(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ExternalID != "g1" || !items[0].FirstSeenAt.Equal(time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("items after 0005 = %+v", items)
	}
	states, err := s.SourceStates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := states["todoist"]; ok {
		t.Fatal("todoist source_state survived")
	}
	if _, ok := states["github"]; !ok {
		t.Fatal("github source_state was deleted")
	}
	if _, err := s.db.ExecContext(ctx, `SELECT 1 FROM metrics`); err == nil {
		t.Fatal("metrics table survived")
	}
}
