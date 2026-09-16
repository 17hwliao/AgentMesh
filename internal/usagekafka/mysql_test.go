package usagekafka

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestMySQLPendingAndPublishFailureUseOutboxBackoff(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := &MySQLStore{DB: db}
	now := time.Date(2026, 9, 10, 1, 2, 3, 0, time.UTC)
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT event_id,reservation_id,topic,payload,attempts,available_at FROM usage_kafka_outbox WHERE published_at IS NULL AND available_at<=? AND (lease_until IS NULL OR lease_until<=?) ORDER BY created_at LIMIT ? FOR UPDATE SKIP LOCKED")).
		WithArgs(now, now, 5).WillReturnRows(sqlmock.NewRows([]string{"event_id", "reservation_id", "topic", "payload", "attempts", "available_at"}).AddRow("event-1", "res-1", Topic, []byte(`{"schema_version":1}`), 1, now))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE usage_kafka_outbox SET lease_owner=?,lease_until=? WHERE event_id=? AND published_at IS NULL")).
		WithArgs("relay-a", now.Add(30*time.Second), "event-1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	rows, err := store.Claim(context.Background(), 5, now, "relay-a", 30*time.Second)
	if err != nil || len(rows) != 1 || rows[0].EventID != "event-1" {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT attempts FROM usage_kafka_outbox WHERE event_id=? AND published_at IS NULL AND lease_owner=? AND lease_until>? FOR UPDATE")).
		WithArgs("event-1", "relay-a", now).WillReturnRows(sqlmock.NewRows([]string{"attempts"}).AddRow(1))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE usage_kafka_outbox SET attempts=attempts+1,last_error=?,available_at=?,lease_owner=NULL,lease_until=NULL WHERE event_id=? AND published_at IS NULL AND lease_owner=?")).
		WithArgs("broker unavailable", now.Add(2*time.Second), "event-1", "relay-a").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if err := store.MarkPublished(context.Background(), "event-1", "relay-a", now, errBrokerUnavailable{}); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestMySQLMarkPublishedRejectsStaleRelay(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := &MySQLStore{DB: db}
	now := time.Date(2026, 9, 10, 1, 2, 3, 0, time.UTC)
	mock.ExpectExec(regexp.QuoteMeta("UPDATE usage_kafka_outbox SET published_at=?,last_error='',lease_owner=NULL,lease_until=NULL WHERE event_id=? AND published_at IS NULL AND lease_owner=? AND lease_until>?")).
		WithArgs(now, "event-1", "stale-relay", now).WillReturnResult(sqlmock.NewResult(0, 0))
	if err := store.MarkPublished(context.Background(), "event-1", "stale-relay", now, nil); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("err=%v, want ErrLeaseLost", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestMySQLProjectIsIdempotentAndTransactional(t *testing.T) {
	e := event("event-1")
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := &MySQLStore{DB: db}
	projectQuery := regexp.QuoteMeta("SELECT event_id FROM usage_kafka_projections WHERE reservation_id=? FOR UPDATE")
	mock.ExpectBegin()
	mock.ExpectQuery(projectQuery).WithArgs(e.ReservationID).
		WillReturnRows(sqlmock.NewRows([]string{"event_id"}).AddRow(e.EventID))
	mock.ExpectCommit()
	if err := store.Project(context.Background(), e); err != nil {
		t.Fatal(err)
	}

	e2 := event("event-2")
	e2.ReservationID = "res-2"
	mock.ExpectBegin()
	mock.ExpectQuery(projectQuery).WithArgs(e2.ReservationID).WillReturnError(sql.ErrNoRows)
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO usage_kafka_projections")).
		WithArgs(e2.EventID, e2.ReservationID, e2.TenantID, e2.RequestID, e2.Model, e2.FinalState, e2.OperationVersion, e2.ReservedUnits, e2.SettledUnits, e2.ReleasedUnits, e2.UsageObserved, e2.SettlementKind, e2.FinalizedAt, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if err := store.Project(context.Background(), e2); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestMySQLProjectRejectsDifferentEventForSameReservation(t *testing.T) {
	e := event("event-1")
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := &MySQLStore{DB: db}
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT event_id FROM usage_kafka_projections WHERE reservation_id=? FOR UPDATE")).
		WithArgs(e.ReservationID).WillReturnRows(sqlmock.NewRows([]string{"event_id"}).AddRow("different-event"))
	mock.ExpectRollback()
	if err := store.Project(context.Background(), e); err != ErrConflict {
		t.Fatalf("err=%v, want ErrConflict", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestMySQLSummaryReturnsOnlyAggregates(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := &MySQLStore{DB: db}
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM usage_kafka_outbox WHERE published_at IS NULL")).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM usage_kafka_outbox WHERE published_at IS NOT NULL")).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(3))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM usage_kafka_projections")).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(4))
	summary, err := store.Summary(context.Background())
	if err != nil || summary.PendingOutbox != 2 || summary.PublishedOutbox != 3 || summary.Projections != 4 {
		t.Fatalf("summary=%+v err=%v", summary, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestUsageKafkaMigrationDefinesIdempotentKeysAndSafePayloadStorage(t *testing.T) {
	raw, err := os.ReadFile("../../migrations/004_usage_kafka.sql")
	if err != nil {
		t.Fatal(err)
	}
	sqlText := strings.ToLower(string(raw))
	for _, required := range []string{
		"create table if not exists usage_kafka_outbox",
		"unique key uq_usage_kafka_outbox_reservation",
		"create table if not exists usage_kafka_projections",
		"unique key uq_usage_kafka_projection_reservation",
		"payload json not null",
	} {
		if !strings.Contains(sqlText, required) {
			t.Fatalf("migration missing %q", required)
		}
	}
	for _, prohibitedColumn := range []string{"prompt ", "response ", "api_key ", "token_text "} {
		if strings.Contains(sqlText, prohibitedColumn) {
			t.Fatalf("migration contains prohibited column %q", prohibitedColumn)
		}
	}
}

func TestUsageKafkaLeaseMigrationDefinesClaimColumns(t *testing.T) {
	raw, err := os.ReadFile("../../migrations/005_usage_kafka_outbox_leases.sql")
	if err != nil {
		t.Fatal(err)
	}
	sqlText := strings.ToLower(string(raw))
	for _, required := range []string{"add column lease_owner", "add column lease_until", "ix_usage_kafka_outbox_claim"} {
		if !strings.Contains(sqlText, required) {
			t.Fatalf("migration missing %q", required)
		}
	}
}

type errBrokerUnavailable struct{}

func (errBrokerUnavailable) Error() string { return "broker unavailable" }
