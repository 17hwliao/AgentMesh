package usagekafka

import (
	"context"
	"time"
)

// RecoverUnprojected republishes terminal ledger references after retention or
// broker storage loss. Projection identity makes a concurrent late delivery safe.
func (s *MySQLStore) RecoverUnprojected(ctx context.Context, now time.Time, limit int) (int, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT o.event_id FROM usage_kafka_outbox o LEFT JOIN usage_kafka_projections p ON p.event_id=o.event_id WHERE o.published_at<=? AND p.event_id IS NULL ORDER BY o.published_at LIMIT ? FOR UPDATE SKIP LOCKED`, now.Add(-5*time.Minute), limit)
	if err != nil {
		return 0, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	for _, id := range ids {
		if _, err = tx.ExecContext(ctx, `UPDATE usage_kafka_outbox SET published_at=NULL,available_at=?,lease_owner=NULL,lease_until=NULL WHERE event_id=?`, now, id); err != nil {
			return 0, err
		}
	}
	return len(ids), tx.Commit()
}
