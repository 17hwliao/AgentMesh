package tenant

import (
	"crypto/sha256"
	"crypto/subtle"
	"regexp"
	"strings"
	"sync"
)

var tenantIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

type Tenant struct {
	ID          string
	Enabled     bool
	ModelRoutes map[string][]string
}
type APIKeyRecord struct {
	Prefix   string
	Hash     [sha256.Size]byte
	TenantID string
	Enabled  bool
}
type Store interface {
	Authenticate(string, [sha256.Size]byte) (Tenant, bool)
	Route(string, string) ([]string, bool)
}

// ValidDefinition accepts only the static portion of a tenant declaration.
// Resolver still checks its routes against the process-local Provider selection.
func ValidDefinition(value Tenant) bool {
	if !tenantIDPattern.MatchString(value.ID) || len(value.ModelRoutes) == 0 {
		return false
	}
	for model, route := range value.ModelRoutes {
		if strings.TrimSpace(model) == "" || len(route) == 0 {
			return false
		}
		seen := map[string]bool{}
		for _, name := range route {
			if seen[name] || (name != "mock" && name != "ark" && name != "ollama") {
				return false
			}
			seen[name] = true
		}
	}
	return true
}

// RouteAllowed enforces the same mock/real provider boundary as runtime.Build:
// mock is exclusive, while real routes are ordered subsets of the configured
// process-local provider order.
func RouteAllowed(route, logical []string) bool {
	if len(route) == 0 || len(logical) == 0 {
		return false
	}
	if len(logical) == 1 && logical[0] == "mock" {
		return len(route) == 1 && route[0] == "mock"
	}
	last := -1
	for _, name := range route {
		if name == "mock" {
			return false
		}
		index := -1
		for candidate, global := range logical {
			if global == name {
				index = candidate
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

type MemoryStore struct {
	mu      sync.RWMutex
	tenants map[string]Tenant
	keys    map[string]APIKeyRecord
	dummy   [sha256.Size]byte
}

func NewMemory(tenants []Tenant, keys []APIKeyRecord) *MemoryStore {
	s := &MemoryStore{tenants: map[string]Tenant{}, keys: map[string]APIKeyRecord{}}
	for _, t := range tenants {
		s.tenants[t.ID] = cloneTenant(t)
	}
	for _, k := range keys {
		s.keys[k.Prefix] = k
	}
	return s
}
func (s *MemoryStore) Authenticate(prefix string, digest [sha256.Size]byte) (Tenant, bool) {
	s.mu.RLock()
	record, exists := s.keys[prefix]
	t := s.tenants[record.TenantID]
	s.mu.RUnlock()
	target := s.dummy
	if exists {
		target = record.Hash
	}
	matched := subtle.ConstantTimeCompare(digest[:], target[:]) == 1
	if !exists || !matched || !record.Enabled || !t.Enabled {
		return Tenant{}, false
	}
	return cloneTenant(t), true
}
func (s *MemoryStore) Route(tenantID, model string) ([]string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.tenants[tenantID]
	if !ok || !t.Enabled {
		return nil, false
	}
	route, ok := t.ModelRoutes[model]
	if !ok {
		return nil, false
	}
	return append([]string(nil), route...), true
}
func cloneTenant(t Tenant) Tenant {
	routes := map[string][]string{}
	for m, r := range t.ModelRoutes {
		routes[m] = append([]string(nil), r...)
	}
	t.ModelRoutes = routes
	return t
}
