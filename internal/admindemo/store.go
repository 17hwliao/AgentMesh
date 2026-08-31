// Package admindemo provides an ephemeral lifecycle store for the local demo.
package admindemo

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"sync"
	"time"

	"agentmesh/internal/tenant"
)

// Store implements tenant authentication and lifecycle only for cmd/demo-admin.
// It is not selected by cmd/api and loses all state when its process exits.
type Store struct {
	mu        sync.RWMutex
	tenants   map[string]tenant.Tenant
	keys      map[string]keyRecord
	keyByID   map[string]string
	generated func() (string, string, error)
	dummy     [sha256.Size]byte
}

type keyRecord struct {
	id       string
	tenantID string
	hash     [sha256.Size]byte
	enabled  bool
}

func NewStore() *Store {
	return &Store{
		tenants:   map[string]tenant.Tenant{},
		keys:      map[string]keyRecord{},
		keyByID:   map[string]string{},
		generated: newKey,
	}
}

func (s *Store) CreateTenant(_ context.Context, value tenant.Tenant) error {
	if s == nil || !tenant.ValidDefinition(value) {
		return tenant.ErrTenantInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.tenants[value.ID]; exists {
		return tenant.ErrTenantExists
	}
	s.tenants[value.ID] = clone(value)
	return nil
}

func (s *Store) CreateAPIKey(_ context.Context, tenantID string) (tenant.APIKey, string, error) {
	if s == nil || tenantID == "" {
		return tenant.APIKey{}, "", tenant.ErrTenantInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	value, exists := s.tenants[tenantID]
	if !exists {
		return tenant.APIKey{}, "", tenant.ErrTenantNotFound
	}
	if !value.Enabled {
		return tenant.APIKey{}, "", tenant.ErrTenantDisabled
	}
	for range 3 {
		id, raw, err := s.generated()
		if err != nil {
			return tenant.APIKey{}, "", tenant.ErrKeyGeneration
		}
		prefix := raw[:8]
		if _, exists := s.keys[prefix]; exists {
			continue
		}
		s.keys[prefix] = keyRecord{id: id, tenantID: tenantID, hash: sha256.Sum256([]byte(raw)), enabled: true}
		s.keyByID[id] = prefix
		return tenant.APIKey{ID: id, TenantID: tenantID, Prefix: prefix, CreatedAt: time.Now().UTC()}, raw, nil
	}
	return tenant.APIKey{}, "", tenant.ErrKeyGeneration
}

func (s *Store) RevokeAPIKey(_ context.Context, keyID string) error {
	if s == nil || keyID == "" {
		return tenant.ErrKeyNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	prefix, exists := s.keyByID[keyID]
	if !exists {
		return tenant.ErrKeyNotFound
	}
	record := s.keys[prefix]
	record.enabled = false
	s.keys[prefix] = record
	return nil
}

func (s *Store) Authenticate(prefix string, digest [sha256.Size]byte) (tenant.Tenant, bool) {
	s.mu.RLock()
	record, exists := s.keys[prefix]
	value := s.tenants[record.tenantID]
	s.mu.RUnlock()
	target := s.dummy
	if exists {
		target = record.hash
	}
	matched := subtle.ConstantTimeCompare(digest[:], target[:]) == 1
	if !exists || !matched || !record.enabled || !value.Enabled {
		return tenant.Tenant{}, false
	}
	return clone(value), true
}

func (s *Store) Route(tenantID, model string) ([]string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, exists := s.tenants[tenantID]
	if !exists || !value.Enabled {
		return nil, false
	}
	route, exists := value.ModelRoutes[model]
	return append([]string(nil), route...), exists
}

func (s *Store) Routes(tenantID string) [][]string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, exists := s.tenants[tenantID]
	if !exists || !value.Enabled {
		return nil
	}
	return routes(value)
}

// AllRoutes declares the demo's sole locally constructed adapter, enabling
// Resolver validation before the first tenant is created through admin HTTP.
func (*Store) AllRoutes() [][]string { return [][]string{{"mock"}} }

func routes(value tenant.Tenant) [][]string {
	result := make([][]string, 0, len(value.ModelRoutes))
	for _, route := range value.ModelRoutes {
		result = append(result, append([]string(nil), route...))
	}
	return result
}

func clone(value tenant.Tenant) tenant.Tenant {
	routes := map[string][]string{}
	for model, route := range value.ModelRoutes {
		routes[model] = append([]string(nil), route...)
	}
	value.ModelRoutes = routes
	return value
}

func newKey() (string, string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", "", err
	}
	encoded := hex.EncodeToString(bytes)
	return encoded[:24], encoded, nil
}

var _ tenant.Store = (*Store)(nil)
var _ tenant.Lifecycle = (*Store)(nil)
