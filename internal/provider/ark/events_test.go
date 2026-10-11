package ark

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"agentmesh/internal/provider"
)

func TestMultilineEventAndAbnormalTermination(t *testing.T) {
	for _, tc := range []struct {
		body string
		ok   bool
	}{
		{": heartbeat\r\n\ndata:{\ndata:\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata:[DONE]\n\n", true},
		{"data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n", false},
		{"data:[DONE]\n", false},
		{"event:error\ndata:{\"code\":\"private\"}\n\n", false},
	} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, tc.body) }))
		var out strings.Builder
		err := testAdapter(t, s.URL).Stream(context.Background(), testRequest(), func(c provider.Chunk) error { out.WriteString(c.Delta); return nil })
		s.Close()
		if (err == nil) != tc.ok {
			t.Fatalf("body=%q err=%v", tc.body, err)
		}
		if tc.ok && out.String() != "ok" {
			t.Fatal("multiline response lost")
		}
	}
}

func TestCancellationWhileWaitingForEventBoundary(t *testing.T) {
	started, stopped := make(chan struct{}), make(chan struct{})
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(stopped)
		fmt.Fprint(w, "data: {\"choices\":")
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
	}))
	defer s.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- testAdapter(t, s.URL).Stream(ctx, testRequest(), func(provider.Chunk) error { t.Error("partial event emitted"); return nil })
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("stream did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancel blocked")
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("upstream connection retained")
	}
}
