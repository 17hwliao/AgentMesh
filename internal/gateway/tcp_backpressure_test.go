package gateway

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"agentmesh/internal/provider"
)

type tcpFixture struct {
	sent    atomic.Int64
	entered atomic.Int64
	stopped chan struct{}
}

func (f *tcpFixture) Stream(ctx context.Context, r provider.ChatRequest, emit provider.Emit) error {
	if r.Messages[0].Content == "recovery" {
		return emit(provider.Chunk{Delta: "recovered"})
	}
	defer close(f.stopped)
	for i := 0; i < 10000; i++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		f.entered.Add(1)
		if e := emit(provider.Chunk{Delta: strings.Repeat("x", 8192)}); e != nil {
			return e
		}
		f.sent.Add(1)
	}
	return fmt.Errorf("non-reading consumer unexpectedly drained all output")
}

func TestTCPBackpressureCancelsBlockedUpstream(t *testing.T) {
	fixture := &tcpFixture{stopped: make(chan struct{})}
	server := httptest.NewUnstartedServer(New(fixture).Handler())
	server.Config.ConnState = func(c net.Conn, state http.ConnState) {
		if state == http.StateNew {
			if tcp, ok := c.(*net.TCPConn); ok {
				tcp.SetWriteBuffer(1024)
			}
		}
	}
	server.Start()
	defer server.Close()
	conn, e := net.Dial("tcp", server.Listener.Addr().String())
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	conn.(*net.TCPConn).SetReadBuffer(1024)
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	body := `{"model":"m","stream":true,"messages":[{"role":"user","content":"backpressure"}]}`
	fmt.Fprintf(conn, "POST /v1/chat/completions HTTP/1.1\r\nHost: fixture\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s", len(body), body)
	reader := bufio.NewReader(conn)
	status, e := reader.ReadString('\n')
	if e != nil || !strings.Contains(status, "200") {
		t.Fatalf("headers: %q %v", status, e)
	}
	for {
		line, e := reader.ReadString('\n')
		if e != nil {
			t.Fatal(e)
		}
		if line == "\r\n" {
			break
		}
	}
	sent := fixture.sent.Load()
	stable := time.Now()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(30 * time.Millisecond)
		current := fixture.sent.Load()
		if current != sent {
			sent = current
			stable = time.Now()
		}
		if fixture.entered.Load() > 0 && time.Since(stable) > 200*time.Millisecond {
			break
		}
	}
	// A small TCP send buffer may block inside the very first flush. Verify
	// one entered write is still blocked, rather than requiring a prior flush.
	if fixture.entered.Load() != sent+1 || time.Since(stable) <= 200*time.Millisecond || sent >= 10000 {
		t.Fatal("TCP write did not apply upstream backpressure")
	}
	select {
	case <-fixture.stopped:
		t.Fatal("upstream stopped before TCP cancellation")
	default:
	}
	conn.Close()
	select {
	case <-fixture.stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("blocked upstream did not stop after TCP close")
	}
	recovery := `{"model":"m","stream":true,"messages":[{"role":"user","content":"recovery"}]}`
	response, e := http.Post(server.URL+"/v1/chat/completions", "application/json", strings.NewReader(recovery))
	if e != nil {
		t.Fatal(e)
	}
	defer response.Body.Close()
	raw, e := io.ReadAll(io.LimitReader(response.Body, 4096))
	if e != nil || !strings.Contains(string(raw), "[DONE]") || !strings.Contains(string(raw), "recovered") {
		t.Fatal("post-backpressure recovery")
	}
}
