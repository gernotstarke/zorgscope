// Package libsql implements ports.Store against a libSQL server — libsql-server locally, Turso in
// production — over the Hrana HTTP protocol. It is the only package allowed to import the libSQL
// driver (QS-5.2, enforced by depguard).
//
// Two conventions run through the whole package:
//
//   - Times are time.Time in Go and RFC 3339 UTC strings in SQL; the zero time is the empty
//     string. See sqlTime and parseTime.
//   - first_seen_at is written when an item row is inserted and never updated again. That single
//     asymmetry, in upsertItemSQL, is what makes the NEW badge correct (FR-5.3 AC2, QS-1.2).
//   - Every exported method passes its error through Store.scrub before returning it, so that no
//     auth token can leave this package inside error text (QS-4.3). See scrubber.
package libsql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	libsqldriver "github.com/tursodatabase/libsql-client-go/libsql"

	"github.com/gernotstarke/zorgscope/internal/domain"
	"github.com/gernotstarke/zorgscope/internal/ports"
)

// Store is the libSQL-backed ports.Store.
//
// scrub holds the auth token for one purpose only: removing it again from any error text on the
// way out (QS-4.3). See scrubber.
type Store struct {
	db    *sql.DB
	scrub scrubber
}

// A signature drift between ports.Store and this adapter must fail the build here, not four tasks
// later.
var _ ports.Store = (*Store)(nil)

// Keys used in the app_state table.
const (
	lastVisitKey = "last_visit_at"
	leaseKey     = "refresh_lease"
)

// maxParams caps how many placeholders one generated statement uses. SQLite's default
// SQLITE_MAX_VARIABLE_NUMBER is 999 in older builds, so lists are sent in chunks well below that.
const maxParams = 400

// Open connects to the libSQL server at rawURL, authenticating with authToken when it is not
// empty (Turso needs one; a local libsql-server does not).
//
// The token is handed to the driver as an option and never as part of the URL. That is not a
// stylistic choice: libsql-client-go's NewConnector rejects a URL carrying the token outright —
// "'authToken' usage forbidden. Please use 'WithAuthToken' option instead", and the same for
// 'auth_token' and 'jwt' — and rejects every unknown query parameter besides. A DSN is therefore
// not a supported way to authenticate at all, only the option is. (The driver's older, connector-
// less Driver.Open still tolerates the parameter, which is the only reason the DSN this replaced
// ever reached Turso; it is the deprecated path and not one to go back to.)
//
// No error returned from this package ever contains the auth token: a secret must not reach a
// log, and least of all the dashboard (QS-4.3). See scrubber.
func Open(rawURL, authToken string) (*Store, error) {
	scrub := newScrubber(authToken)
	connector, err := newConnector(rawURL, authToken)
	if err != nil {
		return nil, scrub.clean(err)
	}
	return &Store{db: sql.OpenDB(connector), scrub: scrub}, nil
}

// newConnector builds the driver connector for rawURL, carrying the auth token in the driver's
// WithAuthToken option when there is one. WithAuthToken refuses an empty token, so the option is
// added only when there is something to add — a local libsql-server wants no token at all.
//
// Its errors name the host at most, never the token; Open scrubs them regardless.
func newConnector(rawURL, authToken string) (driver.Connector, error) {
	if _, err := url.Parse(rawURL); err != nil {
		return nil, errors.New("invalid database URL")
	}
	opts := make([]libsqldriver.Option, 0, 1)
	if authToken != "" {
		opts = append(opts, libsqldriver.WithAuthToken(authToken))
	}
	connector, err := libsqldriver.NewConnector(rawURL, opts...)
	if err != nil {
		return nil, fmt.Errorf("open libsql database: %w", err)
	}
	return connector, nil
}

// redacted stands in for the auth token wherever it would otherwise have appeared.
const redacted = "[REDACTED]"

