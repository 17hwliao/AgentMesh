package billing

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"math/rand"
	"os"
	"testing"
	"time"

	"agentmesh/internal/provider"
	"agentmesh/migrations"
)

func referenceCost(p Price, u provider.Usage) uint64 {
	cached := p.InputNanoPerMillion
	if p.CachedInputNanoPerMillion != nil {
		cached = *p.CachedInputNanoPerMillion
	}
	total := new(big.Int)
	for _, pair := range [][2]uint64{{uint64(u.InputTokens - u.CachedInputTokens), p.InputNanoPerMillion}, {uint64(u.CachedInputTokens), cached}, {uint64(u.OutputTokens), p.OutputNanoPerMillion}} {
		total.Add(total, new(big.Int).Mul(new(big.Int).SetUint64(pair[0]), new(big.Int).SetUint64(pair[1])))
	}
	total.Add(total, big.NewInt(999999))
	total.Div(total, big.NewInt(1000000))
	return total.Uint64()
}

func TestMoneyCostAgainstIndependentIntegerOracle(t *testing.T) {
	r := rand.New(rand.NewSource(20261011))
	for i := 0; i < 1000; i++ {
		input := r.Intn(MaxInputTokens + 1)
		cached := r.Intn(input + 1)
		p := Price{MaxInput: MaxInputTokens, InputNanoPerMillion: uint64(r.Int63n(1000000000000)), OutputNanoPerMillion: uint64(r.Int63n(1000000000000))}
		if i%2 == 0 {
			rate := uint64(r.Int63n(int64(p.InputNanoPerMillion + 1)))
			p.CachedInputNanoPerMillion = &rate
		}
		u := provider.Usage{InputTokens: input, OutputTokens: r.Intn(MaxOutputTokens + 1), CachedInputTokens: cached}
		got, err := p.Cost(u)
		if err != nil || got != referenceCost(p, u) {
			t.Fatalf("sample=%d got=%d err=%v", i, got, err)
		}
	}
}

