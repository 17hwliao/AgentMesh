package reservation

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"
	"unicode/utf8"

	"agentmesh/internal/provider"
	"agentmesh/internal/router"
)

const quotaExhaustedCode = "quota_exhausted"

// StreamGate is the Gateway-facing, request-scoped reservation boundary.
type StreamGate interface {
	Begin(context.Context, string, string, []provider.Message) (StreamSession, error)
}

// StreamSession provides Router's synchronous before-call evidence hook and a
// single terminal action for the Gateway.
type StreamSession interface {
	router.AttemptHook
	Complete(context.Context, string, bool) error
}

type CoordinatorConfig struct {
	ReservationUnits uint64
	FlushChunks      int
	FlushInterval    time.Duration
	NextID           func() string
}

type Coordinator struct {
	records coordinatorRecords
	quota   coordinatorQuota
	config  CoordinatorConfig
	now     func() time.Time
}

type coordinatorRecords interface {
	persistentCreator
	StartAttempt(context.Context, string, string, uint64, string, string) (PersistentAttempt, PersistentReservation, error)
	RecordProgress(context.Context, string, string, uint64, uint64, time.Time) error
	FinishAttempt(context.Context, string, string, uint64, string) error
	MarkSettled(context.Context, string, string, uint64, uint64, uint64, bool, string) (PersistentReservation, error)
	MarkCancelled(context.Context, string, string, uint64) (PersistentReservation, error)
	ClaimExpired(context.Context, time.Time, int) ([]ReconcileCandidate, error)
}

type coordinatorQuota interface {
	quotaReserver
	Settle(context.Context, string, string, uint64, uint64, uint64) (QuotaOperationResult, error)
	Cancel(context.Context, string, string, uint64, uint64) (QuotaOperationResult, error)
	ReserveApplied(context.Context, string, string) (bool, error)
}

func NewCoordinator(records coordinatorRecords, quota coordinatorQuota, config CoordinatorConfig, now func() time.Time) (*Coordinator, error) {
	if records == nil || quota == nil || config.ReservationUnits == 0 {
		return nil, domainError(CodeStateInvalid)
	}
	if config.FlushChunks <= 0 {
		config.FlushChunks = 16
	}
	if config.FlushInterval <= 0 {
		config.FlushInterval = time.Second
	}
	if config.NextID == nil {
		config.NextID = randomReservationID
	}
	if now == nil {
		now = time.Now
	}
	return &Coordinator{records: records, quota: quota, config: config, now: now}, nil
}

func (c *Coordinator) Begin(ctx context.Context, tenantID, model string, messages []provider.Message) (StreamSession, error) {
	if tenantID == "" || model == "" || len(messages) == 0 {
		return nil, domainError(CodeStateInvalid)
	}
	reservationID, requestID := c.config.NextID(), c.config.NextID()
	if reservationID == "" || requestID == "" || reservationID == requestID {
		return nil, domainError(CodeStateInvalid)
	}
	created, err := newPreDeductor(c.records, c.quota).Begin(ctx, CreatePersistentReservation{
		ID: reservationID, TenantID: tenantID, RequestID: requestID, Model: model, ReservedUnits: c.config.ReservationUnits,
	})
	if err != nil {
		return nil, err
	}
	return &coordinatorSession{
		coordinator: c, reservation: created, tenantID: tenantID, model: model,
		messages: append([]provider.Message(nil), messages...), lastFlush: c.now(),
	}, nil
}

type coordinatorSession struct {
	coordinator *Coordinator
	reservation PersistentReservation
	tenantID    string
	model       string
	messages    []provider.Message
	lastFlush   time.Time
	attempts    []*attemptMeter
	current     *attemptMeter
	completed   bool
}

type attemptMeter struct {
	attempt        PersistentAttempt
	inputRunes     uint64
	forwardedRunes uint64
	chunks         int
	providerUsage  *provider.Usage
}

func (s *coordinatorSession) BeforeAttempt(ctx context.Context, providerName string, request provider.ChatRequest) error {
	if s.completed || providerName == "" || request.Model != s.model {
		return domainError(CodeStateInvalid)
	}
	inputRunes := messageRunes(request.Messages)
	if s.estimatedConsumed()+inputRunes > s.reservation.ReservedUnits {
		return domainError(quotaExhaustedCode)
	}
	attempt, updated, err := s.coordinator.records.StartAttempt(ctx, s.tenantID, s.reservation.ID, s.reservation.Version, providerName, s.model)
	if err != nil {
		return err
	}
	s.reservation = updated
	meter := &attemptMeter{attempt: attempt, inputRunes: inputRunes}
	s.attempts = append(s.attempts, meter)
	s.current = meter
	return nil
}

func (s *coordinatorSession) BeforeEmit(ctx context.Context, providerName string, chunk provider.Chunk) error {
	if s.current == nil || s.current.attempt.ProviderName != providerName {
		return domainError(CodeStateInvalid)
	}
	if s.current.chunks == 0 || s.coordinator.now().Sub(s.lastFlush) < s.coordinator.config.FlushInterval {
		return nil
	}
	return s.flush(ctx)
}

// AfterEmit advances the durable lower bound only after the SSE writer
// accepted the chunk. A persistence failure then stops any further forwarding.
func (s *coordinatorSession) AfterEmit(ctx context.Context, providerName string, chunk provider.Chunk) error {
	if s.current == nil || s.current.attempt.ProviderName != providerName {
		return domainError(CodeStateInvalid)
	}
	s.current.forwardedRunes += uint64(utf8.RuneCountInString(chunk.Delta))
	s.current.chunks++
	if chunk.Usage != nil {
		usage := *chunk.Usage
		s.current.providerUsage = &usage
	}
	if s.current.chunks < s.coordinator.config.FlushChunks {
		return nil
	}
	return s.flush(ctx)
}

