package catalog

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrNotFound = errors.New("catalog_not_found")
	ErrConflict = errors.New("catalog_conflict")
)

func InsertProvider(ctx context.Context, tx *sql.Tx, p Provider) error {
	if p.Kind == "" {
		p.Kind = ProviderOpenAICompatible
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO providers
		(id,slug,name,base_url,kind,auth_type,tls_insecure,enabled,revision,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		p.ID, p.Slug, p.Name, p.BaseURL, p.Kind, p.AuthType, boolInt(p.TLSInsecure), boolInt(p.Enabled), p.Revision,
		formatTime(p.CreatedAt), formatTime(p.UpdatedAt))
	if isUniqueError(err) {
		return ErrConflict
	}
	return err
}

func UpdateProvider(ctx context.Context, tx *sql.Tx, p Provider) error {
	result, err := tx.ExecContext(ctx, `UPDATE providers SET
		name=?,base_url=?,kind=?,auth_type=?,tls_insecure=?,enabled=?,revision=?,updated_at=?
		WHERE id=? AND deleted_at IS NULL`,
		p.Name, p.BaseURL, p.Kind, p.AuthType, boolInt(p.TLSInsecure), boolInt(p.Enabled), p.Revision, formatTime(p.UpdatedAt), p.ID)
	if err != nil {
		return err
	}
	return requireChanged(result)
}

func SoftDeleteProvider(ctx context.Context, tx *sql.Tx, id string, at time.Time) error {
	result, err := tx.ExecContext(ctx, `UPDATE providers SET deleted_at=?,enabled=0,revision=revision+1,updated_at=?
		WHERE id=? AND deleted_at IS NULL`, formatTime(at), formatTime(at), id)
	if err != nil {
		return err
	}
	return requireChanged(result)
}

func InsertCredential(ctx context.Context, tx *sql.Tx, c Credential) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO provider_credentials
		(id,provider_id,key_id,ciphertext,nonce,mask,version,active,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?)`,
		c.ID, c.ProviderID, c.KeyID, c.Ciphertext, c.Nonce, c.Mask, c.Version, boolInt(c.Active),
		formatTime(c.CreatedAt), formatTime(c.UpdatedAt))
	if isUniqueError(err) {
		return ErrConflict
	}
	return err
}

func UpdateCredential(ctx context.Context, tx *sql.Tx, c Credential) error {
	result, err := tx.ExecContext(ctx, `UPDATE provider_credentials SET
		key_id=?,ciphertext=?,nonce=?,mask=?,version=?,updated_at=?
		WHERE id=? AND provider_id=? AND active=1`,
		c.KeyID, c.Ciphertext, c.Nonce, c.Mask, c.Version, formatTime(c.UpdatedAt), c.ID, c.ProviderID)
	if err != nil {
		return err
	}
	return requireChanged(result)
}

type DiscoveryResult struct {
	Added   int `json:"added"`
	Kept    int `json:"kept"`
	Stale   int `json:"stale"`
	Updated int `json:"updated"`
}

func UpsertDiscoveredModels(ctx context.Context, tx *sql.Tx, providerID, slug string, upstream []string, at time.Time) (DiscoveryResult, error) {
	var result DiscoveryResult
	seen := make(map[string]bool, len(upstream))
	for _, id := range upstream {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		publicID := slug + "/" + id
		seen[id] = true
		var existing string
		err := tx.QueryRowContext(ctx, `SELECT id FROM models WHERE provider_id=? AND upstream_id=?`, providerID, id).Scan(&existing)
		if errors.Is(err, sql.ErrNoRows) {
			modelID := NewID("mdl")
			if _, err := tx.ExecContext(ctx, `INSERT INTO models
				(id,provider_id,upstream_id,public_id,enabled,stale,tools,vision,max_output_tokens,source,created_at,updated_at)
				VALUES(?,?,?,?,0,0,0,0,?,'discovered',?,?)`,
				modelID, providerID, id, publicID, DefaultModelOutputTokens, formatTime(at), formatTime(at)); err != nil {
				if isUniqueError(err) {
					return result, ErrConflict
				}
				return result, err
			}
			result.Added++
			continue
		}
		if err != nil {
			return result, err
		}
		// Discovery refreshes identity and stale state only. It must not
		// overwrite administrator-controlled enabled/tools/vision values.
		if _, err := tx.ExecContext(ctx, `UPDATE models SET public_id=?,stale=0,updated_at=?
			WHERE id=?`, publicID, formatTime(at), existing); err != nil {
			return result, err
		}
		result.Updated++
	}
	if len(seen) == 0 {
		return result, nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT upstream_id FROM models WHERE provider_id=? AND stale=0`, providerID)
	if err != nil {
		return result, err
	}
	var missing []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return result, err
		}
		if !seen[id] {
			missing = append(missing, id)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return result, err
	}
	rows.Close()
	for _, id := range missing {
		if _, err := tx.ExecContext(ctx, `UPDATE models SET stale=1,updated_at=? WHERE provider_id=? AND upstream_id=?`,
			formatTime(at), providerID, id); err != nil {
			return result, err
		}
		result.Stale++
	}
	return result, nil
}

