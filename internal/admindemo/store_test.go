package admindemo

import (
	"context"
	"crypto/sha256"
	"testing"

	"agentmesh/internal/tenant"
)

func TestStoreLifecycleRevokesAuthenticationWithoutStoringRawKey(t *testing.T) {
	store := NewStore()
	store.generated = func() (string, string, error) { return "key-id", "12345678-raw-demo-key", nil }
	value := tenant.Tenant{ID: "demo-tenant", Enabled: true, ModelRoutes: map[string][]string{"mock-model": {"mock"}}}
	if err := store.CreateTenant(context.Background(), value); err != nil {
		t.Fatal(err)
	}
	key, raw, err := store.CreateAPIKey(context.Background(), value.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Authenticate(raw[:8], sha256.Sum256([]byte(raw))); !ok {
		t.Fatal("created key did not authenticate")
	}
	if record := store.keys[key.Prefix]; record.hash != sha256.Sum256([]byte(raw)) || record.enabled == false {
		t.Fatalf("record=%+v", record)
	}
	if err := store.RevokeAPIKey(context.Background(), key.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Authenticate(raw[:8], sha256.Sum256([]byte(raw))); ok {
		t.Fatal("revoked key still authenticated")
	}
}
