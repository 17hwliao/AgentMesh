package reservation

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	redis "github.com/redis/go-redis/v9"
)

const (
	CodeMigrationApplyFailed      = "migration_apply_failed"
	CodeMigrationInspectionFailed = "migration_inspection_failed"
)

// ValidateRealStorage is the sole real-endpoint entry point. It prints no
// values itself; callers can serialize the returned safe report.
func ValidateRealStorage(ctx context.Context, lookup func(string) string, migrationPath string) (ValidationReport, error) {
	config, report, err := LoadRealValidationConfig(lookup)
	if err != nil {
		return report, err
	}
	content, err := os.ReadFile(migrationPath)
	if err != nil {
		return failedValidationReport(report, CodeMigrationDefinitionInvalid), domainError(CodeMigrationDefinitionInvalid)
	}
	definitions, err := ParseQuotaMigration(string(content))
	if err != nil {
		return failedValidationReport(report, Code(err)), err
	}
	options, err := redis.ParseURL(config.RedisURL)
	if err != nil {
		return failedValidationReport(report, CodeQuotaConfigurationInvalid), domainError(CodeQuotaConfigurationInvalid)
	}
	repository, db, err := OpenMySQLRepository(config.MySQLDSN, nil)
	if err != nil {
		return failedValidationReport(report, CodeQuotaConfigurationInvalid), domainError(CodeQuotaConfigurationInvalid)
	}
	defer db.Close()
	pingContext, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	report.NetworkAttempts++
	if err := db.PingContext(pingContext); err != nil {
		return failedValidationReport(report, quotaUnavailableCode), domainError(quotaUnavailableCode)
	}
	client := redis.NewClient(options)
	defer client.Close()
	report.NetworkAttempts++
	if err := client.Ping(pingContext).Err(); err != nil {
		return failedValidationReport(report, quotaUnavailableCode), domainError(quotaUnavailableCode)
	}
	migration, err := EnsureQuotaMigration(ctx, mysqlMigrationStore{db: db}, definitions)
	if err != nil {
		code := Code(err)
		if code == "" {
			code = CodeMigrationApplyFailed
		}
		return failedValidationReport(report, code), domainError(code)
	}
	report.Migration = migration
	runID, err := randomValidationRunID()
	if err != nil {
		return failedValidationReport(report, CodeValidationScenarioFailed), domainError(CodeValidationScenarioFailed)
	}
	scope, err := NewValidationScope(config.Namespace, runID)
	if err != nil {
		return failedValidationReport(report, Code(err)), err
	}
	scenarios, err := RunValidationScenarios(ctx, newRealValidationScenarioStore(repository, db, client), scope)
	scenarios.NetworkAttempts = report.NetworkAttempts
	scenarios.ProviderAttempts = 0
	scenarios.Migration = migration
	if err != nil {
		return scenarios, err
	}
	return scenarios, nil
}

func failedValidationReport(report ValidationReport, code string) ValidationReport {
	report.Status = "verification_failed"
	report.Code = code
	report.ProviderAttempts = 0
	return report
}

