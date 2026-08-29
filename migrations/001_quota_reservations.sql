-- AgentMesh 006: durable evidence for quota reservations and provider attempts.
-- Apply once on MySQL 8 before enabling AGENTMESH_QUOTA_MODE=reservation.
CREATE TABLE quota_reservations (
    reservation_id CHAR(36) NOT NULL,
    tenant_id VARCHAR(128) NOT NULL,
    request_id VARCHAR(128) NOT NULL,
    model VARCHAR(128) NOT NULL,
    state VARCHAR(32) NOT NULL,
    version BIGINT UNSIGNED NOT NULL,
    reserved_units BIGINT UNSIGNED NOT NULL,
    settled_units BIGINT UNSIGNED NOT NULL DEFAULT 0,
    released_units BIGINT UNSIGNED NOT NULL DEFAULT 0,
    usage_observed BOOLEAN NOT NULL DEFAULT FALSE,
    settlement_kind VARCHAR(32) NULL,
    heartbeat_at DATETIME(6) NOT NULL,
    created_at DATETIME(6) NOT NULL,
    updated_at DATETIME(6) NOT NULL,
    PRIMARY KEY (reservation_id),
    UNIQUE KEY uq_quota_reservations_tenant_request (tenant_id, request_id),
    KEY ix_quota_reservations_reconcile (state, heartbeat_at),
    CONSTRAINT chk_quota_reservations_state CHECK (state IN ('creating', 'reserved', 'settled', 'cancelled', 'expired_pending'))
) ENGINE=InnoDB;

CREATE TABLE provider_attempts (
    attempt_id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    reservation_id CHAR(36) NOT NULL,
    ordinal BIGINT UNSIGNED NOT NULL,
    provider_name VARCHAR(128) NOT NULL,
    model VARCHAR(128) NOT NULL,
    result_code VARCHAR(64) NULL,
    started_at DATETIME(6) NOT NULL,
    finished_at DATETIME(6) NULL,
    heartbeat_at DATETIME(6) NOT NULL,
    forwarded_runes BIGINT UNSIGNED NOT NULL DEFAULT 0,
    provider_input_units BIGINT UNSIGNED NULL,
    provider_output_units BIGINT UNSIGNED NULL,
    usage_observed BOOLEAN NOT NULL DEFAULT FALSE,
    PRIMARY KEY (attempt_id),
    UNIQUE KEY uq_provider_attempts_reservation_ordinal (reservation_id, ordinal),
    KEY ix_provider_attempts_reservation_started (reservation_id, started_at),
    CONSTRAINT fk_provider_attempts_reservation FOREIGN KEY (reservation_id) REFERENCES quota_reservations (reservation_id)
) ENGINE=InnoDB;
