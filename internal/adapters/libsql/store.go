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
package libsql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	_ "github.com/tursodatabase/libsql-client-go/libsql" // registers the "libsql" sql driver

	"github.com/gernotstarke/zorgscope/internal/domain"
	"github.com/gernotstarke/zorgscope/internal/ports"
)

// Store is the libSQL-backed ports.Store.
type Store struct{ db *sql.DB }

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
// No error returned from this package ever contains the DSN: the auth token lives in it, and a
// secret must not reach a log (QS-4.3).
func Open(rawURL, authToken string) (*Store, error) {
	dsn, err := dsn(rawURL, authToken)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("libsql", dsn)
	if err != nil {
		return nil, fmt.Errorf("open libsql database: %w", err)
	}
	return &Store{db: db}, nil
}

// dsn builds the driver DSN, appending the auth token as a query parameter when there is one.
// Its errors name the host at most, never the DSN (QS-4.3).
func dsn(rawURL, authToken string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", errors.New("invalid database URL")
	}
	if authToken == "" {
		return u.String(), nil
	}
	q := u.Query()
	q.Set("authToken", authToken)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// Close releases the underlying connection pool.
func (s *Store) Close() error {
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
  source, external_id, kind, repo, number, title, url, author, state,
  created_at, updated_at, due_at, priority, first_seen_at, last_fetched_at, payload)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,NULL)
ON CONFLICT(source, external_id) DO UPDATE SET
  kind = excluded.kind, repo = excluded.repo, number = excluded.number,
  title = excluded.title, url = excluded.url, author = excluded.author,
  state = excluded.state, created_at = excluded.created_at,
  updated_at = excluded.updated_at, due_at = excluded.due_at,
  priority = excluded.priority, last_fetched_at = excluded.last_fetched_at`

// itemColumns is the read side of the same row. COALESCE keeps a NULL written by an older schema
// or by hand from failing the scan.
const itemColumns = `
  source, external_id, COALESCE(kind,''), COALESCE(repo,''), COALESCE(number,0),
  COALESCE(title,''), COALESCE(url,''), COALESCE(author,''), COALESCE(state,''),
  COALESCE(created_at,''), COALESCE(updated_at,''), COALESCE(due_at,''),
  COALESCE(priority,0), first_seen_at`

// ReplaceItems makes the stored items of one source exactly items: it upserts every item,
// preserving the first_seen_at of rows that already existed, and deletes the rows of that source
// that items no longer contains. Other sources are untouched (FR-5.5 AC1). It returns how many of
// the items were seen for the first time, i.e. how many rows were inserted rather than updated.
//
// Everything happens in one transaction, so a failed refresh leaves the previous set intact
// rather than a half-replaced one.
func (s *Store) ReplaceItems(ctx context.Context, source string, items []domain.Item, now time.Time) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// The ids already stored for this source answer two questions in one round trip: which
	// incoming items are new, and which stored rows have gone away.
	known, err := storedIDs(ctx, tx, source)
	if err != nil {
		return 0, err
	}

	present := make(map[string]bool, len(items))
	newCount := 0
	for _, it := range items {
		if !known[it.ExternalID] && !present[it.ExternalID] {
			newCount++
		}
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
	return newCount, nil
}

// upsertArgs lays out one item in the order upsertItemSQL expects. The item's own FirstSeenAt is
// ignored: a fetcher does not know it, and the store is the only place that decides it.
func upsertArgs(it domain.Item, source string, now time.Time) []any {
	return []any{
		source, it.ExternalID, string(it.Kind), it.Repo, it.Number, it.Title, it.URL,
		it.Author, it.State, sqlTime(it.CreatedAt), sqlTime(it.UpdatedAt), sqlTime(it.DueAt),
		it.Priority, sqlTime(now), sqlTime(now),
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
func (s *Store) Items(ctx context.Context) ([]domain.Item, error) {
	q := `SELECT ` + itemColumns + ` FROM items ORDER BY first_seen_at DESC, source, external_id`
	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("read items: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var items []domain.Item
	for rows.Next() {
		var (
			it                                   domain.Item
			kind, created, updated, due, firstAt string
		)
		if err := rows.Scan(&it.Source, &it.ExternalID, &kind, &it.Repo, &it.Number, &it.Title,
			&it.URL, &it.Author, &it.State, &created, &updated, &due, &it.Priority, &firstAt); err != nil {
			return nil, fmt.Errorf("scan item: %w", err)
		}
		it.Kind = domain.Kind(kind)
		if err := parseInto(map[*time.Time]string{
			&it.CreatedAt: created, &it.UpdatedAt: updated, &it.DueAt: due, &it.FirstSeenAt: firstAt,
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

// ---------------------------------------------------------------- builds and metrics

// UpsertBuilds records the latest CI run per repository, stamping every row with now as its fetch
// time.
func (s *Store) UpsertBuilds(ctx context.Context, builds []domain.Build, now time.Time) error {
	const q = `
