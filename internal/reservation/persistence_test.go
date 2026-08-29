package reservation

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
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

type fakeDatabase struct{ tx *fakeTransaction }

func (d fakeDatabase) BeginTx(context.Context, *sql.TxOptions) (sqlTransaction, error) {
	return d.tx, nil
}

type fakeTransaction struct {
	results      []sqlResult
	rows         []sqlRow
	execQueries  []string
	queryQueries []string
	committed    bool
}

func (t *fakeTransaction) ExecContext(_ context.Context, query string, _ ...any) (sqlResult, error) {
	t.execQueries = append(t.execQueries, query)
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

func (t *fakeTransaction) QueryContext(context.Context, string, ...any) (sqlRows, error) {
	return fakeRows{}, nil
}

func (t *fakeTransaction) Commit() error   { t.committed = true; return nil }
func (t *fakeTransaction) Rollback() error { return nil }

type fakeRows struct{}

func (fakeRows) Next() bool        { return false }
func (fakeRows) Scan(...any) error { return errors.New("no row") }
func (fakeRows) Err() error        { return nil }
func (fakeRows) Close() error      { return nil }

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
