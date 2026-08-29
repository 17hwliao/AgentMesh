package reservation

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	mysql "github.com/go-sql-driver/mysql"
)

func TestSQLRepositoryCreateIsRequestIdempotent(t *testing.T) {
	now := time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC)
	tx := &fakeTransaction{rows: []sqlRow{reservationRow("reservation-original", Creating, 1, now)}}
	repo := newSQLRepository(fakeDatabase{tx: tx}, func() time.Time { return now })

	value, err := repo.Create(context.Background(), CreatePersistentReservation{
		ID: "reservation-retry", TenantID: "tenant-a", RequestID: "request-a", Model: "mock-model", ReservedUnits: 64,
	})
	if err != nil {
		t.Fatal(err)
	}
	if value.ID != "reservation-original" || value.State != Creating || !tx.committed {
		t.Fatalf("value=%+v committed=%t", value, tx.committed)
	}
	if len(tx.execQueries) != 1 || !strings.Contains(tx.execQueries[0], "ON DUPLICATE KEY") {
		t.Fatalf("create queries=%q", tx.execQueries)
	}
	if len(tx.queryQueries) != 1 || !strings.Contains(tx.queryQueries[0], "tenant_id = ? AND request_id = ?") {
		t.Fatalf("idempotency lookup=%q", tx.queryQueries)
	}
}

func TestSQLRepositoryStartAttemptWritesEvidenceBeforeCommit(t *testing.T) {
	now := time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC)
	tx := &fakeTransaction{rows: []sqlRow{
		fakeRow{values: []any{uint64(0)}},
		reservationRow("reservation-a", Reserved, 3, now),
	}}
	repo := newSQLRepository(fakeDatabase{tx: tx}, func() time.Time { return now })

	attempt, value, err := repo.StartAttempt(context.Background(), "tenant-a", "reservation-a", 2, "mock", "mock-model")
	if err != nil {
		t.Fatal(err)
	}
	if attempt.Ordinal != 1 || attempt.ProviderName != "mock" || value.Version != 3 || !tx.committed {
		t.Fatalf("attempt=%+v value=%+v committed=%t", attempt, value, tx.committed)
	}
	if len(tx.execQueries) != 2 || !strings.Contains(tx.execQueries[0], "version = version + 1") || !strings.Contains(tx.execQueries[1], "INSERT INTO provider_attempts") {
		t.Fatalf("attempt persistence sequence=%q", tx.execQueries)
	}
	if len(tx.queryQueries) != 2 || !strings.Contains(tx.queryQueries[0], "MAX(ordinal)") {
		t.Fatalf("attempt queries=%q", tx.queryQueries)
	}
}

func TestSQLRepositoryProgressIsMonotonicAndTenantScoped(t *testing.T) {
	now := time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC)
	tx := &fakeTransaction{}
	repo := newSQLRepository(fakeDatabase{tx: tx}, func() time.Time { return now })
	if err := repo.RecordProgress(context.Background(), "tenant-a", "reservation-a", 1, 24, now); err != nil {
		t.Fatal(err)
	}
	if !tx.committed || len(tx.execQueries) != 2 || !strings.Contains(tx.execQueries[0], "GREATEST(a.forwarded_runes, ?)") || !strings.Contains(tx.execQueries[0], "r.tenant_id") {
		t.Fatalf("progress queries=%q committed=%t", tx.execQueries, tx.committed)
	}
}

func TestSQLRepositorySameValueProgressUsesMatchedRowSemantics(t *testing.T) {
	now := time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC)
	// With CLIENT_FOUND_ROWS MySQL returns one matched row even when GREATEST
	// leaves both values unchanged. This fake models that server response.
	tx := &fakeTransaction{results: []sqlResult{fakeResult(1), fakeResult(1)}}
	repo := newSQLRepository(fakeDatabase{tx: tx}, nil)
	if err := repo.RecordProgress(context.Background(), "tenant-a", "reservation-a", 1, 24, now); err != nil || !tx.committed {
		t.Fatalf("err=%v committed=%t", err, tx.committed)
	}
}

func TestSQLRepositoryClaimExpiredForTenantNeverUsesGlobalScan(t *testing.T) {
	tx := &fakeTransaction{}
	repo := newSQLRepository(fakeDatabase{tx: tx}, nil)
	if _, err := repo.ClaimExpiredForTenant(context.Background(), "tenant-validation", time.Now(), 1); err != nil {
		t.Fatal(err)
	}
	if len(tx.queryQueries) != 1 || !strings.Contains(tx.queryQueries[0], "r.tenant_id = ?") {
		t.Fatalf("tenant-scoped reconciliation query=%q", tx.queryQueries)
	}
}