INSERT INTO builds (repo, workflow, conclusion, status, run_url, finished_at, fetched_at)
VALUES (?,?,?,?,?,?,?)
ON CONFLICT(repo) DO UPDATE SET
  workflow = excluded.workflow, conclusion = excluded.conclusion, status = excluded.status,
  run_url = excluded.run_url, finished_at = excluded.finished_at, fetched_at = excluded.fetched_at`

	return s.inTx(ctx, func(tx *sql.Tx) error {
		for _, b := range builds {
			_, err := tx.ExecContext(ctx, q, b.Repo, b.Workflow, b.Conclusion, b.Status,
				b.RunURL, sqlTime(b.FinishedAt), sqlTime(now))
			if err != nil {
				return fmt.Errorf("upsert build %s: %w", b.Repo, err)
			}
		}
		return nil
	})
}

// Builds returns every stored build.
func (s *Store) Builds(ctx context.Context) ([]domain.Build, error) {
	const q = `
SELECT repo, COALESCE(workflow,''), COALESCE(conclusion,''), COALESCE(status,''),
       COALESCE(run_url,''), COALESCE(finished_at,''), COALESCE(fetched_at,'')
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
		if err := rows.Scan(&b.Repo, &b.Workflow, &b.Conclusion, &b.Status, &b.RunURL,
			&finished, &fetched); err != nil {
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

// UpsertMetrics records an analytics snapshot per site and window, stamping every row with now as
// its fetch time.
func (s *Store) UpsertMetrics(ctx context.Context, metrics []domain.Metric, now time.Time) error {
	const q = `
INSERT INTO metrics (site, window_days, visitors, pageviews, prev_visitors, prev_pageviews, fetched_at)
VALUES (?,?,?,?,?,?,?)
ON CONFLICT(site, window_days) DO UPDATE SET
  visitors = excluded.visitors, pageviews = excluded.pageviews,
  prev_visitors = excluded.prev_visitors, prev_pageviews = excluded.prev_pageviews,
  fetched_at = excluded.fetched_at`

	return s.inTx(ctx, func(tx *sql.Tx) error {
		for _, m := range metrics {
			_, err := tx.ExecContext(ctx, q, m.Site, m.WindowDays, m.Visitors, m.Pageviews,
				m.PrevVisitors, m.PrevPageviews, sqlTime(now))
			if err != nil {
				return fmt.Errorf("upsert metric %s/%d: %w", m.Site, m.WindowDays, err)
			}
		}
		return nil
	})
}

// Metrics returns every stored metric.
func (s *Store) Metrics(ctx context.Context) ([]domain.Metric, error) {
	const q = `
SELECT site, COALESCE(window_days,0), COALESCE(visitors,0), COALESCE(pageviews,0),
       COALESCE(prev_visitors,0), COALESCE(prev_pageviews,0), COALESCE(fetched_at,'')
FROM metrics ORDER BY site, window_days`

	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("read metrics: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var metrics []domain.Metric
	for rows.Next() {
		var (
			m       domain.Metric
			fetched string
		)
		if err := rows.Scan(&m.Site, &m.WindowDays, &m.Visitors, &m.Pageviews,
			&m.PrevVisitors, &m.PrevPageviews, &fetched); err != nil {
			return nil, fmt.Errorf("scan metric: %w", err)
		}
		if err := parseInto(map[*time.Time]string{&m.FetchedAt: fetched}); err != nil {
			return nil, fmt.Errorf("metric %s: %w", m.Site, err)
		}
		metrics = append(metrics, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read metrics: %w", err)
	}
	return metrics, nil
}

// ---------------------------------------------------------------- source health

// RecordSourceOK records a successful fetch. It writes only the success columns: a success must
// not erase the record of the last error, and vice versa (FR-1.4 AC2).
func (s *Store) RecordSourceOK(ctx context.Context, source string, at time.Time, count int) error {
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
func (s *Store) RecordSourceError(ctx context.Context, source string, at time.Time, msg string) error {
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
func (s *Store) SourceStates(ctx context.Context) (map[string]domain.SourceState, error) {
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
func (s *Store) LastVisit(ctx context.Context) (time.Time, error) {
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
func (s *Store) SetLastVisit(ctx context.Context, t time.Time) error {
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
func (s *Store) AcquireRefreshLease(ctx context.Context, holder string, now time.Time, ttl time.Duration) (bool, error) {
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
func (s *Store) ReleaseRefreshLease(ctx context.Context, holder string) error {
	const q = `DELETE FROM app_state
	           WHERE key = ? AND substr(value, 1, instr(value, '|') - 1) = ?`
	if _, err := s.db.ExecContext(ctx, q, leaseKey, holder); err != nil {
		return fmt.Errorf("release refresh lease: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------- refresh runs

// StartRun records the beginning of a refresh run and returns its id.
func (s *Store) StartRun(ctx context.Context, trigger string, at time.Time) (int64, error) {
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
func (s *Store) FinishRun(ctx context.Context, id int64, at time.Time, ok bool, detail string) error {
	const q = `UPDATE refresh_run SET finished_at = ?, ok = ?, detail = ? WHERE id = ?`
	if _, err := s.db.ExecContext(ctx, q, sqlTime(at), boolToInt(ok), detail, id); err != nil {
		return fmt.Errorf("finish run %d: %w", id, err)
	}
	return nil
}

// LastRun returns the most recently started refresh run. A store that has never run one is not an
// error: the zero RefreshRun comes back with a nil error, and its StartedAt is the zero time.
func (s *Store) LastRun(ctx context.Context) (domain.RefreshRun, error) {
	const q = `
SELECT id, COALESCE(started_at,''), COALESCE(finished_at,''), COALESCE("trigger",''),
       COALESCE(ok,0), COALESCE(detail,'')
FROM refresh_run ORDER BY id DESC LIMIT 1`

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
		return domain.RefreshRun{}, fmt.Errorf("read last run: %w", err)
	}
	run.OK = ok != 0
	if err := parseInto(map[*time.Time]string{
		&run.StartedAt: started, &run.FinishedAt: finished,
	}); err != nil {
		return domain.RefreshRun{}, fmt.Errorf("last run: %w", err)
	}
	return run, nil
}

// ---------------------------------------------------------------- notifications

// MarkNotified records that keys have been announced. An existing row keeps its original sent_at:
// an item is announced once, and the first announcement is the one that counts (FR-6.1 AC2).
func (s *Store) MarkNotified(ctx context.Context, keys []string, at time.Time) error {
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
func (s *Store) UnnotifiedKeys(ctx context.Context, keys []string) ([]string, error) {
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
func (s *Store) TruncateAll(ctx context.Context) error {
	tables := []string{"items", "builds", "metrics", "refresh_run", "source_state", "app_state", "notified"}
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

// parseTime is the inverse of sqlTime.
func parseTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse time %q: %w", s, err)
	}
	return t, nil
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
