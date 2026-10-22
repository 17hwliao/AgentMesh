package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestExternalStreamingMeasurementAndTruncation(t *testing.T) {
	for _, complete := range []bool{true, false} {
		t.Run(fmt.Sprint(complete), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\n")
				w.(http.Flusher).Flush()
				time.Sleep(15 * time.Millisecond)
				if complete {
					fmt.Fprint(w, "data: [DONE]\n\n")
				} else {
					fmt.Fprint(w, "data: [DONE]")
				}
			}))
			defer server.Close()
			got := request(context.Background(), server.Client(), server.URL, "fixture", 10, 0, 0)
			if got.Completed != complete || got.Bytes != 1 || got.FirstMS < 0 || got.TotalMS < 10 {
				t.Fatalf("unexpected measurement: %+v", got)
			}
		})
	}
}
func TestOpenArrivalDropsAreCountedWithoutWaitingForResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()
	got := execute(context.Background(), server.Client(), server.URL, "fixture", 150*time.Millisecond, 1, 100, 10, 0, 0)
	if got.Scheduled < 10 || got.Dropped == 0 || got.Scheduled != len(got.Samples)+got.Dropped || got.Unexpected != 0 {
		t.Fatalf("arrival contract failed: %+v", got)
	}
}
func TestCancellationAndAdmissionStatusAreSeparate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(time.Second):
		}
	}))
	defer server.Close()
	got := request(context.Background(), server.Client(), server.URL, "fixture", 10, 0, 10*time.Millisecond)
	if !got.Cancelled || got.Completed {
		t.Fatal("cancelled stream counted successful")
	}
}

func TestClosedAdmissionBackoffPreventsRetryStorm(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Header().Set("Retry-After", "1"); w.WriteHeader(429) }))
	defer server.Close()
	got := execute(context.Background(), server.Client(), server.URL, "fixture", 80*time.Millisecond, 3, 0, 10, 0, 0)
	if got.Scheduled != 3 || got.Statuses[429] != 3 || got.Unexpected != 0 {
		t.Fatalf("closed client ignored admission backoff: %+v", got)
	}
}

func TestOpenScheduleStopsDuringArrivalWait(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "data: [DONE]\n\n") }))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	timer := time.AfterFunc(40*time.Millisecond, cancel)
	defer timer.Stop()
	got := execute(ctx, server.Client(), server.URL, "fixture", 500*time.Millisecond, 1, 5, 10, 0, 0)
	if got.Scheduled != 1 || got.Completed != 1 {
		t.Fatalf("scheduled a request after cancellation: %+v", got)
	}
}
