package reservation

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestRedisQuotaStoreReserveIsOperationIdempotent(t *testing.T) {
	evaluator := &fakeRedisEvaluator{results: []any{
		[]any{"reserved", int64(36), int64(0)},
		[]any{"reserved", int64(36), int64(0)},
	}}
	store := newRedisQuotaStore(evaluator)
	first, err := store.Reserve(context.Background(), "tenant-a", "reservation-a", 2, 64)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := store.Reserve(context.Background(), "tenant-a", "reservation-a", 2, 64)
	if err != nil {
		t.Fatal(err)
	}
	if first != replay || len(evaluator.calls) != 2 || evaluator.calls[0].keys[1] != evaluator.calls[1].keys[1] {
		t.Fatalf("first=%+v replay=%+v calls=%+v", first, replay, evaluator.calls)
	}
	if !strings.Contains(reserveLua, "DECRBY") || !strings.Contains(reserveLua, "cjson.encode") {
		t.Fatal("reserve script must atomically debit and journal the original result")
	}
}

func TestRedisQuotaStoreRejectsInsufficientAndConservativelySettles(t *testing.T) {
	evaluator := &fakeRedisEvaluator{results: []any{
		[]any{"quota_exhausted", int64(12), int64(0)},
		[]any{"settled", int64(92), int64(0)},
	}}
	store := newRedisQuotaStore(evaluator)
	if _, err := store.Reserve(context.Background(), "tenant-a", "reservation-a", 2, 64); Code(err) != "quota_exhausted" {
		t.Fatalf("reserve code=%q", Code(err))
	}
	settled, err := store.Settle(context.Background(), "tenant-a", "reservation-a", 3, 64, 64)
	if err != nil || settled.ReleasedUnits != 0 {
		t.Fatalf("settled=%+v err=%v", settled, err)
	}
	if !strings.Contains(settleLua, "INCRBY") || !strings.Contains(settleLua, "reserved - consumed") {
		t.Fatal("settle script must release only the confirmed unused amount")
	}
}

func TestPreDeductorLeavesCreatingWhenRedisReserveFails(t *testing.T) {
	records := &fakePersistentCreator{created: PersistentReservation{ID: "reservation-a", TenantID: "tenant-a", State: Creating, Version: 1}}
	quota := fakeQuotaReserver{err: errors.New("redis unavailable")}
	preDeductor := newPreDeductor(records, quota)
	value, err := preDeductor.Begin(context.Background(), CreatePersistentReservation{ID: "reservation-a", TenantID: "tenant-a", RequestID: "request-a", Model: "mock-model", ReservedUnits: 64})
	if err == nil || value.State != Creating || records.markCalls != 0 {
		t.Fatalf("value=%+v err=%v reserve updates=%d", value, err, records.markCalls)
	}
}

type redisCall struct {
	script string
	keys   []string
	args   []any
}

type fakeRedisEvaluator struct {
	results []any
	err     error
	calls   []redisCall
}

func (f *fakeRedisEvaluator) Eval(_ context.Context, script string, keys []string, args ...any) (any, error) {
	f.calls = append(f.calls, redisCall{script: script, keys: append([]string(nil), keys...), args: append([]any(nil), args...)})
	if f.err != nil {
		return nil, f.err
	}
	if len(f.results) == 0 {
		return nil, errors.New("unexpected redis call")
	}
	result := f.results[0]
	f.results = f.results[1:]
	return result, nil
}

type fakePersistentCreator struct {
	created   PersistentReservation
	markCalls int
	markErr   error
}

func (f *fakePersistentCreator) Create(_ context.Context, input CreatePersistentReservation) (PersistentReservation, error) {
	if f.created.ID == "" {
		f.created = PersistentReservation{ID: input.ID, TenantID: input.TenantID, State: Creating, Version: 1, ReservedUnits: input.ReservedUnits}
	}
	return f.created, nil
}

func (f *fakePersistentCreator) MarkReserved(_ context.Context, _, _ string, _ uint64) (PersistentReservation, error) {
	f.markCalls++
	if f.markErr != nil {
		return PersistentReservation{}, f.markErr
	}
	value := f.created
	value.State = Reserved
	value.Version++
	return value, nil
}

type fakeQuotaReserver struct {
	result QuotaOperationResult
	err    error
}

func (f fakeQuotaReserver) Reserve(context.Context, string, string, uint64, uint64) (QuotaOperationResult, error) {
	return f.result, f.err
}
