# Plan: Usage Kafka Async Workflow

## Design

1. Keep the existing `quota_reservations` and `usage_outbox` ledger unchanged
   as the durable source. Add `usage_kafka_outbox` with a unique reservation
   key and deterministic event id.
2. Build `internal/usagekafka` with a safe event model, MySQL store, relay,
   Kafka adapters, retry/backoff policy, DLQ envelope, and in-memory adapters.
3. Add `usage_kafka_projections` with unique event and reservation keys so
   duplicate delivery is harmless and divergent history is detected.
4. Provide an isolated Kafka/MySQL Compose stack and a Go smoke command using
   dedicated ports and environment variables.

## Verification

- Unit tests: model sanitization, strict decoding, retry delay and in-memory
  relay/worker behavior.
- Contract tests: sqlmock query arguments, row locking, transaction boundaries,
  retry updates and conflict handling.
- Repository tests: terminal ledger and Kafka outbox are executed in the same
  transaction; non-terminal `MarkReserved` does not write a usage event.
- Static checks: `go test ./...`, `go vet ./...`, `go build -buildvcs=false
  ./...`.
- Real smoke, when Docker is available: start the isolated Compose stack,
  run `cmd/usage-kafka-smoke`, inspect its JSON result, then tear down the
  stack. If unavailable, report it as not run rather than inferred.
