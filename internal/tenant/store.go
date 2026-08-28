package tenant

import (
	"crypto/sha256"
	"crypto/subtle"
	"sync"
)

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
