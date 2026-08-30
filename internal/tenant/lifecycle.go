package tenant

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	mysql "github.com/go-sql-driver/mysql"
)

var (
	ErrTenantExists   = errors.New("tenant_exists")
	ErrTenantInvalid  = errors.New("tenant_invalid")
	ErrTenantNotFound = errors.New("tenant_not_found")
	ErrTenantDisabled = errors.New("tenant_disabled")
	ErrKeyNotFound    = errors.New("api_key_not_found")
	ErrKeyGeneration  = errors.New("api_key_generation_failed")
)

type APIKey struct {
	ID        string
	TenantID  string
	Prefix    string
	CreatedAt time.Time
}

type Lifecycle interface {
	CreateTenant(context.Context, Tenant) error
	CreateAPIKey(context.Context, string) (APIKey, string, error)
	RevokeAPIKey(context.Context, string) error
}

type lifecycleDatabase interface {
	BeginTx(context.Context, *sql.TxOptions) (lifecycleTransaction, error)
}
type lifecycleTransaction interface {
	ExecContext(context.Context, string, ...any) (lifecycleResult, error)
	QueryRowContext(context.Context, string, ...any) lifecycleRow
	Commit() error
	Rollback() error
}
type lifecycleResult interface{ RowsAffected() (int64, error) }
type lifecycleRow interface{ Scan(...any) error }

type stdLifecycleDatabase struct{ db *sql.DB }
type stdLifecycleTransaction struct{ tx *sql.Tx }

func (d stdLifecycleDatabase) BeginTx(ctx context.Context, options *sql.TxOptions) (lifecycleTransaction, error) {
	tx, err := d.db.BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	return stdLifecycleTransaction{tx: tx}, nil
}
func (t stdLifecycleTransaction) ExecContext(ctx context.Context, query string, args ...any) (lifecycleResult, error) {
	return t.tx.ExecContext(ctx, query, args...)
}
func (t stdLifecycleTransaction) QueryRowContext(ctx context.Context, query string, args ...any) lifecycleRow {
	return t.tx.QueryRowContext(ctx, query, args...)
}
func (t stdLifecycleTransaction) Commit() error   { return t.tx.Commit() }
func (t stdLifecycleTransaction) Rollback() error { return t.tx.Rollback() }

type keyGenerator func() (string, string, error)

// MySQLLifecycle owns only local administration writes. It never returns a
// digest and never stores the one-time raw Key after its CreateAPIKey call.
type MySQLLifecycle struct {
	db       lifecycleDatabase
	now      func() time.Time
	newKey   keyGenerator
	maxTries int
}

func NewMySQLLifecycle(db *sql.DB) *MySQLLifecycle {
	return newMySQLLifecycle(stdLifecycleDatabase{db: db}, time.Now, randomAPIKey)
}
func newMySQLLifecycle(db lifecycleDatabase, now func() time.Time, newKey keyGenerator) *MySQLLifecycle {
	if now == nil {
		now = time.Now
	}
	if newKey == nil {
		newKey = randomAPIKey
	}
	return &MySQLLifecycle{db: db, now: now, newKey: newKey, maxTries: 3}
}

func (r *MySQLLifecycle) CreateTenant(ctx context.Context, value Tenant) error {
	if r == nil || r.db == nil || !ValidDefinition(value) {
		return ErrTenantInvalid
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := r.now().UTC()
	if _, err := tx.ExecContext(ctx, `INSERT INTO tenants (tenant_id, enabled, created_at, updated_at) VALUES (?, ?, ?, ?)`, value.ID, true, now, now); err != nil {
		if isDuplicate(err) {
			return ErrTenantExists
		}
		return err
	}
	for model, route := range value.ModelRoutes {
		for index, provider := range route {
			if _, err := tx.ExecContext(ctx, `INSERT INTO tenant_model_routes (tenant_id, model, ordinal, provider) VALUES (?, ?, ?, ?)`, value.ID, model, index+1, provider); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func (r *MySQLLifecycle) CreateAPIKey(ctx context.Context, tenantID string) (APIKey, string, error) {
	if r == nil || r.db == nil || tenantID == "" {
		return APIKey{}, "", ErrTenantInvalid
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return APIKey{}, "", err
	}
	defer tx.Rollback()
	var enabled bool
	if err := tx.QueryRowContext(ctx, `SELECT enabled FROM tenants WHERE tenant_id = ? LIMIT 1`, tenantID).Scan(&enabled); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return APIKey{}, "", ErrTenantNotFound
		}
		return APIKey{}, "", err
	}
	if !enabled {
		return APIKey{}, "", ErrTenantDisabled
	}
	now := r.now().UTC()
	for range r.maxTries {
		id, raw, err := r.newKey()
		if err != nil || len(raw) < 8 || id == "" {
			return APIKey{}, "", ErrKeyGeneration
		}
		digest := sha256.Sum256([]byte(raw))
		_, err = tx.ExecContext(ctx, `INSERT INTO api_keys (key_id, tenant_id, key_prefix, key_hash, enabled, created_at) VALUES (?, ?, ?, ?, TRUE, ?)`, id, tenantID, raw[:8], digest[:], now)
		if err == nil {
			if err := tx.Commit(); err != nil {
				return APIKey{}, "", err
			}
			return APIKey{ID: id, TenantID: tenantID, Prefix: raw[:8], CreatedAt: now}, raw, nil
		}
		if !isDuplicate(err) {
			return APIKey{}, "", err
		}
	}
	return APIKey{}, "", ErrKeyGeneration
}

func (r *MySQLLifecycle) RevokeAPIKey(ctx context.Context, keyID string) error {
	if r == nil || r.db == nil || keyID == "" {
		return ErrKeyNotFound
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var ignored bool
	if err := tx.QueryRowContext(ctx, `SELECT enabled FROM api_keys WHERE key_id = ? LIMIT 1`, keyID).Scan(&ignored); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrKeyNotFound
		}
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE api_keys SET enabled = FALSE, revoked_at = COALESCE(revoked_at, ?) WHERE key_id = ? AND enabled = TRUE`, r.now().UTC(), keyID); err != nil {
		return err
	}
	return tx.Commit()
}

func randomAPIKey() (string, string, error) {
	idBytes, keyBytes := make([]byte, 16), make([]byte, 32)
	if _, err := rand.Read(idBytes); err != nil {
		return "", "", err
	}
	if _, err := rand.Read(keyBytes); err != nil {
		return "", "", err
	}
	idBytes[6] = (idBytes[6] & 0x0f) | 0x40
	idBytes[8] = (idBytes[8] & 0x3f) | 0x80
	id := fmt.Sprintf("%x-%x-%x-%x-%x", idBytes[:4], idBytes[4:6], idBytes[6:8], idBytes[8:10], idBytes[10:])
	return id, hex.EncodeToString(keyBytes), nil
}

func isDuplicate(err error) bool {
	var mysqlError *mysql.MySQLError
	return errors.As(err, &mysqlError) && mysqlError.Number == 1062
}
