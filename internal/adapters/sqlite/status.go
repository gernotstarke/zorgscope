package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/gernotstarke/zorgscope/internal/domain"
)

// RecordStatus implements ports.StatusStore.
func (s *Store) RecordStatus(ctx context.Context, st domain.FetchStatus) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO fetch_status (source_id, kind, last_success, last_error, error_msg, next_run, item_count, duration_ms, in_flight, auth_failed)
		VALUES (?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(source_id) DO UPDATE SET kind=excluded.kind, last_success=excluded.last_success, last_error=excluded.last_error,
		error_msg=excluded.error_msg, next_run=excluded.next_run, item_count=excluded.item_count, duration_ms=excluded.duration_ms,
		in_flight=excluded.in_flight, auth_failed=excluded.auth_failed`,
		st.SourceID, st.Kind, unix(st.LastSuccess), unix(st.LastError), st.ErrorMsg, unix(st.NextRun), st.ItemCount,
		st.Duration.Milliseconds(), boolInt(st.InFlight), boolInt(st.AuthFailed))
	return err
}

const statusColumns = `source_id, kind, last_success, last_error, error_msg, next_run, item_count, duration_ms, in_flight, auth_failed`

func scanStatus(sc interface{ Scan(...any) error }) (domain.FetchStatus, error) {
	var st domain.FetchStatus
	var ls, le, nr, dur int64
	var inflight, auth int
	err := sc.Scan(&st.SourceID, &st.Kind, &ls, &le, &st.ErrorMsg, &nr, &st.ItemCount, &dur, &inflight, &auth)
	st.LastSuccess, st.LastError, st.NextRun = fromUnix(ls), fromUnix(le), fromUnix(nr)
	st.Duration = time.Duration(dur) * time.Millisecond
	st.InFlight, st.AuthFailed = inflight == 1, auth == 1
	return st, err
}

// Status implements ports.StatusStore.
func (s *Store) Status(ctx context.Context, sourceID string) (*domain.FetchStatus, error) {
	st, err := scanStatus(s.db.QueryRowContext(ctx, `SELECT `+statusColumns+` FROM fetch_status WHERE source_id = ?`, sourceID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &st, nil
}

// Statuses implements ports.StatusStore.
func (s *Store) Statuses(ctx context.Context) ([]domain.FetchStatus, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+statusColumns+` FROM fetch_status ORDER BY source_id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []domain.FetchStatus
	for rows.Next() {
		st, err := scanStatus(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

// boolInt converts b to SQLite's 0/1 integer representation.
func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
