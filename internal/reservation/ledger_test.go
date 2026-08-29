package reservation

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestReconcileUsageLedgerReportsOnlyStableEvidenceStates(t *testing.T) {
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	complete := ledgerEntry(now)
	for _, testCase := range []struct {
		name      string
		entry     UsageLedgerEntry
		operation QuotaOperationResult
		found     bool
		want      string
	}{
		{name: "complete", entry: complete, operation: QuotaOperationResult{Code: "settled", ReleasedUnits: 40}, found: true, want: ReconciliationComplete},
		{name: "outbox missing", entry: UsageLedgerEntry{Reservation: complete.Reservation}, want: OutboxMissing},
		{name: "record missing", entry: UsageLedgerEntry{Reservation: complete.Reservation, Outbox: complete.Outbox}, want: UsageRecordMissing},
		{name: "redis operation missing", entry: complete, want: RedisOperationMissing},
		{name: "operation version mismatch", entry: ledgerEntryWithOperationVersion(now, 2), want: LedgerMismatch},
		{name: "unit mismatch", entry: complete, operation: QuotaOperationResult{Code: "settled", ReleasedUnits: 0}, found: true, want: LedgerMismatch},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			report, err := ReconcileUsageLedger(context.Background(), fakeUsageLedgerReader{entries: []UsageLedgerEntry{testCase.entry}}, fakeTerminalOperationReader{result: testCase.operation, found: testCase.found}, 8)
			if err != nil || len(report.Items) != 1 || report.Items[0].Status != testCase.want {
				t.Fatalf("report=%+v err=%v", report, err)
			}
		})
	}
}

func TestReconcileUsageLedgerPropagatesReadFailureWithoutRepair(t *testing.T) {
	_, err := ReconcileUsageLedger(context.Background(), fakeUsageLedgerReader{entries: []UsageLedgerEntry{ledgerEntry(time.Now().UTC())}}, fakeTerminalOperationReader{err: errors.New("redis unavailable")}, 1)
	if err == nil {
		t.Fatal("expected redis read error")
	}
}

func TestRedisTerminalOperationReadsOnlyOperationKey(t *testing.T) {
	evaluator := &fakeRedisEvaluator{results: []any{[]any{"settled", int64(76), int64(40)}, []any{"missing", int64(0), int64(0)}}}
	store := newRedisQuotaStore(evaluator)
	result, found, err := store.TerminalOperation(context.Background(), "tenant-a", "reservation-a", 3, Settled)
	if err != nil || !found || result.Code != "settled" || result.ReleasedUnits != 40 {
		t.Fatalf("result=%+v found=%t err=%v", result, found, err)
	}
	_, found, err = store.TerminalOperation(context.Background(), "tenant-a", "reservation-a", 3, Cancelled)
	if err != nil || found || len(evaluator.calls) != 2 || evaluator.calls[0].script != readOperationLua || len(evaluator.calls[0].keys) != 1 || evaluator.calls[0].keys[0] == balanceKey("tenant-a") {
		t.Fatalf("found=%t err=%v calls=%+v", found, err, evaluator.calls)
	}
}

type fakeUsageLedgerReader struct {
	entries []UsageLedgerEntry
	err     error
}

func (f fakeUsageLedgerReader) ListUsageLedgerEntries(context.Context, int) ([]UsageLedgerEntry, error) {
	return f.entries, f.err
}

type fakeTerminalOperationReader struct {
	result QuotaOperationResult
	found  bool
	err    error
}

func (f fakeTerminalOperationReader) TerminalOperation(context.Context, string, string, uint64, State) (QuotaOperationResult, bool, error) {
	return f.result, f.found, f.err
}

func ledgerEntry(now time.Time) UsageLedgerEntry {
	return ledgerEntryWithOperationVersion(now, 3)
}

func ledgerEntryWithOperationVersion(now time.Time, operationVersion uint64) UsageLedgerEntry {
	reservation := PersistentReservation{
		ID: "reservation-a", TenantID: "tenant-a", RequestID: "request-a", Model: "mock-model", State: Settled, Version: 4,
		ReservedUnits: 64, SettledUnits: 24, ReleasedUnits: 40, UsageObserved: true, SettlementKind: "provider_usage",
	}
	outbox := &UsageOutbox{
		ReservationID: "reservation-a", TenantID: "tenant-a", RequestID: "request-a", Model: "mock-model", FinalState: Settled, OperationVersion: operationVersion,
		ReservedUnits: 64, SettledUnits: 24, ReleasedUnits: 40, UsageObserved: true, SettlementKind: "provider_usage", FinalizedAt: now,
	}
	record := &UsageRecord{
		ReservationID: "reservation-a", TenantID: "tenant-a", RequestID: "request-a", Model: "mock-model", FinalState: Settled, OperationVersion: operationVersion,
		ReservedUnits: 64, SettledUnits: 24, ReleasedUnits: 40, UsageObserved: true, SettlementKind: "provider_usage", FinalizedAt: now, RecordedAt: now,
	}
	return UsageLedgerEntry{Reservation: reservation, Outbox: outbox, Record: record}
}