func TestMySQLDSNForcesClientFoundRows(t *testing.T) {
	normalized, err := mysqlDSNWithFoundRows("agentmesh:secret@tcp(127.0.0.1:3306)/agentmesh?parseTime=true")
	if err != nil {
		t.Fatal(err)
	}
	config, err := mysql.ParseDSN(normalized)
	if err != nil {
		t.Fatal(err)
	}
	if !config.ClientFoundRows {
		t.Fatal("CLIENT_FOUND_ROWS is required for same-value progress replays")
	}
}

func TestSQLRepositoryVersionConflictDoesNotCommit(t *testing.T) {
	tx := &fakeTransaction{results: []sqlResult{fakeResult(0)}}
	repo := newSQLRepository(fakeDatabase{tx: tx}, nil)
	_, err := repo.MarkReserved(context.Background(), "tenant-a", "reservation-a", 9)
	if Code(err) != CodeVersionConflict || tx.committed {
		t.Fatalf("code=%q committed=%t", Code(err), tx.committed)
	}
}

func TestSQLRepositoryTerminalWritesOutboxInSameTransaction(t *testing.T) {
	now := time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC)
	for _, testCase := range []struct {
		name  string
		state State
		run   func(*SQLRepository) error
	}{
		{name: "settled", state: Settled, run: func(repo *SQLRepository) error {
			_, err := repo.MarkSettled(context.Background(), "tenant-a", "reservation-a", 3, 24, 40, true, "provider_usage")
			return err
		}},
		{name: "cancelled", state: Cancelled, run: func(repo *SQLRepository) error {
			_, err := repo.MarkCancelled(context.Background(), "tenant-a", "reservation-a", 3)
			return err
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			tx := &fakeTransaction{rows: []sqlRow{reservationRow("reservation-a", testCase.state, 4, now)}}
			repo := newSQLRepository(fakeDatabase{tx: tx}, func() time.Time { return now })
			if err := testCase.run(repo); err != nil {
				t.Fatal(err)
			}
			if !tx.committed || len(tx.execQueries) != 2 || !strings.Contains(tx.execQueries[1], "INSERT INTO usage_outbox") || !strings.Contains(tx.execQueries[1], "SELECT r.reservation_id") {
				t.Fatalf("committed=%t queries=%q", tx.committed, tx.execQueries)
			}
		})
	}
}

func TestSQLRepositoryTerminalOutboxFailureRollsBack(t *testing.T) {
	tx := &fakeTransaction{execFailureAt: 2, execErr: errors.New("outbox unavailable")}
	repo := newSQLRepository(fakeDatabase{tx: tx}, nil)
	_, err := repo.MarkSettled(context.Background(), "tenant-a", "reservation-a", 3, 24, 40, true, "provider_usage")
	if err == nil || tx.committed || !tx.rolledBack {
		t.Fatalf("err=%v committed=%t rolled_back=%t", err, tx.committed, tx.rolledBack)
	}
}

func TestSQLRepositoryTerminalCommitFailureRollsBackOutbox(t *testing.T) {
	now := time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC)
	tx := &fakeTransaction{rows: []sqlRow{reservationRow("reservation-a", Cancelled, 4, now)}, commitErr: errors.New("commit unavailable")}
	repo := newSQLRepository(fakeDatabase{tx: tx}, func() time.Time { return now })
	_, err := repo.MarkCancelled(context.Background(), "tenant-a", "reservation-a", 3)
	if err == nil || tx.committed || !tx.rolledBack || len(tx.execQueries) != 2 {
		t.Fatalf("err=%v committed=%t rolled_back=%t queries=%q", err, tx.committed, tx.rolledBack, tx.execQueries)
	}
}

func TestSQLRepositoryDrainProjectsAndMarksOutboxInSingleTransaction(t *testing.T) {
	now := time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC)
	tx := &fakeTransaction{queryRows: []sqlRows{&fakeRows{values: [][]any{usageOutboxValues("reservation-a", Settled, now)}}}}
	repo := newSQLRepository(fakeDatabase{tx: tx}, func() time.Time { return now })
	projected, err := repo.DrainUsageOutbox(context.Background(), 8)
	if err != nil || projected != 1 || !tx.committed {
		t.Fatalf("projected=%d err=%v committed=%t", projected, err, tx.committed)
	}
	if len(tx.queryQueries) != 1 || !strings.Contains(tx.queryQueries[0], "FOR UPDATE SKIP LOCKED") {
		t.Fatalf("claim query=%q", tx.queryQueries)
	}
	if len(tx.execQueries) != 2 || !strings.Contains(tx.execQueries[0], "INSERT INTO usage_records") || !strings.Contains(tx.execQueries[0], "ON DUPLICATE KEY") || !strings.Contains(tx.execQueries[1], "SET projected_at") {
		t.Fatalf("projection queries=%q", tx.execQueries)
	}
}

