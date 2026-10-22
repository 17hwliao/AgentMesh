package billing

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"agentmesh/internal/provider"
	"agentmesh/migrations"
)

func crashCatalog() []byte {
	raw, _ := json.Marshal(Catalog{Currency: "CNY", ExpiresAt: time.Now().Add(time.Hour), Prices: []Price{{Provider: "compatible", Model: "m", UpstreamModel: "fixture", MaxInput: 1000, InputNanoPerMillion: 800000000, OutputNanoPerMillion: 2000000000, Source: "process crash fixture"}}})
	return raw
}

// This entry point only runs inside the parent's dedicated subprocess. The
// parent kills the process after a durable-boundary marker, bypassing defers.
func TestMoneyCrashProbeChild(t *testing.T) {
	stage := os.Getenv("AGENTMESH_CRASH_STAGE")
	if stage == "" {
		t.Skip("subprocess-only entry point")
	}
	db, e := sql.Open("mysql", os.Getenv("AGENTMESH_TEST_MYSQL_DSN"))
	if e != nil {
		t.Fatal("fixture DB")
	}
	defer db.Close()
	ctx := context.Background()
	var database string
	if db.QueryRow("SELECT DATABASE()").Scan(&database) != nil || database != "agentmesh_improvement" {
		t.Fatal("dedicated database required")
	}
	store, e := New(ctx, db, crashCatalog())
	if e != nil {
		t.Fatal("store")
	}
	messages := []provider.Message{{Role: "user", Content: "fixture"}}
	session, e := store.Begin(ctx, os.Getenv("AGENTMESH_CRASH_TENANT"), "m", messages)
	if e != nil {
		t.Fatal("begin")
	}
	if stage != "held" {
		if session.BeforeAttempt(ctx, "compatible", provider.ChatRequest{Model: "m", Messages: messages}) != nil {
			t.Fatal("attempt")
		}
	}
	if stage == "usage" || stage == "finished" || stage == "settled" {
		if session.BeforeEmit(ctx, "compatible", provider.Chunk{Usage: &provider.Usage{InputTokens: 100, OutputTokens: 50}}) != nil {
			t.Fatal("usage")
		}
	}
	if stage == "finished" || stage == "settled" {
		if session.FinishAttempt(ctx, "compatible", "success") != nil {
			t.Fatal("finish attempt")
		}
	}
	if stage == "settled" {
		if session.Complete(ctx, "success", false) != nil {
			t.Fatal("complete")
		}
	}
	id := session.(interface{ BillingID() string }).BillingID()
	if os.WriteFile(os.Getenv("AGENTMESH_CRASH_MARKER"), []byte(id), 0600) != nil {
		t.Fatal("marker")
	}
	select {}
}

func TestPersistentProcessCrashRecovery(t *testing.T) {
	dsn := os.Getenv("AGENTMESH_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("isolated MySQL required")
	}
	db, e := sql.Open("mysql", dsn)
	if e != nil {
		t.Fatal("fixture DB")
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	var database string
	if db.QueryRowContext(ctx, "SELECT DATABASE()").Scan(&database) != nil || database != "agentmesh_improvement" {
		t.Fatal("dedicated database required")
	}
	if migrations.Apply(ctx, db) != nil {
		t.Fatal("migration")
	}
	store, e := New(ctx, db, crashCatalog())
	if e != nil {
		t.Fatal("store")
	}
	executable, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	for _, stage := range []string{"held", "started", "usage", "finished", "settled"} {
		t.Run(stage, func(t *testing.T) {
			tenant := fmt.Sprintf("crash-%s-%d", stage, time.Now().UnixNano())
			if _, e := db.ExecContext(ctx, "INSERT INTO tenants(tenant_id,enabled,created_at,updated_at) VALUES (?,1,UTC_TIMESTAMP(6),UTC_TIMESTAMP(6))", tenant); e != nil {
				t.Fatal("tenant")
			}
			if store.Credit(ctx, tenant, tenant, "process crash fixture credit", 1000000000) != nil {
				t.Fatal("credit")
			}
			marker := filepath.Join(t.TempDir(), "durable-boundary")
			child := exec.CommandContext(ctx, executable, "-test.run=^TestMoneyCrashProbeChild$")
			child.Env = append(os.Environ(), "AGENTMESH_CRASH_STAGE="+stage, "AGENTMESH_CRASH_TENANT="+tenant, "AGENTMESH_CRASH_MARKER="+marker)
			if child.Start() != nil {
				t.Fatal("subprocess start")
			}
			killed := false
			defer func() {
				if !killed {
					child.Process.Kill()
					child.Wait()
				}
			}()
			var raw []byte
			deadline := time.Now().Add(15 * time.Second)
			for time.Now().Before(deadline) {
				raw, e = os.ReadFile(marker)
				if e == nil {
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			if len(raw) != 32 {
				t.Fatal("durable boundary not reached")
			}
			if child.Process.Kill() != nil {
				t.Fatal("process kill")
			}
			child.Wait()
			killed = true
			id := string(raw)
			// Move only this fixture's clock marker; do not wait five minutes or change global time.
			if _, e = db.ExecContext(ctx, "UPDATE billing_requests SET updated_at=UTC_TIMESTAMP(6)-INTERVAL 6 MINUTE WHERE request_id=? AND tenant_id=?", id, tenant); e != nil {
				t.Fatal("age fixture")
			}
			for i := 0; i < 3; i++ {
				if store.Recover(ctx) != nil {
					t.Fatal("recovery replay")
				}
			}
			var state string
			var reserve, charged, balance, held uint64
			if db.QueryRowContext(ctx, "SELECT state,reserved_nano,charged_nano FROM billing_requests WHERE request_id=?", id).Scan(&state, &reserve, &charged) != nil {
				t.Fatal("request")
			}
			if db.QueryRowContext(ctx, "SELECT balance_nano,held_nano FROM billing_wallets WHERE tenant_id=?", tenant).Scan(&balance, &held) != nil {
				t.Fatal("wallet")
			}
			want := uint64(0)
			if stage == "finished" || stage == "settled" {
				want = 180000
			}
			if charged != want || balance+charged != 1000000000 {
				t.Fatalf("crash duplicated/lost debit: stage=%s state=%s charged=%d balance=%d want=%d", stage, state, charged, balance, want)
			}
			if stage == "started" || stage == "usage" {
				if state != "pending_reconciliation" || held != reserve {
					t.Fatal("unknown upstream usage refunded")
				}
			} else if held != 0 {
				t.Fatal("terminal hold not released")
			}
			var terminal int
			if db.QueryRowContext(ctx, "SELECT COUNT(*) FROM billing_audit WHERE request_id=? AND action IN ('settled','cancelled')", id).Scan(&terminal) != nil {
				t.Fatal("audit")
			}
			if stage != "started" && stage != "usage" && terminal != 1 {
				t.Fatal("terminal audit must be unique")
			}
		})
	}
}