// scrubber removes the auth token from error text.
//
// This package's own error strings never contain it. The driver's make no such promise: a failed
// connection or request can quote the URL it dialled or the payload it sent, and %w carries that
// text out through every method here. From there the path to a user's screen is complete —
// internal/refresh's runner puts it in source_state.last_error, which becomes Tile.Error, which is
// rendered. No layer above this one holds the token and so none of them can redact it; this one
// does (QS-4.3).
//
// Both spellings are replaced: the raw token, and the percent-encoded forms it takes when it
// travels inside a URL.
type scrubber struct{ r *strings.Replacer }

// newScrubber builds the scrubber for one auth token. The zero scrubber — for the empty token of
// a local libsql-server — is a no-op, because there is nothing to hide.
func newScrubber(authToken string) scrubber {
	if authToken == "" {
		return scrubber{}
	}
	pairs := []string{authToken, redacted}
	for _, encoded := range []string{url.QueryEscape(authToken), url.PathEscape(authToken)} {
		if encoded != authToken && !slices.Contains(pairs, encoded) {
			pairs = append(pairs, encoded, redacted)
		}
	}
	return scrubber{r: strings.NewReplacer(pairs...)}
}

// clean returns err with every occurrence of the auth token replaced by redacted.
//
// When nothing had to be replaced — the ordinary case — the error comes back untouched, wrapping
// and all. When something did, the wrapping is deliberately dropped: an error whose text is clean
// but whose wrapped cause still spells the token out would hand the secret back to anyone who
// called errors.Unwrap. Nothing in this repository matches on a store error's cause.
func (s scrubber) clean(err error) error {
	if err == nil || s.r == nil {
		return err
	}
	msg := err.Error()
	cleaned := s.r.Replace(msg)
	if cleaned == msg {
		return err
	}
	return errors.New(cleaned)
}

