package libsql

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// bootstrapSQL creates the bookkeeping table that Migrate itself needs before it can decide what
// to apply. Every migration file also creates it, so applying 0001 to an empty database works
// whichever way round it happens.
const bootstrapSQL = `CREATE TABLE IF NOT EXISTS schema_migrations (
  version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`

// Migrate brings the schema up to date by applying every embedded migration whose version is not
// yet recorded in schema_migrations, in file-name order. Each migration is applied inside one
// transaction together with its schema_migrations row, so a half-applied migration cannot be
// recorded as done. It is safe to call on every startup.
func (s *Store) Migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, bootstrapSQL); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}
	applied, err := s.appliedVersions(ctx)
	if err != nil {
		return err
	}
	names, err := migrationNames()
	if err != nil {
		return err
	}
	for _, name := range names {
		version, err := versionOf(name)
		if err != nil {
			return err
		}
		if applied[version] {
			continue
		}
		body, err := migrationFS.ReadFile(path.Join("migrations", name))
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}
		if err := s.apply(ctx, version, string(body)); err != nil {
			return fmt.Errorf("migration %s: %w", name, err)
		}
	}
	return nil
}

// appliedVersions returns the set of migration versions already recorded as applied.
func (s *Store) appliedVersions(ctx context.Context) (map[int]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("read schema_migrations: %w", err)
	}
	defer func() { _ = rows.Close() }()

	applied := map[int]bool{}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("scan schema_migrations: %w", err)
		}
		applied[v] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read schema_migrations: %w", err)
	}
	return applied, nil
}

// apply runs every statement of one migration and records its version, all in one transaction.
func (s *Store) apply(ctx context.Context, version int, body string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for _, stmt := range splitStatements(body) {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("exec %q: %w", firstLine(stmt), err)
		}
	}
	if err := recordVersion(ctx, tx, version); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// recordVersion marks a migration as applied. INSERT OR IGNORE, because two machines starting at
// the same time may both apply an idempotent migration; only the row must not be duplicated.
func recordVersion(ctx context.Context, tx *sql.Tx, version int) error {
	const q = `INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)
	           ON CONFLICT(version) DO NOTHING`
	if _, err := tx.ExecContext(ctx, q, version, sqlTime(time.Now())); err != nil {
		return fmt.Errorf("record version %d: %w", version, err)
	}
	return nil
}

// migrationNames lists the embedded migration files in ascending name order, which is the order
// they are applied in.
func migrationNames() ([]string, error) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("read migrations: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

// versionOf reads the numeric prefix of a migration file name: 0001_initial.sql is version 1.
func versionOf(name string) (int, error) {
	prefix, _, ok := strings.Cut(name, "_")
	if !ok {
		return 0, fmt.Errorf("migration %s: name must start with a version, e.g. 0001_initial.sql", name)
	}
	v, err := strconv.Atoi(prefix)
	if err != nil {
		return 0, fmt.Errorf("migration %s: %w", name, err)
	}
	return v, nil
}

// splitStatements cuts a migration file into single statements, because the driver sends one
// statement per request when it is given arguments and wraps multi-statement scripts in its own
// transaction — which would nest inside ours. Line comments are stripped first, so a semicolon in
// a comment cannot split a statement. Migrations must therefore not contain a semicolon inside a
// string literal; none of them needs to.
func splitStatements(body string) []string {
	var sb strings.Builder
	for _, line := range strings.Split(body, "\n") {
		if i := strings.Index(line, "--"); i >= 0 {
			line = line[:i]
		}
		sb.WriteString(line)
		sb.WriteString("\n")
	}
	var stmts []string
	for _, raw := range strings.Split(sb.String(), ";") {
		if stmt := strings.TrimSpace(raw); stmt != "" {
			stmts = append(stmts, stmt)
		}
	}
	return stmts
}

// firstLine is used to name a failing statement in an error without dumping the whole statement.
func firstLine(stmt string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(stmt), "\n")
	return line
}