func TestSQLRepositoryDrainRollbackCanRetryWithoutDuplicateProjection(t *testing.T) {
	now := time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC)
	rolledBack := &fakeTransaction{queryRows: []sqlRows{&fakeRows{values: [][]any{usageOutboxValues("reservation-a", Settled, now)}}}, commitErr: errors.New("commit unavailable")}
	retry := &fakeTransaction{queryRows: []sqlRows{&fakeRows{values: [][]any{usageOutboxValues("reservation-a", Settled, now)}}}}
	repo := newSQLRepository(&fakeSequenceDatabase{transactions: []*fakeTransaction{rolledBack, retry}}, func() time.Time { return now })
	if _, err := repo.DrainUsageOutbox(context.Background(), 1); err == nil || rolledBack.committed || !rolledBack.rolledBack {
		t.Fatalf("first drain err=%v committed=%t rolled_back=%t", err, rolledBack.committed, rolledBack.rolledBack)
	}
	projected, err := repo.DrainUsageOutbox(context.Background(), 1)
	if err != nil || projected != 1 || !retry.committed || len(retry.execQueries) != 2 {
		t.Fatalf("retry projected=%d err=%v committed=%t queries=%q", projected, err, retry.committed, retry.execQueries)
	}
}

func TestSQLRepositoryDrainConcurrentClaimProjectsOnce(t *testing.T) {
	now := time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC)
	claimed := &fakeTransaction{queryRows: []sqlRows{&fakeRows{values: [][]any{usageOutboxValues("reservation-a", Settled, now)}}}}
	skipped := &fakeTransaction{queryRows: []sqlRows{&fakeRows{}}}
	repo := newSQLRepository(&fakeSequenceDatabase{transactions: []*fakeTransaction{claimed, skipped}}, func() time.Time { return now })

	var group sync.WaitGroup
	results := make(chan int, 2)
	errors := make(chan error, 2)
	for range 2 {
		group.Add(1)
		go func() {
			defer group.Done()
			count, err := repo.DrainUsageOutbox(context.Background(), 1)
			if err != nil {
				errors <- err
				return
			}
			results <- count
		}()
	}
	group.Wait()
	close(results)
	close(errors)
	for err := range errors {
		t.Fatal(err)
	}
	projected := 0
	for count := range results {
		projected += count
	}
	if projected != 1 || len(claimed.execQueries) != 2 || len(skipped.execQueries) != 0 {
		t.Fatalf("projected=%d claimed=%q skipped=%q", projected, claimed.execQueries, skipped.execQueries)
	}
}

func TestSQLRepositoryDrainRecordFailureLeavesOutboxUnprojected(t *testing.T) {
	now := time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC)
	tx := &fakeTransaction{queryRows: []sqlRows{&fakeRows{values: [][]any{usageOutboxValues("reservation-a", Cancelled, now)}}}, execFailureAt: 1, execErr: errors.New("record unavailable")}
	repo := newSQLRepository(fakeDatabase{tx: tx}, func() time.Time { return now })
	if _, err := repo.DrainUsageOutbox(context.Background(), 1); err == nil || tx.committed || !tx.rolledBack || len(tx.execQueries) != 1 {
		t.Fatalf("err=%v committed=%t rolled_back=%t queries=%q", err, tx.committed, tx.rolledBack, tx.execQueries)
	}
}

func TestQuotaReservationMigrationDefinesEvidenceAndReconcileIndexes(t *testing.T) {
	path := filepath.Join("..", "..", "migrations", "001_quota_reservations.sql")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"CREATE TABLE quota_reservations", "CREATE TABLE provider_attempts", "uq_quota_reservations_tenant_request",
		"ix_quota_reservations_reconcile", "uq_provider_attempts_reservation_ordinal", "forwarded_runes", "heartbeat_at",
	} {
		if !strings.Contains(string(content), required) {
			t.Fatalf("migration missing %q", required)
		}
	}
}

