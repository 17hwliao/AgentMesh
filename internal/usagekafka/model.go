// Package usagekafka projects the existing safe usage ledger snapshot through
// Kafka. It never accepts prompt, response, API key, endpoint, or raw token text.
package usagekafka

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"
)

const (
	Topic    = "agentmesh.usage.v1"
	DLQTopic = "agentmesh.usage.v1.dlq"
)

var ErrConflict = errors.New("usage kafka projection conflict")

type Event struct {
	SchemaVersion    int       `json:"schema_version"`
	EventID          string    `json:"event_id"`
	Attempt          int       `json:"attempt"`
	ReservationID    string    `json:"reservation_id"`
	TenantID         string    `json:"tenant_id"`
	RequestID        string    `json:"request_id"`
	Model            string    `json:"model"`
	FinalState       string    `json:"final_state"`
	OperationVersion uint64    `json:"operation_version"`
	ReservedUnits    uint64    `json:"reserved_units"`
	SettledUnits     uint64    `json:"settled_units"`
	ReleasedUnits    uint64    `json:"released_units"`
	UsageObserved    bool      `json:"usage_observed"`
	SettlementKind   string    `json:"settlement_kind"`
	FinalizedAt      time.Time `json:"finalized_at"`
}

func (e Event) Validate() error {
	if e.SchemaVersion != 1 || e.EventID == "" || e.Attempt < 1 || e.ReservationID == "" || e.TenantID == "" || e.RequestID == "" || e.Model == "" || e.OperationVersion == 0 || e.FinalizedAt.IsZero() {
		return errors.New("invalid usage event")
	}
	if e.FinalState != "settled" && e.FinalState != "cancelled" {
		return errors.New("invalid usage event final state")
	}
	return nil
}

func (e Event) MarshalSafe() ([]byte, error) {
	if err := e.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(e)
}

func DecodeEvent(data []byte) (Event, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var event Event
	if err := dec.Decode(&event); err != nil {
		return Event{}, err
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return Event{}, errors.New("trailing usage event")
	}
	return event, event.Validate()
}

type OutboxRecord struct {
	EventID       string
	ReservationID string
	Topic         string
	Payload       []byte
	Attempts      int
	AvailableAt   time.Time
	LeaseOwner    string
	LeaseUntil    time.Time
}
type Message struct {
	Topic     string
	Key       string
	Value     []byte
	Partition int
	Offset    int64
}

type OutboxStore interface {
	Claim(context.Context, int, time.Time, string, time.Duration) ([]OutboxRecord, error)
	MarkPublished(context.Context, string, string, time.Time, error) error
}
type Publisher interface {
	Publish(context.Context, string, string, []byte) error
}
type Consumer interface {
	Receive(context.Context) (Message, error)
	Commit(context.Context, Message) error
}
type ProjectionStore interface {
	Project(context.Context, Event) error
}

type Config struct {
	MaxAttempts int
	RetryBase   time.Duration
	RetryMax    time.Duration
	Owner       string
}

// ErrLeaseLost means another relay is now allowed to publish the event. The
// caller must not overwrite a newer relay's retry or publish result.
var ErrLeaseLost = errors.New("usage kafka outbox lease lost")

func (c Config) defaults() Config {
	if c.MaxAttempts < 1 {
		c.MaxAttempts = 3
	}
	if c.RetryBase <= 0 {
		c.RetryBase = time.Second
	}
	if c.RetryMax <= 0 {
		c.RetryMax = time.Minute
	}
	if c.Owner == "" {
		c.Owner = "usage-worker"
	}
	return c
}
func RetryDelay(base, max time.Duration, attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	d := base
	for i := 1; i < attempt && d < max; i++ {
		if d > max/2 {
			return max
		}
		d *= 2
	}
	if d > max {
		return max
	}
	return d
}
func digest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func truncate(err error) string {
	s := err.Error()
	if len(s) > 256 {
		return s[:256]
	}
	return s
}

func safeDLQReason(err error) string {
	if errors.Is(err, ErrConflict) {
		return "projection conflict"
	}
	return "projection failed after retry budget"
}

type DeadLetterEvent struct {
	SchemaVersion int    `json:"schema_version"`
	Event         Event  `json:"event"`
	Reason        string `json:"reason"`
}

func (d DeadLetterEvent) MarshalSafe() ([]byte, error) {
	if err := d.Event.Validate(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(d.Reason) == "" {
		return nil, errors.New("dead-letter reason is required")
	}
	return json.Marshal(d)
}