// Close releases the underlying connection pool.
func (s *Store) Close() (err error) {
	defer func() { err = s.scrub.clean(err) }()
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("close database: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------- items

// upsertItemSQL is the heart of the store.
//
// first_seen_at appears in the INSERT column list and is deliberately ABSENT from the
// ON CONFLICT DO UPDATE SET clause: an item's first sighting is recorded once and is never moved
// by a later refresh, which is exactly what makes NEW correct (FR-5.3 AC2, QS-1.2). Adding
// first_seen_at to the SET clause would silently turn the NEW badge into "changed since the last
// refresh". last_fetched_at, by contrast, is updated every time.
const upsertItemSQL = `
INSERT INTO items (
  source, external_id, kind, repo, number, title, summary, url, author, state,
  created_at, updated_at, first_seen_at, last_fetched_at, payload)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,NULL)
ON CONFLICT(source, external_id) DO UPDATE SET
  kind = excluded.kind, repo = excluded.repo, number = excluded.number,
  title = excluded.title, summary = excluded.summary, url = excluded.url,
  author = excluded.author,
  state = excluded.state, created_at = excluded.created_at,
  updated_at = excluded.updated_at, last_fetched_at = excluded.last_fetched_at`

// itemColumns is the read side of the same row. COALESCE keeps a NULL written by an older schema
// or by hand from failing the scan.
const itemColumns = `
  source, external_id, COALESCE(kind,''), COALESCE(repo,''), COALESCE(number,0),
  COALESCE(title,''), COALESCE(summary,''), COALESCE(url,''), COALESCE(author,''),
  COALESCE(state,''),
  COALESCE(created_at,''), COALESCE(updated_at,''), first_seen_at`

// ReplaceItems makes the stored items of one source exactly items: it upserts every item,
// preserving the first_seen_at of rows that already existed, and deletes the rows of that source
// that items no longer contains. Other sources are untouched (FR-5.5 AC1).
//
// It returns the number of items now stored for source — len(items) — not the number that are new.
// That count becomes source_state.item_count, which answers "how much does this source hold?"; how
// many of them are new is domain.CountNew's question, answered against first_seen_at.
//
// Everything happens in one transaction, so a failed refresh leaves the previous set intact
// rather than a half-replaced one.
func (s *Store) ReplaceItems(ctx context.Context, source string, items []domain.Item, now time.Time) (_ int, err error) {
	defer func() { err = s.scrub.clean(err) }()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// The ids already stored for this source, read inside the transaction, are what deleteAbsent
	// subtracts the incoming ones from.
	known, err := storedIDs(ctx, tx, source)
	if err != nil {
		return 0, err
	}

	present := make(map[string]bool, len(items))
	for _, it := range items {
		present[it.ExternalID] = true
		if _, err := tx.ExecContext(ctx, upsertItemSQL, upsertArgs(it, source, now)...); err != nil {
			return 0, fmt.Errorf("upsert %s/%s: %w", source, it.ExternalID, err)
		}
	}
	if err := deleteAbsent(ctx, tx, source, known, present); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return len(items), nil
}

// upsertArgs lays out one item in the order upsertItemSQL expects. The item's own FirstSeenAt is
// ignored: a fetcher does not know it, and the store is the only place that decides it.
func upsertArgs(it domain.Item, source string, now time.Time) []any {
	return []any{
		source, it.ExternalID, string(it.Kind), it.Repo, it.Number, it.Title, it.Summary, it.URL,
		it.Author, it.State, sqlTime(it.CreatedAt), sqlTime(it.UpdatedAt), sqlTime(now), sqlTime(now),
	}
}

// storedIDs returns the external ids currently stored for one source.
func storedIDs(ctx context.Context, tx *sql.Tx, source string) (map[string]bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT external_id FROM items WHERE source = ?`, source)
	if err != nil {
		return nil, fmt.Errorf("read %s ids: %w", source, err)
	}
	defer func() { _ = rows.Close() }()

	ids := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan %s id: %w", source, err)
		}
		ids[id] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read %s ids: %w", source, err)
	}
	return ids, nil
}

// deleteAbsent removes the rows of source that the new item set no longer contains.
//
// The brief's formulation is `external_id NOT IN (?,?,…)` with one placeholder per incoming item,
// which a repository with hundreds of issues would push towards SQLite's variable limit — and a
// NOT IN list cannot be chunked, because each chunk would delete the rows named in every other
// chunk. So this deletes the complement instead: the ids known to be gone, in chunks of maxParams
// placeholders. With no incoming items at all it degrades to the plain
// `DELETE FROM items WHERE source = ?`.
func deleteAbsent(ctx context.Context, tx *sql.Tx, source string, known, present map[string]bool) error {
	if len(present) == 0 {
		if _, err := tx.ExecContext(ctx, `DELETE FROM items WHERE source = ?`, source); err != nil {
			return fmt.Errorf("delete all %s items: %w", source, err)
		}
		return nil
	}
	gone := make([]string, 0, len(known))
	for id := range known {
		if !present[id] {
			gone = append(gone, id)
		}
	}
	for _, chunk := range chunks(gone, maxParams-1) {
		args := make([]any, 0, len(chunk)+1)
		args = append(args, source)
		for _, id := range chunk {
			args = append(args, id)
		}
		//nolint:gosec // G202: the only thing concatenated is "?,?,…"; every value is bound
		q := `DELETE FROM items WHERE source = ? AND external_id IN (` + placeholders(len(chunk)) + `)`
		if _, err := tx.ExecContext(ctx, q, args...); err != nil {
			return fmt.Errorf("delete absent %s items: %w", source, err)
		}
	}
	return nil
}

// Items returns every stored item, newest sighting first.
func (s *Store) Items(ctx context.Context) (_ []domain.Item, err error) {
	defer func() { err = s.scrub.clean(err) }()
	q := `SELECT ` + itemColumns + ` FROM items ORDER BY first_seen_at DESC, source, external_id`
	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("read items: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var items []domain.Item
	for rows.Next() {
		var (
			it                              domain.Item
			kind, created, updated, firstAt string
		)
		if err := rows.Scan(&it.Source, &it.ExternalID, &kind, &it.Repo, &it.Number, &it.Title,
			&it.Summary, &it.URL, &it.Author, &it.State, &created, &updated, &firstAt); err != nil {
			return nil, fmt.Errorf("scan item: %w", err)
		}
		it.Kind = domain.Kind(kind)
		if err := parseInto(map[*time.Time]string{
			&it.CreatedAt: created, &it.UpdatedAt: updated, &it.FirstSeenAt: firstAt,
		}); err != nil {
			return nil, fmt.Errorf("item %s/%s: %w", it.Source, it.ExternalID, err)
		}
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read items: %w", err)
	}
	return items, nil
}

// ---------------------------------------------------------------- builds

// UpsertBuilds makes the stored builds exactly builds: it upserts every one, keyed by repository,
// and deletes the rows for repositories builds no longer contains — the same complement delete
// ReplaceItems does, and for the same reason. Without it, dropping a repository from the
// configuration leaves its build row on the tile forever, and the row cannot even go stale
// visibly, since nothing refreshes it. Every row is stamped with now as its fetch time.
//
// Unlike items there is no source to scope this to: the builds table is one flat set of
// repositories, written by one fetcher, so the incoming slice is the whole truth about it.
//
// Everything happens in one transaction, so a failed write leaves the previous set intact rather
// than a half-replaced one.
func (s *Store) UpsertBuilds(ctx context.Context, builds []domain.Build, now time.Time) (err error) {
	defer func() { err = s.scrub.clean(err) }()
	const q = `
INSERT INTO builds (repo, workflow, workflow_path, conclusion, status, run_url, badge, finished_at, fetched_at)
VALUES (?,?,?,?,?,?,?,?,?)
ON CONFLICT(repo) DO UPDATE SET
  workflow = excluded.workflow, workflow_path = excluded.workflow_path,
  conclusion = excluded.conclusion, status = excluded.status,
  run_url = excluded.run_url, badge = excluded.badge,
  finished_at = excluded.finished_at, fetched_at = excluded.fetched_at`

	return s.inTx(ctx, func(tx *sql.Tx) error {
		known, err := storedBuildRepos(ctx, tx)
		if err != nil {
			return err
		}
		present := make(map[string]bool, len(builds))
		for _, b := range builds {
			present[b.Repo] = true
			_, err := tx.ExecContext(ctx, q, b.Repo, b.Workflow, b.WorkflowPath, b.Conclusion,
				b.Status, b.RunURL, b.Badge, sqlTime(b.FinishedAt), sqlTime(now))
			if err != nil {
				return fmt.Errorf("upsert build %s: %w", b.Repo, err)
			}
		}
		return deleteAbsentBuilds(ctx, tx, known, present)
	})
}

// storedBuildRepos returns the repositories that currently have a build row.
func storedBuildRepos(ctx context.Context, tx *sql.Tx) (map[string]bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT repo FROM builds`)
	if err != nil {
		return nil, fmt.Errorf("read build repos: %w", err)
	}
	defer func() { _ = rows.Close() }()

	repos := map[string]bool{}
	for rows.Next() {
		var repo string
		if err := rows.Scan(&repo); err != nil {
			return nil, fmt.Errorf("scan build repo: %w", err)
		}
		repos[repo] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read build repos: %w", err)
	}
	return repos, nil
}

// deleteAbsentBuilds removes the build rows the new set no longer contains.
//
// Like deleteAbsent for items, it deletes the complement — the repositories known to be gone, in
// chunks — rather than a `repo NOT IN (?,?,…)` list: a NOT IN list cannot be chunked, because each
// chunk would delete the rows named in every other chunk.
//
// An empty incoming set needs no special case here, unlike deleteAbsent's `DELETE FROM items WHERE
// source = ?`: known is read inside the same transaction, so with nothing present every stored
// repository is in gone and the chunked delete empties the table by itself. A `DELETE FROM builds`
// shortcut was written first and removed again — it changed no outcome, and a branch that changes
// no outcome cannot be defended by a test.
func deleteAbsentBuilds(ctx context.Context, tx *sql.Tx, known, present map[string]bool) error {
	gone := make([]string, 0, len(known))
	for repo := range known {
		if !present[repo] {
			gone = append(gone, repo)
		}
	}
	for _, chunk := range chunks(gone, maxParams) {
		args := make([]any, 0, len(chunk))
		for _, repo := range chunk {
			args = append(args, repo)
		}
		//nolint:gosec // G202: the only thing concatenated is "?,?,…"; every value is bound
		q := `DELETE FROM builds WHERE repo IN (` + placeholders(len(chunk)) + `)`
		if _, err := tx.ExecContext(ctx, q, args...); err != nil {
			return fmt.Errorf("delete absent builds: %w", err)
		}
	}
	return nil
}

// Builds returns every stored build.
func (s *Store) Builds(ctx context.Context) (_ []domain.Build, err error) {
	defer func() { err = s.scrub.clean(err) }()
	const q = `
SELECT repo, COALESCE(workflow,''), COALESCE(workflow_path,''), COALESCE(conclusion,''),
       COALESCE(status,''), COALESCE(run_url,''), COALESCE(badge,x''),
       COALESCE(finished_at,''), COALESCE(fetched_at,'')
FROM builds ORDER BY repo`

	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("read builds: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var builds []domain.Build
	for rows.Next() {
		var (
			b                 domain.Build
			finished, fetched string
		)
		if err := rows.Scan(&b.Repo, &b.Workflow, &b.WorkflowPath, &b.Conclusion, &b.Status,
			&b.RunURL, &b.Badge, &finished, &fetched); err != nil {
			return nil, fmt.Errorf("scan build: %w", err)
		}
		if err := parseInto(map[*time.Time]string{
			&b.FinishedAt: finished, &b.FetchedAt: fetched,
		}); err != nil {
			return nil, fmt.Errorf("build %s: %w", b.Repo, err)
		}
		builds = append(builds, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read builds: %w", err)
	}
	return builds, nil
}

// ---------------------------------------------------------------- source health

// RecordSourceOK records a successful fetch. It writes only the success columns: a success must
// not erase the record of the last error, and vice versa (FR-1.4 AC2).
func (s *Store) RecordSourceOK(ctx context.Context, source string, at time.Time, count int) (err error) {
	defer func() { err = s.scrub.clean(err) }()
	const q = `
INSERT INTO source_state (source, last_success_at, last_error, last_error_at, item_count)
VALUES (?,?,'','',?)
ON CONFLICT(source) DO UPDATE SET
  last_success_at = excluded.last_success_at, item_count = excluded.item_count`

	if _, err := s.db.ExecContext(ctx, q, source, sqlTime(at), count); err != nil {
		return fmt.Errorf("record %s success: %w", source, err)
	}
	return nil
}

// RecordSourceError records a failed fetch. It leaves last_success_at and item_count alone, so
// the dashboard can still say how old the last good data is (FR-1.4 AC2).
func (s *Store) RecordSourceError(ctx context.Context, source string, at time.Time, msg string) (err error) {
	defer func() { err = s.scrub.clean(err) }()
	const q = `
INSERT INTO source_state (source, last_success_at, last_error, last_error_at, item_count)
VALUES (?,'',?,?,0)
ON CONFLICT(source) DO UPDATE SET
  last_error = excluded.last_error, last_error_at = excluded.last_error_at`

	if _, err := s.db.ExecContext(ctx, q, source, msg, sqlTime(at)); err != nil {
		return fmt.Errorf("record %s error: %w", source, err)
	}
	return nil
}

// SourceStates returns the last known health of every source, keyed by source name.
func (s *Store) SourceStates(ctx context.Context) (_ map[string]domain.SourceState, err error) {
	defer func() { err = s.scrub.clean(err) }()
	const q = `
SELECT source, COALESCE(last_success_at,''), COALESCE(last_error,''),
       COALESCE(last_error_at,''), COALESCE(item_count,0)
FROM source_state`

	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("read source states: %w", err)
	}
	defer func() { _ = rows.Close() }()

	states := map[string]domain.SourceState{}
	for rows.Next() {
		var (
			st             domain.SourceState
			success, errAt string
		)
		if err := rows.Scan(&st.Source, &success, &st.LastError, &errAt, &st.ItemCount); err != nil {
			return nil, fmt.Errorf("scan source state: %w", err)
		}
		if err := parseInto(map[*time.Time]string{
			&st.LastSuccessAt: success, &st.LastErrorAt: errAt,
		}); err != nil {
			return nil, fmt.Errorf("source state %s: %w", st.Source, err)
		}
		states[st.Source] = st
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read source states: %w", err)
	}
	return states, nil
}

// ---------------------------------------------------------------- last visit

// LastVisit returns when the dashboard was last viewed, or the zero time if it never was (QS-1.3).
func (s *Store) LastVisit(ctx context.Context) (_ time.Time, err error) {
	defer func() { err = s.scrub.clean(err) }()
	v, err := s.appState(ctx, lastVisitKey)
	if err != nil {
		return time.Time{}, err
	}
	t, err := parseTime(v)
	if err != nil {
		return time.Time{}, fmt.Errorf("last visit: %w", err)
	}
	return t, nil
}

// SetLastVisit records t as the moment the dashboard was last viewed.
func (s *Store) SetLastVisit(ctx context.Context, t time.Time) (err error) {
	defer func() { err = s.scrub.clean(err) }()
	return s.setAppState(ctx, lastVisitKey, sqlTime(t))
}

// appState reads one app_state value, returning "" when the key is absent.
func (s *Store) appState(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(value,'') FROM app_state WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read app_state %s: %w", key, err)
	}
	return v, nil
}

// setAppState writes one app_state value.
func (s *Store) setAppState(ctx context.Context, key, value string) error {
	const q = `INSERT INTO app_state (key, value) VALUES (?,?)
	           ON CONFLICT(key) DO UPDATE SET value = excluded.value`
	if _, err := s.db.ExecContext(ctx, q, key, value); err != nil {
		return fmt.Errorf("write app_state %s: %w", key, err)
	}
	return nil
}

// ---------------------------------------------------------------- refresh lease

// AcquireRefreshLease takes the refresh lease for holder until now+ttl, and reports whether it
// got it. Two machines must never refresh at the same time (QS-1.7), so the whole decision is one
// statement: a read followed by a write would let both pass the read.
//
// The lease lives in app_state under refresh_lease, with the value "holder|expiry". The
// ON CONFLICT clause admits the write only when the stored lease has expired or is held by the
// same holder — RFC 3339 UTC strings compare lexicographically, so the expiry test is a plain
// string comparison. One affected row means the lease is ours.
func (s *Store) AcquireRefreshLease(ctx context.Context, holder string, now time.Time, ttl time.Duration) (_ bool, err error) {
	defer func() { err = s.scrub.clean(err) }()
	if strings.Contains(holder, "|") {
		return false, errors.New("refresh lease holder must not contain '|'")
	}
	const q = `
INSERT INTO app_state (key, value) VALUES (?, ?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value
WHERE substr(app_state.value, instr(app_state.value, '|') + 1) <= ?
   OR substr(app_state.value, 1, instr(app_state.value, '|') - 1) = ?`

	value := holder + "|" + sqlTime(now.Add(ttl))
	res, err := s.db.ExecContext(ctx, q, leaseKey, value, sqlTime(now), holder)
	if err != nil {
		return false, fmt.Errorf("acquire refresh lease: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("acquire refresh lease: %w", err)
	}
	return n == 1, nil
}

// ReleaseRefreshLease drops the lease if holder is the one holding it. Releasing a lease someone
// else has taken over — after this holder's own lease expired, say — must not free theirs.
func (s *Store) ReleaseRefreshLease(ctx context.Context, holder string) (err error) {
	defer func() { err = s.scrub.clean(err) }()
	const q = `DELETE FROM app_state
	           WHERE key = ? AND substr(value, 1, instr(value, '|') - 1) = ?`
	if _, err := s.db.ExecContext(ctx, q, leaseKey, holder); err != nil {
		return fmt.Errorf("release refresh lease: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------- refresh runs

// StartRun records the beginning of a refresh run and returns its id.
func (s *Store) StartRun(ctx context.Context, trigger string, at time.Time) (_ int64, err error) {
	defer func() { err = s.scrub.clean(err) }()
	const q = `INSERT INTO refresh_run (started_at, finished_at, "trigger", ok, detail)
	           VALUES (?, '', ?, 0, '')`
	res, err := s.db.ExecContext(ctx, q, sqlTime(at), trigger)
	if err != nil {
		return 0, fmt.Errorf("start run: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("start run: %w", err)
	}
	return id, nil
}

// FinishRun records the outcome of the refresh run with the given id.
func (s *Store) FinishRun(ctx context.Context, id int64, at time.Time, ok bool, detail string) (err error) {
	defer func() { err = s.scrub.clean(err) }()
	const q = `UPDATE refresh_run SET finished_at = ?, ok = ?, detail = ? WHERE id = ?`
	if _, err := s.db.ExecContext(ctx, q, sqlTime(at), boolToInt(ok), detail, id); err != nil {
		return fmt.Errorf("finish run %d: %w", id, err)
	}
	return nil
}

// runSelect reads one refresh_run row, newest first. The caller appends its own WHERE clause and
// the LIMIT; the column list is shared so that LastRun and LastSuccessfulRun cannot drift apart in
// what they read or in the order they scan it.
const runSelect = `
SELECT id, COALESCE(started_at,''), COALESCE(finished_at,''), COALESCE("trigger",''),
       COALESCE(ok,0), COALESCE(detail,'')
FROM refresh_run `

// LastRun returns the most recently started refresh run. A store that has never run one is not an
// error: the zero RefreshRun comes back with a nil error, and its StartedAt is the zero time.
func (s *Store) LastRun(ctx context.Context) (_ domain.RefreshRun, err error) {
	defer func() { err = s.scrub.clean(err) }()
	return s.queryRun(ctx, runSelect+`ORDER BY id DESC LIMIT 1`, "last run")
}

// LastSuccessfulRun returns the most recently finished refresh run that succeeded, or the zero
// RefreshRun with a nil error when none ever has — the same way LastVisit and LastRun answer
// "never" (ports.Store).
//
// Both halves of the condition are load-bearing. StartRun inserts the row with ok = 0 and an empty
// finished_at and FinishRun fills both in, so an open run can never satisfy ok = 1 — but a run
// finished at the zero time would store an empty finished_at beside ok = 1, and a run with no
// finishing time has no time to put in the header. Requiring both keeps this method's answer one
// that can always be rendered: a run that is still running is not a successful one.
func (s *Store) LastSuccessfulRun(ctx context.Context) (_ domain.RefreshRun, err error) {
	defer func() { err = s.scrub.clean(err) }()
	return s.queryRun(ctx,
		runSelect+`WHERE ok = 1 AND COALESCE(finished_at,'') <> '' ORDER BY id DESC LIMIT 1`,
		"last successful run")
}

// queryRun reads the single refresh_run row q selects, or the zero RefreshRun when q selects none.
// what names the row in the error text.
func (s *Store) queryRun(ctx context.Context, q, what string) (domain.RefreshRun, error) {
	var (
		run               domain.RefreshRun
		started, finished string
		ok                int
	)
	err := s.db.QueryRowContext(ctx, q).Scan(&run.ID, &started, &finished, &run.Trigger, &ok, &run.Detail)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.RefreshRun{}, nil
	}
	if err != nil {
		return domain.RefreshRun{}, fmt.Errorf("read %s: %w", what, err)
	}
	run.OK = ok != 0
	if err := parseInto(map[*time.Time]string{
		&run.StartedAt: started, &run.FinishedAt: finished,
	}); err != nil {
		return domain.RefreshRun{}, fmt.Errorf("%s: %w", what, err)
	}
	return run, nil
}

// ---------------------------------------------------------------- notifications

// MarkNotified records that keys have been announced. An existing row keeps its original sent_at:
// an item is announced once, and the first announcement is the one that counts (FR-6.1 AC2).
func (s *Store) MarkNotified(ctx context.Context, keys []string, at time.Time) (err error) {
	defer func() { err = s.scrub.clean(err) }()
	const q = `INSERT INTO notified (key, sent_at) VALUES (?,?) ON CONFLICT(key) DO NOTHING`
	return s.inTx(ctx, func(tx *sql.Tx) error {
		for _, k := range keys {
			if _, err := tx.ExecContext(ctx, q, k, sqlTime(at)); err != nil {
				return fmt.Errorf("mark notified %s: %w", k, err)
			}
		}
		return nil
	})
}

// UnnotifiedKeys returns those of keys that have not been announced yet, in the order given. The
// lookup runs in chunks so a long list cannot exceed SQLite's variable limit.
func (s *Store) UnnotifiedKeys(ctx context.Context, keys []string) (_ []string, err error) {
	defer func() { err = s.scrub.clean(err) }()
	sent := make(map[string]bool, len(keys))
	for _, chunk := range chunks(keys, maxParams) {
		args := make([]any, 0, len(chunk))
		for _, k := range chunk {
			args = append(args, k)
		}
		q := `SELECT key FROM notified WHERE key IN (` + placeholders(len(chunk)) + `)`
		if err := s.collectSent(ctx, q, args, sent); err != nil {
			return nil, err
		}
	}
	var out []string
	for _, k := range keys {
		if !sent[k] {
			out = append(out, k)
		}
	}
	return out, nil
}

// collectSent adds every key the query returns to sent.
func (s *Store) collectSent(ctx context.Context, q string, args []any, sent map[string]bool) error {
	rows, err := s.db.QueryContext(ctx, q, args...) //nolint:gosec // placeholders are "?,?,…"; every value is a bound parameter
	if err != nil {
		return fmt.Errorf("read notified: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return fmt.Errorf("scan notified: %w", err)
		}
		sent[k] = true
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read notified: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------- test support

// TruncateAll empties every table except schema_migrations. It exists for the tests, which is why
// it is a method on *Store and not part of ports.Store.
func (s *Store) TruncateAll(ctx context.Context) (err error) {
	defer func() { err = s.scrub.clean(err) }()
	tables := []string{"items", "builds", "refresh_run", "source_state", "app_state", "notified"}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		for _, t := range tables {
			if _, err := tx.ExecContext(ctx, `DELETE FROM `+t); err != nil { //nolint:gosec // t comes from the fixed list above
				return fmt.Errorf("truncate %s: %w", t, err)
			}
		}
		return nil
	})
}

// ---------------------------------------------------------------- helpers

// inTx runs fn inside a transaction, rolling back on any error.
func (s *Store) inTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// sqlTime renders a time for storage: RFC 3339 in UTC, and the empty string for the zero time, so
// that a missing due date round-trips as a zero time.Time rather than as year 1.
func sqlTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// parseTime is the inverse of sqlTime. It converts to UTC rather than trusting the stored offset:
// sqlTime only ever writes "Z", but a row repaired by hand or written by a future migration could
// carry +02:00, and the package guarantees that everything reads back in UTC.
func parseTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse time %q: %w", s, err)
	}
	return t.UTC(), nil
}

// parseInto parses each stored string into the time it belongs to, failing on the first bad one.
func parseInto(fields map[*time.Time]string) error {
	for dst, s := range fields {
		t, err := parseTime(s)
		if err != nil {
			return err
		}
		*dst = t
	}
	return nil
}

// placeholders renders "?,?,…" for n bound parameters.
func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// chunks splits xs into slices of at most size elements.
func chunks(xs []string, size int) [][]string {
	var out [][]string
	for i := 0; i < len(xs); i += size {
		end := min(i+size, len(xs))
		out = append(out, xs[i:end])
	}
	return out
}

// boolToInt stores a bool as SQLite's 0 or 1.
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
