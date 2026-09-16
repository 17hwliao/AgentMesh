// Command usage-kafka-relay runs the durable usage outbox relay as an
// independently restartable process. It never owns quota authorization.
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
	"time"

	segmentkafka "github.com/segmentio/kafka-go"

	"agentmesh/internal/reservation"
	"agentmesh/internal/usagekafka"
)

const (
	mysqlDSNEnvironment = "AGENTMESH_USAGE_KAFKA_MYSQL_DSN"
	brokersEnvironment  = "AGENTMESH_USAGE_KAFKA_BROKERS"
	intervalEnvironment = "AGENTMESH_USAGE_KAFKA_RELAY_INTERVAL"
	batchEnvironment    = "AGENTMESH_USAGE_KAFKA_RELAY_BATCH_SIZE"
	ownerEnvironment    = "AGENTMESH_USAGE_KAFKA_RELAY_OWNER"
	leaseEnvironment    = "AGENTMESH_USAGE_KAFKA_RELAY_LEASE"
)

type config struct {
	MySQLDSN string
	Brokers  []string
	Interval time.Duration
	Batch    int
	Owner    string
	LeaseFor time.Duration
}

func main() {
	ctx, stop := signalContext()
	defer stop()
	if err := run(os.Getenv, ctx); err != nil {
		log.Printf("usage kafka relay stopped with error: %v", err)
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

	writer := segmentkafka.NewWriter(segmentkafka.WriterConfig{Brokers: cfg.Brokers, Balancer: &segmentkafka.Hash{}})
	defer writer.Close()
	relay := usagekafka.Relay{Store: &usagekafka.MySQLStore{DB: db}, Publisher: usagekafka.KafkaWriter{Writer: writer}, BatchSize: cfg.Batch, Owner: cfg.Owner, LeaseFor: cfg.LeaseFor}
	log.Printf("usage kafka relay started interval=%s batch_size=%d lease=%s", cfg.Interval, cfg.Batch, cfg.LeaseFor)
	return usagekafka.RunPolling(ctx, cfg.Interval, relay.RunOnce, func(result usagekafka.PollResult) {
		if result.Err != nil {
			log.Printf("usage kafka relay scan failed published=%d error=%v", result.Published, result.Err)
			return
		}
		if result.Published > 0 {
			log.Printf("usage kafka relay scan completed published=%d", result.Published)
		}
	})
}

func loadConfig(lookup func(string) string) (config, error) {
	if lookup == nil {
		return config{}, errors.New("usage kafka relay environment lookup is required")
	}
	dsn := strings.TrimSpace(lookup(mysqlDSNEnvironment))
	brokersRaw := strings.TrimSpace(lookup(brokersEnvironment))
	if dsn == "" || brokersRaw == "" {
		return config{}, errors.New("usage kafka relay requires AGENTMESH_USAGE_KAFKA_MYSQL_DSN and AGENTMESH_USAGE_KAFKA_BROKERS")
	}
	interval := time.Second
	if raw := strings.TrimSpace(lookup(intervalEnvironment)); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil || parsed <= 0 {
			return config{}, errors.New("AGENTMESH_USAGE_KAFKA_RELAY_INTERVAL must be a positive duration")
		}
		interval = parsed
	}
	batch := 16
	if raw := strings.TrimSpace(lookup(batchEnvironment)); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 {
			return config{}, errors.New("AGENTMESH_USAGE_KAFKA_RELAY_BATCH_SIZE must be a positive integer")
		}
		batch = parsed
	}
	leaseFor := 30 * time.Second
	if raw := strings.TrimSpace(lookup(leaseEnvironment)); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil || parsed <= 0 {
			return config{}, errors.New("AGENTMESH_USAGE_KAFKA_RELAY_LEASE must be a positive duration")
		}
		leaseFor = parsed
	}
	owner := strings.TrimSpace(lookup(ownerEnvironment))
	if owner == "" {
		host, err := os.Hostname()
		if err != nil || strings.TrimSpace(host) == "" {
			host = "unknown-host"
		}
		owner = fmt.Sprintf("%s-%d", host, os.Getpid())
	}
	if len(owner) > 128 {
		return config{}, errors.New("AGENTMESH_USAGE_KAFKA_RELAY_OWNER must be at most 128 characters")
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
	return config{MySQLDSN: dsn, Brokers: brokers, Interval: interval, Batch: batch, Owner: owner, LeaseFor: leaseFor}, nil
}

func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}
