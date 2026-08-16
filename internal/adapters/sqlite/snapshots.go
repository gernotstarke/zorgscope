package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/gernotstarke/zorgscope/internal/domain"
)

// PutSnapshot implements ports.SnapshotStore (upsert by source+date).
func (s *Store) PutSnapshot(ctx context.Context, snap domain.Snapshot) error {
	ids, err := json.Marshal(snap.IDList())
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO snapshots (source_id, date, taken_at, ids) VALUES (?,?,?,?)
		ON CONFLICT(source_id, date) DO UPDATE SET taken_at = excluded.taken_at, ids = excluded.ids`,
		snap.SourceID, snap.Date, unix(snap.TakenAt), string(ids))
	return err
}

func (s *Store) querySnapshot(ctx context.Context, query string, args ...any) (*domain.Snapshot, error) {
	var src, date, ids string
	var taken int64
	err := s.db.QueryRowContext(ctx, query, args...).Scan(&src, &date, &taken, &ids)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var list []string
	if err := json.Unmarshal([]byte(ids), &list); err != nil {
		return nil, err
	}
	snap := domain.NewSnapshot(src, date, fromUnix(taken), list)
	return &snap, nil
}

// LatestSnapshot implements ports.SnapshotStore.
func (s *Store) LatestSnapshot(ctx context.Context, sourceID string) (*domain.Snapshot, error) {
	return s.querySnapshot(ctx, `SELECT source_id, date, taken_at, ids FROM snapshots WHERE source_id = ? ORDER BY date DESC LIMIT 1`, sourceID)
}

// SnapshotBefore implements ports.SnapshotStore.
func (s *Store) SnapshotBefore(ctx context.Context, sourceID, date string) (*domain.Snapshot, error) {
	return s.querySnapshot(ctx, `SELECT source_id, date, taken_at, ids FROM snapshots WHERE source_id = ? AND date < ? ORDER BY date DESC LIMIT 1`, sourceID, date)
}

// PruneSnapshots implements ports.SnapshotStore.
func (s *Store) PruneSnapshots(ctx context.Context, beforeDate string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM snapshots WHERE date < ?`, beforeDate)
	return err
}
