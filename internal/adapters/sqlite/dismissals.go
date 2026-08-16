package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"github.com/gernotstarke/zorgscope/internal/domain"
)

// PutDismissal implements ports.DismissalStore.
func (s *Store) PutDismissal(ctx context.Context, d domain.Dismissal) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO dismissals (source_id, external_id, updated_at, dismissed_at) VALUES (?,?,?,?)
		ON CONFLICT(source_id, external_id) DO UPDATE SET updated_at = excluded.updated_at, dismissed_at = excluded.dismissed_at`,
		d.ID.SourceID, d.ID.ExternalID, unix(d.UpdatedAt), unix(d.DismissedAt))
	return err
}

// Dismissal implements ports.DismissalStore.
func (s *Store) Dismissal(ctx context.Context, id domain.ItemID) (*domain.Dismissal, error) {
	var upd, dis int64
	err := s.db.QueryRowContext(ctx, `SELECT updated_at, dismissed_at FROM dismissals WHERE source_id = ? AND external_id = ?`, id.SourceID, id.ExternalID).Scan(&upd, &dis)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &domain.Dismissal{ID: id, UpdatedAt: fromUnix(upd), DismissedAt: fromUnix(dis)}, nil
}

// Dismissals implements ports.DismissalStore.
func (s *Store) Dismissals(ctx context.Context) (map[domain.ItemID]domain.Dismissal, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT source_id, external_id, updated_at, dismissed_at FROM dismissals`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[domain.ItemID]domain.Dismissal{}
	for rows.Next() {
		var d domain.Dismissal
		var upd, dis int64
		if err := rows.Scan(&d.ID.SourceID, &d.ID.ExternalID, &upd, &dis); err != nil {
			return nil, err
		}
		d.UpdatedAt, d.DismissedAt = fromUnix(upd), fromUnix(dis)
		out[d.ID] = d
	}
	return out, rows.Err()
}
