// Command usage-kafka-worker runs the idempotent usage projection consumer as
// an independently restartable process.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	segmentkafka "github.com/segmentio/kafka-go"

	"agentmesh/internal/reservation"
	"agentmesh/internal/usagekafka"
)

const (
	mysqlDSNEnvironment    = "AGENTMESH_USAGE_KAFKA_MYSQL_DSN"
	brokersEnvironment     = "AGENTMESH_USAGE_KAFKA_BROKERS"
	groupEnvironment       = "AGENTMESH_USAGE_KAFKA_WORKER_GROUP"
	maxAttemptsEnvironment = "AGENTMESH_USAGE_KAFKA_WORKER_MAX_ATTEMPTS"
)

type config struct {
	MySQLDSN    string
	Brokers     []string
	GroupID     string
	MaxAttempts int
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(os.Getenv, ctx); err != nil {
		log.Printf("usage kafka worker stopped with error: %v", err)
		os.Exit(1)
	}
}

func run(lookup func(string) string, ctx context.Context) error {
	cfg, err := loadConfig(lookup)
	if err != nil {
		return err
	}
	_, db, err := reservation.OpenMySQLRepository(cfg.MySQLDSN, nil)
	if err != nil {
		return fmt.Errorf("open usage kafka mysql repository: %w", err)
	}
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("ping usage kafka mysql: %w", err)
	}

	reader := segmentkafka.NewReader(segmentkafka.ReaderConfig{
		Brokers:  cfg.Brokers,
		Topic:    usagekafka.Topic,
		GroupID:  cfg.GroupID,
		MinBytes: 1,
		MaxBytes: 1 << 20,
	})
	defer reader.Close()
	dlqWriter := segmentkafka.NewWriter(segmentkafka.WriterConfig{Brokers: cfg.Brokers, Balancer: &segmentkafka.Hash{}})
	defer dlqWriter.Close()

	worker := &usagekafka.Worker{
		Source:    usagekafka.KafkaReader{Reader: reader},
		Projector: &usagekafka.MySQLStore{DB: db},
		DLQ:       usagekafka.KafkaWriter{Writer: dlqWriter},
		Config:    usagekafka.Config{MaxAttempts: cfg.MaxAttempts},
	}
	log.Printf("usage kafka worker started group_id=%s max_attempts=%d", cfg.GroupID, cfg.MaxAttempts)
	err = worker.Run(ctx)
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
		return nil
	}
	return err
}

func loadConfig(lookup func(string) string) (config, error) {
	if lookup == nil {
		return config{}, errors.New("usage kafka worker environment lookup is required")
	}
	dsn := strings.TrimSpace(lookup(mysqlDSNEnvironment))
	brokersRaw := strings.TrimSpace(lookup(brokersEnvironment))
	if dsn == "" || brokersRaw == "" {
		return config{}, errors.New("usage kafka worker requires AGENTMESH_USAGE_KAFKA_MYSQL_DSN and AGENTMESH_USAGE_KAFKA_BROKERS")
	}
	groupID := strings.TrimSpace(lookup(groupEnvironment))
	if groupID == "" {
		groupID = "agentmesh-usage-projection-v1"
	}
	maxAttempts := 3
	if raw := strings.TrimSpace(lookup(maxAttemptsEnvironment)); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 {
			return config{}, errors.New("AGENTMESH_USAGE_KAFKA_WORKER_MAX_ATTEMPTS must be a positive integer")
		}
		maxAttempts = parsed
	}
	brokers := make([]string, 0)
	for _, broker := range strings.Split(brokersRaw, ",") {
		if broker = strings.TrimSpace(broker); broker != "" {
			brokers = append(brokers, broker)
		}
	}
	if len(brokers) == 0 {
		return config{}, errors.New("AGENTMESH_USAGE_KAFKA_BROKERS must contain at least one broker")
	}
	return config{MySQLDSN: dsn, Brokers: brokers, GroupID: groupID, MaxAttempts: maxAttempts}, nil
}
