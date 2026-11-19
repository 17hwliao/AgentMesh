package catalog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const (
	DefaultModelOutputTokens = 4096
	MetaGeneration           = "generation"
	MetaAdminTokenHash       = "admin_token_hash"
	MetaLocalKeyID           = "local_key_id"
)

type Store struct {
	DB   *sql.DB
	Path string
}

type Queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func Open(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("catalog path required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	dsn := "file:" + filepath.ToSlash(path) + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// SQLite is intentionally one-writer local storage. A small connection
	// pool keeps concurrent readers usable while avoiding lock storms.
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{DB: db, Path: path}, nil
}

func (s *Store) Close() error {
	if s == nil || s.DB == nil {
		return nil
	}
	return s.DB.Close()
}

func (s *Store) Migrate(ctx context.Context) error {
	if s == nil || s.DB == nil {
		return errors.New("catalog not open")
	}
	for _, statement := range schemaStatements {
		if _, err := s.DB.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("catalog migration: %w", err)
		}
	}
	if err := s.ensureColumn(ctx, "providers", "tls_insecure", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return fmt.Errorf("catalog migration: %w", err)
	}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO catalog_metadata(key,value) VALUES(?,?)
		ON CONFLICT(key) DO NOTHING`, MetaGeneration, "0")
	return err
}

func (s *Store) ensureColumn(ctx context.Context, table, column, definition string) error {
	rows, err := s.DB.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
	if err != nil {
		return err
	}
	found := false
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, dataType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			return err
		}
		if name == column {
			found = true
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if found {
		return nil
	}
	_, err = s.DB.ExecContext(ctx, `ALTER TABLE `+table+` ADD COLUMN `+column+` `+definition)
	return err
}

func (s *Store) GetMeta(ctx context.Context, key string) (string, error) {
	var value string
	err := s.DB.QueryRowContext(ctx, `SELECT value FROM catalog_metadata WHERE key=?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return value, err
}

