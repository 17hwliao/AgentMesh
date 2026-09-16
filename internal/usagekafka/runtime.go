package usagekafka

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	segmentkafka "github.com/segmentio/kafka-go"
)

type Relay struct {
	Store     OutboxStore
	Publisher Publisher
	BatchSize int
	Owner     string
	LeaseFor  time.Duration
	Now       func() time.Time
}

func (r Relay) RunOnce(ctx context.Context) (int, error) {
	if r.Store == nil || r.Publisher == nil {
		return 0, errors.New("relay store and publisher are required")
	}
	if r.BatchSize < 1 {
		r.BatchSize = 16
	}
	if r.Now == nil {
		r.Now = time.Now
	}
	if r.Owner == "" {
		r.Owner = "usage-relay"
	}
	if r.LeaseFor <= 0 {
		r.LeaseFor = 30 * time.Second
	}
	now := r.Now().UTC()
	rows, err := r.Store.Claim(ctx, r.BatchSize, now, r.Owner, r.LeaseFor)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, row := range rows {
		pubErr := r.Publisher.Publish(ctx, row.Topic, row.EventID, row.Payload)
		if err := r.Store.MarkPublished(ctx, row.EventID, r.Owner, r.Now().UTC(), pubErr); err != nil {
			return count, err
		}
		if pubErr != nil {
			return count, fmt.Errorf("publish event %s: %w", row.EventID, pubErr)
		}
		count++
	}
	return count, nil
}

type Worker struct {
	Source    Consumer
	Projector ProjectionStore
	DLQ       Publisher
	Config    Config
	Now       func() time.Time
	mu        sync.Mutex
	failures  map[string]int
}

func (w *Worker) Handle(ctx context.Context, message Message) error {
	if w.Projector == nil {
		return errors.New("projection store is required")
	}
	event, err := DecodeEvent(message.Value)
	if err != nil {
		return err
	}
	return w.Projector.Project(ctx, event)
}
func (w *Worker) Run(ctx context.Context) error {
	if w.Source == nil || w.Projector == nil {
		return errors.New("worker source and projector are required")
	}
	cfg := w.Config.defaults()
	if w.Now == nil {
		w.Now = time.Now
	}
	if w.failures == nil {
		w.failures = map[string]int{}
	}
	for {
		msg, err := w.Source.Receive(ctx)
		if err != nil {
			return err
		}
		err = w.Handle(ctx, msg)
		if err == nil {
			if err = w.Source.Commit(ctx, msg); err != nil {
				return err
			}
			continue
		}
		event, decodeErr := DecodeEvent(msg.Value)
		if decodeErr != nil {
			return decodeErr
		}
		attempt := w.failureCount(event.EventID)
		if attempt >= cfg.MaxAttempts {
			if w.DLQ == nil {
				return fmt.Errorf("dlq publisher unavailable: %w", err)
			}
			dlq, marshalErr := (DeadLetterEvent{SchemaVersion: 1, Event: event, Reason: safeDLQReason(err)}).MarshalSafe()
			if marshalErr != nil {
				return marshalErr
			}
			if pubErr := w.DLQ.Publish(ctx, DLQTopic, event.EventID, dlq); pubErr != nil {
				return pubErr
			}
			if err = w.Source.Commit(ctx, msg); err != nil {
				return err
			}
			continue
		}
		select {
		case <-time.After(RetryDelay(cfg.RetryBase, cfg.RetryMax, attempt)):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
func (w *Worker) failureCount(eventID string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.failures[eventID]++
	return w.failures[eventID]
}

type KafkaWriter struct{ Writer *segmentkafka.Writer }

func (p KafkaWriter) Publish(ctx context.Context, topic, key string, value []byte) error {
	if p.Writer == nil {
		return errors.New("kafka writer is required")
	}
	return p.Writer.WriteMessages(ctx, segmentkafka.Message{Topic: topic, Key: []byte(key), Value: append([]byte(nil), value...)})
}

type KafkaReader struct{ Reader *segmentkafka.Reader }

func (c KafkaReader) Receive(ctx context.Context) (Message, error) {
	if c.Reader == nil {
		return Message{}, errors.New("kafka reader is required")
	}
	m, err := c.Reader.FetchMessage(ctx)
	if err != nil {
		return Message{}, err
	}
	return Message{Topic: m.Topic, Key: string(m.Key), Value: append([]byte(nil), m.Value...), Partition: m.Partition, Offset: m.Offset}, nil
}
func (c KafkaReader) Commit(ctx context.Context, m Message) error {
	if c.Reader == nil {
		return errors.New("kafka reader is required")
	}
	return c.Reader.CommitMessages(ctx, segmentkafka.Message{Topic: m.Topic, Partition: m.Partition, Offset: m.Offset})
}

type MemoryBroker struct {
	mu       sync.Mutex
	messages []Message
	next     int
}

func NewMemoryBroker() *MemoryBroker { return &MemoryBroker{} }
func (b *MemoryBroker) Publish(_ context.Context, topic, key string, value []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.messages = append(b.messages, Message{Topic: topic, Key: key, Value: append([]byte(nil), value...)})
	return nil
}
func (b *MemoryBroker) Receive(ctx context.Context) (Message, error) {
	for {
		b.mu.Lock()
		if b.next < len(b.messages) {
			m := b.messages[b.next]
			b.mu.Unlock()
			return m, nil
		}
		b.mu.Unlock()
		select {
		case <-ctx.Done():
			return Message{}, ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
	}
}
func (b *MemoryBroker) Commit(_ context.Context, _ Message) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.next < len(b.messages) {
		b.next++
	}
	return nil
}
