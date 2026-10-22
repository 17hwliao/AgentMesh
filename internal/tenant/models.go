package tenant

import (
	"context"
	"errors"
	"sort"
)

func (s *MemoryStore) Models(ctx context.Context, id string) ([]string, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.tenants[id]
	if !ok || !t.Enabled {
		return nil, errors.New("tenant unavailable")
	}
	result := make([]string, 0, len(t.ModelRoutes))
	for m := range t.ModelRoutes {
		result = append(result, m)
	}
	sort.Strings(result)
	return result, nil
}
func (s *MySQLStore) Models(ctx context.Context, id string) ([]string, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	rows, e := s.db.QueryContext(ctx, routesMySQL, id)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	seen := map[string]bool{}
	for rows.Next() {
		var m, p string
		if rows.Scan(&m, &p) != nil {
			return nil, errors.New("model scan failed")
		}
		seen[m] = true
	}
	if rows.Err() != nil {
		return nil, rows.Err()
	}
	result := make([]string, 0, len(seen))
	for m := range seen {
		result = append(result, m)
	}
	sort.Strings(result)
	return result, nil
}
func (r *Resolver) Models(ctx context.Context, id string) ([]string, error) {
	source, ok := r.store.(interface {
		Models(context.Context, string) ([]string, error)
	})
	if !ok {
		return nil, errors.New("model catalog unavailable")
	}
	return source.Models(ctx, id)
}
