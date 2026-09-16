# Spec: Single-Host Compose Reference Runtime

## Goal

Assemble the existing AgentMesh reliability components into a locally deployable
single-host reference runtime. The target is:

```text
docker compose up --build
  → MySQL / Redis / Kafka / topic-init are ready
  → migrations and bootstrap complete
  → API, usage relay and usage worker stay running
  → a request can be sent and its terminal usage can be observed through
    outbox, Kafka projection and DLQ state
```

This is a **local, single-host reference architecture**. It must not be
described as a public Internet production deployment or as multi-instance
high-availability infrastructure.

## Existing components to assemble

- Reservation terminal state, usage ledger and `usage_kafka_outbox` are already
  written in one MySQL transaction.
- `usagekafka.Relay.RunOnce` already scans due pending rows, publishes them and
  stores success/failure/backoff state.
- The Kafka worker, idempotent projection, retry and DLQ behavior already exist.
- MySQL/Kafka smoke verification and a dependency-only Compose file already
  exist.

The missing scope is the process, startup and verification assembly layer; this
spec does not add a new quota source of truth or new business semantics.

## Runtime topology

The final Compose topology contains these services:

```text
mysql
redis
kafka
topic-init
migration-init
api
usage-kafka-relay
usage-kafka-worker
```

- `mysql`, `redis` and `kafka` are single local dependency instances.
- `topic-init` creates `agentmesh.usage.v1` and `agentmesh.usage.v1.dlq` only
  after Kafka is healthy.
- `migration-init` applies the required AgentMesh schema migrations and performs
  the explicit local bootstrap needed for API Key/tenant startup. It exits only
  after success and must be safe to rerun.
- `api` is the existing long-running HTTP service. The default provider is mock;
  Ollama remains opt-in and must not be required for Compose to become ready.
- `usage-kafka-relay` is a separate long-running process that repeatedly invokes
  the existing relay logic.
- `usage-kafka-worker` is a separate long-running Kafka consumer process that
  applies the existing idempotent projection and DLQ behavior.

`api`, `usage-kafka-relay` and `usage-kafka-worker` may share one Go image and
use different commands. They must not be hidden goroutines inside the API
process: independent processes make restart state and operational ownership
observable.

## Service lifecycle contracts

### Relay

Provide a dedicated command, such as `cmd/usage-kafka-relay`.

It must:

1. Open the configured MySQL outbox store and Kafka writer.
2. Construct the existing `usagekafka.Relay`.
3. Poll at a configurable interval (default: one second) until context
   cancellation.
4. Invoke `Relay.RunOnce(ctx)` on each poll; `available_at` remains the source
   of truth for whether a failed row is eligible to retry.
5. Log scan/publish/error counts without logging prompt, response, API Key or
   secret material.
6. Stop promptly and cleanly on SIGINT/SIGTERM.

The ticker controls scan frequency. It must not override retry backoff stored in
`usage_kafka_outbox.available_at`.

### Kafka worker

Provide a dedicated command, such as `cmd/usage-kafka-worker`.

It must:

1. Open the configured Kafka reader with a stable local consumer group and the
   MySQL projection store.
2. Provide the configured DLQ writer.
3. Run the existing worker until context cancellation.
4. Commit a Kafka offset only after the existing projection/DLQ contract has
   completed successfully.
5. Stop receiving new work during shutdown and allow in-flight work to finish
   or be safely redelivered.

## Startup and configuration contracts

1. `api`, relay and worker start only after `migration-init` completed
   successfully.
2. Worker also starts only after Kafka and `topic-init` completed successfully.
3. API, relay and worker receive the same explicit MySQL, Redis and Kafka
   endpoint configuration through Compose environment variables.
4. Default Compose configuration is local-only and contains no real provider
   credentials.
5. Health checks must distinguish a running container from a dependency-ready
   service. A process with unusable MySQL/Kafka configuration must fail clearly,
   not claim readiness.

## Required observability

The first runtime version must make these states inspectable without adding a
Prometheus/Grafana dependency:

```text
API liveness/readiness
relay last scan result and publish/failure counters
pending outbox count
worker last successful consumption time
projection count
DLQ count
```

Structured logs plus `/healthz`, `/readyz` and a safe administrative usage
summary endpoint are sufficient. Administrative output must never include model
prompts, responses, API Keys, bearer tokens or provider secrets.

## Acceptance criteria

### Compose startup

1. On a Docker-only machine, `docker compose up --build` starts the topology
   without requiring local Ollama or manually running a smoke command.
2. MySQL, Redis, Kafka and topics become ready before application processes are
   considered ready.
3. Migrations and local bootstrap complete exactly once per successful startup
   and safely tolerate a restart.
4. API, relay and worker are separately visible as long-running services.

### Normal terminal usage flow

1. An authenticated mock-backed API request reaches a terminal Reservation
   state.
2. The terminal transaction creates usage ledger and Kafka outbox facts.
3. Without manually invoking `Relay.RunOnce` or the smoke command, the relay
   publishes the pending event and sets `published_at`.
4. The worker consumes the event and exactly one projection is visible for the
   Reservation.

### Failure and recovery flow

1. When Kafka is unavailable, the relay preserves the outbox row, increases
   attempts and moves `available_at` forward according to backoff.
2. After Kafka is restored, the relay automatically publishes the due event and
   the worker eventually creates its projection.
3. A relay crash after a broker acknowledgement may redeliver an event; the
   projection remains one row per Reservation.
4. A repeated worker delivery remains idempotent.
5. Repeated worker processing failure produces a sanitized DLQ envelope and is
   observable through the runtime status surface.

### Verification artefacts

1. Keep unit and SQL contract tests for relay, worker and idempotence.
2. Add Compose-level automated verification for normal flow, Kafka outage and
   recovery, duplicate delivery and DLQ behavior.
3. The verification script prints explicit pass/fail evidence and cleans up only
   resources it created.

## Non-goals for this specification

- Multi-Relay claiming, leasing or `SELECT ... FOR UPDATE SKIP LOCKED`.
- Multi-instance API deployment and globally shared Redis rate limiting.
- Kafka TLS, ACLs, multi-broker replication or cross-region recovery.
- A claim that this Compose topology is public production infrastructure.
- Changing the existing MySQL-based quota authorization source of truth.

## Follow-up evolution

After this local reference runtime is accepted, multi-instance safety can be
added by claiming outbox rows with `lease_owner` / `lease_until` or database row
locking. Multi-API rate limiting can then evolve from the current process-local
Token Bucket to a Redis Lua-backed shared limiter.
