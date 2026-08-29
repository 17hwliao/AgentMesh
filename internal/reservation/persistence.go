package reservation

import (
	"context"
	"database/sql"
	"errors"
	"time"

	mysql "github.com/go-sql-driver/mysql"
)

// PersistentReservation is the durable counterpart of the 005 state model.
// Units are a reservation budget, not a claim of exact token billing.
type PersistentReservation struct {
	ID             string
	TenantID       string
	RequestID      string
	Model          string
	State          State
	Version        uint64
	ReservedUnits  uint64
	SettledUnits   uint64
	ReleasedUnits  uint64
	UsageObserved  bool
	SettlementKind string
	HeartbeatAt    time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// PersistentAttempt is written before the corresponding Provider call.
type PersistentAttempt struct {
	ReservationID  string
	Ordinal        uint64
	ProviderName   string
	Model          string
	StartedAt      time.Time
	HeartbeatAt    time.Time
	ForwardedRunes uint64
	UsageObserved  bool
}

type CreatePersistentReservation struct {
	ID            string
	TenantID      string
	RequestID     string
	Model         string
	ReservedUnits uint64
}

// SQLRepository persists only safe reservation state and metering summaries.
// It intentionally has no Redis or Gateway dependency; the Coordinator owns
// cross-store ordering in a later task.
type SQLRepository struct {
	db  sqlDatabase
	now func() time.Time
}

type sqlDatabase interface {
	BeginTx(context.Context, *sql.TxOptions) (sqlTransaction, error)
}

type sqlTransaction interface {
	ExecContext(context.Context, string, ...any) (sqlResult, error)
	QueryRowContext(context.Context, string, ...any) sqlRow
	QueryContext(context.Context, string, ...any) (sqlRows, error)
	Commit() error
	Rollback() error
}

type sqlResult interface{ RowsAffected() (int64, error) }
type sqlRow interface{ Scan(...any) error }
type sqlRows interface {
	Next() bool
	Scan(...any) error
	Err() error
	Close() error
}

type stdDatabase struct{ db *sql.DB }
type stdTransaction struct{ tx *sql.Tx }

func (d stdDatabase) BeginTx(ctx context.Context, options *sql.TxOptions) (sqlTransaction, error) {
	tx, err := d.db.BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	return stdTransaction{tx: tx}, nil
}

func (t stdTransaction) ExecContext(ctx context.Context, query string, args ...any) (sqlResult, error) {
	return t.tx.ExecContext(ctx, query, args...)
}

func (t stdTransaction) QueryRowContext(ctx context.Context, query string, args ...any) sqlRow {
	return t.tx.QueryRowContext(ctx, query, args...)
}

func (t stdTransaction) QueryContext(ctx context.Context, query string, args ...any) (sqlRows, error) {
	return t.tx.QueryContext(ctx, query, args...)
}

func (t stdTransaction) Commit() error   { return t.tx.Commit() }
func (t stdTransaction) Rollback() error { return t.tx.Rollback() }

func newMySQLRepository(db *sql.DB, now func() time.Time) *SQLRepository {
	return newSQLRepository(stdDatabase{db: db}, now)
}

// OpenMySQLRepository forces matched-row semantics. RecordProgress uses
// GREATEST, so a legitimate same-value replay must not look like a failed CAS
// merely because MySQL reports zero changed rows by default. The returned DB is
// owned by the caller and must be closed during shutdown.
func OpenMySQLRepository(dsn string, now func() time.Time) (*SQLRepository, *sql.DB, error) {
	normalized, err := mysqlDSNWithFoundRows(dsn)
	if err != nil {
		return nil, nil, err
	}
	db, err := sql.Open("mysql", normalized)
	if err != nil {
		return nil, nil, err
	}
	return newMySQLRepository(db, now), db, nil
}

func mysqlDSNWithFoundRows(dsn string) (string, error) {
	config, err := mysql.ParseDSN(dsn)
	if err != nil {
		return "", err
	}
	config.ClientFoundRows = true
	return config.FormatDSN(), nil
}

func newSQLRepository(db sqlDatabase, now func() time.Time) *SQLRepository {
	if now == nil {
		now = time.Now
	}
	return &SQLRepository{db: db, now: now}
}

// Create records creating before any quota pre-deduction. Replaying the same
// tenant/request pair reads the original row rather than adding a second one.
func (r *SQLRepository) Create(ctx context.Context, input CreatePersistentReservation) (PersistentReservation, error) {
	if input.ID == "" || input.TenantID == "" || input.RequestID == "" || input.Model == "" || input.ReservedUnits == 0 {
		return PersistentReservation{}, domainError(CodeStateInvalid)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return PersistentReservation{}, err
	}
	defer tx.Rollback()
	now := r.now().UTC()
	if _, err := tx.ExecContext(ctx, insertCreatingSQL, input.ID, input.TenantID, input.RequestID, input.Model, input.ReservedUnits, now, now, now); err != nil {
		return PersistentReservation{}, err
	}
	value, err := scanReservation(tx.QueryRowContext(ctx, reservationByRequestSQL, input.TenantID, input.RequestID))
	if err != nil {
		return PersistentReservation{}, err
	}
	if err := tx.Commit(); err != nil {
		return PersistentReservation{}, err
	}
	return value, nil
}

// MarkReserved performs the MySQL half only after Redis reserve succeeds.
func (r *SQLRepository) MarkReserved(ctx context.Context, tenantID, reservationID string, expectedVersion uint64) (PersistentReservation, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return PersistentReservation{}, err
	}
	defer tx.Rollback()
	now := r.now().UTC()
	result, err := tx.ExecContext(ctx, markReservedSQL, now, now, reservationID, tenantID, Creating, expectedVersion)
	if err != nil {
		return PersistentReservation{}, err
	}
	if err := exactlyOne(result); err != nil {
		return PersistentReservation{}, err
	}
	value, err := scanReservation(tx.QueryRowContext(ctx, reservationByIDSQL, reservationID, tenantID))
	if err != nil {
		return PersistentReservation{}, err
	}
	if err := tx.Commit(); err != nil {
		return PersistentReservation{}, err
	}
	return value, nil
}

