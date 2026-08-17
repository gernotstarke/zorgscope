package libsql

import (
	"strings"
	"testing"
	"time"
)

// QS-4.3: the auth token travels in the DSN, so it must reach the driver — and nothing else.
func TestDSNCarriesTheTokenAndErrorsDoNot(t *testing.T) {
	const token = "s3cr3t-token.value"

	local, err := dsn("http://localhost:8080", "")
	if err != nil {
		t.Fatalf("dsn: %v", err)
	}
	if local != "http://localhost:8080" {
		t.Errorf("dsn without a token = %q, want the URL unchanged", local)
	}

	turso, err := dsn("libsql://zorgscope.turso.io", token)
	if err != nil {
		t.Fatalf("dsn: %v", err)
	}
	if !strings.Contains(turso, "authToken=") || !strings.Contains(turso, "s3cr3t-token.value") {
		t.Errorf("dsn with a token = %q, want it to carry authToken", turso)
	}

	_, err = dsn("http://%zz", token)
	if err == nil {
		t.Fatal("dsn on an unparseable URL = nil error, want one")
	}
	if strings.Contains(err.Error(), token) {
		t.Errorf("error %q leaks the auth token (QS-4.3)", err)
	}
}

func TestSQLTimeRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		in   time.Time
		sql  string
	}{
		{"zero", time.Time{}, ""},
		{"utc", time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC), "2026-08-17T10:00:00Z"},
		{"local is stored as UTC", time.Date(2026, 8, 17, 12, 0, 0, 0, time.FixedZone("CEST", 2*60*60)),
			"2026-08-17T10:00:00Z"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := sqlTime(c.in); got != c.sql {
				t.Fatalf("sqlTime(%v) = %q, want %q", c.in, got, c.sql)
			}
			back, err := parseTime(c.sql)
			if err != nil {
				t.Fatalf("parseTime(%q): %v", c.sql, err)
			}
			if !back.Equal(c.in) {
				t.Errorf("parseTime(%q) = %v, want %v", c.sql, back, c.in)
			}
			if c.in.IsZero() && !back.IsZero() {
				t.Errorf("the zero time did not round-trip: got %v", back)
			}
		})
	}
	if _, err := parseTime("17.08.2026"); err == nil {
		t.Error("parseTime on a non-RFC-3339 value = nil error, want one")
	}
}

// The lease compares expiry timestamps as strings, which only works because sqlTime always emits
// the same fixed-width UTC layout.
func TestSQLTimeOrdersLexicographically(t *testing.T) {
	earlier := sqlTime(time.Date(2026, 8, 17, 9, 59, 59, 0, time.UTC))
	later := sqlTime(time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC))
	if earlier >= later {
		t.Errorf("%q < %q is false; the refresh lease's expiry test depends on it", earlier, later)
	}
}

func TestSplitStatementsIgnoresCommentsAndSemicolonsInThem(t *testing.T) {
	const body = `
-- a comment; with a semicolon in it
CREATE TABLE a (x TEXT);

CREATE INDEX i ON a(x); -- trailing comment
`
	got := splitStatements(body)
	if len(got) != 2 {
		t.Fatalf("splitStatements returned %d statements, want 2: %q", len(got), got)
	}
	if !strings.HasPrefix(got[0], "CREATE TABLE a") || !strings.HasPrefix(got[1], "CREATE INDEX i") {
		t.Errorf("splitStatements = %q, want the two CREATE statements", got)
	}
}

// Every embedded migration has to be splittable and versioned, or startup fails in production
// rather than here.
func TestEmbeddedMigrationsAreWellFormed(t *testing.T) {
	names, err := migrationNames()
	if err != nil {
		t.Fatalf("migrationNames: %v", err)
	}
	if len(names) == 0 {
		t.Fatal("no embedded migrations found")
	}
	for _, name := range names {
		v, err := versionOf(name)
		if err != nil {
			t.Errorf("versionOf(%q): %v", name, err)
			continue
		}
		if v <= 0 {
			t.Errorf("versionOf(%q) = %d, want a positive version", name, v)
		}
		body, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if len(splitStatements(string(body))) == 0 {
			t.Errorf("migration %s contains no statements", name)
		}
	}
	if _, err := versionOf("initial.sql"); err == nil {
		t.Error("versionOf on a name without a version prefix = nil error, want one")
	}
}

func TestPlaceholdersAndChunks(t *testing.T) {
	if got := placeholders(3); got != "?,?,?" {
		t.Errorf("placeholders(3) = %q, want %q", got, "?,?,?")
	}
	if got := placeholders(1); got != "?" {
		t.Errorf("placeholders(1) = %q, want %q", got, "?")
	}

	ids := make([]string, 0, 5)
	for i := range 5 {
		ids = append(ids, string(rune('a'+i)))
	}
	got := chunks(ids, 2)
	if len(got) != 3 || len(got[0]) != 2 || len(got[2]) != 1 {
		t.Fatalf("chunks(5 ids, 2) = %v, want three chunks of 2, 2 and 1", got)
	}
	if chunks(nil, 2) != nil {
		t.Error("chunks(nil) is not nil; an empty list must produce no statement at all")
	}
}
