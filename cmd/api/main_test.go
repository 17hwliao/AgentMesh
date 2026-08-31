package main

import (
	"net/http"
	"testing"
	"time"
)

func TestNewHTTPServerUsesBoundedReadTimeoutsAndLeavesSSEWritesUnlimited(t *testing.T) {
	server := newHTTPServer("127.0.0.1:18080", http.NotFoundHandler())
	if server.ReadHeaderTimeout != 5*time.Second || server.ReadTimeout != 15*time.Second || server.IdleTimeout != 60*time.Second || server.WriteTimeout != 0 {
		t.Fatalf("timeouts header=%s read=%s idle=%s write=%s", server.ReadHeaderTimeout, server.ReadTimeout, server.IdleTimeout, server.WriteTimeout)
	}
}