// StartAttempt atomically advances the reservation version and writes started
// evidence before an Adapter can be invoked.
func (r *SQLRepository) StartAttempt(ctx context.Context, tenantID, reservationID string, expectedVersion uint64, providerName, model string) (PersistentAttempt, PersistentReservation, error) {
	if providerName == "" || model == "" {
		return PersistentAttempt{}, PersistentReservation{}, domainError(CodeStateInvalid)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return PersistentAttempt{}, PersistentReservation{}, err
	}
	defer tx.Rollback()
	now := r.now().UTC()
	result, err := tx.ExecContext(ctx, advanceAttemptSQL, now, now, reservationID, tenantID, Reserved, expectedVersion)
	if err != nil {
		return PersistentAttempt{}, PersistentReservation{}, err
	}
	if err := exactlyOne(result); err != nil {
		return PersistentAttempt{}, PersistentReservation{}, err
	}
	var ordinal uint64
	if err := tx.QueryRowContext(ctx, nextAttemptOrdinalSQL, reservationID).Scan(&ordinal); err != nil {
		return PersistentAttempt{}, PersistentReservation{}, err
	}
	ordinal++
	if _, err := tx.ExecContext(ctx, insertAttemptSQL, reservationID, ordinal, providerName, model, now, now); err != nil {
		return PersistentAttempt{}, PersistentReservation{}, err
	}
	value, err := scanReservation(tx.QueryRowContext(ctx, reservationByIDSQL, reservationID, tenantID))
	if err != nil {
		return PersistentAttempt{}, PersistentReservation{}, err
	}
	if err := tx.Commit(); err != nil {
		return PersistentAttempt{}, PersistentReservation{}, err
	}
	return PersistentAttempt{ReservationID: reservationID, Ordinal: ordinal, ProviderName: providerName, Model: model, StartedAt: now, HeartbeatAt: now}, value, nil
}

