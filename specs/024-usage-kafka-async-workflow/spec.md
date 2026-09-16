# Spec: Usage Kafka Async Workflow

## Scope

Project the existing durable `usage_outbox` ledger into Kafka and provide an
idempotent consumer projection. This change does not move quota decisions to
Kafka and does not replace MySQL reconciliation.

## Event contract

- Source topic: `agentmesh.usage.v1`; dead-letter topic:
  `agentmesh.usage.v1.dlq`.
- Every event has schema version `1`, a deterministic `event_id`, reservation,
  tenant, request and model identifiers, final state, operation version,
  reserved/settled/released units, usage-observed flag, settlement kind and
  finalization time.
- The payload is strictly encoded and decoded. Unknown fields and trailing JSON
  are rejected so accidental expansion is visible in tests.
- Prompt, response, API key, bearer token, raw token text, endpoint and provider
  secrets are outside the event contract and must never be serialized.

## Reliability boundary

- Terminal Reservation state, `usage_outbox`, and `usage_kafka_outbox` are
  inserted in one InnoDB transaction. A failed outbox write prevents the
  terminal state from committing.
- The relay publishes an outbox row and marks it published only after Kafka
  acknowledges it. Publish failures retain the row and schedule exponential
  backoff; duplicate publication is expected.
- The consumer commits its Kafka offset only after the projection transaction
  commits. A repeated `event_id` is a no-op; a different event for the same
  reservation is a conflict and is not silently overwritten.
- After the configured retry budget, the consumer writes a sanitized envelope
  to the DLQ and then commits the original message. A DLQ publish failure
  leaves the original uncommitted for later retry.
- Kafka is an asynchronous projection and is not the source of truth for quota
  authorization or settlement reconciliation.

## Acceptance criteria

1. Safe event serialization and sensitive-field rejection are covered by unit
   tests.
2. Outbox insertion, publish success/failure/backoff, event-id idempotence,
   consumer redelivery, retry and DLQ behavior are covered by memory tests.
3. MySQL pending, retry SQL, row-lock/idempotent projection, conflict and
   transaction commit/rollback semantics are covered by sqlmock contract tests.
4. Migration, Compose services and a smoke command are reproducible locally.
5. Real Kafka/MySQL smoke results are reported separately from deterministic
   unit/contract test results.
