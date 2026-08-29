package reservation

import (
	"context"
	"errors"
	"testing"
	"time"

	"agentmesh/internal/provider"
)

func TestCoordinatorPersistsAttemptProgressAndEstimatedSettlement(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	records := &fakeCoordinatorRecords{}
	quota := &fakeCoordinatorQuota{}
	coordinator, err := NewCoordinator(records, quota, CoordinatorConfig{
		ReservationUnits: 64, FlushChunks: 2, FlushInterval: time.Hour, NextID: sequenceIDs("reservation-a", "request-a"),
	}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	session, err := coordinator.Begin(context.Background(), "tenant-a", "mock-model", []provider.Message{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatal(err)
	}
	request := provider.ChatRequest{Model: "mock-model", Messages: []provider.Message{{Role: "user", Content: "hi"}}}
	if err := session.BeforeAttempt(context.Background(), "primary", request); err != nil {
		t.Fatal(err)
	}
	if err := session.BeforeEmit(context.Background(), "primary", provider.Chunk{Delta: "o"}); err != nil {
		t.Fatal(err)
	}
	if err := session.AfterEmit(context.Background(), "primary", provider.Chunk{Delta: "o"}); err != nil {
		t.Fatal(err)
	}
	if err := session.BeforeEmit(context.Background(), "primary", provider.Chunk{Delta: "k"}); err != nil {
		t.Fatal(err)
	}
	if err := session.AfterEmit(context.Background(), "primary", provider.Chunk{Delta: "k"}); err != nil {
		t.Fatal(err)
	}
	if err := session.FinishAttempt(context.Background(), "primary", "succeeded"); err != nil {
		t.Fatal(err)
	}
	if err := session.Complete(context.Background(), "success", false); err != nil {
		t.Fatal(err)
	}
	if records.progressCalls != 2 || records.started != 1 || records.finished != 1 || quota.settleConsumed != 64 || quota.settleReleased != 0 || records.settledKind != "estimated" {
		t.Fatalf("progress=%d started=%d finished=%d consumed=%d released=%d kind=%q", records.progressCalls, records.started, records.finished, quota.settleConsumed, quota.settleReleased, records.settledKind)
	}
}

func TestCoordinatorStopsOnProgressFailureAndSettlesConservatively(t *testing.T) {
	records := &fakeCoordinatorRecords{progressErr: errors.New("mysql unavailable")}
	quota := &fakeCoordinatorQuota{}
	coordinator, err := NewCoordinator(records, quota, CoordinatorConfig{
		ReservationUnits: 64, FlushChunks: 1, NextID: sequenceIDs("reservation-b", "request-b"),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	session, err := coordinator.Begin(context.Background(), "tenant-a", "mock-model", []provider.Message{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatal(err)
	}
	request := provider.ChatRequest{Model: "mock-model", Messages: []provider.Message{{Role: "user", Content: "hi"}}}
	if err := session.BeforeAttempt(context.Background(), "primary", request); err != nil {
		t.Fatal(err)
	}
	if err := session.BeforeEmit(context.Background(), "primary", provider.Chunk{Delta: "unflushed"}); err != nil {
		t.Fatal(err)
	}
	if err := session.AfterEmit(context.Background(), "primary", provider.Chunk{Delta: "unflushed"}); err == nil {
		t.Fatal("progress persistence failure must stop forwarding")
	}
	if err := session.Complete(context.Background(), "progress_persist_failed", false); err != nil {
		t.Fatal(err)
	}
	if quota.settleConsumed != 64 || quota.settleReleased != 0 || records.settledKind != "estimated" {
		t.Fatalf("consumed=%d released=%d kind=%q", quota.settleConsumed, quota.settleReleased, records.settledKind)
	}
}

func TestCoordinatorReleasesOnlyProviderObservedUsage(t *testing.T) {
	records := &fakeCoordinatorRecords{}
	quota := &fakeCoordinatorQuota{}
	coordinator, err := NewCoordinator(records, quota, CoordinatorConfig{
		ReservationUnits: 64, NextID: sequenceIDs("reservation-usage", "request-usage"),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	request := provider.ChatRequest{Model: "mock-model", Messages: []provider.Message{{Role: "user", Content: "hi"}}}
	session, err := coordinator.Begin(context.Background(), "tenant-a", request.Model, request.Messages)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.BeforeAttempt(context.Background(), "primary", request); err != nil {
		t.Fatal(err)
	}
	chunk := provider.Chunk{Delta: "ok", Usage: &provider.Usage{InputTokens: 2, OutputTokens: 3}}
	if err := session.BeforeEmit(context.Background(), "primary", chunk); err != nil {
		t.Fatal(err)
	}
	if err := session.AfterEmit(context.Background(), "primary", chunk); err != nil {
		t.Fatal(err)
	}
	if err := session.FinishAttempt(context.Background(), "primary", "succeeded"); err != nil {
		t.Fatal(err)
	}
	if err := session.Complete(context.Background(), "success", false); err != nil {
		t.Fatal(err)
	}
	if quota.settleConsumed != 5 || quota.settleReleased != 59 || records.settledKind != "provider_usage" {
		t.Fatalf("consumed=%d released=%d kind=%q", quota.settleConsumed, quota.settleReleased, records.settledKind)
	}
}

func TestCoordinatorRejectsBeforeAttemptAndCancelsReservation(t *testing.T) {
	records := &fakeCoordinatorRecords{}
	quota := &fakeCoordinatorQuota{}
	coordinator, err := NewCoordinator(records, quota, CoordinatorConfig{
		ReservationUnits: 1, NextID: sequenceIDs("reservation-c", "request-c"),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	session, err := coordinator.Begin(context.Background(), "tenant-a", "mock-model", []provider.Message{{Role: "user", Content: "too large"}})
	if err != nil {
		t.Fatal(err)
	}
	err = session.BeforeAttempt(context.Background(), "primary", provider.ChatRequest{Model: "mock-model", Messages: []provider.Message{{Role: "user", Content: "too large"}}})
	if Code(err) != quotaExhaustedCode || records.started != 0 {
		t.Fatalf("code=%q started=%d", Code(err), records.started)
	}
	if err := session.Complete(context.Background(), quotaExhaustedCode, false); err != nil {
		t.Fatal(err)
	}
	if quota.cancelled != 1 || records.cancelled != 1 {
		t.Fatalf("cancelled quota=%d records=%d", quota.cancelled, records.cancelled)
	}
}

func TestCoordinatorAccumulatesFallbackAttemptInputsWithinReservation(t *testing.T) {
	records := &fakeCoordinatorRecords{}
	quota := &fakeCoordinatorQuota{}
	coordinator, err := NewCoordinator(records, quota, CoordinatorConfig{
		ReservationUnits: 4, NextID: sequenceIDs("reservation-fallback", "request-fallback"),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	request := provider.ChatRequest{Model: "mock-model", Messages: []provider.Message{{Role: "user", Content: "hi"}}}
	session, err := coordinator.Begin(context.Background(), "tenant-a", request.Model, request.Messages)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.BeforeAttempt(context.Background(), "primary", request); err != nil {
		t.Fatal(err)
	}
	if err := session.FinishAttempt(context.Background(), "primary", "failed_before_first_chunk"); err != nil {
		t.Fatal(err)
	}
	if err := session.BeforeAttempt(context.Background(), "fallback", request); err != nil {
		t.Fatal(err)
	}
	if err := session.BeforeAttempt(context.Background(), "third", request); Code(err) != quotaExhaustedCode {
		t.Fatalf("third fallback code=%q", Code(err))
	}
	if err := session.Complete(context.Background(), quotaExhaustedCode, false); err != nil {
		t.Fatal(err)
	}
	if records.started != 2 || records.finished != 1 || quota.settleConsumed != 4 {
		t.Fatalf("started=%d finished=%d settled=%d", records.started, records.finished, quota.settleConsumed)
	}
}

func TestReconcilerSettlesStartedAndCancelsOnlyAppliedUnstarted(t *testing.T) {
	records := &fakeCoordinatorRecords{candidates: []ReconcileCandidate{
		{Reservation: PersistentReservation{ID: "started", TenantID: "tenant-a", State: ExpiredPending, Version: 4, ReservedUnits: 64}, AttemptStarted: true},
		{Reservation: PersistentReservation{ID: "unstarted", TenantID: "tenant-a", State: ExpiredPending, Version: 2, ReservedUnits: 32}},
	}}
	quota := &fakeCoordinatorQuota{reserveApplied: map[string]bool{"unstarted": true}}
	reconciler := NewReconciler(records, quota)
	completed, err := reconciler.Reconcile(context.Background(), time.Now(), 8)
	if err != nil {
		t.Fatal(err)
	}
	if completed != 2 || quota.settled != 1 || quota.cancelled != 1 || records.settled != 1 || records.cancelled != 1 {
		t.Fatalf("completed=%d quota=%+v records=%+v", completed, quota, records)
	}
	records.candidates = nil
	completed, err = reconciler.Reconcile(context.Background(), time.Now(), 8)
	if err != nil || completed != 0 || quota.settled != 1 || quota.cancelled != 1 {
		t.Fatalf("replay completed=%d err=%v quota=%+v", completed, err, quota)
	}
}

func TestOpenConfiguredCoordinatorReturnsNilStreamGateWhenDisabled(t *testing.T) {
	gate, cleanup, err := OpenConfiguredCoordinator(func(string) string { return "" })
	if err != nil || gate != nil {
		t.Fatalf("disabled gate=%v err=%v", gate, err)
	}
	cleanup()

}

func TestOpenConfiguredCoordinatorFailsBeforeNetworkWhenConfigIncomplete(t *testing.T) {
	var cleanup func()
	var err error
	_, cleanup, err = OpenConfiguredCoordinator(func(name string) string {
		if name == quotaModeEnvironment {
			return quotaModeReservation
		}
		return ""
	})
	if Code(err) != CodeQuotaConfigurationMissing {
		t.Fatalf("missing configuration code=%q", Code(err))
	}
	cleanup()
}

// TestStage4DemoScenario is the reproducible, offline stage-4 demonstration
// invoked by make demo-stage-4. It uses deterministic store doubles and is not
// evidence that a real MySQL or Redis endpoint was contacted.
func TestStage4DemoScenario(t *testing.T) {
	records := &fakeCoordinatorRecords{}
	quota := &fakeCoordinatorQuota{}
	coordinator, err := NewCoordinator(records, quota, CoordinatorConfig{
		ReservationUnits: 64, FlushChunks: 1, NextID: sequenceIDs("demo-reservation", "demo-request"),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	request := provider.ChatRequest{Model: "mock-model", Messages: []provider.Message{{Role: "user", Content: "demo"}}}
	session, err := coordinator.Begin(context.Background(), "demo-tenant", request.Model, request.Messages)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.BeforeAttempt(context.Background(), "primary", request); err != nil {
		t.Fatal(err)
	}
	if err := session.FinishAttempt(context.Background(), "primary", "failed_before_first_chunk"); err != nil {
		t.Fatal(err)
	}
	if err := session.BeforeAttempt(context.Background(), "fallback", request); err != nil {
		t.Fatal(err)
	}
	chunk := provider.Chunk{Delta: "forwarded"}
	if err := session.BeforeEmit(context.Background(), "fallback", chunk); err != nil {
		t.Fatal(err)
	}
	if err := session.AfterEmit(context.Background(), "fallback", chunk); err != nil {
		t.Fatal(err)
	}
	if err := session.FinishAttempt(context.Background(), "fallback", "interrupted"); err != nil {
		t.Fatal(err)
	}
	if err := session.Complete(context.Background(), "stream_interrupted", false); err != nil {
		t.Fatal(err)
	}
	t.Logf("pre_deduction=creating->redis_reserve->reserved attempts=%d forwarded_progress_writes=%d settled_units=%d released_units=%d kind=%s", records.started, records.progressCalls, quota.settleConsumed, quota.settleReleased, records.settledKind)

	records.candidates = []ReconcileCandidate{{Reservation: PersistentReservation{ID: "demo-crash", TenantID: "demo-tenant", State: ExpiredPending, Version: 4, ReservedUnits: 64}, AttemptStarted: true}}
	completed, err := NewReconciler(records, quota).Reconcile(context.Background(), time.Now(), 1)
	if err != nil || completed != 1 {
		t.Fatalf("reconciler completed=%d err=%v", completed, err)
	}
	t.Logf("reconciler=settled_estimated completed=%d total_settle_calls=%d", completed, quota.settled)
}

type fakeCoordinatorRecords struct {
	value         PersistentReservation
	progressErr   error
	candidates    []ReconcileCandidate
	started       int
	finished      int
	progressCalls int
	settled       int
	cancelled     int
	settledKind   string
}

func (f *fakeCoordinatorRecords) Create(_ context.Context, input CreatePersistentReservation) (PersistentReservation, error) {
	if f.value.ID == "" {
		f.value = PersistentReservation{ID: input.ID, TenantID: input.TenantID, RequestID: input.RequestID, Model: input.Model, State: Creating, Version: 1, ReservedUnits: input.ReservedUnits}
	}
	return f.value, nil
}

func (f *fakeCoordinatorRecords) MarkReserved(context.Context, string, string, uint64) (PersistentReservation, error) {
	f.value.State = Reserved
	f.value.Version++
	return f.value, nil
}

func (f *fakeCoordinatorRecords) StartAttempt(_ context.Context, _, _ string, _ uint64, providerName, model string) (PersistentAttempt, PersistentReservation, error) {
	f.started++
	f.value.Version++
	return PersistentAttempt{ReservationID: f.value.ID, Ordinal: uint64(f.started), ProviderName: providerName, Model: model}, f.value, nil
}

func (f *fakeCoordinatorRecords) RecordProgress(context.Context, string, string, uint64, uint64, time.Time) error {
	f.progressCalls++
	return f.progressErr
}

func (f *fakeCoordinatorRecords) FinishAttempt(context.Context, string, string, uint64, string) error {
	f.finished++
	return nil
}

func (f *fakeCoordinatorRecords) MarkSettled(_ context.Context, _, _ string, _ uint64, _, _ uint64, _ bool, kind string) (PersistentReservation, error) {
	f.settled++
	f.settledKind = kind
	f.value.State = Settled
	return f.value, nil
}

func (f *fakeCoordinatorRecords) MarkCancelled(context.Context, string, string, uint64) (PersistentReservation, error) {
	f.cancelled++
	f.value.State = Cancelled
	return f.value, nil
}

func (f *fakeCoordinatorRecords) ClaimExpired(context.Context, time.Time, int) ([]ReconcileCandidate, error) {
	return f.candidates, nil
}

type fakeCoordinatorQuota struct {
	settleConsumed uint64
	settleReleased uint64
	settled        int
	cancelled      int
	reserveApplied map[string]bool
}

func (*fakeCoordinatorQuota) Reserve(context.Context, string, string, uint64, uint64) (QuotaOperationResult, error) {
	return QuotaOperationResult{}, nil
}

func (f *fakeCoordinatorQuota) Settle(_ context.Context, _ string, _ string, _ uint64, reserved, consumed uint64) (QuotaOperationResult, error) {
	f.settled++
	f.settleConsumed = consumed
	f.settleReleased = reserved - consumed
	return QuotaOperationResult{Code: "settled", ReleasedUnits: reserved - consumed}, nil
}

func (f *fakeCoordinatorQuota) Cancel(context.Context, string, string, uint64, uint64) (QuotaOperationResult, error) {
	f.cancelled++
	return QuotaOperationResult{Code: "cancelled"}, nil
}

func (f *fakeCoordinatorQuota) ReserveApplied(_ context.Context, _ string, reservationID string) (bool, error) {
	return f.reserveApplied[reservationID], nil
}

func sequenceIDs(values ...string) func() string {
	return func() string {
		value := values[0]
		values = values[1:]
		return value
	}
}
