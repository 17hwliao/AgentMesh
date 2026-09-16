package usagekafka

import (
	"context"
	"errors"
	"sync"
	"time"
)

type MemoryStore struct {
	mu                      sync.Mutex
	records                 map[string]OutboxRecord
	projections             map[string]Event
	outboxByReservation     map[string]string
	projectionByReservation map[string]string
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{records: map[string]OutboxRecord{}, projections: map[string]Event{}, outboxByReservation: map[string]string{}, projectionByReservation: map[string]string{}}
}
func (s *MemoryStore) Add(event Event, availableAt time.Time) error {
	if err := event.Validate(); err != nil {
		return err
	}
	payload, _ := event.MarshalSafe()
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.records[event.EventID]; ok {
		return nil
	}
	if _, ok := s.outboxByReservation[event.ReservationID]; ok {
		return ErrConflict
	}
	s.records[event.EventID] = OutboxRecord{EventID: event.EventID, ReservationID: event.ReservationID, Topic: Topic, Payload: payload, AvailableAt: availableAt}
	s.outboxByReservation[event.ReservationID] = event.EventID
	return nil
}
func (s *MemoryStore) Claim(_ context.Context, limit int, now time.Time, owner string, leaseFor time.Duration) ([]OutboxRecord, error) {
	if limit < 1 {
		return nil, errors.New("outbox limit must be positive")
	}
	if owner == "" || leaseFor <= 0 {
		return nil, errors.New("outbox lease owner and duration are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []OutboxRecord
	for _, r := range s.records {
		if r.Payload != nil && !r.AvailableAt.After(now) && (r.LeaseUntil.IsZero() || !r.LeaseUntil.After(now)) {
			r.LeaseOwner = owner
			r.LeaseUntil = now.Add(leaseFor)
			s.records[r.EventID] = r
			out = append(out, r)
			if len(out) == limit {
				break
			}
		}
	}
	return out, nil
}
func (s *MemoryStore) MarkPublished(_ context.Context, eventID, owner string, now time.Time, publishErr error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.records[eventID]
	if !ok {
		return nil
	}
	if r.LeaseOwner != owner || !r.LeaseUntil.After(now) {
		return ErrLeaseLost
	}
	if publishErr == nil {
		r.Payload = nil
	} else {
		r.Attempts++
		r.AvailableAt = now.Add(RetryDelay(time.Second, time.Minute, r.Attempts))
	}
	r.LeaseOwner = ""
	r.LeaseUntil = time.Time{}
	s.records[eventID] = r
	return nil
}
func (s *MemoryStore) Project(_ context.Context, event Event) error {
	if err := event.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.projectionByReservation[event.ReservationID]; ok {
		if old, ok := s.projections[existing]; ok && old.EventID == event.EventID {
			return nil
		}
		return ErrConflict
	}
	if _, ok := s.projections[event.EventID]; ok {
		return nil
	}
	s.projections[event.EventID] = event
	s.projectionByReservation[event.ReservationID] = event.EventID
	return nil
}
