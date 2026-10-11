package usagekafka

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	kafka "github.com/segmentio/kafka-go"
)

type transientProjector struct{ calls []string }

func (p *transientProjector) Project(_ context.Context, e Event) error {
	p.calls = append(p.calls, e.EventID)
	if len(p.calls) == 1 {
		return errors.New("temporary projection failure")
	}
	return nil
}

type stopAfterCommits struct {
	Consumer
	n    int
	stop context.CancelFunc
}

func (s *stopAfterCommits) Commit(ctx context.Context, m Message) error {
	if err := s.Consumer.Commit(ctx, m); err != nil {
		return err
	}
	s.n++
	if s.n == 2 {
		s.stop()
	}
	return nil
}

func TestRealKafkaFailedFirstOffsetIsRetriedBeforeSecondAndCommitted(t *testing.T) {
	broker := os.Getenv("AGENTMESH_TEST_KAFKA_BROKER")
	if broker == "" {
		t.Skip("set isolated test Kafka broker")
	}
	ctx, stop := context.WithTimeout(context.Background(), 40*time.Second)
	defer stop()
	topic := fmt.Sprintf("codex-order-%d", time.Now().UnixNano())
	conn, err := kafka.DialContext(ctx, "tcp", broker)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err = conn.CreateTopics(kafka.TopicConfig{Topic: topic, NumPartitions: 1, ReplicationFactor: 1}); err != nil {
		t.Fatal(err)
	}
	defer conn.DeleteTopics(topic)
	writer := &kafka.Writer{Addr: kafka.TCP(broker), RequiredAcks: kafka.RequireAll}
	defer writer.Close()
	for _, id := range []string{"first", "second"} {
		if err = (KafkaWriter{Writer: writer}).Publish(ctx, topic, id, mustMarshal(t, event(id))); err != nil {
			t.Fatal(err)
		}
	}
	group := topic + "-group"
	reader := kafka.NewReader(kafka.ReaderConfig{Brokers: []string{broker}, Topic: topic, GroupID: group, StartOffset: kafka.FirstOffset, MinBytes: 1, MaxBytes: 1 << 20, MaxWait: 100 * time.Millisecond})
	source := &stopAfterCommits{Consumer: KafkaReader{Reader: reader}, stop: stop}
	p := &transientProjector{}
	w := Worker{Source: source, Projector: p, Config: Config{RetryBase: time.Millisecond, RetryMax: time.Millisecond}}
	err = w.Run(ctx)
	reader.Close()
	if !errors.Is(err, context.Canceled) || source.n != 2 || fmt.Sprint(p.calls) != "[first first second]" {
		t.Fatalf("commits=%d, calls=%v, err=%v", source.n, p.calls, err)
	}
	// A fresh reader in the same group must not replay either committed event.
	restarted := kafka.NewReader(kafka.ReaderConfig{Brokers: []string{broker}, Topic: topic, GroupID: group, MinBytes: 1, MaxBytes: 1 << 20, MaxWait: 100 * time.Millisecond})
	defer restarted.Close()
	check, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err = restarted.FetchMessage(check); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("unexpected replay: %v", err)
	}
}
