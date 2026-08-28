package provider

import "errors"

// Stable adapter failure categories. Their wrapped details are intentionally
// never returned by the public gateway response.
var (
	ErrUpstream = errors.New("provider upstream failure")
	ErrProtocol = errors.New("provider protocol failure")
)
