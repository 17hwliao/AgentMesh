-- AgentMesh 011: immutable terminal usage snapshots and their projections.
-- Apply after 001_quota_reservations.sql and before enabling 011 code.
CREATE TABLE IF NOT EXISTS usage_outbox (
    reservation_id CHAR(36) NOT NULL,
    tenant_id VARCHAR(128) NOT NULL,
    request_id VARCHAR(128) NOT NULL,
    model VARCHAR(128) NOT NULL,
    final_state VARCHAR(32) NOT NULL,
    operation_version BIGINT UNSIGNED NOT NULL,
    reserved_units BIGINT UNSIGNED NOT NULL,
    settled_units BIGINT UNSIGNED NOT NULL,
    released_units BIGINT UNSIGNED NOT NULL,
    usage_observed BOOLEAN NOT NULL,
    settlement_kind VARCHAR(32) NOT NULL,
    finalized_at DATETIME(6) NOT NULL,
    projected_at DATETIME(6) NULL,
    created_at DATETIME(6) NOT NULL,
    PRIMARY KEY (reservation_id),
    KEY ix_usage_outbox_unprojected (projected_at, created_at),
    CONSTRAINT fk_usage_outbox_reservation FOREIGN KEY (reservation_id) REFERENCES quota_reservations (reservation_id),
    CONSTRAINT chk_usage_outbox_final_state CHECK (final_state IN ('settled', 'cancelled'))
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS usage_records (
    reservation_id CHAR(36) NOT NULL,
    tenant_id VARCHAR(128) NOT NULL,
    request_id VARCHAR(128) NOT NULL,
    model VARCHAR(128) NOT NULL,
    final_state VARCHAR(32) NOT NULL,
    operation_version BIGINT UNSIGNED NOT NULL,
    reserved_units BIGINT UNSIGNED NOT NULL,
    settled_units BIGINT UNSIGNED NOT NULL,
    released_units BIGINT UNSIGNED NOT NULL,
    usage_observed BOOLEAN NOT NULL,
    settlement_kind VARCHAR(32) NOT NULL,
    finalized_at DATETIME(6) NOT NULL,
    recorded_at DATETIME(6) NOT NULL,
    PRIMARY KEY (reservation_id),
    CONSTRAINT fk_usage_records_outbox FOREIGN KEY (reservation_id) REFERENCES usage_outbox (reservation_id),
    CONSTRAINT chk_usage_records_final_state CHECK (final_state IN ('settled', 'cancelled'))
) ENGINE=InnoDB;
