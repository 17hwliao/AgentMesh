// Package imagestore owns small private user uploads, not a public image CDN.
package imagestore

import (
	"agentmesh/internal/provider"
	"agentmesh/internal/tenant"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"regexp"
	"strings"
	"time"
)

const MaxBytes = provider.MaxInlineImageBytes
const MaxTenantBytes = 16 << 20
const MaxTenantImages = 100
const Retention = 7 * 24 * time.Hour

var ErrInvalid = errors.New("invalid_image")
var ErrNotFound = errors.New("image_not_found")
var ErrQuota = errors.New("image_quota_exceeded")
var identifier = regexp.MustCompile(`^img_[a-f0-9]{32}$`)

type Image struct {
	ID        string    `json:"id"`
	MediaType string    `json:"media_type"`
	Bytes     int       `json:"bytes"`
	SHA256    string    `json:"sha256"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
	Reference string    `json:"reference"`
	Content   []byte    `json:"-"`
}
type Store struct{ DB *sql.DB }

func Validate(raw []byte, media string) error {
	if len(raw) == 0 || len(raw) > MaxBytes || (media != "image/png" && media != "image/jpeg") {
		return ErrInvalid
	}
	cfg, kind, e := image.DecodeConfig(bytes.NewReader(raw))
	if e != nil || cfg.Width < 1 || cfg.Height < 1 || cfg.Width > 2048 || cfg.Height > 2048 || "image/"+kind != media {
		return ErrInvalid
	}
	// Decode the bounded dimensions too: a header-only/truncated image is not a
	// valid persisted upload, even if an upstream might accept its header.
	if _, _, e = image.Decode(bytes.NewReader(raw)); e != nil {
		return ErrInvalid
	}
	return nil
}
func (s *Store) Put(ctx context.Context, id, media string, raw []byte) (Image, error) {
	if !tenant.ValidID(id) || Validate(raw, media) != nil {
		return Image{}, ErrInvalid
	}
	var nonce [16]byte
	if _, e := rand.Read(nonce[:]); e != nil {
		return Image{}, e
	}
	sum := sha256.Sum256(raw)
	now := time.Now().UTC()
	out := Image{ID: "img_" + hex.EncodeToString(nonce[:]), MediaType: media, Bytes: len(raw), SHA256: hex.EncodeToString(sum[:]), CreatedAt: now, ExpiresAt: now.Add(Retention)}
	out.Reference = "agentmesh://images/" + out.ID
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return Image{}, e
	}
	defer tx.Rollback()
	// Serialize per-tenant uploads and account disabling, including empty stores.
	var enabled bool
	e = tx.QueryRowContext(ctx, "SELECT enabled FROM tenants WHERE tenant_id=? FOR UPDATE", id).Scan(&enabled)
	if errors.Is(e, sql.ErrNoRows) || e == nil && !enabled {
		return Image{}, ErrNotFound
	}
	if e != nil {
		return Image{}, e
	}
	var count, total int64
	// Expired bytes still occupy storage until cleanup commits. Counting them
	// prevents a stopped/slow janitor from permitting unbounded expired data.
	if e = tx.QueryRowContext(ctx, "SELECT COUNT(*),COALESCE(SUM(byte_size),0) FROM private_images WHERE tenant_id=?", id).Scan(&count, &total); e != nil {
		return Image{}, e
	}
	if count >= MaxTenantImages || total+int64(len(raw)) > MaxTenantBytes {
		return Image{}, ErrQuota
	}
	_, e = tx.ExecContext(ctx, "INSERT INTO private_images(image_id,tenant_id,media_type,byte_size,sha256,content,created_at,expires_at) VALUES (?,?,?,?,?,?,?,?)", out.ID, id, media, len(raw), out.SHA256, raw, now, out.ExpiresAt)
	if e != nil {
		return Image{}, e
	}
	return out, tx.Commit()
}
func (s *Store) Get(ctx context.Context, owner, id string) (Image, error) {
	if !identifier.MatchString(id) || !tenant.ValidID(owner) {
		return Image{}, ErrNotFound
	}
	var out Image
	e := s.DB.QueryRowContext(ctx, "SELECT i.image_id,i.media_type,i.byte_size,i.sha256,i.content,i.created_at,i.expires_at FROM private_images i JOIN tenants t ON t.tenant_id=i.tenant_id WHERE i.tenant_id=? AND i.image_id=? AND i.expires_at>? AND t.enabled=TRUE", owner, id, time.Now().UTC()).Scan(&out.ID, &out.MediaType, &out.Bytes, &out.SHA256, &out.Content, &out.CreatedAt, &out.ExpiresAt)
	if errors.Is(e, sql.ErrNoRows) {
		return out, ErrNotFound
	}
	if e != nil {
		return out, e
	}
	out.Reference = "agentmesh://images/" + out.ID
	return out, nil
}
func (s *Store) Delete(ctx context.Context, owner, id string) error {
	if !identifier.MatchString(id) || !tenant.ValidID(owner) {
		return ErrNotFound
	}
	result, e := s.DB.ExecContext(ctx, "DELETE FROM private_images WHERE tenant_id=? AND image_id=?", owner, id)
	if e != nil {
		return e
	}
	n, e := result.RowsAffected()
	if e != nil {
		return e
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
func (s *Store) PurgeExpired(ctx context.Context) error {
	_, e := s.DB.ExecContext(ctx, "DELETE FROM private_images WHERE expires_at<=? LIMIT 100", time.Now().UTC())
	return e
}
func (s *Store) Resolve(ctx context.Context, owner string, messages []provider.Message) error {
	// Check count before any database work; do not permit arbitrary URL fetching.
	count := 0
	for _, m := range messages {
		for _, p := range m.Parts {
			if p.ImageURL != nil && strings.HasPrefix(p.ImageURL.URL, "agentmesh:") {
				count++
			}
		}
	}
	if count > provider.MaxImages {
		return ErrInvalid
	}
	for i := range messages {
		for j := range messages[i].Parts {
			p := &messages[i].Parts[j]
			if p.ImageURL == nil || !strings.HasPrefix(p.ImageURL.URL, "agentmesh:") {
				continue
			}
			if messages[i].Role != "user" || p.Type != "image_url" || !strings.HasPrefix(p.ImageURL.URL, "agentmesh://images/") {
				return ErrInvalid
			}
			row, e := s.Get(ctx, owner, strings.TrimPrefix(p.ImageURL.URL, "agentmesh://images/"))
			if e != nil {
				return e
			}
			// Copy before rewriting so shared caller objects cannot retain encoded data.
			value := *p.ImageURL
			value.URL = "data:" + row.MediaType + ";base64," + base64.StdEncoding.EncodeToString(row.Content)
			p.ImageURL = &value
		}
	}
	return nil
}
