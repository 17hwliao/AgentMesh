// Package reservation defines the pre-persistence quota Reservation boundary.
// Its in-memory implementation is for state-machine verification only; it
// neither reserves a balance nor claims Redis/MySQL durability.
package reservation

import (
	"errors"
	"sync"
)

type State string

const (
	Creating       State = "creating"
	Reserved       State = "reserved"
	Settled        State = "settled"
	Cancelled      State = "cancelled"
	ExpiredPending State = "expired_pending"
)

const (
	CodeNotFound        = "reservation_not_found"
	CodeVersionConflict = "reservation_version_conflict"
	CodeStateInvalid    = "reservation_state_invalid"
	CodeMustSettle      = "reservation_must_settle"
)

type Error struct{ Code string }

func (e *Error) Error() string { return e.Code }

func Code(err error) string {
	var domain *Error
	if errors.As(err, &domain) {
		return domain.Code
	}
	return ""
}

// Reservation carries no amount in this first state-only slice. Amounts and
// real pre-deduction belong to the Redis/MySQL stage.
type Reservation struct {
	ID        string    `json:"reservation_id"`
	RequestID string    `json:"request_id"`
	TenantID  string    `json:"tenant_id"`
	State     State     `json:"state"`
	Version   uint64    `json:"version"`
	Attempts  []Attempt `json:"attempts"`
}

// Attempt represents the durable-before-call intent that later storage must
// preserve. Here it is only an in-memory semantic record.
type Attempt struct {
	Ordinal uint64 `json:"ordinal"`
	Started bool   `json:"started"`
}

type Repository interface {
	Create(Reservation) (Reservation, error)
	Get(tenantID, reservationID string) (Reservation, error)
	Reserve(tenantID, reservationID string, expectedVersion uint64) (Reservation, error)
	StartAttempt(tenantID, reservationID string, expectedVersion uint64) (Reservation, error)
	Settle(tenantID, reservationID string, expectedVersion uint64) (Reservation, error)
	Cancel(tenantID, reservationID string, expectedVersion uint64) (Reservation, error)
}

type operation string

const (
	reserveOperation      operation = "reserve"
	startAttemptOperation operation = "start_attempt"
	settleOperation       operation = "settle"
	cancelOperation       operation = "cancel"
)

type journalKey struct {
	reservationID string
	version       uint64
	operation     operation
}

// MemoryRepository serializes each state change under one process-local lock.
// Its success journal deliberately excludes rejections: rejected commands are
// deterministic from current state and must be evaluated again on retry.
type MemoryRepository struct {
	mu           sync.Mutex
	reservations map[string]Reservation
	successes    map[journalKey]Reservation
}

func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{reservations: map[string]Reservation{}, successes: map[journalKey]Reservation{}}
}

func (r *MemoryRepository) Create(input Reservation) (Reservation, error) {
	if input.ID == "" || input.RequestID == "" || input.TenantID == "" {
		return Reservation{}, domainError(CodeStateInvalid)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.reservations[input.ID]; exists {
		return Reservation{}, domainError(CodeVersionConflict)
	}
	created := Reservation{ID: input.ID, RequestID: input.RequestID, TenantID: input.TenantID, State: Creating, Version: 1, Attempts: []Attempt{}}
	r.reservations[created.ID] = created
	return clone(created), nil
}

func (r *MemoryRepository) Get(tenantID, reservationID string) (Reservation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	value, ok := r.reservations[reservationID]
	if !ok || value.TenantID != tenantID {
		return Reservation{}, domainError(CodeNotFound)
	}
	return clone(value), nil
}

func (r *MemoryRepository) Reserve(tenantID, reservationID string, expectedVersion uint64) (Reservation, error) {
	return r.change(tenantID, reservationID, expectedVersion, reserveOperation, func(value *Reservation) error {
		if value.State != Creating {
			return domainError(CodeStateInvalid)
		}
		value.State = Reserved
		return nil
	})
}

func (r *MemoryRepository) StartAttempt(tenantID, reservationID string, expectedVersion uint64) (Reservation, error) {
	return r.change(tenantID, reservationID, expectedVersion, startAttemptOperation, func(value *Reservation) error {
		if value.State != Reserved {
			return domainError(CodeStateInvalid)
		}
		value.Attempts = append(value.Attempts, Attempt{Ordinal: uint64(len(value.Attempts) + 1), Started: true})
		return nil
	})
}

func (r *MemoryRepository) Settle(tenantID, reservationID string, expectedVersion uint64) (Reservation, error) {
	return r.change(tenantID, reservationID, expectedVersion, settleOperation, func(value *Reservation) error {
		if value.State != Reserved || !hasStartedAttempt(*value) {
			return domainError(CodeStateInvalid)
		}
		value.State = Settled
		return nil
	})
}

func (r *MemoryRepository) Cancel(tenantID, reservationID string, expectedVersion uint64) (Reservation, error) {
	return r.change(tenantID, reservationID, expectedVersion, cancelOperation, func(value *Reservation) error {
		if value.State != Creating && value.State != Reserved {
			return domainError(CodeStateInvalid)
		}
		if hasStartedAttempt(*value) {
			return domainError(CodeMustSettle)
		}
		value.State = Cancelled
		return nil
	})
}

func (r *MemoryRepository) change(tenantID, reservationID string, expectedVersion uint64, op operation, apply func(*Reservation) error) (Reservation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	value, ok := r.reservations[reservationID]
	if !ok || value.TenantID != tenantID {
		return Reservation{}, domainError(CodeNotFound)
	}
	key := journalKey{reservationID: reservationID, version: expectedVersion, operation: op}
	if prior, ok := r.successes[key]; ok {
		return clone(prior), nil
	}
	if value.Version != expectedVersion {
		return Reservation{}, domainError(CodeVersionConflict)
	}
	updated := clone(value)
	if err := apply(&updated); err != nil {
		return Reservation{}, err
	}
	updated.Version++
	r.reservations[reservationID] = updated
	r.successes[key] = clone(updated)
	return clone(updated), nil
}

func hasStartedAttempt(value Reservation) bool {
	for _, attempt := range value.Attempts {
		if attempt.Started {
			return true
		}
	}
	return false
}

func clone(value Reservation) Reservation {
	value.Attempts = append([]Attempt(nil), value.Attempts...)
	return value
}

func domainError(code string) error { return &Error{Code: code} }