// RecordProgress only moves a persisted lower bound forward. It does not
// settle, refund, or infer unobserved Provider output.
func (r *SQLRepository) RecordProgress(ctx context.Context, tenantID, reservationID string, ordinal, forwardedRunes uint64, heartbeat time.Time) error {
	if ordinal == 0 || heartbeat.IsZero() {
		return domainError(CodeStateInvalid)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, updateAttemptProgressSQL, forwardedRunes, heartbeat.UTC(), reservationID, tenantID, ordinal)
	if err != nil {
		return err
	}
	if err := exactlyOne(result); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, updateReservationHeartbeatSQL, heartbeat.UTC(), heartbeat.UTC(), reservationID, tenantID); err != nil {
		return err
	}
	return tx.Commit()
}

// FinishAttempt stores only a stable result code. Provider error bodies never
// belong in durable quota evidence.
func (r *SQLRepository) FinishAttempt(ctx context.Context, tenantID, reservationID string, ordinal uint64, outcome string) error {
	if ordinal == 0 || outcome == "" {
		return domainError(CodeStateInvalid)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, finishAttemptSQL, outcome, r.now().UTC(), reservationID, tenantID, ordinal)
	if err != nil {
		return err
	}
	if err := exactlyOne(result); err != nil {
		return err
	}
	return tx.Commit()
}

// MarkSettled writes the terminal MySQL evidence only after Redis settle has
// completed. An attempt must exist, including for an uncertain failure.
func (r *SQLRepository) MarkSettled(ctx context.Context, tenantID, reservationID string, expectedVersion, settledUnits, releasedUnits uint64, usageObserved bool, kind string) (PersistentReservation, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return PersistentReservation{}, err
	}
	defer tx.Rollback()
	now := r.now().UTC()
	result, err := tx.ExecContext(ctx, markSettledSQL, settledUnits, releasedUnits, usageObserved, kind, now, now, reservationID, tenantID, expectedVersion)
	if err != nil {
		return PersistentReservation{}, err
	}
	if err := exactlyOne(result); err != nil {
		return PersistentReservation{}, err
	}
	value, err := scanReservation(tx.QueryRowContext(ctx, reservationByIDSQL, reservationID, tenantID))
	if err != nil {
		return PersistentReservation{}, err
	}
	if err := tx.Commit(); err != nil {
		return PersistentReservation{}, err
	}
	return value, nil
}

// MarkCancelled is only legal when no durable started attempt exists.
func (r *SQLRepository) MarkCancelled(ctx context.Context, tenantID, reservationID string, expectedVersion uint64) (PersistentReservation, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return PersistentReservation{}, err
	}
	defer tx.Rollback()
	now := r.now().UTC()
	result, err := tx.ExecContext(ctx, markCancelledSQL, now, now, reservationID, tenantID, expectedVersion)
	if err != nil {
		return PersistentReservation{}, err
	}
	if err := exactlyOne(result); err != nil {
		return PersistentReservation{}, err
	}
	value, err := scanReservation(tx.QueryRowContext(ctx, reservationByIDSQL, reservationID, tenantID))
	if err != nil {
		return PersistentReservation{}, err
	}
	if err := tx.Commit(); err != nil {
		return PersistentReservation{}, err
	}
	return value, nil
}

type ReconcileCandidate struct {
	Reservation    PersistentReservation
	AttemptStarted bool
	ForwardedRunes uint64
}

// ClaimExpired is the only normal path that enters expired_pending. Row locks
// make concurrent reconciler scans claim a candidate at most once.
func (r *SQLRepository) ClaimExpired(ctx context.Context, before time.Time, limit int) ([]ReconcileCandidate, error) {
	return r.claimExpired(ctx, "", before, limit)
}

// ClaimExpiredForTenant is the validation-safe variant of ClaimExpired. It
// never locks or transitions another tenant's stale reservation.
func (r *SQLRepository) ClaimExpiredForTenant(ctx context.Context, tenantID string, before time.Time, limit int) ([]ReconcileCandidate, error) {
	if tenantID == "" {
		return nil, domainError(CodeStateInvalid)
	}
	return r.claimExpired(ctx, tenantID, before, limit)
}

