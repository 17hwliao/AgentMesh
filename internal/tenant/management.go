package tenant

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Account is the existing tenant principal, not a separate password identity.
type Account struct {
	ID          string              `json:"tenant_id"`
	Enabled     bool                `json:"enabled"`
	CreatedAt   time.Time           `json:"created_at"`
	UpdatedAt   time.Time           `json:"updated_at"`
	ModelRoutes map[string][]string `json:"model_routes,omitempty"`
}
type KeyInfo struct {
	ID        string     `json:"key_id"`
	TenantID  string     `json:"tenant_id"`
	Prefix    string     `json:"prefix"`
	Enabled   bool       `json:"enabled"`
	CreatedAt time.Time  `json:"created_at"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
}
type AccountPatch struct {
	Enabled     *bool               `json:"enabled,omitempty"`
	ModelRoutes map[string][]string `json:"model_routes,omitempty"`
}
type Management interface {
	ListAccounts(context.Context, string, int) ([]Account, error)
	Account(context.Context, string) (Account, error)
	UpdateAccount(context.Context, string, AccountPatch) error
	ListKeys(context.Context, string, string, int) ([]KeyInfo, error)
}

func (r *MySQLLifecycle) Database() *sql.DB { return r.managementDB }
func (r *MySQLLifecycle) ListAccounts(ctx context.Context, after string, limit int) ([]Account, error) {
	if r.managementDB == nil || limit < 1 || limit > 100 || (after != "" && !ValidID(after)) {
		return nil, ErrTenantInvalid
	}
	rows, e := r.managementDB.QueryContext(ctx, "SELECT tenant_id,enabled,created_at,updated_at FROM tenants WHERE tenant_id>? ORDER BY tenant_id LIMIT ?", after, limit)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Account{}
	for rows.Next() {
		var a Account
		if e = rows.Scan(&a.ID, &a.Enabled, &a.CreatedAt, &a.UpdatedAt); e != nil {
			return nil, e
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
func (r *MySQLLifecycle) Account(ctx context.Context, id string) (Account, error) {
	if r.managementDB == nil || !ValidID(id) {
		return Account{}, ErrTenantInvalid
	}
	// Consistent detail: state and routes must describe the same committed version.
	tx, e := r.managementDB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if e != nil {
		return Account{}, e
	}
	defer tx.Rollback()
	var a Account
	e = tx.QueryRowContext(ctx, "SELECT tenant_id,enabled,created_at,updated_at FROM tenants WHERE tenant_id=?", id).Scan(&a.ID, &a.Enabled, &a.CreatedAt, &a.UpdatedAt)
	if errors.Is(e, sql.ErrNoRows) {
		return a, ErrTenantNotFound
	}
	if e != nil {
		return a, e
	}
	rows, e := tx.QueryContext(ctx, "SELECT model,provider FROM tenant_model_routes WHERE tenant_id=? ORDER BY model,ordinal", id)
	if e != nil {
		return a, e
	}
	a.ModelRoutes = map[string][]string{}
	for rows.Next() {
		var m, p string
		if e = rows.Scan(&m, &p); e != nil {
			rows.Close()
			return a, e
		}
		a.ModelRoutes[m] = append(a.ModelRoutes[m], p)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return a, e
	}
	return a, tx.Commit()
}
func (r *MySQLLifecycle) UpdateAccount(ctx context.Context, id string, patch AccountPatch) error {
	if r.managementDB == nil || !ValidID(id) || (patch.Enabled == nil && patch.ModelRoutes == nil) {
		return ErrTenantInvalid
	}
	if patch.ModelRoutes != nil && !ValidDefinition(Tenant{ID: id, ModelRoutes: patch.ModelRoutes}) {
		return ErrTenantInvalid
	}
	tx, e := r.managementDB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var enabled bool
	e = tx.QueryRowContext(ctx, "SELECT enabled FROM tenants WHERE tenant_id=? FOR UPDATE", id).Scan(&enabled)
	if errors.Is(e, sql.ErrNoRows) {
		return ErrTenantNotFound
	}
	if e != nil {
		return e
	}
	if patch.Enabled != nil {
		enabled = *patch.Enabled
	}
	if _, e = tx.ExecContext(ctx, "UPDATE tenants SET enabled=?,updated_at=? WHERE tenant_id=?", enabled, r.now().UTC(), id); e != nil {
		return e
	}
	if patch.ModelRoutes != nil {
		if _, e = tx.ExecContext(ctx, "DELETE FROM tenant_model_routes WHERE tenant_id=?", id); e != nil {
			return e
		}
		for m, route := range patch.ModelRoutes {
			for i, p := range route {
				if _, e = tx.ExecContext(ctx, "INSERT INTO tenant_model_routes(tenant_id,model,ordinal,provider) VALUES (?,?,?,?)", id, m, i+1, p); e != nil {
					return e
				}
			}
		}
	}
	return tx.Commit()
}
func (r *MySQLLifecycle) ListKeys(ctx context.Context, id, after string, limit int) ([]KeyInfo, error) {
	if r.managementDB == nil || !ValidID(id) || len(after) > 36 || limit < 1 || limit > 100 {
		return nil, ErrTenantInvalid
	}
	// Do not select key_hash or return one-time credentials in a listing.
	rows, e := r.managementDB.QueryContext(ctx, "SELECT key_id,tenant_id,key_prefix,enabled,created_at,revoked_at FROM api_keys WHERE tenant_id=? AND key_id>? ORDER BY key_id LIMIT ?", id, after, limit)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []KeyInfo{}
	for rows.Next() {
		var k KeyInfo
		if e = rows.Scan(&k.ID, &k.TenantID, &k.Prefix, &k.Enabled, &k.CreatedAt, &k.RevokedAt); e != nil {
			return nil, e
		}
		out = append(out, k)
	}
	return out, rows.Err()
}