func TestUsageLedgerMigrationDefinesOnlyNewLedgerTables(t *testing.T) {
	path := filepath.Join("..", "..", "migrations", "002_usage_ledger.sql")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"CREATE TABLE IF NOT EXISTS usage_outbox", "CREATE TABLE IF NOT EXISTS usage_records", "PRIMARY KEY (reservation_id)",
		"ix_usage_outbox_unprojected", "fk_usage_outbox_reservation", "fk_usage_records_outbox",
	} {
		if !strings.Contains(string(content), required) {
			t.Fatalf("migration missing %q", required)
		}
	}
	for _, prohibited := range []string{"ALTER TABLE", "DROP TABLE", "INSERT INTO quota_reservations", "INSERT INTO usage_outbox"} {
		if strings.Contains(string(content), prohibited) {
			t.Fatalf("migration must not contain %q", prohibited)
		}
	}
}

type fakeDatabase struct{ tx *fakeTransaction }

func (d fakeDatabase) BeginTx(context.Context, *sql.TxOptions) (sqlTransaction, error) {
	return d.tx, nil
}

type fakeSequenceDatabase struct {
	transactions []*fakeTransaction
	next         int
	mu           sync.Mutex
}

func (d *fakeSequenceDatabase) BeginTx(context.Context, *sql.TxOptions) (sqlTransaction, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.next >= len(d.transactions) {
		return nil, errors.New("unexpected transaction")
	}
	transaction := d.transactions[d.next]
	d.next++
	return transaction, nil
}

type fakeTransaction struct {
	results       []sqlResult
	rows          []sqlRow
	queryRows     []sqlRows
	execQueries   []string
	queryQueries  []string
	committed     bool
	rolledBack    bool
	execFailureAt int
	execErr       error
	commitErr     error
}

func (t *fakeTransaction) ExecContext(_ context.Context, query string, _ ...any) (sqlResult, error) {
	t.execQueries = append(t.execQueries, query)
	if t.execFailureAt == len(t.execQueries) {
		return nil, t.execErr
	}
	if len(t.results) == 0 {
		return fakeResult(1), nil
	}
	result := t.results[0]
	t.results = t.results[1:]
	return result, nil
}

func (t *fakeTransaction) QueryRowContext(_ context.Context, query string, _ ...any) sqlRow {
	t.queryQueries = append(t.queryQueries, query)
	if len(t.rows) == 0 {
		return fakeRow{err: sql.ErrNoRows}
	}
	row := t.rows[0]
	t.rows = t.rows[1:]
	return row
}

func (t *fakeTransaction) QueryContext(_ context.Context, query string, _ ...any) (sqlRows, error) {
	t.queryQueries = append(t.queryQueries, query)
	if len(t.queryRows) > 0 {
		rows := t.queryRows[0]
		t.queryRows = t.queryRows[1:]
		return rows, nil
	}
	return &fakeRows{}, nil
}

func (t *fakeTransaction) Commit() error {
	if t.commitErr != nil {
		return t.commitErr
	}
	t.committed = true
	return nil
}
func (t *fakeTransaction) Rollback() error {
	if !t.committed {
		t.rolledBack = true
	}
	return nil
}

type fakeRows struct {
	values [][]any
	next   int
}

func (r *fakeRows) Next() bool { return r.next < len(r.values) }
func (r *fakeRows) Scan(destinations ...any) error {
	if r.next >= len(r.values) {
		return errors.New("no row")
	}
	err := fakeRow{values: r.values[r.next]}.Scan(destinations...)
	r.next++
	return err
}
func (fakeRows) Err() error   { return nil }
func (fakeRows) Close() error { return nil }

type fakeResult int64

func (r fakeResult) RowsAffected() (int64, error) { return int64(r), nil }

type fakeRow struct {
	values []any
	err    error
}

func (r fakeRow) Scan(destinations ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(destinations) != len(r.values) {
		return errors.New("scan destination count mismatch")
	}
	for index, value := range r.values {
		switch destination := destinations[index].(type) {
		case *string:
			*destination = value.(string)
		case *uint64:
			*destination = value.(uint64)
		case *bool:
			*destination = value.(bool)
		case *sql.NullString:
			*destination = value.(sql.NullString)
		case *sql.NullTime:
			*destination = value.(sql.NullTime)
		case *time.Time:
			*destination = value.(time.Time)
		default:
			return errors.New("unsupported scan destination")
		}
	}
	return nil
}

func reservationRow(id string, state State, version uint64, now time.Time) fakeRow {
	return fakeRow{values: []any{
		id, "tenant-a", "request-a", "mock-model", string(state), version,
		uint64(64), uint64(0), uint64(0), false, sql.NullString{}, now, now, now,
	}}
}

func usageOutboxValues(reservationID string, state State, now time.Time) []any {
	return []any{
		reservationID, "tenant-a", "request-a", "mock-model", string(state), uint64(3),
		uint64(64), uint64(24), uint64(40), true, "provider_usage", now, sql.NullTime{}, now,
	}
}
