package tenant

import (
	"context"
	"errors"
	"testing"

	"agentmesh/internal/provider"
)

func TestResolverRejectsMixedAndOutOfOrderDeclarationsAtStartup(t *testing.T) {
	tests := []struct {
		name    string
		logical []string
		route   []string
	}{
		{"mixed mock and real", []string{"mock"}, []string{"mock", "ark"}},
		{"reversed real order", []string{"ark", "ollama"}, []string{"ollama", "ark"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := NewMemory([]Tenant{{ID: "tenant_a", Enabled: true, ModelRoutes: map[string][]string{"model": test.route}}}, nil)
			providers := []provider.Provider{provider.NewMock(provider.MockConfig{Name: "mock-primary", FailAfterChunks: -1})}
			if _, err := NewResolver(context.Background(), store, test.logical, providers); err == nil || err.Error() != "tenant_route_configuration_invalid" {
				t.Fatalf("NewResolver() error=%v", err)
			}
		})
	}
}

func TestResolverRejectsEmptyPersistedRouteSetAtStartup(t *testing.T) {
	store := NewMemory([]Tenant{{ID: "tenant_a", Enabled: true}}, nil)
	providers := []provider.Provider{provider.NewMock(provider.MockConfig{Name: "mock-primary", FailAfterChunks: -1})}
	if _, err := NewResolver(context.Background(), store, []string{"mock"}, providers); err == nil || err.Error() != "tenant_route_configuration_invalid" {
		t.Fatalf("NewResolver() error=%v", err)
	}
}

func TestResolverRejectsPartialInvalidOrUnreadableMySQLIdentityState(t *testing.T) {
	providerMock := provider.NewMock(provider.MockConfig{Name: "mock-primary", FailAfterChunks: -1})
	for _, testCase := range []struct {
		name string
		db   *fakeReadDatabase
	}{
		{
			name: "partial records without route",
			db:   &fakeReadDatabase{row: countRow{tenants: 1}, rows: []mysqlRows{&fakeRows{}}},
		},
		{
			name: "invalid persisted route",
			db:   &fakeReadDatabase{row: countRow{tenants: 1, routes: 1, keys: 1}, rows: []mysqlRows{&fakeRows{values: [][]string{{"tenant-a", "model-a", "ark"}}}}},
		},
		{
			name: "count query failure",
			db:   &fakeReadDatabase{row: countErrorRow{err: errors.New("database unavailable")}},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := NewResolver(context.Background(), NewMySQLStore(testCase.db), []string{"mock"}, []provider.Provider{providerMock}); err == nil || err.Error() != "tenant_route_configuration_invalid" {
				t.Fatalf("NewResolver() error=%v", err)
			}
		})
	}
}

func TestRouteAllowedKeepsMockAndRealModesSeparate(t *testing.T) {
	if !RouteAllowed([]string{"ark", "ollama"}, []string{"ark", "ollama"}) || RouteAllowed([]string{"ollama", "ark"}, []string{"ark", "ollama"}) || RouteAllowed([]string{"mock"}, []string{"ark"}) {
		t.Fatal("provider route boundary changed")
	}
}

func TestResolverUsesOnlyAuthorizedMockRoute(t *testing.T) {
	store := NewMemory([]Tenant{{ID: "tenant_a", Enabled: true, ModelRoutes: map[string][]string{"allowed": {"mock"}}}}, nil)
	primary := provider.NewMock(provider.MockConfig{Name: "mock-primary", FailAfterChunks: -1})
	fallback := provider.NewMock(provider.MockConfig{Name: "mock-fallback", FailAfterChunks: -1})
	resolver, err := NewResolver(context.Background(), store, []string{"mock"}, []provider.Provider{primary, fallback})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := resolver.Streamer(context.Background(), "tenant_a", "blocked"); ok {
		t.Fatal("blocked model received a provider route")
	}
	visible := resolver.VisibleProviders(context.Background(), "tenant_a")
	if len(visible) != 2 || visible[0].Name() != "mock-primary" || visible[1].Name() != "mock-fallback" {
		t.Fatalf("visible=%v", visible)
	}
}