func randomValidationRunID() (string, error) {
	value := make([]byte, 6)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

type mysqlMigrationStore struct{ db *sql.DB }

func (s mysqlMigrationStore) TableShape(ctx context.Context, table string) (MigrationTableShape, error) {
	shape := MigrationTableShape{Columns: map[string]bool{}, Indexes: map[string]bool{}, Constraints: map[string]bool{}}
	var exists int
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = ?)`, table).Scan(&exists)
	if err != nil {
		return shape, err
	}
	if exists != 1 {
		return shape, nil
	}
	shape.Exists = true
	if err := collectNames(ctx, s.db, `SELECT column_name FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = ?`, table, shape.Columns); err != nil {
		return shape, err
	}
	if err := collectNames(ctx, s.db, `SELECT index_name FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = ?`, table, shape.Indexes); err != nil {
		return shape, err
	}
	if err := collectNames(ctx, s.db, `SELECT constraint_name FROM information_schema.table_constraints WHERE table_schema = DATABASE() AND table_name = ?`, table, shape.Constraints); err != nil {
		return shape, err
	}
	return shape, nil
}

func (s mysqlMigrationStore) ExecuteCreate(ctx context.Context, statement string) error {
	_, err := s.db.ExecContext(ctx, statement)
	return err
}

func collectNames(ctx context.Context, db *sql.DB, query, table string, destination map[string]bool) error {
	rows, err := db.QueryContext(ctx, query, table)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return err
		}
		destination[value] = true
	}
	return rows.Err()
}

type realValidationScenarioStore struct {
	repository   *SQLRepository
	db           *sql.DB
	client       *redis.Client
	quota        *RedisQuotaStore
	createdKeys  map[string]struct{}
	createdUsers map[string]struct{}
}

func newRealValidationScenarioStore(repository *SQLRepository, db *sql.DB, client *redis.Client) *realValidationScenarioStore {
	return &realValidationScenarioStore{
		repository: repository, db: db, client: client, quota: NewRedisQuotaStore(client),
		createdKeys: map[string]struct{}{}, createdUsers: map[string]struct{}{},
	}
}

func (s *realValidationScenarioStore) RunScenario(ctx context.Context, scope ValidationScope, name string) (ValidationScenarioReport, error) {
	switch name {
	case "settle_replay":
		return s.runSettleReplay(ctx, scope, name)
	case "cancel_replay":
		return s.runCancelReplay(ctx, scope, name)
	case "started_reconcile":
		return s.runStartedReconcile(ctx, scope, name)
	case "unstarted_reconcile":
		return s.runUnstartedReconcile(ctx, scope, name)
	default:
		return ValidationScenarioReport{}, errors.New("unknown validation scenario")
	}
}

func (s *realValidationScenarioStore) runSettleReplay(ctx context.Context, scope ValidationScope, name string) (ValidationScenarioReport, error) {
	tenant, value, err := s.createReserved(ctx, scope, name)
	if err != nil {
		return ValidationScenarioReport{}, err
	}
	if _, value, err = s.repository.StartAttempt(ctx, tenant, value.ID, value.Version, "validation", "validation-model"); err != nil {
		return ValidationScenarioReport{}, err
	}
	result, err := s.settle(ctx, tenant, value, 24)
	if err != nil {
		return ValidationScenarioReport{}, err
	}
	settled, err := s.repository.MarkSettled(ctx, tenant, value.ID, value.Version, 24, result.ReleasedUnits, true, "provider_usage")
	if err != nil {
		return ValidationScenarioReport{}, err
	}
	replay, err := s.settle(ctx, tenant, value, 24)
	if err != nil {
		return ValidationScenarioReport{}, err
	}
	available, err := s.available(ctx, tenant)
	if err != nil {
		return ValidationScenarioReport{}, err
	}
	return ValidationScenarioReport{Name: name, FinalState: settled.State, AvailableUnits: available, ReplayUnits: replay.Available, ReleasedUnits: result.ReleasedUnits, UsageObserved: settled.UsageObserved, SettlementKind: settled.SettlementKind}, nil
}

func (s *realValidationScenarioStore) runCancelReplay(ctx context.Context, scope ValidationScope, name string) (ValidationScenarioReport, error) {
	tenant, value, err := s.createReserved(ctx, scope, name)
	if err != nil {
		return ValidationScenarioReport{}, err
	}
	result, err := s.cancel(ctx, tenant, value)
	if err != nil {
		return ValidationScenarioReport{}, err
	}
	cancelled, err := s.repository.MarkCancelled(ctx, tenant, value.ID, value.Version)
	if err != nil {
		return ValidationScenarioReport{}, err
	}
	replay, err := s.cancel(ctx, tenant, value)
	if err != nil {
		return ValidationScenarioReport{}, err
	}
	available, err := s.available(ctx, tenant)
	if err != nil {
		return ValidationScenarioReport{}, err
	}
	return ValidationScenarioReport{Name: name, FinalState: cancelled.State, AvailableUnits: available, ReplayUnits: replay.Available, ReleasedUnits: result.ReleasedUnits, SettlementKind: cancelled.SettlementKind}, nil
}

func (s *realValidationScenarioStore) runStartedReconcile(ctx context.Context, scope ValidationScope, name string) (ValidationScenarioReport, error) {
	tenant, value, err := s.createReserved(ctx, scope, name)
	if err != nil {
		return ValidationScenarioReport{}, err
	}
	attempt, value, err := s.repository.StartAttempt(ctx, tenant, value.ID, value.Version, "validation", "validation-model")
	if err != nil {
		return ValidationScenarioReport{}, err
	}
	if err := s.repository.RecordProgress(ctx, tenant, value.ID, attempt.Ordinal, 1, time.Now().UTC()); err != nil {
		return ValidationScenarioReport{}, err
	}
	if err := s.expire(ctx, tenant, value.ID); err != nil {
		return ValidationScenarioReport{}, err
	}
	completed, err := s.reconcile(ctx, tenant)
	if err != nil {
		return ValidationScenarioReport{}, err
	}
	if completed != 1 {
		return ValidationScenarioReport{}, errors.New("started reconciliation did not claim exactly one reservation")
	}
	completed, err = s.reconcile(ctx, tenant)
	if err != nil {
		return ValidationScenarioReport{}, err
	}
	if completed != 0 {
		return ValidationScenarioReport{}, errors.New("started reconciliation replay changed a reservation")
	}
	final, err := s.reservation(ctx, tenant, value.ID)
	if err != nil {
		return ValidationScenarioReport{}, err
	}
	available, err := s.available(ctx, tenant)
	if err != nil {
		return ValidationScenarioReport{}, err
	}
	return ValidationScenarioReport{Name: name, FinalState: final.State, AvailableUnits: available, ReplayUnits: available, ReleasedUnits: final.ReleasedUnits, UsageObserved: final.UsageObserved, SettlementKind: final.SettlementKind}, nil
}

func (s *realValidationScenarioStore) runUnstartedReconcile(ctx context.Context, scope ValidationScope, name string) (ValidationScenarioReport, error) {
	tenant, value, err := s.createReserved(ctx, scope, name)
	if err != nil {
		return ValidationScenarioReport{}, err
	}
	if err := s.expire(ctx, tenant, value.ID); err != nil {
		return ValidationScenarioReport{}, err
	}
	completed, err := s.reconcile(ctx, tenant)
	if err != nil {
		return ValidationScenarioReport{}, err
	}
	if completed != 1 {
		return ValidationScenarioReport{}, errors.New("unstarted reconciliation did not claim exactly one reservation")
	}
	completed, err = s.reconcile(ctx, tenant)
	if err != nil {
		return ValidationScenarioReport{}, err
	}
	if completed != 0 {
		return ValidationScenarioReport{}, errors.New("unstarted reconciliation replay changed a reservation")
	}
	final, err := s.reservation(ctx, tenant, value.ID)
	if err != nil {
		return ValidationScenarioReport{}, err
	}
	available, err := s.available(ctx, tenant)
	if err != nil {
		return ValidationScenarioReport{}, err
	}
	return ValidationScenarioReport{Name: name, FinalState: final.State, AvailableUnits: available, ReplayUnits: available, ReleasedUnits: 64, SettlementKind: final.SettlementKind}, nil
}

func (s *realValidationScenarioStore) createReserved(ctx context.Context, scope ValidationScope, name string) (string, PersistentReservation, error) {
	tenant, reservationID := scope.Tenant(name), scope.ReservationID(name)
	s.createdUsers[tenant] = struct{}{}
	if err := s.setBalance(ctx, tenant, 100); err != nil {
		return "", PersistentReservation{}, err
	}
	value, err := s.repository.Create(ctx, CreatePersistentReservation{ID: reservationID, TenantID: tenant, RequestID: scope.RequestID(name), Model: "validation-model", ReservedUnits: 64})
	if err != nil {
		return "", PersistentReservation{}, err
	}
	if _, err := s.reserve(ctx, tenant, value); err != nil {
		return "", PersistentReservation{}, err
	}
	value, err = s.repository.MarkReserved(ctx, tenant, value.ID, value.Version)
	return tenant, value, err
}

func (s *realValidationScenarioStore) reserve(ctx context.Context, tenant string, value PersistentReservation) (QuotaOperationResult, error) {
	s.rememberOperation(tenant, value.ID, value.Version, "reserve")
	s.rememberKey(reserveMarkerKey(tenant, value.ID))
	return s.quota.Reserve(ctx, tenant, value.ID, value.Version, value.ReservedUnits)
}

func (s *realValidationScenarioStore) settle(ctx context.Context, tenant string, value PersistentReservation, consumed uint64) (QuotaOperationResult, error) {
	s.rememberOperation(tenant, value.ID, value.Version, "settle")
	return s.quota.Settle(ctx, tenant, value.ID, value.Version, value.ReservedUnits, consumed)
}

func (s *realValidationScenarioStore) cancel(ctx context.Context, tenant string, value PersistentReservation) (QuotaOperationResult, error) {
	s.rememberOperation(tenant, value.ID, value.Version, "cancel")
	return s.quota.Cancel(ctx, tenant, value.ID, value.Version, value.ReservedUnits)
}

func (s *realValidationScenarioStore) setBalance(ctx context.Context, tenant string, units uint64) error {
	key := balanceKey(tenant)
	s.rememberKey(key)
	return s.client.Set(ctx, key, units, 0).Err()
}

func (s *realValidationScenarioStore) available(ctx context.Context, tenant string) (uint64, error) {
	value, err := s.client.Get(ctx, balanceKey(tenant)).Result()
	if err != nil {
		return 0, err
	}
	return strconv.ParseUint(value, 10, 64)
}

func (s *realValidationScenarioStore) reservation(ctx context.Context, tenantID, reservationID string) (PersistentReservation, error) {
	return scanReservationFields(s.db.QueryRowContext(ctx, `SELECT `+reservationColumns+` FROM quota_reservations WHERE reservation_id = ? AND tenant_id = ?`, reservationID, tenantID).Scan)
}

func (s *realValidationScenarioStore) expire(ctx context.Context, tenantID, reservationID string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE quota_reservations SET heartbeat_at = ?, updated_at = ? WHERE tenant_id = ? AND reservation_id = ? AND state = 'reserved'`, time.Now().UTC().Add(-time.Hour), time.Now().UTC(), tenantID, reservationID)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil || count != 1 {
		return errors.New("validation reservation was not expired")
	}
	return nil
}

