package libsql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	libsqldriver "github.com/tursodatabase/libsql-client-go/libsql"
)

// canary is an auth token shaped like a real one — dots, dashes, base64 padding, a slash and a
// space — so that its percent-encoded spellings differ from the raw one in both directions.
const canary = "eyJhbGciOi/JIUzI1 NiJ9.canary-token.SIGNATURE=="

// Open must hand the auth token to the driver, and it must arrive. Nothing below this line runs
// against Turso, so the proof is a local HTTP server that reports what the driver sent it.
//
// This is the path that the local test database cannot exercise: TEST_TURSO_URL needs no token,
// so every store test runs with an empty one and the token branch is never taken. That is why the
// DSN this replaced survived: it was only ever built in production.
func TestOpenAuthenticatesWithTheAuthToken(t *testing.T) {
	var gotAuth, gotURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotURL = r.Header.Get("Authorization"), r.URL.String()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"no database here"}`))
	}))
	defer srv.Close()

	s, err := Open(srv.URL, canary)
	if err != nil {
		t.Fatalf("Open with an auth token: %v", err)
	}
	defer func() { _ = s.Close() }()

	// The server answers 500, so the query fails; that it was made at all is the point.
	if _, err := s.Items(context.Background()); err == nil {
		t.Fatal("Items against a server answering 500 = nil error, want one")
	}
	if want := "Bearer " + canary; gotAuth != want {
		t.Errorf("Authorization header = %q, want %q: the token did not reach the driver", gotAuth, want)
	}
	if strings.Contains(gotURL, canary) || strings.Contains(gotURL, "authToken") {
		t.Errorf("request URL %q carries the token; it belongs in the header alone", gotURL)
	}
}

// The approach this replaced: the token as a query parameter on the DSN. The pinned driver's
// supported constructor refuses all three spellings of it outright, and refuses every unknown
// query parameter besides — so a DSN is not a way to authenticate, it is an error.
func TestTheDriverRefusesAnAuthTokenInTheURL(t *testing.T) {
	const rawURL = "libsql://zorgscope.turso.io"
	for _, param := range []string{"authToken", "auth_token", "jwt"} {
		q := url.Values{param: {canary}}
		_, err := libsqldriver.NewConnector(rawURL + "?" + q.Encode())
		if err == nil {
			t.Errorf("NewConnector accepted ?%s=; Open could go back to building a DSN", param)
			continue
		}
		if !strings.Contains(err.Error(), "forbidden") {
			t.Errorf("NewConnector(?%s=) error = %v, want it to name the parameter as forbidden", param, err)
		}
		if strings.Contains(err.Error(), canary) {
			t.Errorf("driver error %q leaks the auth token (QS-4.3)", err)
		}
	}

	// What Open does instead is accepted, and yields a connector rather than an error.
	c, err := newConnector(rawURL, canary)
	if err != nil {
		t.Fatalf("newConnector with an auth token: %v", err)
	}
	if c == nil || c.Driver() == nil {
		t.Fatal("newConnector returned no usable connector")
	}
}

// Open builds the connector up front, so a URL the driver cannot use fails at startup rather than
// at the first query — on a Machine that has just cold-started with a reader waiting. sql.Open,
// which this replaced, is lazy and reports nothing.
func TestOpenRejectsAnUnusableURLUpFront(t *testing.T) {
	s, err := Open("ftp://zorgscope.turso.io", canary)
	if err == nil {
		_ = s.Close()
		t.Fatal("Open on an unsupported URL scheme = nil error, want one")
	}
	if !strings.Contains(err.Error(), "unsupported URL scheme") {
		t.Errorf("Open error = %v, want it to name the unsupported scheme", err)
	}
	assertNoToken(t, "Open", err.Error())
}

// A local libsql-server wants no token at all, and WithAuthToken refuses an empty one, so the
// option must not be added when there is nothing to add.
func TestConnectorWithoutATokenAndOnABadURL(t *testing.T) {
	if _, err := newConnector("http://localhost:8080", ""); err != nil {
		t.Errorf("newConnector without a token: %v", err)
	}
	_, err := newConnector("http://%zz", canary)
	if err == nil {
		t.Fatal("newConnector on an unparseable URL = nil error, want one")
	}
	if strings.Contains(err.Error(), canary) {
		t.Errorf("error %q leaks the auth token (QS-4.3)", err)
	}
}

// QS-4.3: no secret value may be logged or rendered. This package's own strings never name the
// token, but the driver's error text can quote what it dialled or sent, and %w carries that out
// through every method here — on to source_state.last_error, Tile.Error and the dashboard. So
// every exported method scrubs, and this test walks all of them.
func TestNoExportedMethodLeaksTheAuthToken(t *testing.T) {
	ctx := context.Background()
	// A connector whose every connection fails with the token spelled out three ways.
	poison := "dial https://db.turso.io/?authToken=" + url.QueryEscape(canary) +
		" (path form " + url.PathEscape(canary) + ", raw " + canary + "): connection refused"
	s := &Store{db: sql.OpenDB(failingConnector{msg: poison}), scrub: newScrubber(canary)}
	defer func() { _ = s.Close() }()

	calls := map[string]func() error{
		"Migrate":             func() error { return s.Migrate(ctx) },
		"ReplaceItems":        func() error { _, err := s.ReplaceItems(ctx, "github", nil, time.Now()); return err },
		"Items":               func() error { _, err := s.Items(ctx); return err },
		"UpsertBuilds":        func() error { return s.UpsertBuilds(ctx, nil, time.Now()) },
		"Builds":              func() error { _, err := s.Builds(ctx); return err },
		"UpsertMetrics":       func() error { return s.UpsertMetrics(ctx, nil, time.Now()) },
		"Metrics":             func() error { _, err := s.Metrics(ctx); return err },
		"RecordSourceOK":      func() error { return s.RecordSourceOK(ctx, "github", time.Now(), 1) },
		"RecordSourceError":   func() error { return s.RecordSourceError(ctx, "github", time.Now(), "boom") },
		"SourceStates":        func() error { _, err := s.SourceStates(ctx); return err },
		"LastVisit":           func() error { _, err := s.LastVisit(ctx); return err },
		"SetLastVisit":        func() error { return s.SetLastVisit(ctx, time.Now()) },
		"AcquireRefreshLease": func() error { _, err := s.AcquireRefreshLease(ctx, "m1", time.Now(), time.Minute); return err },
		"ReleaseRefreshLease": func() error { return s.ReleaseRefreshLease(ctx, "m1") },
		"StartRun":            func() error { _, err := s.StartRun(ctx, "cron", time.Now()); return err },
		"FinishRun":           func() error { return s.FinishRun(ctx, 1, time.Now(), true, "") },
		"LastRun":             func() error { _, err := s.LastRun(ctx); return err },
		"MarkNotified":        func() error { return s.MarkNotified(ctx, []string{"k"}, time.Now()) },
		"UnnotifiedKeys":      func() error { _, err := s.UnnotifiedKeys(ctx, []string{"k"}); return err },
		"TruncateAll":         func() error { return s.TruncateAll(ctx) },
	}

	// Close is the one exported method that cannot fail against a pool with no open connection;
	// every other one must be in the table above, or a new method could ship unscrubbed.
	covered := map[string]bool{"Close": true}
	for name := range calls {
		covered[name] = true
	}
	storeType := reflect.TypeOf(&Store{})
	for i := range storeType.NumMethod() {
		if name := storeType.Method(i).Name; !covered[name] {
			t.Errorf("exported method %s is not covered by this test; does it scrub its error?", name)
		}
	}

	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			err := call()
			if err == nil {
				t.Fatalf("%s against a dead connection = nil error, want one", name)
			}
			assertNoToken(t, name, err.Error())
		})
	}
}

// The token travels through a URL, so it can come back percent-encoded rather than raw.
func TestScrubberReplacesEverySpellingOfTheToken(t *testing.T) {
	scrub := newScrubber(canary)
	text := "raw " + canary + " query " + url.QueryEscape(canary) + " path " + url.PathEscape(canary)
	cleaned := scrub.clean(errors.New(text))
	assertNoToken(t, "scrubber", cleaned.Error())
	if !strings.Contains(cleaned.Error(), redacted) {
		t.Errorf("scrubbed text %q does not mark what it removed", cleaned)
	}

	// An error with nothing to hide keeps its wrapping, so errors.Is still works for callers.
	sentinel := errors.New("nothing secret here")
	if got := scrub.clean(sentinel); !errors.Is(got, sentinel) {
		t.Errorf("clean rewrote an error that needed no scrubbing: %v", got)
	}
	// An error that did have to be rewritten deliberately drops its cause: an Unwrap that still
	// spelled the token out would hand the secret straight back.
	leaky := errors.New(canary)
	if got := scrub.clean(leaky); errors.Is(got, leaky) {
		t.Error("clean kept a cause whose own text is the token")
	}
	if scrub.clean(nil) != nil {
		t.Error("clean(nil) is not nil")
	}
	// No token, nothing to scrub, nothing changed.
	if got := newScrubber("").clean(sentinel); !errors.Is(got, sentinel) {
		t.Errorf("the empty scrubber rewrote %v", got)
	}
}

// assertNoToken fails when text names the auth token in any spelling it could travel in.
func assertNoToken(t *testing.T, what, text string) {
	t.Helper()
	for label, form := range map[string]string{
		"raw":              canary,
		"query-encoded":    url.QueryEscape(canary),
		"path-encoded":     url.PathEscape(canary),
		"signature suffix": "SIGNATURE==",
	} {
		if strings.Contains(text, form) {
			t.Errorf("%s leaks the %s auth token (QS-4.3): %s", what, label, text)
		}
	}
}

// failingConnector stands in for a libSQL server that cannot be reached, with an error message
// shaped like the driver's: it quotes what it tried to dial, token and all.
type failingConnector struct{ msg string }

func (c failingConnector) Connect(context.Context) (driver.Conn, error) {
	return nil, errors.New(c.msg)
}
func (c failingConnector) Driver() driver.Driver { return failingDriver{} }

type failingDriver struct{}

func (failingDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("failingDriver is only ever used through failingConnector")
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

// sqlTime only ever writes "Z", but a row repaired by hand or written by a later migration could
// carry an offset. Everything must still read back in UTC.
func TestParseTimeConvertsAnOffsetToUTC(t *testing.T) {
	got, err := parseTime("2026-08-17T12:00:00+02:00")
	if err != nil {
		t.Fatalf("parseTime: %v", err)
	}
	if got.Location() != time.UTC {
		t.Errorf("location = %v, want UTC", got.Location())
	}
	if want := time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC); !got.Equal(want) || got.Hour() != 10 {
		t.Errorf("parseTime = %v, want %v", got, want)
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
