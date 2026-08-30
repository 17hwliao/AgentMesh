-- AgentMesh 013: durable tenants, ordered model routes, and derived API keys.
-- Apply explicitly on MySQL 8 before AGENTMESH_AUTH_STORE=mysql is enabled.
CREATE TABLE IF NOT EXISTS tenants (
    tenant_id VARCHAR(64) NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_at DATETIME(6) NOT NULL,
    updated_at DATETIME(6) NOT NULL,
    PRIMARY KEY (tenant_id)
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS tenant_model_routes (
    tenant_id VARCHAR(64) NOT NULL,
    model VARCHAR(128) NOT NULL,
    ordinal BIGINT UNSIGNED NOT NULL,
    provider VARCHAR(16) NOT NULL,
    PRIMARY KEY (tenant_id, model, ordinal),
    UNIQUE KEY uq_tenant_model_routes_provider (tenant_id, model, provider),
    CONSTRAINT fk_tenant_model_routes_tenant FOREIGN KEY (tenant_id) REFERENCES tenants (tenant_id),
    CONSTRAINT chk_tenant_model_routes_ordinal CHECK (ordinal > 0),
    CONSTRAINT chk_tenant_model_routes_provider CHECK (provider IN ('mock', 'ark', 'ollama'))
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS api_keys (
    key_id CHAR(36) NOT NULL,
    tenant_id VARCHAR(64) NOT NULL,
    key_prefix CHAR(8) NOT NULL,
    key_hash BINARY(32) NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_at DATETIME(6) NOT NULL,
    revoked_at DATETIME(6) NULL,
    PRIMARY KEY (key_id),
    UNIQUE KEY uq_api_keys_prefix (key_prefix),
    KEY ix_api_keys_tenant (tenant_id),
    CONSTRAINT fk_api_keys_tenant FOREIGN KEY (tenant_id) REFERENCES tenants (tenant_id)
) ENGINE=InnoDB;