func (r *SQLRepository) claimExpired(ctx context.Context, tenantID string, before time.Time, limit int) ([]ReconcileCandidate, error) {
	if limit <= 0 {
		return nil, domainError(CodeStateInvalid)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	query, arguments := expiredCandidatesSQL, []any{before.UTC(), limit}
	if tenantID != "" {
		query, arguments = expiredCandidatesForTenantSQL, []any{tenantID, before.UTC(), limit}
	}
	rows, err := tx.QueryContext(ctx, query, arguments...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var candidates []ReconcileCandidate
	for rows.Next() {
		candidate, err := scanReconcileCandidate(rows)
		if err != nil {
			return nil, err
		}
		if candidate.Reservation.State != ExpiredPending {
			now := r.now().UTC()
			result, err := tx.ExecContext(ctx, markExpiredPendingSQL, now, now, candidate.Reservation.ID, candidate.Reservation.TenantID, candidate.Reservation.Version)
			if err != nil {
				return nil, err
			}
			if err := exactlyOne(result); err != nil {
				return nil, err
			}
			candidate.Reservation.State = ExpiredPending
			candidate.Reservation.Version++
			candidate.Reservation.HeartbeatAt = now
			candidate.Reservation.UpdatedAt = now
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return candidates, nil
}

func exactlyOne(result sqlResult) error {
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return domainError(CodeVersionConflict)
	}
	return nil
}

func scanReservation(row sqlRow) (PersistentReservation, error) {
	return scanReservationFields(row.Scan)
}

func scanReservationFields(scan func(...any) error) (PersistentReservation, error) {
	var value PersistentReservation
	var state string
	var settlement sql.NullString
	err := scan(
		&value.ID, &value.TenantID, &value.RequestID, &value.Model, &state, &value.Version,
		&value.ReservedUnits, &value.SettledUnits, &value.ReleasedUnits, &value.UsageObserved,
		&settlement, &value.HeartbeatAt, &value.CreatedAt, &value.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return PersistentReservation{}, domainError(CodeNotFound)
	}
	if err != nil {
		return PersistentReservation{}, err
	}
	value.State = State(state)
	value.SettlementKind = settlement.String
	return value, nil
}

func scanReconcileCandidate(row sqlRow) (ReconcileCandidate, error) {
	var candidate ReconcileCandidate
	var state string
	var settlement sql.NullString
	err := row.Scan(
		&candidate.Reservation.ID, &candidate.Reservation.TenantID, &candidate.Reservation.RequestID, &candidate.Reservation.Model,
		&state, &candidate.Reservation.Version, &candidate.Reservation.ReservedUnits, &candidate.Reservation.SettledUnits,
		&candidate.Reservation.ReleasedUnits, &candidate.Reservation.UsageObserved, &settlement, &candidate.Reservation.HeartbeatAt,
		&candidate.Reservation.CreatedAt, &candidate.Reservation.UpdatedAt, &candidate.AttemptStarted, &candidate.ForwardedRunes,
	)
	if err != nil {
		return ReconcileCandidate{}, err
	}
	candidate.Reservation.State = State(state)
	candidate.Reservation.SettlementKind = settlement.String
	return candidate, nil
}

const insertCreatingSQL = `INSERT INTO quota_reservations
    (reservation_id, tenant_id, request_id, model, state, version, reserved_units, settled_units, released_units, usage_observed, settlement_kind, heartbeat_at, created_at, updated_at)
    VALUES (?, ?, ?, ?, 'creating', 1, ?, 0, 0, FALSE, NULL, ?, ?, ?)
    ON DUPLICATE KEY UPDATE reservation_id = reservation_id`

const reservationColumns = `reservation_id, tenant_id, request_id, model, state, version, reserved_units, settled_units, released_units, usage_observed, settlement_kind, heartbeat_at, created_at, updated_at`
const reservationByRequestSQL = `SELECT ` + reservationColumns + ` FROM quota_reservations WHERE tenant_id = ? AND request_id = ?`
const reservationByIDSQL = `SELECT ` + reservationColumns + ` FROM quota_reservations WHERE reservation_id = ? AND tenant_id = ? FOR UPDATE`

const markReservedSQL = `UPDATE quota_reservations
    SET state = 'reserved', version = version + 1, heartbeat_at = ?, updated_at = ?
    WHERE reservation_id = ? AND tenant_id = ? AND state = ? AND version = ?`

const advanceAttemptSQL = `UPDATE quota_reservations
    SET version = version + 1, heartbeat_at = ?, updated_at = ?
    WHERE reservation_id = ? AND tenant_id = ? AND state = ? AND version = ?`

const nextAttemptOrdinalSQL = `SELECT COALESCE(MAX(ordinal), 0) FROM provider_attempts WHERE reservation_id = ? FOR UPDATE`
const insertAttemptSQL = `INSERT INTO provider_attempts
    (reservation_id, ordinal, provider_name, model, started_at, heartbeat_at, forwarded_runes, usage_observed)
    VALUES (?, ?, ?, ?, ?, ?, 0, FALSE)`

const updateAttemptProgressSQL = `UPDATE provider_attempts AS a
    INNER JOIN quota_reservations AS r ON r.reservation_id = a.reservation_id
    SET a.forwarded_runes = GREATEST(a.forwarded_runes, ?), a.heartbeat_at = GREATEST(a.heartbeat_at, ?)
    WHERE a.reservation_id = ? AND r.tenant_id = ? AND a.ordinal = ?`

const updateReservationHeartbeatSQL = `UPDATE quota_reservations
    SET heartbeat_at = GREATEST(heartbeat_at, ?), updated_at = ?
    WHERE reservation_id = ? AND tenant_id = ?`

const finishAttemptSQL = `UPDATE provider_attempts AS a
    INNER JOIN quota_reservations AS r ON r.reservation_id = a.reservation_id
    SET a.result_code = ?, a.finished_at = ?
    WHERE a.reservation_id = ? AND r.tenant_id = ? AND a.ordinal = ?`

const markSettledSQL = `UPDATE quota_reservations AS r
    SET r.state = 'settled', r.version = r.version + 1, r.settled_units = ?, r.released_units = ?, r.usage_observed = ?, r.settlement_kind = ?, r.heartbeat_at = ?, r.updated_at = ?
    WHERE r.reservation_id = ? AND r.tenant_id = ? AND r.version = ? AND r.state IN ('reserved', 'expired_pending')
      AND EXISTS (SELECT 1 FROM provider_attempts AS a WHERE a.reservation_id = r.reservation_id)`

const markCancelledSQL = `UPDATE quota_reservations AS r
    SET r.state = 'cancelled', r.version = r.version + 1, r.settlement_kind = 'cancelled', r.heartbeat_at = ?, r.updated_at = ?
    WHERE r.reservation_id = ? AND r.tenant_id = ? AND r.version = ? AND r.state IN ('creating', 'reserved', 'expired_pending')
      AND NOT EXISTS (SELECT 1 FROM provider_attempts AS a WHERE a.reservation_id = r.reservation_id)`

const expiredCandidatesSQL = `SELECT ` + reservationColumns + `,
    EXISTS (SELECT 1 FROM provider_attempts AS a WHERE a.reservation_id = r.reservation_id),
    COALESCE((SELECT SUM(a.forwarded_runes) FROM provider_attempts AS a WHERE a.reservation_id = r.reservation_id), 0)
    FROM quota_reservations AS r
    WHERE r.state IN ('creating', 'reserved', 'expired_pending') AND r.heartbeat_at < ?
    ORDER BY r.heartbeat_at ASC LIMIT ? FOR UPDATE SKIP LOCKED`

const expiredCandidatesForTenantSQL = `SELECT ` + reservationColumns + `,
    EXISTS (SELECT 1 FROM provider_attempts AS a WHERE a.reservation_id = r.reservation_id),
    COALESCE((SELECT SUM(a.forwarded_runes) FROM provider_attempts AS a WHERE a.reservation_id = r.reservation_id), 0)
    FROM quota_reservations AS r
    WHERE r.tenant_id = ? AND r.state IN ('creating', 'reserved', 'expired_pending') AND r.heartbeat_at < ?
    ORDER BY r.heartbeat_at ASC LIMIT ? FOR UPDATE SKIP LOCKED`

const markExpiredPendingSQL = `UPDATE quota_reservations
    SET state = 'expired_pending', version = version + 1, heartbeat_at = ?, updated_at = ?
    WHERE reservation_id = ? AND tenant_id = ? AND version = ? AND state IN ('creating', 'reserved')`
