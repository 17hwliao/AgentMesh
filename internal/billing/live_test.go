package billing

import (
	"agentmesh/internal/provider"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	_ "github.com/go-sql-driver/mysql"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRealMySQLMoneySettlementAndRecovery(t *testing.T) {
	dsn := os.Getenv("AGENTMESH_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("explicit acceptance-only database required")
	}
	db, e := sql.Open("mysql", dsn)
	if e != nil {
		t.Fatal("open failed")
	}
	defer db.Close()
	db.SetMaxOpenConns(16)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	tenant := fmt.Sprintf("money-%d", time.Now().UnixNano())
	if _, e = db.ExecContext(ctx, "INSERT INTO tenants(tenant_id,enabled,created_at,updated_at) VALUES (?,1,UTC_TIMESTAMP(6),UTC_TIMESTAMP(6))", tenant); e != nil {
		t.Fatal("tenant insert failed")
	}
	cached := uint64(160000000)
	raw, _ := json.Marshal(Catalog{Currency: "CNY", ExpiresAt: time.Now().Add(time.Hour), Prices: []Price{{Provider: "compatible", Model: "m", UpstreamModel: "qwen-plus", MaxInput: 65536, InputNanoPerMillion: 800000000, OutputNanoPerMillion: 2000000000, CachedInputNanoPerMillion: &cached, Source: "fixture"}}})
	s, e := New(ctx, db, raw)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Credit(ctx, tenant, tenant, "fixture-top-up", 1000000000); e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	var failed atomic.Int32
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if s.Credit(ctx, tenant, tenant, "fixture-top-up", 1000000000) != nil {
				failed.Add(1)
			}
		}()
	}
	wg.Wait()
	if failed.Load() != 0 {
		t.Fatal("idempotent concurrent credit failed")
	}
	var balance, held uint64
	read := func() {
		if e = db.QueryRowContext(ctx, "SELECT balance_nano,held_nano FROM billing_wallets WHERE tenant_id=?", tenant).Scan(&balance, &held); e != nil {
			t.Fatal(e)
		}
	}
	read()
	if balance != 1000000000 || held != 0 {
		t.Fatal("credit replay changed balance")
	}
	if s.Credit(ctx, tenant, tenant, "fixture-top-up", 2) == nil {
		t.Fatal("conflicting credit accepted")
	}
	request := provider.ChatRequest{Model: "m", Messages: []provider.Message{{Role: "user", Content: "hello"}}}
	v, e := s.Begin(ctx, tenant, "m", request.Messages)
	if e != nil {
		t.Fatal(e)
	}
	if e = v.BeforeAttempt(ctx, "compatible", request); e != nil {
		t.Fatal(e)
	}
	usage := provider.Usage{InputTokens: 1000, OutputTokens: 100, CachedInputTokens: 200}
	chunk := provider.Chunk{Usage: &usage}
	if e = v.BeforeEmit(ctx, "compatible", chunk); e != nil {
		t.Fatal(e)
	}
	if e = v.BeforeEmit(ctx, "compatible", chunk); e != nil {
		t.Fatal(e)
	}
	if e = v.FinishAttempt(ctx, "compatible", "succeeded"); e != nil {
		t.Fatal(e)
	}
	if e = v.Complete(ctx, "success", false); e != nil {
		t.Fatal(e)
	}
	if e = v.Complete(ctx, "success", false); e != nil {
		t.Fatal(e)
	}
	read()
	if balance != 1000000000-872000 || held != 0 {
		t.Fatalf("wrong split token charge %d held %d", balance, held)
	}
	pending, e := s.Begin(ctx, tenant, "m", request.Messages)
	if e != nil {
		t.Fatal(e)
	}
	if e = pending.BeforeAttempt(ctx, "compatible", request); e != nil {
		t.Fatal(e)
	}
	id := pending.(*session).id
	if _, e = db.ExecContext(ctx, "UPDATE billing_requests SET updated_at=UTC_TIMESTAMP(6)-INTERVAL 6 MINUTE WHERE request_id=?", id); e != nil {
		t.Fatal(e)
	}
	if e = s.Recover(ctx); e != nil {
		t.Fatal(e)
	}
	var state string
	if e = db.QueryRowContext(ctx, "SELECT state FROM billing_requests WHERE request_id=?", id).Scan(&state); e != nil || state != "pending_reconciliation" {
		t.Fatal("unknown usage not retained")
	}
	read()
	if held == 0 || balance != 1000000000-872000 {
		t.Fatal("unknown usage was charged or released")
	}
	if e = s.Resolve(ctx, id, "fixture-invoice-verified", []Resolution{{Ordinal: 1, Usage: usage}}); e != nil {
		t.Fatal(e)
	}
	read()
	if held != 0 || balance != 1000000000-2*872000 {
		t.Fatal("reconciliation charged twice or leaked hold")
	}
	// Concurrent holds cannot exceed the available balance.
	if _, e = db.ExecContext(ctx, "UPDATE billing_wallets SET balance_nano=200000000 WHERE tenant_id=?", tenant); e != nil {
		t.Fatal(e)
	}
	var winners atomic.Int32
	sessions := make(chan *session, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, e := s.Begin(ctx, tenant, "m", request.Messages)
			if e == nil {
				winners.Add(1)
				sessions <- v.(*session)
			}
		}()
	}
	wg.Wait()
	close(sessions)
	if winners.Load() != 1 {
		t.Fatalf("overspent concurrent holds: %d", winners.Load())
	}
	for v := range sessions {
		if e = v.Complete(ctx, "cancel", true); e != nil {
			t.Fatal(e)
		}
	}
	read()
	if held != 0 || balance != 200000000 {
		t.Fatal("unstarted cancellation did not release hold")
	}
}
