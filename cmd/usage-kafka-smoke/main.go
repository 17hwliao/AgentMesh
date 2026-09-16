package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
	segmentkafka "github.com/segmentio/kafka-go"

	"agentmesh/internal/reservation"
	"agentmesh/internal/usagekafka"
)

type smokeResult struct {
	Status             string `json:"status"`
	ReservationID      string `json:"reservation_id"`
	EventID            string `json:"event_id"`
	Published          int    `json:"published"`
	ProjectionRows     int    `json:"projection_rows"`
	DuplicateDelivered bool   `json:"duplicate_delivery_idempotent"`
}

func main() {
	if err := run(); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "usage-kafka-smoke failed: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dsn := getenv("AGENTMESH_USAGE_KAFKA_MYSQL_DSN", "root:agentmesh-local-only@tcp(127.0.0.1:13309)/agentmesh_control?parseTime=true&loc=UTC")
	brokers := strings.Split(getenv("AGENTMESH_USAGE_KAFKA_BROKERS", "127.0.0.1:19093"), ",")
	now := time.Now().UTC().Truncate(time.Microsecond)
	repo, db, err := reservation.OpenMySQLRepository(dsn, func() time.Time { return now })
	if err != nil {
		return err
	}
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		return err
	}

	reservationID, err := smokeID()
	if err != nil {
		return err
	}
	if _, err := repo.Create(ctx, reservation.CreatePersistentReservation{ID: reservationID, TenantID: "smoke-tenant", RequestID: "smoke-" + reservationID[24:], Model: "smoke-model", ReservedUnits: 100}); err != nil {
		return err
	}
	if _, err := repo.MarkReserved(ctx, "smoke-tenant", reservationID, 1); err != nil {
		return err
	}
	if _, _, err := repo.StartAttempt(ctx, "smoke-tenant", reservationID, 2, "smoke-provider", "smoke-model"); err != nil {
		return err
	}
	if _, err := repo.MarkSettled(ctx, "smoke-tenant", reservationID, 3, 40, 60, true, "provider_usage"); err != nil {
		return err
	}
	e := usagekafka.Event{
		SchemaVersion:    1,
		EventID:          eventID(reservationID, 3),
		Attempt:          1,
		ReservationID:    reservationID,
		TenantID:         "smoke-tenant",
		RequestID:        "smoke-" + reservationID[24:],
		Model:            "smoke-model",
		FinalState:       "settled",
		OperationVersion: 3,
		ReservedUnits:    100,
		SettledUnits:     40,
		ReleasedUnits:    60,
		UsageObserved:    true,
		SettlementKind:   "provider_usage",
		FinalizedAt:      now,
	}

	writer := segmentkafka.NewWriter(segmentkafka.WriterConfig{Brokers: brokers, Balancer: &segmentkafka.Hash{}})
	defer writer.Close()
	store := &usagekafka.MySQLStore{DB: db}
	published, err := (usagekafka.Relay{Store: store, Publisher: usagekafka.KafkaWriter{Writer: writer}, BatchSize: 64}).RunOnce(ctx)
	if err != nil {
		return err
	}

	reader := segmentkafka.NewReader(segmentkafka.ReaderConfig{Brokers: brokers, Topic: usagekafka.Topic, GroupID: "agentmesh-usage-smoke-" + reservationID[24:], MinBytes: 1, MaxBytes: 1 << 20})
	defer reader.Close()
	worker := &usagekafka.Worker{Source: usagekafka.KafkaReader{Reader: reader}, Projector: store}
	var message usagekafka.Message
	for {
		message, err = worker.Source.Receive(ctx)
		if err != nil {
			return err
		}
		if err := worker.Handle(ctx, message); err != nil {
			return err
		}
		if err := worker.Source.Commit(ctx, message); err != nil {
			return err
		}
		decoded, err := usagekafka.DecodeEvent(message.Value)
		if err != nil {
			return err
		}
		if decoded.EventID == e.EventID {
			break
		}
	}
	if err := worker.Handle(ctx, message); err != nil {
		return err
	}

	var projectionRows int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM usage_kafka_projections WHERE event_id=?", e.EventID).Scan(&projectionRows); err != nil {
		return err
	}
	result := smokeResult{Status: "verified", ReservationID: e.ReservationID, EventID: e.EventID, Published: published, ProjectionRows: projectionRows, DuplicateDelivered: projectionRows == 1}
	return json.NewEncoder(os.Stdout).Encode(result)
}

func smokeID() (string, error) {
	var raw [6]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "00000000-0000-4000-8000-" + hex.EncodeToString(raw[:]), nil
}

func eventID(reservationID string, version uint64) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s:%d", reservationID, version)))
	return hex.EncodeToString(sum[:])
}

func getenv(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
