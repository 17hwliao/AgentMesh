package auth

import (
	"context"
	"errors"
	"testing"

	"agentmesh/internal/tenant"
)

func TestOpenConfiguredStoreRejectsMissingPersistentSettingsBeforeOpen(t *testing.T) {
	calls := 0
	runtime, err := openConfiguredRuntime(func(name string) string {
		if name == authStoreEnvironment {
			return authStoreMySQL
		}
		return ""
	}, func(string) (tenant.Store, tenant.Lifecycle, func(), error) {
		calls++
		return nil, nil, func() {}, errors.New("must not open")
	})
	if code, ok := IsConfigurationError(err); !ok || code != CodeConfigurationMissing || calls != 0 {
		t.Fatalf("runtime=%v code=%q ok=%t calls=%d", runtime, code, ok, calls)
	}
}

func TestOpenConfiguredStoreUsesExplicitMySQLMode(t *testing.T) {
	store := tenant.NewMemory(nil, nil)
	calls := 0
	runtime, err := openConfiguredRuntime(func(name string) string {
		switch name {
		case authStoreEnvironment:
			return authStoreMySQL
		case authMySQLDSNEnvironment:
			return "mysql://not-reported"
		case adminTokenEnvironment:
			return "admin-token"
		}
		return ""
	}, func(dsn string) (tenant.Store, tenant.Lifecycle, func(), error) {
		calls++
		if dsn != "mysql://not-reported" {
			t.Fatalf("dsn mismatch")
		}
		return store, fakeLifecycle{}, func() {}, nil
	})
	if runtime != nil {
		defer runtime.Close()
	}
	if err != nil || runtime == nil || runtime.Store != store || calls != 1 {
		t.Fatalf("runtime=%+v err=%v calls=%d", runtime, err, calls)
	}
}

type fakeLifecycle struct{}

func (fakeLifecycle) CreateTenant(context.Context, tenant.Tenant) error { return nil }
func (fakeLifecycle) CreateAPIKey(context.Context, string) (tenant.APIKey, string, error) {
	return tenant.APIKey{}, "", nil
}
func (fakeLifecycle) RevokeAPIKey(context.Context, string) error { return nil }
