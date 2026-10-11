package gatewayclient

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTruncatedSSEFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n")
	}))
	defer server.Close()
	if err := Stream(context.Background(), server.URL, "private-key", "model", nil, io.Discard); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("truncation accepted: %v", err)
	}
}