func (s *Store) SetMeta(ctx context.Context, key, value string) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO catalog_metadata(key,value) VALUES(?,?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

func (s *Store) WithTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (s *Store) LoadData(ctx context.Context, q Queryer) (SnapshotData, error) {
	data := SnapshotData{Credentials: map[string]Credential{}}
	generation, err := queryMeta(ctx, q, MetaGeneration)
	if err == nil {
		data.Generation, _ = strconv.ParseInt(generation, 10, 64)
	}

	rows, err := q.QueryContext(ctx, `SELECT id,slug,name,base_url,kind,auth_type,tls_insecure,enabled,revision,created_at,updated_at
		FROM providers WHERE deleted_at IS NULL ORDER BY slug`)
	if err != nil {
		return data, err
	}
	for rows.Next() {
		var p Provider
		var tlsInsecure, enabled int
		var created, updated string
		if err := rows.Scan(&p.ID, &p.Slug, &p.Name, &p.BaseURL, &p.Kind, &p.AuthType, &tlsInsecure, &enabled, &p.Revision, &created, &updated); err != nil {
			rows.Close()
			return data, err
		}
		p.TLSInsecure = tlsInsecure != 0
		p.Enabled = enabled != 0
		p.CreatedAt = parseTime(created)
		p.UpdatedAt = parseTime(updated)
		data.Providers = append(data.Providers, p)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return data, err
	}
	rows.Close()

	rows, err = q.QueryContext(ctx, `SELECT id,provider_id,key_id,ciphertext,nonce,mask,version,active,created_at,updated_at
		FROM provider_credentials WHERE active=1`)
	if err != nil {
		return data, err
	}
	for rows.Next() {
		var c Credential
		var active int
		var created, updated string
		if err := rows.Scan(&c.ID, &c.ProviderID, &c.KeyID, &c.Ciphertext, &c.Nonce, &c.Mask, &c.Version, &active, &created, &updated); err != nil {
			rows.Close()
			return data, err
		}
		c.Active = active != 0
		c.CreatedAt = parseTime(created)
		c.UpdatedAt = parseTime(updated)
		data.Credentials[c.ProviderID] = c
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return data, err
	}
	rows.Close()

	rows, err = q.QueryContext(ctx, `SELECT id,provider_id,upstream_id,public_id,enabled,stale,tools,vision,max_output_tokens,source,created_at,updated_at
		FROM models ORDER BY public_id`)
	if err != nil {
		return data, err
	}
	for rows.Next() {
		var m Model
		var enabled, stale, tools, vision int
		var created, updated string
		if err := rows.Scan(&m.ID, &m.ProviderID, &m.UpstreamID, &m.PublicID, &enabled, &stale, &tools, &vision, &m.MaxOutputTokens, &m.Source, &created, &updated); err != nil {
			rows.Close()
			return data, err
		}
		m.Enabled = enabled != 0
		m.Stale = stale != 0
		m.Tools = tools != 0
		m.Vision = vision != 0
		m.CreatedAt = parseTime(created)
		m.UpdatedAt = parseTime(updated)
		data.Models = append(data.Models, m)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return data, err
	}
	rows.Close()

	rows, err = q.QueryContext(ctx, `SELECT id,name,prefix,hash,scopes,enabled,rpm,daily_requests,daily_tokens,model_allowlist,tenant_id,created_at,updated_at,last_used_at,expires_at
		FROM client_keys ORDER BY created_at`)
	if err != nil {
		return data, err
	}
	for rows.Next() {
		var k ClientKey
		var scopes, allowlist string
		var enabled int
		var created, updated string
		var lastUsed, expires sql.NullString
		if err := rows.Scan(&k.ID, &k.Name, &k.Prefix, &k.Hash, &scopes, &enabled, &k.RPM, &k.DailyRequests, &k.DailyTokens, &allowlist, &k.TenantID, &created, &updated, &lastUsed, &expires); err != nil {
			rows.Close()
			return data, err
		}
		k.Enabled = enabled != 0
		k.Scopes = splitList(scopes)
		k.ModelAllowlist = splitList(allowlist)
		k.CreatedAt = parseTime(created)
		k.UpdatedAt = parseTime(updated)
		if lastUsed.Valid && lastUsed.String != "" {
			t := parseTime(lastUsed.String)
			k.LastUsedAt = &t
		}
		if expires.Valid && expires.String != "" {
			t := parseTime(expires.String)
			k.ExpiresAt = &t
		}
		data.Keys = append(data.Keys, k)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return data, err
	}
	rows.Close()

	rows, err = q.QueryContext(ctx, `SELECT name,description,enabled FROM routing_groups ORDER BY name`)
	if err != nil {
		return data, err
	}
	for rows.Next() {
		var g RoutingGroup
		var enabled int
		if err := rows.Scan(&g.Name, &g.Description, &enabled); err != nil {
			rows.Close()
			return data, err
		}
		g.Enabled = enabled != 0
		data.RoutingGroups = append(data.RoutingGroups, g)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return data, err
	}
	rows.Close()

	for index := range data.RoutingGroups {
		rows, err = q.QueryContext(ctx, `SELECT group_name,model_id,priority,weight,enabled
			FROM routing_members WHERE group_name=? ORDER BY priority,model_id`, data.RoutingGroups[index].Name)
		if err != nil {
			return data, err
		}
		for rows.Next() {
			var m RoutingMember
			var enabled int
			if err := rows.Scan(&m.Group, &m.ModelID, &m.Priority, &m.Weight, &enabled); err != nil {
				rows.Close()
				return data, err
			}
			m.Enabled = enabled != 0
			data.RoutingGroups[index].Members = append(data.RoutingGroups[index].Members, m)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return data, err
		}
		rows.Close()
	}
	return data, nil
}

func queryMeta(ctx context.Context, q Queryer, key string) (string, error) {
	var value string
	err := q.QueryRowContext(ctx, `SELECT value FROM catalog_metadata WHERE key=?`, key).Scan(&value)
	return value, err
}

func IncrementGeneration(ctx context.Context, tx *sql.Tx) (int64, error) {
	current, err := queryMeta(ctx, tx, MetaGeneration)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	next, _ := strconv.ParseInt(current, 10, 64)
	next++
	_, err = tx.ExecContext(ctx, `INSERT INTO catalog_metadata(key,value) VALUES(?,?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, MetaGeneration, strconv.FormatInt(next, 10))
	return next, err
}

func parseTime(value string) time.Time {
	if value == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}
	}
	return t
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func splitList(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func joinList(values []string) string {
	return strings.Join(values, ",")
}

var schemaStatements = []string{
	`CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)`,
	`INSERT INTO schema_version(version) SELECT 1 WHERE NOT EXISTS (SELECT 1 FROM schema_version)`,
	`CREATE TABLE IF NOT EXISTS catalog_metadata (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS providers (
		id TEXT PRIMARY KEY,
		slug TEXT NOT NULL UNIQUE,
		name TEXT NOT NULL,
		base_url TEXT NOT NULL,
		kind TEXT NOT NULL DEFAULT 'openai_compatible',
		auth_type TEXT NOT NULL DEFAULT 'bearer',
		tls_insecure INTEGER NOT NULL DEFAULT 0,
		enabled INTEGER NOT NULL DEFAULT 1,
		revision INTEGER NOT NULL DEFAULT 1,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		deleted_at TEXT
	)`,
	`CREATE TABLE IF NOT EXISTS provider_credentials (
		id TEXT PRIMARY KEY,
		provider_id TEXT NOT NULL,
		key_id TEXT NOT NULL,
		ciphertext BLOB NOT NULL,
		nonce BLOB NOT NULL,
		mask TEXT NOT NULL,
		version INTEGER NOT NULL,
		active INTEGER NOT NULL DEFAULT 1,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS provider_credentials_active
		ON provider_credentials(provider_id) WHERE active=1`,
	`CREATE TABLE IF NOT EXISTS models (
		id TEXT PRIMARY KEY,
		provider_id TEXT NOT NULL,
		upstream_id TEXT NOT NULL,
		public_id TEXT NOT NULL UNIQUE,
		enabled INTEGER NOT NULL DEFAULT 0,
		stale INTEGER NOT NULL DEFAULT 0,
		tools INTEGER NOT NULL DEFAULT 0,
		vision INTEGER NOT NULL DEFAULT 0,
		max_output_tokens INTEGER NOT NULL DEFAULT 4096,
		source TEXT NOT NULL DEFAULT 'discovered',
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		UNIQUE(provider_id, upstream_id)
	)`,
	`CREATE TABLE IF NOT EXISTS client_keys (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		prefix TEXT NOT NULL UNIQUE,
		hash BLOB NOT NULL,
		scopes TEXT NOT NULL,
		enabled INTEGER NOT NULL DEFAULT 1,
		rpm INTEGER NOT NULL DEFAULT 0,
		daily_requests INTEGER NOT NULL DEFAULT 0,
		daily_tokens INTEGER NOT NULL DEFAULT 0,
		model_allowlist TEXT NOT NULL DEFAULT '',
		tenant_id TEXT NOT NULL DEFAULT 'local',
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		last_used_at TEXT,
		expires_at TEXT
	)`,
	`CREATE TABLE IF NOT EXISTS routing_groups (
		name TEXT PRIMARY KEY,
		description TEXT NOT NULL DEFAULT '',
		enabled INTEGER NOT NULL DEFAULT 1
	)`,
	`CREATE TABLE IF NOT EXISTS routing_members (
		group_name TEXT NOT NULL,
		model_id TEXT NOT NULL,
		priority INTEGER NOT NULL DEFAULT 0,
		weight INTEGER NOT NULL DEFAULT 1,
		enabled INTEGER NOT NULL DEFAULT 1,
		PRIMARY KEY(group_name, model_id)
	)`,
	`CREATE TABLE IF NOT EXISTS usage_events (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		key_id TEXT NOT NULL,
		provider_id TEXT NOT NULL,
		model TEXT NOT NULL,
		input_tokens INTEGER NOT NULL DEFAULT 0,
		output_tokens INTEGER NOT NULL DEFAULT 0,
		result TEXT NOT NULL,
		created_at TEXT NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS usage_events_created ON usage_events(created_at)`,
	`CREATE INDEX IF NOT EXISTS usage_events_key_created ON usage_events(key_id, created_at)`,
	`CREATE TABLE IF NOT EXISTS cooldowns (
		provider_id TEXT NOT NULL,
		model TEXT NOT NULL,
		until TEXT NOT NULL,
		reason TEXT NOT NULL,
		PRIMARY KEY(provider_id, model)
	)`,
	`CREATE TABLE IF NOT EXISTS audit_log (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		at TEXT NOT NULL,
		actor TEXT NOT NULL,
		action TEXT NOT NULL,
		target TEXT NOT NULL,
		outcome TEXT NOT NULL,
		detail TEXT NOT NULL DEFAULT ''
	)`,
}
