package reservation

import (
	"agentmesh/internal/provider"
	"context"
	redis "github.com/redis/go-redis/v9"
	"os"
	"testing"
	"time"
)

func TestForwardedOutputCannotExceedReservation(t *testing.T) {
	records := &fakeCoordinatorRecords{}
	quota := &fakeCoordinatorQuota{}
	c, err := NewCoordinator(records, quota, CoordinatorConfig{ReservationUnits: 5, NextID: sequenceIDs("r", "q")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	request := provider.ChatRequest{Model: "m", Messages: []provider.Message{{Role: "user", Content: "hi"}}}
	s, err := c.Begin(context.Background(), "t", "m", request.Messages)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.BeforeAttempt(context.Background(), "p", request); err != nil {
		t.Fatal(err)
	}
	if limit := s.(interface{ OutputLimit() int }).OutputLimit(); limit != 3 {
		t.Fatal(limit)
	}
	chunk := provider.Chunk{Delta: "abc"}
	if err = s.BeforeEmit(context.Background(), "p", chunk); err != nil {
		t.Fatal(err)
	}
	if err = s.AfterEmit(context.Background(), "p", chunk); err != nil {
		t.Fatal(err)
	}
	if Code(s.BeforeEmit(context.Background(), "p", provider.Chunk{Delta: "d"})) != "quota_exhausted" {
		t.Fatal("extra output accepted")
	}
}

type cancellationRecords struct {
	fakeCoordinatorRecords
	t *testing.T
}

func (r *cancellationRecords) FinishAttempt(ctx context.Context, t, id string, n uint64, result string) error {
	if ctx.Err() != nil {
		r.t.Fatal("cleanup inherited cancellation")
	}
	return r.fakeCoordinatorRecords.FinishAttempt(ctx, t, id, n, result)
}
func (r *cancellationRecords) RecordProgress(ctx context.Context, t, id string, n, units uint64, at time.Time) error {
	if ctx.Err() != nil {
		r.t.Fatal("settlement inherited cancellation")
	}
	return r.fakeCoordinatorRecords.RecordProgress(ctx, t, id, n, units, at)
}
func TestCancelledRequestStillSettles(t *testing.T) {
	records := &cancellationRecords{t: t}
	q := &fakeCoordinatorQuota{}
	c, _ := NewCoordinator(records, q, CoordinatorConfig{ReservationUnits: 8, NextID: sequenceIDs("r", "q")}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	req := provider.ChatRequest{Model: "m", Messages: []provider.Message{{Content: "hi"}}}
	s, err := c.Begin(ctx, "t", "m", req.Messages)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.BeforeAttempt(ctx, "p", req); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err = s.FinishAttempt(ctx, "p", "cancelled"); err != nil {
		t.Fatal(err)
	}
	if err = s.Complete(ctx, "cancelled", true); err != nil {
		t.Fatal(err)
	}
	if q.settled != 1 {
		t.Fatal("not settled")
	}
}
func TestRealRedisTerminalIdempotencySurvivesVersionChanges(t *testing.T) {
	raw := os.Getenv("AGENTMESH_TEST_REDIS_URL")
	if raw == "" {
		t.Skip("set AGENTMESH_TEST_REDIS_URL for isolated Redis integration")
	}
	options, err := redis.ParseURL(raw)
	if err != nil {
		t.Fatal(err)
	}
	client := redis.NewClient(options)
	defer client.Close()
	ctx := context.Background()
	tenant := "regression-" + randomReservationID()
	client.Set(ctx, balanceKey(tenant), 64, 0)
	defer client.Del(ctx, balanceKey(tenant), operationKey(tenant, "r", 1, "reserve"), operationKey(tenant, "r", 1, "terminal"), reserveMarkerKey(tenant, "r"))
	q := NewRedisQuotaStore(client)
	if _, err = q.Reserve(ctx, tenant, "r", 1, 64); err != nil {
		t.Fatal(err)
	}
	if _, err = q.Cancel(ctx, tenant, "r", 2, 64); err != nil {
		t.Fatal(err)
	}
	if _, err = q.Cancel(ctx, tenant, "r", 99, 64); err != nil {
		t.Fatal(err)
	}
	if value := client.Get(ctx, balanceKey(tenant)).Val(); value != "64" {
		t.Fatalf("double refund balance=%s", value)
	}
	if _, err = q.Settle(ctx, tenant, "r", 100, 64, 32); err == nil {
		t.Fatal("opposite terminal operation accepted")
	}
}

func TestRealRedisMissingBalanceCannotBeRecreatedByTerminal(t *testing.T) {
	raw := os.Getenv("AGENTMESH_TEST_REDIS_URL")
	if raw == "" {
		t.Skip("isolated Redis integration")
	}
	options, err := redis.ParseURL(raw)
	if err != nil {
		t.Fatal(err)
	}
	client := redis.NewClient(options)
	defer client.Close()
	ctx := context.Background()
	tenant := "lost-wallet-" + randomReservationID()
	q := NewRedisQuotaStore(client)
	defer client.Del(ctx, balanceKey(tenant), operationKey(tenant, "r", 1, "reserve"), operationKey(tenant, "r", 1, "terminal"), reserveMarkerKey(tenant, "r"))
	for _, operation := range []string{"settle", "cancel"} {
		client.Set(ctx, balanceKey(tenant), 64, 0)
		if _, err = q.Reserve(ctx, tenant, "r", 1, 64); err != nil {
			t.Fatal(err)
		}
		client.Del(ctx, balanceKey(tenant))
		if operation == "settle" {
			_, err = q.Settle(ctx, tenant, "r", 2, 64, 32)
		} else {
			_, err = q.Cancel(ctx, tenant, "r", 2, 64)
		}
		if err == nil || client.Exists(ctx, balanceKey(tenant)).Val() != 0 {
			t.Fatalf("%s silently recreated lost wallet", operation)
		}
	}
}
