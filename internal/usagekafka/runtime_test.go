package usagekafka

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func event(id string) Event {
	return Event{SchemaVersion: 1, EventID: id, Attempt: 1, ReservationID: "res-1", TenantID: "tenant-1", RequestID: "request-1", Model: "mock", FinalState: "settled", OperationVersion: 2, ReservedUnits: 100, SettledUnits: 40, ReleasedUnits: 60, UsageObserved: true, SettlementKind: "provider_usage", FinalizedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func TestEventIsStrictAndDoesNotCarrySensitiveFields(t *testing.T) {
	e := event("event-1")
	raw, err := e.MarshalSafe()
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"prompt", "response", "api_key", "token_text"} {
		if strings.Contains(string(raw), bad) {
			t.Fatalf("payload leaks %q: %s", bad, raw)
		}
	}
	if _, err := DecodeEvent(append(raw, []byte(`{"prompt":"leak"}`)...)); err == nil {
		t.Fatal("unknown sensitive field accepted")
	}
	if _, err := DecodeEvent([]byte(string(raw) + string(raw))); err == nil {
		t.Fatal("trailing event accepted")
	}
}

func TestRelayAndProjectionAreIdempotent(t *testing.T) {
	store := NewMemoryStore()
	e := event("event-1")
	if err := store.Add(e, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := store.Add(e, time.Now()); err != nil {
		t.Fatalf("duplicate outbox insert: %v", err)
	}
	broker := NewMemoryBroker()
	n, err := (Relay{Store: store, Publisher: broker, BatchSize: 1}).RunOnce(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("relay n=%d err=%v", n, err)
	}
	msg, err := broker.Receive(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := (&Worker{Projector: store}).Handle(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	if err := (&Worker{Projector: store}).Handle(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	if err := broker.Commit(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	if n, err := (Relay{Store: store, Publisher: broker, BatchSize: 1}).RunOnce(context.Background()); err != nil || n != 0 {
		t.Fatalf("published outbox twice n=%d err=%v", n, err)
	}
}

func TestRelayPublishFailureUsesBackoffThenPublishes(t *testing.T) {
	store := NewMemoryStore()
	e := event("event-retry")
	now := time.Date(2026, 9, 10, 1, 2, 3, 0, time.UTC)
	if err := store.Add(e, now); err != nil {
		t.Fatal(err)
	}
	publisher := &failOncePublisher{}
	relay := Relay{Store: store, Publisher: publisher, BatchSize: 1, Now: func() time.Time { return now }}
	if _, err := relay.RunOnce(context.Background()); err == nil {
		t.Fatal("first publish should fail")
	}
	if n, err := relay.RunOnce(context.Background()); err != nil || n != 0 {
		t.Fatalf("event should remain delayed n=%d err=%v", n, err)
	}
	relay.Now = func() time.Time { return now.Add(2 * time.Second) }
	if n, err := relay.RunOnce(context.Background()); err != nil || n != 1 {
		t.Fatalf("event should publish after backoff n=%d err=%v", n, err)
	}
	if publisher.calls != 2 {
		t.Fatalf("publish calls=%d, want 2", publisher.calls)
	}
}

func TestRelayLeasePreventsConcurrentClaimAndRecoversAfterExpiry(t *testing.T) {
	store := NewMemoryStore()
	now := time.Date(2026, 9, 10, 1, 2, 3, 0, time.UTC)
	e := event("event-lease")
	if err := store.Add(e, now); err != nil {
		t.Fatal(err)
	}
	claimedA, err := store.Claim(context.Background(), 1, now, "relay-a", time.Second)
	if err != nil || len(claimedA) != 1 {
		t.Fatalf("first claim=%+v err=%v", claimedA, err)
	}
	claimedB, err := store.Claim(context.Background(), 1, now, "relay-b", time.Second)
	if err != nil || len(claimedB) != 0 {
		t.Fatalf("concurrent claim=%+v err=%v", claimedB, err)
	}
	claimedB, err = store.Claim(context.Background(), 1, now.Add(2*time.Second), "relay-b", time.Second)
	if err != nil || len(claimedB) != 1 {
		t.Fatalf("expired claim=%+v err=%v", claimedB, err)
	}
	if err := store.MarkPublished(context.Background(), e.EventID, "relay-a", now.Add(2*time.Second), nil); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("stale relay result=%v, want ErrLeaseLost", err)
	}
	if err := store.MarkPublished(context.Background(), e.EventID, "relay-b", now.Add(2*time.Second), nil); err != nil {
		t.Fatalf("active relay publish: %v", err)
	}
}

func TestWorkerRetriesWithBackoffAndPublishesDLQ(t *testing.T) {
	store := NewMemoryStore()
	e := event("event-2")
	if err := store.Add(e, time.Now()); err != nil {
		t.Fatal(err)
	}
	broker := NewMemoryBroker()
	msg := Message{Topic: Topic, Key: e.EventID, Value: mustMarshal(t, e)}
	_ = msg
	dlq := NewMemoryBroker()
	worker := &Worker{Source: broker, Projector: failProjection{}, DLQ: dlq, Config: Config{MaxAttempts: 2, RetryBase: time.Millisecond, RetryMax: time.Millisecond}}
	if err := broker.Publish(context.Background(), Topic, e.EventID, msg.Value); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := worker.Run(ctx); err == nil {
		t.Fatal("worker should stop on context")
	}
	dlqMsg, err := dlq.Receive(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if dlqMsg.Topic != DLQTopic {
		t.Fatalf("dlq topic=%q", dlqMsg.Topic)
	}
	var dead DeadLetterEvent
	deadRaw := dlqMsg.Value
	if strings.Contains(string(deadRaw), "prompt") || strings.Contains(string(deadRaw), "response") || strings.Contains(string(deadRaw), "api_key") || strings.Contains(string(deadRaw), "token_text") {
		t.Fatalf("dlq payload leaks sensitive field: %s", deadRaw)
	}
	if err := json.Unmarshal(deadRaw, &dead); err != nil || dead.Event.EventID != e.EventID || dead.Reason == "" {
		t.Fatalf("dead letter=%+v err=%v", dead, err)
	}
}

type failProjection struct{}

type failOncePublisher struct {
	calls int
}

func (p *failOncePublisher) Publish(context.Context, string, string, []byte) error {
	p.calls++
	if p.calls == 1 {
		return errors.New("broker unavailable")
	}
	return nil
}

func (failProjection) Project(context.Context, Event) error {
	return errors.New("projection unavailable prompt=secret response=secret api_key=secret token_text=secret")
}
func mustMarshal(t *testing.T, e Event) []byte {
	t.Helper()
	b, err := e.MarshalSafe()
	if err != nil {
		t.Fatal(err)
	}
	return b
}
