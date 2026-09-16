CREATE TABLE IF NOT EXISTS quota_reservations (
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
    KEY ix_quota_reservations_reconcile (state, heartbeat_at)
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS provider_attempts (
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
    CONSTRAINT fk_provider_attempts_reservation FOREIGN KEY (reservation_id) REFERENCES quota_reservations (reservation_id)
) ENGINE=InnoDB;

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
    CONSTRAINT fk_usage_outbox_reservation FOREIGN KEY (reservation_id) REFERENCES quota_reservations (reservation_id)
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS usage_kafka_outbox (
    event_id CHAR(64) NOT NULL,
    reservation_id CHAR(36) NOT NULL,
    operation_version BIGINT UNSIGNED NOT NULL,
    topic VARCHAR(249) NOT NULL,
    payload JSON NOT NULL,
    attempts INT UNSIGNED NOT NULL DEFAULT 0,
    available_at DATETIME(6) NOT NULL,
    published_at DATETIME(6) NULL,
    last_error VARCHAR(256) NOT NULL DEFAULT '',
    created_at DATETIME(6) NOT NULL,
    PRIMARY KEY (event_id),
    UNIQUE KEY uq_usage_kafka_outbox_reservation (reservation_id),
    KEY ix_usage_kafka_outbox_ready (published_at, available_at),
    CONSTRAINT fk_usage_kafka_outbox_usage FOREIGN KEY (reservation_id) REFERENCES usage_outbox (reservation_id)
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS usage_kafka_projections (
    event_id CHAR(64) NOT NULL,
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
    projected_at DATETIME(6) NOT NULL,
    PRIMARY KEY (event_id),
    UNIQUE KEY uq_usage_kafka_projection_reservation (reservation_id)
) ENGINE=InnoDB;
