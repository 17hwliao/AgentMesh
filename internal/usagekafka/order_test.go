package usagekafka

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type advancingConsumer struct {
	messages []Message
	next     int
	commits  []int64
}

func (s *advancingConsumer) Receive(context.Context) (Message, error) {
	if s.next == len(s.messages) {
		return Message{}, context.Canceled
	}
	m := s.messages[s.next]
	s.next++
	return m, nil
}
func (s *advancingConsumer) Commit(_ context.Context, m Message) error {
	s.commits = append(s.commits, m.Offset)
	return nil
}

type orderedProjector struct {
	first string
	calls []string
}

func (p *orderedProjector) Project(_ context.Context, e Event) error {
	p.calls = append(p.calls, e.EventID)
	if e.EventID == p.first {
		return errors.New("transient projection failure")
	}
	return nil
}
func TestFetchAdvancesButWorkerRetriesBeforeNextOffset(t *testing.T) {
	first, second := event("first"), event("second")
	source := &advancingConsumer{messages: []Message{{Offset: 1, Value: mustMarshal(t, first)}, {Offset: 2, Value: mustMarshal(t, second)}}}
	p := &orderedProjector{first: "first"}
	dlq := NewMemoryBroker()
	w := &Worker{Source: source, Projector: p, DLQ: dlq, Config: Config{MaxAttempts: 3, RetryBase: time.Millisecond, RetryMax: time.Millisecond}}
	if !errors.Is(w.Run(context.Background()), context.Canceled) {
		t.Fatal("expected cancellation after drain")
	}
	if len(p.calls) != 4 || p.calls[0] != "first" || p.calls[1] != "first" || p.calls[2] != "first" || p.calls[3] != "second" {
		t.Fatalf("attempt order %v", p.calls)
	}
	if len(source.commits) != 2 || source.commits[0] != 1 || source.commits[1] != 2 {
		t.Fatalf("commits %v", source.commits)
	}
	if len(dlq.messages) != 1 {
		t.Fatal("failure was lost rather than dead-lettered")
	}
}
func TestPoisonQuarantinesDigestWithoutRawPayload(t *testing.T) {
	source := &advancingConsumer{messages: []Message{{Offset: 1, Value: []byte("api_key=private secret")}}}
	dlq := NewMemoryBroker()
	w := &Worker{Source: source, Projector: &orderedProjector{}, DLQ: dlq}
	_ = w.Run(context.Background())
	if len(source.commits) != 1 || len(dlq.messages) != 1 {
		t.Fatal("poison not quarantined")
	}
	if strings.Contains(string(dlq.messages[0].Value), "private") {
		t.Fatal("payload leaked")
	}
}
