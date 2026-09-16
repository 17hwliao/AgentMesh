package usagekafka

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type MySQLStore struct{ DB *sql.DB }

func (s *MySQLStore) Claim(ctx context.Context, limit int, now time.Time, owner string, leaseFor time.Duration) ([]OutboxRecord, error) {
	if s == nil || s.DB == nil {
		return nil, errors.New("usage kafka DB is required")
	}
	if limit < 1 {
		return nil, errors.New("outbox limit must be positive")
	}
	if owner == "" || leaseFor <= 0 {
		return nil, errors.New("outbox lease owner and duration are required")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT event_id,reservation_id,topic,payload,attempts,available_at FROM usage_kafka_outbox WHERE published_at IS NULL AND available_at<=? AND (lease_until IS NULL OR lease_until<=?) ORDER BY created_at LIMIT ? FOR UPDATE SKIP LOCKED`, now.UTC(), now.UTC(), limit)
	if err != nil {
		return nil, err
	}
	var records []OutboxRecord
	for rows.Next() {
		var r OutboxRecord
		if err := rows.Scan(&r.EventID, &r.ReservationID, &r.Topic, &r.Payload, &r.Attempts, &r.AvailableAt); err != nil {
			return nil, err
		}
		records = append(records, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for _, record := range records {
		if _, err := tx.ExecContext(ctx, `UPDATE usage_kafka_outbox SET lease_owner=?,lease_until=? WHERE event_id=? AND published_at IS NULL`, owner, now.UTC().Add(leaseFor), record.EventID); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return records, nil
}

func (s *MySQLStore) MarkPublished(ctx context.Context, eventID, owner string, now time.Time, publishErr error) error {
	if s == nil || s.DB == nil {
		return errors.New("usage kafka DB is required")
	}
	if owner == "" {
		return errors.New("outbox lease owner is required")
	}
	if publishErr == nil {
		result, err := s.DB.ExecContext(ctx, `UPDATE usage_kafka_outbox SET published_at=?,last_error='',lease_owner=NULL,lease_until=NULL WHERE event_id=? AND published_at IS NULL AND lease_owner=? AND lease_until>?`, now.UTC(), eventID, owner, now.UTC())
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed == 0 {
			return ErrLeaseLost
		}
		return nil
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var attempts int
	if err := tx.QueryRowContext(ctx, `SELECT attempts FROM usage_kafka_outbox WHERE event_id=? AND published_at IS NULL AND lease_owner=? AND lease_until>? FOR UPDATE`, eventID, owner, now.UTC()).Scan(&attempts); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrLeaseLost
		}
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE usage_kafka_outbox SET attempts=attempts+1,last_error=?,available_at=?,lease_owner=NULL,lease_until=NULL WHERE event_id=? AND published_at IS NULL AND lease_owner=?`, truncate(publishErr), now.UTC().Add(RetryDelay(time.Second, time.Minute, attempts+1)), eventID, owner)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *MySQLStore) Project(ctx context.Context, event Event) error {
	if err := event.Validate(); err != nil {
		return err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var existing string
	err = tx.QueryRowContext(ctx, `SELECT event_id FROM usage_kafka_projections WHERE reservation_id=? FOR UPDATE`, event.ReservationID).Scan(&existing)
	if err == nil {
		if existing == event.EventID {
			return tx.Commit()
		}
		return ErrConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO usage_kafka_projections (event_id,reservation_id,tenant_id,request_id,model,final_state,operation_version,reserved_units,settled_units,released_units,usage_observed,settlement_kind,finalized_at,projected_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, event.EventID, event.ReservationID, event.TenantID, event.RequestID, event.Model, event.FinalState, event.OperationVersion, event.ReservedUnits, event.SettledUnits, event.ReleasedUnits, event.UsageObserved, event.SettlementKind, event.FinalizedAt.UTC(), time.Now().UTC())
	if err != nil {
		return err
	}
	return tx.Commit()
}
