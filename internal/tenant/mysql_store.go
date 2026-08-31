package tenant

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"errors"
	"sort"
)

const authenticateMySQL = `SELECT k.key_hash, k.enabled, t.tenant_id, t.enabled
FROM api_keys k JOIN tenants t ON t.tenant_id = k.tenant_id
WHERE k.key_prefix = ? LIMIT 1`

const routeMySQL = `SELECT r.provider FROM tenant_model_routes r
JOIN tenants t ON t.tenant_id = r.tenant_id
WHERE r.tenant_id = ? AND r.model = ? AND t.enabled = TRUE
ORDER BY r.ordinal`

const routesMySQL = `SELECT r.model, r.provider FROM tenant_model_routes r
JOIN tenants t ON t.tenant_id = r.tenant_id
WHERE r.tenant_id = ? AND t.enabled = TRUE ORDER BY r.model, r.ordinal`

const allRoutesMySQL = `SELECT r.tenant_id, r.model, r.provider FROM tenant_model_routes r
JOIN tenants t ON t.tenant_id = r.tenant_id
WHERE t.enabled = TRUE ORDER BY r.tenant_id, r.model, r.ordinal`

const identityCountsMySQL = `SELECT
  (SELECT COUNT(*) FROM tenants),
  (SELECT COUNT(*) FROM tenant_model_routes),
  (SELECT COUNT(*) FROM api_keys)`

type mysqlRow interface{ Scan(...any) error }
type mysqlRows interface {
	Next() bool
	Scan(...any) error
	Err() error
	Close() error
}
type mysqlReadDatabase interface {
	QueryRowContext(context.Context, string, ...any) mysqlRow
	QueryContext(context.Context, string, ...any) (mysqlRows, error)
}

type stdReadDatabase struct{ db *sql.DB }

func (d stdReadDatabase) QueryRowContext(ctx context.Context, query string, args ...any) mysqlRow {
	return d.db.QueryRowContext(ctx, query, args...)
}
func (d stdReadDatabase) QueryContext(ctx context.Context, query string, args ...any) (mysqlRows, error) {
	return d.db.QueryContext(ctx, query, args...)
}

// MySQLStore reads identity state directly for every request so a revocation
// cannot be hidden behind a process-local authentication cache.
type MySQLStore struct {
	db    mysqlReadDatabase
	dummy [sha256.Size]byte
}

func NewMySQLStore(db mysqlReadDatabase) *MySQLStore { return &MySQLStore{db: db} }

func (s *MySQLStore) Authenticate(ctx context.Context, prefix string, digest [sha256.Size]byte) (Tenant, bool) {
	if ctx == nil || ctx.Err() != nil || s == nil || s.db == nil {
		return Tenant{}, false
	}
	var hash []byte
	var keyEnabled, tenantEnabled bool
	var tenantID string
	err := s.db.QueryRowContext(ctx, authenticateMySQL, prefix).Scan(&hash, &keyEnabled, &tenantID, &tenantEnabled)
	target := s.dummy
	found := err == nil && len(hash) == sha256.Size
	if found {
		copy(target[:], hash)
	}
	matched := subtle.ConstantTimeCompare(digest[:], target[:]) == 1
	if !found || !matched || !keyEnabled || !tenantEnabled {
		return Tenant{}, false
	}
	return Tenant{ID: tenantID, Enabled: true}, true
}

func (s *MySQLStore) Route(ctx context.Context, tenantID, model string) ([]string, bool) {
	if ctx == nil || ctx.Err() != nil || s == nil || s.db == nil || tenantID == "" || model == "" {
		return nil, false
	}
	rows, err := s.db.QueryContext(ctx, routeMySQL, tenantID, model)
	if err != nil {
		return nil, false
	}
	defer rows.Close()
	var route []string
	for rows.Next() {
		var provider string
		if rows.Scan(&provider) != nil {
			return nil, false
		}
		route = append(route, provider)
	}
	return route, rows.Err() == nil && len(route) > 0
}

// Routes and LoadStartupState preserve Resolver's intentionally narrow
// visibility and startup validation interfaces without exposing key material.
func (s *MySQLStore) Routes(ctx context.Context, tenantID string) [][]string {
	return s.routes(ctx, routesMySQL, tenantID)
}

func (s *MySQLStore) LoadStartupState(ctx context.Context) (StartupState, error) {
	if ctx == nil || ctx.Err() != nil || s == nil || s.db == nil {
		return StartupState{}, errors.New("identity store unavailable")
	}
	var tenants, routes, keys uint64
	if err := s.db.QueryRowContext(ctx, identityCountsMySQL).Scan(&tenants, &routes, &keys); err != nil {
		return StartupState{}, err
	}
	if tenants == 0 && routes == 0 && keys == 0 {
		return StartupState{Pristine: true}, nil
	}
	loaded, err := s.loadAllRoutes(ctx)
	if err != nil {
		return StartupState{}, err
	}
	return StartupState{Routes: loaded}, nil
}

func (s *MySQLStore) loadAllRoutes(ctx context.Context) ([][]string, error) {
	rows, err := s.db.QueryContext(ctx, allRoutesMySQL)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type key struct{ tenant, model string }
	grouped := map[key][]string{}
	for rows.Next() {
		var tenantID, model, provider string
		if rows.Scan(&tenantID, &model, &provider) != nil {
			return nil, errors.New("identity route scan failed")
		}
		grouped[key{tenantID, model}] = append(grouped[key{tenantID, model}], provider)
	}
	if rows.Err() != nil {
		return nil, rows.Err()
	}
	keys := make([]key, 0, len(grouped))
	for value := range grouped {
		keys = append(keys, value)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].tenant+"\x00"+keys[i].model < keys[j].tenant+"\x00"+keys[j].model })
	result := make([][]string, 0, len(keys))
	for _, value := range keys {
		result = append(result, append([]string(nil), grouped[value]...))
	}
	return result, nil
}

func (s *MySQLStore) routes(ctx context.Context, query, tenantID string) [][]string {
	if ctx == nil || ctx.Err() != nil || s == nil || s.db == nil || tenantID == "" {
		return nil
	}
	rows, err := s.db.QueryContext(ctx, query, tenantID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	grouped := map[string][]string{}
	for rows.Next() {
		var model, provider string
		if rows.Scan(&model, &provider) != nil {
			return nil
		}
		grouped[model] = append(grouped[model], provider)
	}
	if rows.Err() != nil {
		return nil
	}
	models := make([]string, 0, len(grouped))
	for model := range grouped {
		models = append(models, model)
	}
	sort.Strings(models)
	result := make([][]string, 0, len(models))
	for _, model := range models {
		result = append(result, append([]string(nil), grouped[model]...))
	}
	return result
}
