package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/gernotstarke/zorgscope/internal/domain"
)

// ReplaceItems implements ports.ItemStore.
func (s *Store) ReplaceItems(ctx context.Context, sourceID string, items []domain.Item, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	firstSeen := map[string]int64{}
	rows, err := tx.QueryContext(ctx, `SELECT external_id, first_seen FROM items WHERE source_id = ?`, sourceID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id string
		var fs int64
		if err := rows.Scan(&id, &fs); err != nil {
			_ = rows.Close()
			return err
		}
		firstSeen[id] = fs
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_ = rows.Close()

	if _, err := tx.ExecContext(ctx, `DELETE FROM items WHERE source_id = ?`, sourceID); err != nil {
		return err
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO items (source_id, external_id, kind, title, url, author, created_at, updated_at,
		last_activity_by, last_activity_at, labels, payload, first_seen) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer func() { _ = stmt.Close() }()
	for _, it := range items {
		fs, ok := firstSeen[it.ID.ExternalID]
		if !ok {
			fs = now.Unix()
		}
		labels, err := json.Marshal(it.Labels)
		if err != nil {
			return err
		}
		if _, err := stmt.ExecContext(ctx, sourceID, it.ID.ExternalID, string(it.Kind), it.Title, it.URL, it.Author,
			unix(it.CreatedAt), unix(it.UpdatedAt), it.LastActivityBy, unix(it.LastActivityAt), string(labels), string(it.Payload), fs); err != nil {
			return err
		}
	}
	return tx.Commit()
}

const itemColumns = `source_id, external_id, kind, title, url, author, created_at, updated_at, last_activity_by, last_activity_at, labels, payload, first_seen`

func scanItems(rows *sql.Rows) ([]domain.Item, error) {
	defer func() { _ = rows.Close() }()
	var out []domain.Item
	for rows.Next() {
		var it domain.Item
		var kind, labels, payload string
		var created, updated, lastAt, first int64
		if err := rows.Scan(&it.ID.SourceID, &it.ID.ExternalID, &kind, &it.Title, &it.URL, &it.Author, &created, &updated,
			&it.LastActivityBy, &lastAt, &labels, &payload, &first); err != nil {
			return nil, err
		}
		it.Kind = domain.Kind(kind)
		it.CreatedAt, it.UpdatedAt, it.LastActivityAt, it.FirstSeen = fromUnix(created), fromUnix(updated), fromUnix(lastAt), fromUnix(first)
		if err := json.Unmarshal([]byte(labels), &it.Labels); err != nil {
			return nil, err
		}
		if payload != "" {
			it.Payload = json.RawMessage(payload)
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// Items implements ports.ItemStore.
func (s *Store) Items(ctx context.Context, sourceID string) ([]domain.Item, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+itemColumns+` FROM items WHERE source_id = ? ORDER BY created_at DESC, external_id`, sourceID)
	if err != nil {
		return nil, err
	}
	return scanItems(rows)
}

// AllItems implements ports.ItemStore.
func (s *Store) AllItems(ctx context.Context) ([]domain.Item, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+itemColumns+` FROM items ORDER BY created_at DESC, source_id, external_id`)
	if err != nil {
		return nil, err
	}
	return scanItems(rows)
}

// ExternalIDs implements ports.ItemStore.
func (s *Store) ExternalIDs(ctx context.Context, sourceID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT external_id FROM items WHERE source_id = ? ORDER BY external_id`, sourceID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