func (s *coordinatorSession) FinishAttempt(ctx context.Context, providerName, outcome string) error {
	if s.current == nil || s.current.attempt.ProviderName != providerName || outcome == "" {
		return domainError(CodeStateInvalid)
	}
	return s.coordinator.records.FinishAttempt(ctx, s.tenantID, s.reservation.ID, s.current.attempt.Ordinal, outcome)
}

func (s *coordinatorSession) Complete(ctx context.Context, _ string, _ bool) error {
	if s.completed {
		return nil
	}
	persistenceUncertain := false
	if s.current != nil {
		if err := s.flush(ctx); err != nil {
			persistenceUncertain = true
		}
	}
	if len(s.attempts) == 0 {
		if _, err := s.coordinator.quota.Cancel(ctx, s.tenantID, s.reservation.ID, s.reservation.Version, s.reservation.ReservedUnits); err != nil {
			return err
		}
		if _, err := s.coordinator.records.MarkCancelled(ctx, s.tenantID, s.reservation.ID, s.reservation.Version); err != nil {
			return err
		}
		s.completed = true
		return nil
	}
	consumed, observed, kind := s.settlement(persistenceUncertain)
	result, err := s.coordinator.quota.Settle(ctx, s.tenantID, s.reservation.ID, s.reservation.Version, s.reservation.ReservedUnits, consumed)
	if err != nil {
		return err
	}
	if _, err := s.coordinator.records.MarkSettled(ctx, s.tenantID, s.reservation.ID, s.reservation.Version, consumed, result.ReleasedUnits, observed, kind); err != nil {
		return err
	}
	s.completed = true
	return nil
}

func (s *coordinatorSession) flush(ctx context.Context) error {
	if s.current == nil {
		return nil
	}
	now := s.coordinator.now()
	if err := s.coordinator.records.RecordProgress(ctx, s.tenantID, s.reservation.ID, s.current.attempt.Ordinal, s.current.forwardedRunes, now); err != nil {
		return err
	}
	s.current.chunks = 0
	s.lastFlush = now
	return nil
}

func (s *coordinatorSession) settlement(persistenceUncertain bool) (uint64, bool, string) {
	if persistenceUncertain {
		return s.reservation.ReservedUnits, false, "estimated"
	}
	observed := len(s.attempts) > 0
	var providerUnits uint64
	for _, attempt := range s.attempts {
		if attempt.providerUsage == nil || attempt.providerUsage.Estimated {
			observed = false
			break
		}
		providerUnits += uint64(attempt.providerUsage.InputTokens + attempt.providerUsage.OutputTokens)
	}
	if observed {
		return minUnits(providerUnits, s.reservation.ReservedUnits), true, "provider_usage"
	}
	// Rune metering is only a durable lower bound. It can diagnose what the
	// gateway observed, but cannot prove that the unobserved reservation budget
	// was unused, so an estimated settlement releases nothing.
	return s.reservation.ReservedUnits, false, "estimated"
}

func (s *coordinatorSession) estimatedConsumed() uint64 {
	var total uint64
	for _, attempt := range s.attempts {
		total += attempt.inputRunes + attempt.forwardedRunes
	}
	return total
}

func messageRunes(messages []provider.Message) uint64 {
	var total uint64
	for _, message := range messages {
		total += uint64(utf8.RuneCountInString(message.Content))
	}
	return total
}

func minUnits(left, right uint64) uint64 {
	if left > right {
		return right
	}
	return left
}

func randomReservationID() string {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return ""
	}
	return hex.EncodeToString(value)
}

// Reconciler has no scheduler. Callers explicitly choose the expiration bound
// and may safely rerun after any storage failure.
type Reconciler struct {
	records coordinatorRecords
	quota   coordinatorQuota
}

func NewReconciler(records coordinatorRecords, quota coordinatorQuota) *Reconciler {
	return &Reconciler{records: records, quota: quota}
}

func (r *Reconciler) Reconcile(ctx context.Context, before time.Time, limit int) (int, error) {
	if r == nil || r.records == nil || r.quota == nil {
		return 0, errors.New("reconciler unavailable")
	}
	candidates, err := r.records.ClaimExpired(ctx, before, limit)
	if err != nil {
		return 0, err
	}
	completed := 0
	for _, candidate := range candidates {
		value := candidate.Reservation
		if candidate.AttemptStarted || candidate.ForwardedRunes > 0 {
			if _, err := r.quota.Settle(ctx, value.TenantID, value.ID, value.Version, value.ReservedUnits, value.ReservedUnits); err != nil {
				return completed, err
			}
			if _, err := r.records.MarkSettled(ctx, value.TenantID, value.ID, value.Version, value.ReservedUnits, 0, false, "estimated"); err != nil {
				return completed, err
			}
		} else {
			applied, err := r.quota.ReserveApplied(ctx, value.TenantID, value.ID)
			if err != nil {
				return completed, err
			}
			if applied {
				if _, err := r.quota.Cancel(ctx, value.TenantID, value.ID, value.Version, value.ReservedUnits); err != nil {
					return completed, err
				}
			}
			if _, err := r.records.MarkCancelled(ctx, value.TenantID, value.ID, value.Version); err != nil {
				return completed, err
			}
		}
		completed++
	}
	return completed, nil
}
