// Package observability records safe, bounded in-process stream summaries.
package observability

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"

	"agentmesh/internal/provider"
	"agentmesh/internal/router"
)

const DefaultCapacity = 128

type Attempt struct {
	Provider string `json:"provider"`
	Outcome  string `json:"outcome"`
}

// Trace is the tenant-safe query shape. Tenant ownership remains private to
// Recorder and is never serialized.
type Trace struct {
	TraceID             string          `json:"trace_id"`
	Model               string          `json:"model"`
	Attempts            []Attempt       `json:"attempts"`
	FirstChunkLatencyMS *int64          `json:"first_chunk_latency_ms,omitempty"`
	TotalLatencyMS      int64           `json:"total_latency_ms"`
	ResultCode          string          `json:"result_code"`
	Cancelled           bool            `json:"cancelled"`
	UsageObserved       bool            `json:"usage_observed"`
	ProviderUsage       *provider.Usage `json:"provider_usage,omitempty"`
}

type entry struct {
	tenantID string
	started  time.Time
	trace    Trace
	complete bool
}

type Recorder struct {
	mu       sync.Mutex
	capacity int
	now      func() time.Time
	nextID   func() string
	entries  map[string]*entry
	order    []string
}

// NewRecorder accepts deterministic clock and ID generators for tests. Nil
// functions select the production clock and crypto-random identifier source.
func NewRecorder(capacity int, now func() time.Time, nextID func() string) *Recorder {
	if capacity <= 0 {
		capacity = DefaultCapacity
	}
	if now == nil {
		now = time.Now
	}
	if nextID == nil {
		nextID = randomID
	}
	return &Recorder{capacity: capacity, now: now, nextID: nextID, entries: map[string]*entry{}}
}

// Session is a request-local observer. A nil recorder session is an intentional
// degradation when capacity contains only pending records.
type Session struct {
	recorder *Recorder
	traceID  string
}

func (r *Recorder) Start(tenantID, model string) *Session {
	id := r.nextID()
	if id == "" {
		return &Session{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.entries) >= r.capacity && !r.evictOldestCompleted() {
		return &Session{traceID: id}
	}
	if _, exists := r.entries[id]; exists {
		return &Session{}
	}
	r.entries[id] = &entry{tenantID: tenantID, started: r.now(), trace: Trace{TraceID: id, Model: model, Attempts: []Attempt{}}}
	r.order = append(r.order, id)
	return &Session{recorder: r, traceID: id}
}

func (s *Session) TraceID() string { return s.traceID }

// RouterObserverNonBlocking certifies that Observe is lock-only and cannot
// block routing; Router still recovers an unexpected panic defensively.
func (s *Session) RouterObserverNonBlocking() {}

func (s *Session) Observe(event router.AttemptEvent) {
	if s.recorder == nil {
		return
	}
	s.recorder.mu.Lock()
	defer s.recorder.mu.Unlock()
	entry := s.recorder.entries[s.traceID]
	if entry == nil {
		return
	}
	switch event.Kind {
	case router.AttemptStarted:
		entry.trace.Attempts = append(entry.trace.Attempts, Attempt{Provider: event.Provider})
	case router.AttemptFirstChunk:
		if entry.trace.FirstChunkLatencyMS == nil {
			elapsed := s.recorder.now().Sub(entry.started).Milliseconds()
			entry.trace.FirstChunkLatencyMS = &elapsed
		}
		if event.Usage != nil {
			usage := *event.Usage
			entry.trace.ProviderUsage = &usage
			entry.trace.UsageObserved = true
		}
	case router.AttemptFinished:
		for i := len(entry.trace.Attempts) - 1; i >= 0; i-- {
			if entry.trace.Attempts[i].Provider == event.Provider && entry.trace.Attempts[i].Outcome == "" {
				entry.trace.Attempts[i].Outcome = event.Outcome
				break
			}
		}
	}
}

func (s *Session) Complete(resultCode string, cancelled bool) {
	if s.recorder == nil {
		return
	}
	s.recorder.mu.Lock()
	defer s.recorder.mu.Unlock()
	entry := s.recorder.entries[s.traceID]
	if entry == nil || entry.complete {
		return
	}
	entry.trace.TotalLatencyMS = s.recorder.now().Sub(entry.started).Milliseconds()
	entry.trace.ResultCode = resultCode
	entry.trace.Cancelled = cancelled
	entry.complete = true
}

// Get returns a defensive copy only for a completed trace owned by tenantID.
func (r *Recorder) Get(tenantID, traceID string) (Trace, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry := r.entries[traceID]
	if entry == nil || !entry.complete || entry.tenantID != tenantID {
		return Trace{}, false
	}
	return cloneTrace(entry.trace), true
}

func (r *Recorder) evictOldestCompleted() bool {
	for index, id := range r.order {
		if entry := r.entries[id]; entry != nil && entry.complete {
			delete(r.entries, id)
			r.order = append(r.order[:index], r.order[index+1:]...)
			return true
		}
	}
	return false
}

func cloneTrace(trace Trace) Trace {
	trace.Attempts = append([]Attempt(nil), trace.Attempts...)
	if trace.FirstChunkLatencyMS != nil {
		value := *trace.FirstChunkLatencyMS
		trace.FirstChunkLatencyMS = &value
	}
	if trace.ProviderUsage != nil {
		value := *trace.ProviderUsage
		trace.ProviderUsage = &value
	}
	return trace
}

func randomID() string {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return ""
	}
	return hex.EncodeToString(bytes)
}
