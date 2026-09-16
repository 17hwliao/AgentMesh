package usagekafka

import (
	"context"
	"errors"
)

// Summary exposes aggregate pipeline state only. It deliberately contains no
// event payload, request body, credential, or provider response data.
type Summary struct {
	PendingOutbox   int64 `json:"pending_outbox"`
	PublishedOutbox int64 `json:"published_outbox"`
	Projections     int64 `json:"projections"`
}

// Summary reads a consistent operational snapshot from the durable stores.
// It is intended for an authenticated administrative status endpoint.
func (s *MySQLStore) Summary(ctx context.Context) (Summary, error) {
	if s == nil || s.DB == nil {
		return Summary{}, errors.New("usage kafka DB is required")
	}
	var result Summary
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_kafka_outbox WHERE published_at IS NULL`).Scan(&result.PendingOutbox); err != nil {
		return Summary{}, err
	}
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_kafka_outbox WHERE published_at IS NOT NULL`).Scan(&result.PublishedOutbox); err != nil {
		return Summary{}, err
	}
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_kafka_projections`).Scan(&result.Projections); err != nil {
		return Summary{}, err
	}
	return result, nil
}
