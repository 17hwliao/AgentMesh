package usagekafka

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRunPollingRunsImmediatelyAndStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	results := make(chan PollResult, 2)
	done := make(chan error, 1)
	go func() {
		done <- RunPolling(ctx, time.Millisecond, func(context.Context) (int, error) {
			calls++
			if calls == 2 {
				cancel()
			}
			return 1, nil
		}, func(result PollResult) { results <- result })
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunPolling error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("RunPolling did not stop after cancellation")
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
	for i := 0; i < 2; i++ {
		if result := <-results; result.Published != 1 || result.Err != nil {
			t.Fatalf("result = %+v", result)
		}
	}
}

func TestRunPollingReportsErrorsAndContinues(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	results := make(chan PollResult, 2)
	calls := 0
	done := make(chan error, 1)
	go func() {
		done <- RunPolling(ctx, time.Millisecond, func(context.Context) (int, error) {
			calls++
			if calls == 2 {
				cancel()
			}
			return 0, errors.New("broker unavailable")
		}, func(result PollResult) { results <- result })
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunPolling error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("RunPolling did not stop after cancellation")
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
	for i := 0; i < 2; i++ {
		if result := <-results; result.Err == nil || result.Err.Error() != "broker unavailable" {
			t.Fatalf("result = %+v", result)
		}
	}
}

func TestRunPollingRejectsInvalidConfiguration(t *testing.T) {
	if err := RunPolling(context.Background(), time.Second, nil, nil); err == nil {
		t.Fatal("nil runOnce was accepted")
	}
	if err := RunPolling(context.Background(), 0, func(context.Context) (int, error) { return 0, nil }, nil); err == nil {
		t.Fatal("zero interval was accepted")
	}
}
