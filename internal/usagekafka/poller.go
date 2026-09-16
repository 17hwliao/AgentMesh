package usagekafka

import (
	"context"
	"errors"
	"time"
)

// PollResult is the safe operational outcome of one relay scan. It intentionally
// contains no event payload or model request data.
type PollResult struct {
	Published int
	Err       error
}

// RunPolling turns a single-pass relay operation into a cancellable long-running
// service loop. The outbox's available_at column still controls when a failed
// event is eligible; interval only controls how often the store is scanned.
func RunPolling(ctx context.Context, interval time.Duration, runOnce func(context.Context) (int, error), observe func(PollResult)) error {
	if runOnce == nil {
		return errors.New("relay run-once function is required")
	}
	if interval <= 0 {
		return errors.New("relay poll interval must be positive")
	}
	if ctx == nil {
		return errors.New("relay context is required")
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		published, err := runOnce(ctx)
		if observe != nil {
			observe(PollResult{Published: published, Err: err})
		}
		if ctx.Err() != nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