func (s *realValidationScenarioStore) reconcile(ctx context.Context, tenantID string) (int, error) {
	records := tenantScopedRecords{repository: s.repository, tenantID: tenantID}
	return NewReconciler(records, s.quota).Reconcile(ctx, time.Now().UTC(), 1)
}

func (s *realValidationScenarioStore) rememberOperation(tenantID, reservationID string, version uint64, operation string) {
	s.rememberKey(operationKey(tenantID, reservationID, version, operation))
}

func (s *realValidationScenarioStore) rememberKey(key string) { s.createdKeys[key] = struct{}{} }

func (s *realValidationScenarioStore) Cleanup(ctx context.Context, _ ValidationScope) error {
	var first error
	for tenant := range s.createdUsers {
		if _, err := s.db.ExecContext(ctx, `DELETE a FROM provider_attempts AS a INNER JOIN quota_reservations AS r ON r.reservation_id = a.reservation_id WHERE r.tenant_id = ?`, tenant); err != nil && first == nil {
			first = err
		}
		if _, err := s.db.ExecContext(ctx, `DELETE FROM quota_reservations WHERE tenant_id = ?`, tenant); err != nil && first == nil {
			first = err
		}
	}
	keys := make([]string, 0, len(s.createdKeys))
	for key := range s.createdKeys {
		keys = append(keys, key)
	}
	if len(keys) > 0 {
		if err := s.client.Del(ctx, keys...).Err(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

type tenantScopedRecords struct {
	repository *SQLRepository
	tenantID   string
}

func (s tenantScopedRecords) Create(ctx context.Context, input CreatePersistentReservation) (PersistentReservation, error) {
	return s.repository.Create(ctx, input)
}
func (s tenantScopedRecords) MarkReserved(ctx context.Context, tenantID, reservationID string, version uint64) (PersistentReservation, error) {
	return s.repository.MarkReserved(ctx, tenantID, reservationID, version)
}
func (s tenantScopedRecords) StartAttempt(ctx context.Context, tenantID, reservationID string, version uint64, providerName, model string) (PersistentAttempt, PersistentReservation, error) {
	return s.repository.StartAttempt(ctx, tenantID, reservationID, version, providerName, model)
}
func (s tenantScopedRecords) RecordProgress(ctx context.Context, tenantID, reservationID string, ordinal, runes uint64, heartbeat time.Time) error {
	return s.repository.RecordProgress(ctx, tenantID, reservationID, ordinal, runes, heartbeat)
}
func (s tenantScopedRecords) FinishAttempt(ctx context.Context, tenantID, reservationID string, ordinal uint64, outcome string) error {
	return s.repository.FinishAttempt(ctx, tenantID, reservationID, ordinal, outcome)
}
func (s tenantScopedRecords) MarkSettled(ctx context.Context, tenantID, reservationID string, version, settled, released uint64, observed bool, kind string) (PersistentReservation, error) {
	return s.repository.MarkSettled(ctx, tenantID, reservationID, version, settled, released, observed, kind)
}
func (s tenantScopedRecords) MarkCancelled(ctx context.Context, tenantID, reservationID string, version uint64) (PersistentReservation, error) {
	return s.repository.MarkCancelled(ctx, tenantID, reservationID, version)
}
func (s tenantScopedRecords) ClaimExpired(ctx context.Context, before time.Time, limit int) ([]ReconcileCandidate, error) {
	return s.repository.ClaimExpiredForTenant(ctx, s.tenantID, before, limit)
}

func (s ValidationScope) String() string {
	return fmt.Sprintf("%s/%s", s.Namespace, s.RunID)
}
