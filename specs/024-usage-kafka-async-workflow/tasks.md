# Tasks: Usage Kafka Async Workflow

- [x] Define safe versioned usage event and strict JSON contract.
- [x] Add deterministic event id and unique source reservation constraint.
- [x] Bind terminal ledger, usage outbox and Kafka outbox writes to one MySQL
  transaction.
- [x] Implement relay publish acknowledgement and exponential retry/backoff.
- [x] Implement consumer projection idempotence, conflict detection and offset
  commit ordering.
- [x] Implement bounded retries and sanitized DLQ publication.
- [x] Add in-memory broker/store adapters for deterministic tests.
- [x] Add sqlmock MySQL SQL/transaction contract tests.
- [x] Add migration and isolated Kafka/MySQL Compose stack.
- [x] Add reproducible smoke command and document its environment.
- [x] Run real Kafka/MySQL smoke on a Docker-enabled host and record the result
  in the task handoff.
