package tenant

import (
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
			if _, err := NewResolver(store, test.logical, providers); err == nil || err.Error() != "tenant_route_configuration_invalid" {
				t.Fatalf("NewResolver() error=%v", err)
			}
		})
	}
}

func TestResolverUsesOnlyAuthorizedMockRoute(t *testing.T) {
	store := NewMemory([]Tenant{{ID: "tenant_a", Enabled: true, ModelRoutes: map[string][]string{"allowed": {"mock"}}}}, nil)
	primary := provider.NewMock(provider.MockConfig{Name: "mock-primary", FailAfterChunks: -1})
	fallback := provider.NewMock(provider.MockConfig{Name: "mock-fallback", FailAfterChunks: -1})
	resolver, err := NewResolver(store, []string{"mock"}, []provider.Provider{primary, fallback})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := resolver.Streamer("tenant_a", "blocked"); ok {
		t.Fatal("blocked model received a provider route")
	}
	visible := resolver.VisibleProviders("tenant_a")
	if len(visible) != 2 || visible[0].Name() != "mock-primary" || visible[1].Name() != "mock-fallback" {
		t.Fatalf("visible=%v", visible)
	}
}
