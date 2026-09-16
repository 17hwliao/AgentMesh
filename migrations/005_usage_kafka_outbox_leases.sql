-- AgentMesh 026: bounded ownership lease for concurrent usage outbox relays.
-- This migration is applied once by the existing schema_migrations runner.
-- Lease expiry permits crash recovery; Kafka delivery remains at-least-once.
ALTER TABLE usage_kafka_outbox
    ADD COLUMN lease_owner VARCHAR(128) NULL AFTER available_at,
    ADD COLUMN lease_until DATETIME(6) NULL AFTER lease_owner,
    ADD KEY ix_usage_kafka_outbox_claim (published_at, available_at, lease_until, created_at);
