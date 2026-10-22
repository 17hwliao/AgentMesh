package imagestore

import (
	"context"
	"time"
)

func (s *Store) List(ctx context.Context, owner, after string) ([]Image, error) {
	if after != "" && !identifier.MatchString(after) {
		return nil, ErrInvalid
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT i.image_id,i.media_type,i.byte_size,i.sha256,i.created_at,i.expires_at FROM private_images i JOIN tenants t ON t.tenant_id=i.tenant_id WHERE i.tenant_id=? AND i.image_id>? AND i.expires_at>? AND t.enabled=TRUE ORDER BY i.image_id LIMIT 50`, owner, after, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Image{}
	for rows.Next() {
		var image Image
		if err = rows.Scan(&image.ID, &image.MediaType, &image.Bytes, &image.SHA256, &image.CreatedAt, &image.ExpiresAt); err != nil {
			return nil, err
		}
		image.Reference = "agentmesh://images/" + image.ID
		out = append(out, image)
	}
	return out, rows.Err()
}
