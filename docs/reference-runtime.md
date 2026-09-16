# Single-host reference runtime

This is a deployable local reference runtime, not a public production claim.
It starts MySQL, Redis, Kafka, migration initialization, API, usage relay and
usage worker with a mock Provider; it does not require Ollama.

```powershell
docker compose up --build -d --wait
.\scripts\verify-reference-runtime.ps1
docker compose logs -f api usage-kafka-relay usage-kafka-worker
```

The host-exposed API port is intentionally loopback-only:

```text
http://127.0.0.1:18080
```

The Compose API Key is a local-only fixture:

```text
local-demo-api-key-123456
```

Never reuse it outside the local reference runtime. The normal verification
flow proves that a mock chat terminal state creates a durable outbox row, the
relay publishes it without manually invoking `RunOnce`, and the worker creates
an idempotent projection.

The API also uses the real Redis Lua Token Bucket on database 1. Requests are
limited after API Key authentication using their trusted tenant identity, not a
client-provided tenant field. The Lua script reads Redis server time, refills
and consumes a token atomically, then applies a TTL; separate API processes
therefore share one tenant bucket instead of each admitting its own burst.

The Compose file starts one relay, but the outbox schema now supports multiple
relay processes: each records a short ownership lease before publishing. This
is crash-recoverable and prevents concurrent relays from owning the same row;
Kafka delivery is still deliberately at-least-once. The runtime remains a
single-host reference topology, not a multi-broker HA deployment.

Useful inspection commands:

```powershell
docker compose ps
docker compose logs --tail=100 usage-kafka-relay usage-kafka-worker
docker compose exec mysql mysql -uroot -pagentmesh-local-only agentmesh_control
Invoke-RestMethod -Uri http://127.0.0.1:18080/admin/usage/outbox-summary -Headers @{ Authorization = 'Bearer local-demo-admin-token-only' }
```

The administrative summary exposes only aggregate `pending_outbox`,
`published_outbox`, and `projections` counts. It is protected by the separate
admin credential and never returns event payloads or client request content.
