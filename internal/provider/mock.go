package provider

import (
	"context"
	"errors"
	"sync"
	"time"
)

var errConfiguredFailure = errors.New("mock provider configured failure")

// MockConfig injects deterministic stream behavior for local demonstrations
// and offline tests. FailBeforeFirst takes precedence over chunks.
type MockConfig struct {
	Name            string
	Chunks          []string
	Delay           time.Duration
	FailBeforeFirst bool
	FailAfterChunks int // -1 means never fail; 0 means fail before emitting.
	Started         chan<- struct{}
	Cancelled       chan<- struct{}
}

// Mock is a synchronous, context-aware Provider. It deliberately launches no
// goroutines, which makes cancellation and leak tests deterministic.
type Mock struct {
	config MockConfig

	mu            sync.Mutex
	calls         int
	startedOnce   sync.Once
	cancelledOnce sync.Once
}

func NewMock(config MockConfig) *Mock {
	if config.Name == "" {
		config.Name = "mock"
	}
	if config.FailAfterChunks == 0 && !config.FailBeforeFirst {
		config.FailAfterChunks = -1
	}
	return &Mock{config: config}
}

func (m *Mock) Name() string { return m.config.Name }

func (m *Mock) Health(ctx context.Context) error { return ctx.Err() }

func (m *Mock) Calls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

func (m *Mock) Stream(ctx context.Context, _ ChatRequest, emit Emit) error {
	m.mu.Lock()
	m.calls++
	m.mu.Unlock()
	m.notifyStarted()

	if err := m.wait(ctx); err != nil {
		m.notifyCancelled()
		return err
	}
	if m.config.FailBeforeFirst || m.config.FailAfterChunks == 0 {
		return errConfiguredFailure
	}

	for index, value := range m.config.Chunks {
		if err := m.wait(ctx); err != nil {
			m.notifyCancelled()
			return err
		}
		if err := emit(Chunk{Delta: value}); err != nil {
			return err
		}
		if m.config.FailAfterChunks > 0 && index+1 >= m.config.FailAfterChunks {
			return errConfiguredFailure
		}
	}
	return nil
}

func (m *Mock) wait(ctx context.Context) error {
	if m.config.Delay <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(m.config.Delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (m *Mock) notifyCancelled() {
	if m.config.Cancelled == nil {
		return
	}
	m.cancelledOnce.Do(func() { close(m.config.Cancelled) })
}

func (m *Mock) notifyStarted() {
	if m.config.Started == nil {
		return
	}
	m.startedOnce.Do(func() { close(m.config.Started) })
}
