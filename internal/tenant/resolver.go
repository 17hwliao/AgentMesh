package tenant

import (
	"errors"

	"agentmesh/internal/provider"
	"agentmesh/internal/router"
)

// Resolver turns a tenant's declarative model route into already-constructed
// process-local adapters. It never constructs an adapter from tenant data.
type Resolver struct {
	store     Store
	logical   []string
	providers []provider.Provider
	byName    map[string]provider.Provider
	mockMode  bool
}

// NewResolver accepts only routes that are ordered subsets of the global
// runtime selection. The mock mode remains mutually exclusive with real
// adapters, matching runtime.Build's 002 safety contract.
func NewResolver(store Store, logical []string, providers []provider.Provider) (*Resolver, error) {
	if store == nil || len(logical) == 0 || len(providers) == 0 {
		return nil, errors.New("tenant_route_configuration_invalid")
	}
	r := &Resolver{store: store, logical: append([]string(nil), logical...), providers: append([]provider.Provider(nil), providers...), byName: map[string]provider.Provider{}}
	if len(logical) == 1 && logical[0] == "mock" {
		r.mockMode = true
		return r, r.validateDeclaredRoutes()
	}
	for _, name := range logical {
		if name == "mock" {
			return nil, errors.New("tenant_route_configuration_invalid")
		}
	}
	for _, candidate := range providers {
		r.byName[candidate.Name()] = candidate
	}
	for _, name := range logical {
		if _, ok := r.byName[name]; !ok {
			return nil, errors.New("tenant_route_configuration_invalid")
		}
	}
	return r, r.validateDeclaredRoutes()
}

// Streamer returns a fresh router over the authorized, already-built adapter
// order. A route that is not an ordered global subset is refused before any
// provider attempt.
func (r *Resolver) Streamer(tenantID, model string) (router.Streamer, bool) {
	route, ok := r.store.Route(tenantID, model)
	if !ok || !r.validRoute(route) {
		return nil, false
	}
	if r.mockMode {
		return router.New(r.providers...), true
	}
	selected := make([]provider.Provider, 0, len(route))
	for _, name := range route {
		candidate, exists := r.byName[name]
		if !exists {
			return nil, false
		}
		selected = append(selected, candidate)
	}
	return router.New(selected...), true
}

// VisibleProviders returns only adapters authorized by at least one model
// route for the authenticated tenant. It intentionally exposes no route/model
// mapping or tenant identifier.
func (r *Resolver) VisibleProviders(tenantID string) []provider.Provider {
	seen := map[string]bool{}
	result := make([]provider.Provider, 0)
	for _, name := range r.allowedNames(tenantID) {
		if r.mockMode {
			for _, candidate := range r.providers {
				if !seen[candidate.Name()] {
					seen[candidate.Name()] = true
					result = append(result, candidate)
				}
			}
			continue
		}
		candidate := r.byName[name]
		if candidate != nil && !seen[name] {
			seen[name] = true
			result = append(result, candidate)
		}
	}
	return result
}

func (r *Resolver) allowedNames(tenantID string) []string {
	// Store exposes individual routes so callers cannot enumerate tenant records.
	// Resolver validates visibility as requests arrive; health visibility is kept
	// conservative by asking the store's explicit route index below when present.
	if indexed, ok := r.store.(interface{ Routes(string) [][]string }); ok {
		var names []string
		for _, route := range indexed.Routes(tenantID) {
			names = append(names, route...)
		}
		return names
	}
	return nil
}

func (r *Resolver) validRoute(route []string) bool {
	if len(route) == 0 {
		return false
	}
	if r.mockMode {
		return len(route) == 1 && route[0] == "mock"
	}
	last := -1
	for _, name := range route {
		index := -1
		for i, global := range r.logical {
			if global == name {
				index = i
				break
			}
		}
		if index < 0 || index <= last {
			return false
		}
		last = index
	}
	return true
}

// Routes lets Resolver obtain a tenant-local view for health without exposing
// raw keys or other tenants. It returns defensive copies.
func (s *MemoryStore) Routes(tenantID string) [][]string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.tenants[tenantID]
	if !ok || !t.Enabled {
		return nil
	}
	routes := make([][]string, 0, len(t.ModelRoutes))
	for _, route := range t.ModelRoutes {
		routes = append(routes, append([]string(nil), route...))
	}
	return routes
}

func (r *Resolver) validateDeclaredRoutes() error {
	all, ok := r.store.(interface{ AllRoutes() [][]string })
	if !ok {
		return errors.New("tenant_route_configuration_invalid")
	}
	for _, route := range all.AllRoutes() {
		if !r.validRoute(route) {
			return errors.New("tenant_route_configuration_invalid")
		}
	}
	return nil
}

// AllRoutes is used only during local bootstrap validation. Like Routes, it
// returns copies and never exposes API-key records.
func (s *MemoryStore) AllRoutes() [][]string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var routes [][]string
	for _, tenant := range s.tenants {
		for _, route := range tenant.ModelRoutes {
			routes = append(routes, append([]string(nil), route...))
		}
	}
	return routes
}