func InsertModel(ctx context.Context, tx *sql.Tx, m Model) error {
	if m.MaxOutputTokens <= 0 {
		m.MaxOutputTokens = DefaultModelOutputTokens
	}
	if m.Source == "" {
		m.Source = "manual"
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO models
		(id,provider_id,upstream_id,public_id,enabled,stale,tools,vision,max_output_tokens,source,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		m.ID, m.ProviderID, m.UpstreamID, m.PublicID, boolInt(m.Enabled), boolInt(m.Stale),
		boolInt(m.Tools), boolInt(m.Vision), m.MaxOutputTokens, m.Source,
		formatTime(m.CreatedAt), formatTime(m.UpdatedAt))
	if isUniqueError(err) {
		return ErrConflict
	}
	return err
}

func SetModelState(ctx context.Context, tx *sql.Tx, id string, enabled, tools, vision *bool, maxOutput *int, at time.Time) error {
	var sets []string
	var args []any
	if enabled != nil {
		sets = append(sets, "enabled=?")
		args = append(args, boolInt(*enabled))
	}
	if tools != nil {
		sets = append(sets, "tools=?")
		args = append(args, boolInt(*tools))
	}
	if vision != nil {
		sets = append(sets, "vision=?")
		args = append(args, boolInt(*vision))
	}
	if maxOutput != nil {
		if *maxOutput <= 0 {
			return fmt.Errorf("invalid max_output_tokens")
		}
		sets = append(sets, "max_output_tokens=?")
		args = append(args, *maxOutput)
	}
	if len(sets) == 0 {
		return nil
	}
	sets = append(sets, "updated_at=?")
	args = append(args, formatTime(at), id)
	result, err := tx.ExecContext(ctx, `UPDATE models SET `+strings.Join(sets, ",")+` WHERE id=?`, args...)
	if err != nil {
		return err
	}
	return requireChanged(result)
}

func InsertClientKey(ctx context.Context, tx *sql.Tx, k ClientKey) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO client_keys
		(id,name,prefix,hash,scopes,enabled,rpm,daily_requests,daily_tokens,model_allowlist,tenant_id,created_at,updated_at,last_used_at,expires_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		k.ID, k.Name, k.Prefix, k.Hash, joinList(k.Scopes), boolInt(k.Enabled), k.RPM, k.DailyRequests, k.DailyTokens,
		joinList(k.ModelAllowlist), k.TenantID, formatTime(k.CreatedAt), formatTime(k.UpdatedAt),
		nullableTime(k.LastUsedAt), nullableTime(k.ExpiresAt))
	if isUniqueError(err) {
		return ErrConflict
	}
	return err
}

func UpdateClientKey(ctx context.Context, tx *sql.Tx, id string, enabled *bool, rpm, dailyRequests, dailyTokens *int, allowlist *[]string, expires *time.Time, at time.Time) error {
	var sets []string
	var args []any
	if enabled != nil {
		sets = append(sets, "enabled=?")
		args = append(args, boolInt(*enabled))
	}
	if rpm != nil {
		sets = append(sets, "rpm=?")
		args = append(args, *rpm)
	}
	if dailyRequests != nil {
		sets = append(sets, "daily_requests=?")
		args = append(args, *dailyRequests)
	}
	if dailyTokens != nil {
		sets = append(sets, "daily_tokens=?")
		args = append(args, *dailyTokens)
	}
	if allowlist != nil {
		sets = append(sets, "model_allowlist=?")
		args = append(args, joinList(*allowlist))
	}
	if expires != nil {
		sets = append(sets, "expires_at=?")
		args = append(args, nullableTime(expires))
	}
	if len(sets) == 0 {
		return nil
	}
	sets = append(sets, "updated_at=?")
	args = append(args, formatTime(at), id)
	result, err := tx.ExecContext(ctx, `UPDATE client_keys SET `+strings.Join(sets, ",")+` WHERE id=?`, args...)
	if err != nil {
		return err
	}
	return requireChanged(result)
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func nullableTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return formatTime(*value)
}

func requireChanged(result sql.Result) error {
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return ErrNotFound
	}
	return nil
}

func isUniqueError(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "unique") || strings.Contains(text, "constraint failed")
}

func NewID(prefix string) string {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
	}
	return prefix + "_" + hex.EncodeToString(raw[:])
}
