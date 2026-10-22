-- Private bounded image storage. Tenant identity is always obtained from auth.
CREATE TABLE IF NOT EXISTS private_images (
    image_id CHAR(36) NOT NULL PRIMARY KEY,
    tenant_id VARCHAR(64) NOT NULL,
    media_type VARCHAR(16) NOT NULL,
    byte_size INT UNSIGNED NOT NULL,
    sha256 CHAR(64) NOT NULL,
    content MEDIUMBLOB NOT NULL,
    created_at DATETIME(6) NOT NULL,
    expires_at DATETIME(6) NOT NULL,
    KEY ix_private_images_tenant (tenant_id, image_id),
    KEY ix_private_images_expiry (expires_at),
    CONSTRAINT fk_private_images_tenant FOREIGN KEY (tenant_id) REFERENCES tenants (tenant_id),
    CONSTRAINT chk_private_images_size CHECK (byte_size > 0 AND byte_size <= 262144)
) ENGINE=InnoDB;
