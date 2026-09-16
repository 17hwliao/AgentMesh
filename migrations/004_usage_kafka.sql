-- AgentMesh 018: Kafka projection outbox and idempotent consumer projection.
-- Apply after 002_usage_ledger.sql. This migration stores only the existing
-- safe usage snapshot; prompt, response, API key and raw token data are absent.
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
    UNIQUE KEY uq_usage_kafka_projection_reservation (reservation_id),
    CONSTRAINT chk_usage_kafka_projection_state CHECK (final_state IN ('settled', 'cancelled'))
) ENGINE=InnoDB;
