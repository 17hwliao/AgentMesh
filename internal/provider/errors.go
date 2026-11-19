package provider

import (
	"errors"
	"fmt"
)

// Stable adapter failure categories. Their wrapped details are intentionally
// never returned by the public gateway response.
var (
	ErrUpstream = errors.New("provider upstream failure")
	ErrProtocol = errors.New("provider protocol failure")
)

// UpstreamStatusError preserves a non-2xx status for routing/cooldown policy
// while still matching ErrUpstream with errors.Is.
type UpstreamStatusError struct {
	Status int
}

func (e *UpstreamStatusError) Error() string {
	return fmt.Sprintf("%s: HTTP %d", ErrUpstream, e.Status)
}

func (e *UpstreamStatusError) Unwrap() error { return ErrUpstream }

func UpstreamStatus(err error) (int, bool) {
	var status *UpstreamStatusError
	if errors.As(err, &status) {
		return status.Status, true
	}
	return 0, false
}