// This test runs against an explicitly supplied, isolated migrated database.
// Conservation is checked after every transition, not just the final balance.
func TestPersistentMoneyStateInvariants(t *testing.T) {
	dsn := os.Getenv("AGENTMESH_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("isolated MySQL DSN required")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal("database open failed")
	}
	defer db.Close()
	db.SetMaxOpenConns(16)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	var database string
	if err = db.QueryRowContext(ctx, "SELECT DATABASE()").Scan(&database); err != nil || database != "agentmesh_improvement" {
		t.Fatal("dedicated test database required")
	}
	if err = migrations.Apply(ctx, db); err != nil {
		t.Fatal("migration failed")
	}
	tenant := fmt.Sprintf("invariant-%d", time.Now().UnixNano())
	if _, err = db.ExecContext(ctx, "INSERT INTO tenants(tenant_id,enabled,created_at,updated_at) VALUES (?,1,UTC_TIMESTAMP(6),UTC_TIMESTAMP(6))", tenant); err != nil {
		t.Fatal(err)
	}
	rate := uint64(160000000)
	p := Price{Provider: "compatible", Model: "m", UpstreamModel: "qwen-plus", MaxInput: 1000, InputNanoPerMillion: 800000000, OutputNanoPerMillion: 2000000000, CachedInputNanoPerMillion: &rate, Source: "local invariant fixture, not live tariff"}
	raw, _ := json.Marshal(Catalog{Currency: "CNY", ExpiresAt: time.Now().Add(time.Hour), Prices: []Price{p}})
	s, err := New(ctx, db, raw)
	if err != nil {
		t.Fatal(err)
	}
	const initial uint64 = 1000000000
	if err = s.Credit(ctx, tenant, tenant, "local-test-credit", initial); err != nil {
		t.Fatal(err)
	}
	var expectedCharge uint64
	check := func() {
		t.Helper()
		var balance, held, credits, charges, reserved uint64
		if err = db.QueryRowContext(ctx, "SELECT balance_nano,held_nano FROM billing_wallets WHERE tenant_id=?", tenant).Scan(&balance, &held); err != nil {
			t.Fatal(err)
		}
		if err = db.QueryRowContext(ctx, "SELECT COALESCE(SUM(amount_nano),0) FROM billing_credits WHERE tenant_id=?", tenant).Scan(&credits); err != nil {
			t.Fatal(err)
		}
		if err = db.QueryRowContext(ctx, "SELECT COALESCE(SUM(charged_nano),0),COALESCE(SUM(IF(state IN ('held','pending_reconciliation'),reserved_nano,0)),0) FROM billing_requests WHERE tenant_id=?", tenant).Scan(&charges, &reserved); err != nil {
			t.Fatal(err)
		}
		if credits != initial || charges != expectedCharge || balance+charges != credits || held != reserved || held > balance {
			t.Fatalf("conservation violated balance=%d held=%d credits=%d charges=%d expected=%d reserved=%d", balance, held, credits, charges, expectedCharge, reserved)
		}
	}
	rng := rand.New(rand.NewSource(20261011))
	request := provider.ChatRequest{Model: "m", Messages: []provider.Message{{Role: "user", Content: "state-machine"}}}
	for i := 0; i < 24; i++ {
		v, e := s.Begin(ctx, tenant, "m", request.Messages)
		if e != nil {
			t.Fatal(e)
		}
		a := v.(*session)
		check()
		if e = s.Credit(ctx, tenant, tenant, "local-test-credit", initial); e != nil {
			t.Fatal(e)
		}
		check()
		if e = s.Credit(ctx, tenant, tenant, "local-test-credit", initial+1); !errors.Is(e, ErrConflict) {
			t.Fatal("credit conflict accepted")
		}
		check()
		if i%4 == 0 {
			cancelled, cancelNow := context.WithCancel(ctx)
			cancelNow()
			if e = a.BeforeAttempt(cancelled, "compatible", request); e == nil {
				t.Fatal("cancelled attempt started")
			}
			if e = a.Complete(cancelled, "cancel", true); e != nil {
				t.Fatal(e)
			}
		} else {
			if e = a.BeforeAttempt(ctx, "compatible", request); e != nil {
				t.Fatal(e)
			}
			check()
			u := provider.Usage{InputTokens: 500 + rng.Intn(500), OutputTokens: 10 + rng.Intn(100), CachedInputTokens: rng.Intn(200)}
			if i%4 == 1 {
				if e = a.Complete(ctx, "interrupted", true); e != nil {
					t.Fatal(e)
				}
				check()
				if e = s.Resolve(ctx, a.id, "local-verified-invoice", []Resolution{{Ordinal: 1, Usage: u}}); e != nil {
					t.Fatal(e)
				}
				expectedCharge += referenceCost(p, u)
				if e = s.Resolve(ctx, a.id, "local-verified-invoice", []Resolution{{Ordinal: 1, Usage: u}}); !errors.Is(e, ErrConflict) {
					t.Fatal("terminal resolution replay changed state")
				}
			} else {
				chunk := provider.Chunk{Usage: &u}
				if e = a.BeforeEmit(ctx, "compatible", chunk); e != nil {
					t.Fatal(e)
				}
				check()
				if e = a.BeforeEmit(ctx, "compatible", chunk); e != nil {
					t.Fatal(e)
				}
				other := u
				other.OutputTokens++
				if e = a.BeforeEmit(ctx, "compatible", provider.Chunk{Usage: &other}); !errors.Is(e, ErrConflict) {
					t.Fatal("different usage replaced observed usage")
				}
				check()
				if e = a.FinishAttempt(ctx, "compatible", "succeeded"); e != nil {
					t.Fatal(e)
				}
				if i%4 == 3 {
					if _, e = db.ExecContext(ctx, "UPDATE billing_requests SET updated_at=UTC_TIMESTAMP(6)-INTERVAL 6 MINUTE WHERE request_id=?", a.id); e != nil {
						t.Fatal(e)
					}
					if e = s.Recover(ctx); e != nil {
						t.Fatal(e)
					}
				} else if e = a.Complete(ctx, "success", false); e != nil {
					t.Fatal(e)
				}
				expectedCharge += referenceCost(p, u)
			}
		}
		check()
		if e = s.finalize(ctx, a.id, nil, ""); e != nil {
			t.Fatal(e)
		}
		check()
		var audits int
		if e = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM billing_audit WHERE request_id=? AND action IN ('settled','cancelled')", a.id).Scan(&audits); e != nil || audits != 1 {
			t.Fatalf("terminal audit count=%d err=%v", audits, e)
		}
	}
}
