package reservation

import (
	"context"
	"database/sql"
	"errors"
)

const (
	ReconciliationComplete = "reconciliation_complete"
	OutboxMissing          = "outbox_missing"
	UsageRecordMissing     = "usage_record_missing"
	RedisOperationMissing  = "redis_operation_missing"
	LedgerMismatch         = "ledger_mismatch"
)

// UsageLedgerEntry brings together the three durable views for one terminal
// Reservation. Missing snapshots remain nil so reconciliation can report them.
type UsageLedgerEntry struct {
	Reservation PersistentReservation
	Outbox      *UsageOutbox
	Record      *UsageRecord
}

type ReconciliationItem struct {
	ReservationID string `json:"reservation_id"`
	Status        string `json:"status"`
}

type ReconciliationReport struct {
	Items []ReconciliationItem `json:"items"`
}

type usageLedgerReader interface {
	ListUsageLedgerEntries(context.Context, int) ([]UsageLedgerEntry, error)
}

type terminalOperationReader interface {
	TerminalOperation(context.Context, string, string, uint64, State) (QuotaOperationResult, bool, error)
}

// ReconcileUsageLedger is read-only. It neither repairs records nor changes
// Redis, because a missing observation cannot prove a safe correction.
func ReconcileUsageLedger(ctx context.Context, records usageLedgerReader, operations terminalOperationReader, limit int) (ReconciliationReport, error) {
	if records == nil || operations == nil || limit <= 0 {
		return ReconciliationReport{}, domainError(CodeStateInvalid)
	}
	entries, err := records.ListUsageLedgerEntries(ctx, limit)
	if err != nil {
		return ReconciliationReport{}, err
	}
	report := ReconciliationReport{Items: make([]ReconciliationItem, 0, len(entries))}
	for _, entry := range entries {
		status, err := reconcileUsageLedgerEntry(ctx, entry, operations)
		if err != nil {
			return ReconciliationReport{}, err
		}
		report.Items = append(report.Items, ReconciliationItem{ReservationID: entry.Reservation.ID, Status: status})
	}
	return report, nil
}

func reconcileUsageLedgerEntry(ctx context.Context, entry UsageLedgerEntry, operations terminalOperationReader) (string, error) {
	if entry.Outbox == nil {
		return OutboxMissing, nil
	}
	if !matchesOutboxReservation(*entry.Outbox, entry.Reservation) {
		return LedgerMismatch, nil
	}
	if entry.Record == nil {
		return UsageRecordMissing, nil
	}
	if !matchesRecordOutbox(*entry.Record, *entry.Outbox) {
		return LedgerMismatch, nil
	}
	operation, found, err := operations.TerminalOperation(ctx, entry.Reservation.TenantID, entry.Reservation.ID, entry.Outbox.OperationVersion, entry.Reservation.State)
	if err != nil {
		return "", err
	}
	if !found {
		return RedisOperationMissing, nil
	}
	if operation.Code != string(entry.Reservation.State) || operation.ReleasedUnits != entry.Outbox.ReleasedUnits {
		return LedgerMismatch, nil
	}
	return ReconciliationComplete, nil
}

func matchesOutboxReservation(outbox UsageOutbox, reservation PersistentReservation) bool {
	return outbox.ReservationID == reservation.ID && outbox.TenantID == reservation.TenantID && outbox.RequestID == reservation.RequestID &&
		outbox.Model == reservation.Model && outbox.FinalState == reservation.State && outbox.ReservedUnits == reservation.ReservedUnits &&
		outbox.SettledUnits == reservation.SettledUnits && outbox.ReleasedUnits == reservation.ReleasedUnits &&
		outbox.UsageObserved == reservation.UsageObserved && outbox.SettlementKind == reservation.SettlementKind &&
		reservation.Version > 0 && outbox.OperationVersion+1 == reservation.Version
}

func matchesRecordOutbox(record UsageRecord, outbox UsageOutbox) bool {
	return record.ReservationID == outbox.ReservationID && record.TenantID == outbox.TenantID && record.RequestID == outbox.RequestID &&
		record.Model == outbox.Model && record.FinalState == outbox.FinalState && record.OperationVersion == outbox.OperationVersion &&
		record.ReservedUnits == outbox.ReservedUnits && record.SettledUnits == outbox.SettledUnits &&
		record.ReleasedUnits == outbox.ReleasedUnits && record.UsageObserved == outbox.UsageObserved && record.SettlementKind == outbox.SettlementKind &&
		record.FinalizedAt.Equal(outbox.FinalizedAt)
}

// ListUsageLedgerEntries reads a repeatable snapshot of terminal Reservations
// and their optional ledger views. It does not create missing evidence.
func (r *SQLRepository) ListUsageLedgerEntries(ctx context.Context, limit int) ([]UsageLedgerEntry, error) {
	if limit <= 0 {
		return nil, domainError(CodeStateInvalid)
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, terminalReservationsSQL, limit)
	if err != nil {
		return nil, err
	}
	reservations := make([]PersistentReservation, 0, limit)
	for rows.Next() {
		value, err := scanReservationFields(rows.Scan)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		reservations = append(reservations, value)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	entries := make([]UsageLedgerEntry, 0, len(reservations))
	for _, value := range reservations {
		entry := UsageLedgerEntry{Reservation: value}
		outbox, err := scanUsageOutbox(tx.QueryRowContext(ctx, usageOutboxByReservationSQL, value.ID))
		if err == nil {
			entry.Outbox = &outbox
		} else if Code(err) != CodeNotFound {
			return nil, err
		}
		record, err := scanUsageRecord(tx.QueryRowContext(ctx, usageRecordByReservationSQL, value.ID))
		if err == nil {
			entry.Record = &record
		} else if Code(err) != CodeNotFound {
			return nil, err
		}
		entries = append(entries, entry)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return entries, nil
}

func scanUsageRecord(row interface{ Scan(...any) error }) (UsageRecord, error) {
	var value UsageRecord
	var state string
	err := row.Scan(
		&value.ReservationID, &value.TenantID, &value.RequestID, &value.Model, &state, &value.OperationVersion,
		&value.ReservedUnits, &value.SettledUnits, &value.ReleasedUnits, &value.UsageObserved, &value.SettlementKind,
		&value.FinalizedAt, &value.RecordedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return UsageRecord{}, domainError(CodeNotFound)
	}
	if err != nil {
		return UsageRecord{}, err
	}
	value.FinalState = State(state)
	return value, nil
}

const terminalReservationsSQL = `SELECT ` + reservationColumns + ` FROM quota_reservations
    WHERE state IN ('settled', 'cancelled')
    ORDER BY updated_at ASC
    LIMIT ?`

const usageOutboxByReservationSQL = `SELECT ` + usageOutboxColumns + ` FROM usage_outbox WHERE reservation_id = ?`
const usageRecordColumns = `reservation_id, tenant_id, request_id, model, final_state, operation_version, reserved_units, settled_units, released_units, usage_observed, settlement_kind, finalized_at, recorded_at`
const usageRecordByReservationSQL = `SELECT ` + usageRecordColumns + ` FROM usage_records WHERE reservation_id = ?`
