"""Run only against the explicitly isolated codex-backend-acceptance project."""
import hashlib
import hmac
import json
from pathlib import Path
import ssl
import socket
import subprocess
import time
import urllib.error
import urllib.request
import uuid

ROOT = Path(__file__).resolve().parent
COMPOSE = ["docker", "compose", "-p", "codex-backend-acceptance", "-f", "compose.yml", "-f", "acceptance.yml"]
RESULTS = []

def secret(name):
    return (ROOT / "secrets" / name).read_text(encoding="utf-8").strip()

def command(*args, input=None):
    result = subprocess.run(COMPOSE + list(args), input=input, capture_output=True, cwd=ROOT, text=True, encoding="utf-8", errors="replace", timeout=100)
    if result.returncode:
        raise AssertionError("isolated Compose operation failed: " + args[0])
    return result.stdout.strip()

def sql(query, database="agentmesh", service="control"):
    return command("exec", "-T", service, "sh", "-c", 'MYSQL_PWD=$(cat /run/secrets/mysql_root) exec mysql -uroot --batch --skip-column-names "$1"', "sh", database, input=query + ";\n")

def redis(*args):
    return command("exec", "-T", "redis", "sh", "-c", 'REDISCLI_AUTH=$(cat /run/secrets/redis_password) exec redis-cli --raw "$@"', "sh", *args)

def request(path, data=None, token=None, port=29080, method=None, headers=None, context=None):
    url = path if path.startswith("http") else f"http://127.0.0.1:{port}" + path
    head = {"Content-Type": "application/json", **(headers or {})}
    if token:
        head["Authorization"] = "Bearer " + token
    body = data if isinstance(data, bytes) else json.dumps(data).encode() if data is not None else None
    req = urllib.request.Request(url, body, head, method=method)
    try:
        response = urllib.request.urlopen(req, timeout=30, context=context)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        return response.status, dict(response.headers), response.read()

def expect(code, expected):
    assert code == expected, f"HTTP status {code}, expected {expected}"

def wait_job(delivery, target="COMPLETED"):
    deadline = time.monotonic() + 45
    while time.monotonic() < deadline:
        code, _, raw = request("/jobs/" + delivery, token=secret("sentinel_token"), port=29081)
        expect(code, 200)
        value = json.loads(raw)
        if value["status"] == target:
            return value
        if value["status"] in {"REJECTED", "DEAD_LETTER"} and target != value["status"]:
            raise AssertionError("job reached " + value["status"])
        time.sleep(0.5)
    raise AssertionError("job did not finish within acceptance deadline")

CHAT = {"model": "sql-review", "messages": [{"role": "user", "content": "hello"}], "stream": True, "max_tokens": 100, "temperature": 0, "stream_options": {"include_usage": True}}
QUERY = "SELECT id, status, created_at FROM orders WHERE user_id=42 AND status='paid' ORDER BY created_at DESC LIMIT 20"
SPEC = {"table": "orders", "index_name": "idx_cand_acceptance", "columns": [{"name": "user_id"}, {"name": "status"}, {"name": "created_at", "direction": "DESC"}]}

def check(name, function):
    started = time.monotonic()
    function()
    RESULTS.append({"name": name, "status": "PASS", "seconds": round(time.monotonic() - started, 3)})
    print("PASS " + name, flush=True)

def health_auth():
    expect(request("/readyz")[0], 200)
    expect(request("/readyz", port=29081)[0], 200)
    expect(request("/v1/chat/completions", CHAT)[0], 401)
    expect(request("/jobs/missing", port=29081)[0], 401)
    expect(request("/admin/tenants", {"tenant_id": "invalid"}, port=29880)[0], 404)
    ca = command("exec", "-T", "proxy", "cat", "/data/caddy/pki/authorities/local/root.crt")
    context = ssl.create_default_context(cadata=ca)
    expect(request("https://localhost:29443/readyz", context=context)[0], 200)

def chat_embedding():
    projected_before = int(sql("SELECT COUNT(*) FROM usage_kafka_projections"))
    before = int(redis("GET", "agentmesh:quota:v1:balance:operator"))
    code, headers, raw = request("/v1/chat/completions", CHAT, secret("bootstrap_key"), port=29880)
    expect(code, 200)
    assert b"fixture response" in raw and b"[DONE]" in raw
    assert int(redis("GET", "agentmesh:quota:v1:balance:operator")) == before - 150
    trace = headers.get("X-Agentmesh-Trace-Id") or headers.get("X-AgentMesh-Trace-ID")
    assert trace
    expect(request("/v1/observability/traces/" + trace, token=secret("bootstrap_key"))[0], 200)
    before = int(redis("GET", "agentmesh:quota:v1:balance:operator"))
    code, _, raw = request("/v1/embeddings", {"model": "embedding", "input": ["hello", "world"]}, secret("bootstrap_key"))
    expect(code, 200)
    assert len(json.loads(raw)["data"]) == 2
    assert int(redis("GET", "agentmesh:quota:v1:balance:operator")) == before - 4096
    deadline = time.monotonic() + 15
    while time.monotonic() < deadline:
        if int(sql("SELECT COUNT(*) FROM usage_kafka_projections")) >= projected_before + 2:
            return
        time.sleep(0.5)
    raise AssertionError("usage projection not drained")

def manual_and_model():
    for auto in [False, True]:
        delivery = "accept-" + uuid.uuid4().hex
        payload = {"delivery_id": delivery, "pr_number": 1, "sql": QUERY}
        if not auto:
            payload["candidate_spec"] = SPEC
        code, _, raw = request("/jobs", payload, secret("sentinel_token"), port=29081)
        expect(code, 202)
        wait_job(delivery)
        code, _, raw = request("/jobs/" + delivery + "/report", token=secret("sentinel_token"), port=29081)
        expect(code, 200)
        report = json.loads(raw)
        assert len(report["pipeline"]["completed_steps"]) == 4
        assert report["pipeline"]["eligible_for_performance_claim"] is False
        code, _, raw = request("/jobs", payload, secret("sentinel_token"), port=29081)
        expect(code, 202)
        assert json.loads(raw)["duplicate"] is True
        expect(request("/jobs", {**payload, "sql": "SELECT id FROM orders WHERE user_id=7 LIMIT 10"}, secret("sentinel_token"), port=29081)[0], 409)
        assert sql("SELECT COUNT(DISTINCT INDEX_NAME) FROM information_schema.STATISTICS WHERE TABLE_SCHEMA='sentinel' AND TABLE_NAME='orders' AND INDEX_NAME!='PRIMARY'", "sentinel", "candidate") == "0"
    expect(request("/jobs", {"delivery_id": "bad-" + uuid.uuid4().hex, "pr_number": 1, "sql": "DELETE FROM orders"}, secret("sentinel_token"), port=29081)[0], 400)

def native_webhook():
    delivery = uuid.uuid4().hex
    event = {"action": "synchronize", "number": 1, "repository": {"full_name": "17hwliao/sql-sentinel"}, "pull_request": {"head": {"sha": "1" * 40}}}
    body = json.dumps(event).encode()
    headers = {"X-GitHub-Event": "pull_request", "X-GitHub-Delivery": delivery, "X-Hub-Signature-256": "sha256=" + hmac.new(secret("webhook_secret").encode(), body, hashlib.sha256).hexdigest()}
    expect(request("/webhooks/github", body, port=29081)[0], 401)
    code, _, raw = request("/webhooks/github", body, port=29081, headers=headers)
    expect(code, 202)
    jobs = json.loads(raw)["jobs"]
    assert len(jobs) == 1 and jobs[0]["start_line"] == 1 and jobs[0]["end_line"] == 4
    wait_job(jobs[0]["delivery_id"])
    assert sql("SELECT LEFT(CAST(sql_text AS CHAR),6) FROM sql_sentinel_jobs WHERE delivery_id='" + jobs[0]["delivery_id"] + "'", "sentinel_control") == "SELECT"
    marker = json.loads(request("/jobs/" + jobs[0]["delivery_id"], token=secret("sentinel_token"), port=29081)[2])["job_id"]
    deadline = time.monotonic() + 25
    while time.monotonic() < deadline:
        state = json.loads(request("/control", port=29090)[2])
        matching = [c for c in (state["comments"] or []) if marker in c["body"]]
        if matching:
            assert len(matching) == 1
            assert "L1" in matching[0]["body"]
            break
        time.sleep(0.5)
    else:
        raise AssertionError("fixture comment not published")
    code, _, raw = request("/webhooks/github", body, port=29081, headers=headers)
    expect(code, 202)
    assert json.loads(raw)["jobs"][0]["duplicate"]

def wait_ready():
    deadline = time.monotonic() + 40
    while time.monotonic() < deadline:
        try:
            if request("/readyz")[0] == 200:
                return
        except (OSError, urllib.error.URLError):
            pass
        time.sleep(0.5)
    raise AssertionError("gateway did not recover readiness")

def tenant_grants_isolation_rebuild():
    tenant = "test-" + uuid.uuid4().hex
    balance = "agentmesh:quota:v1:balance:" + tenant
    admin = secret("admin_token")
    expect(request("/admin/tenants", {"tenant_id": tenant, "model_routes": {"sql-review": ["ark"]}}, admin)[0], 201)
    assert int(redis("GET", balance)) == 10000000
    payload = {"tenant_id": tenant, "grant_id": "acceptance-grant", "units": 17}
    for _ in range(2):
        expect(request("/admin/quota/grants", payload, admin)[0], 200)
    assert int(redis("GET", balance)) == 10000017
    expect(request("/admin/quota/grants", {**payload, "units": 18}, admin)[0], 409)
    code, _, raw = request("/admin/api-keys", {"tenant_id": tenant}, admin)
    expect(code, 201)
    key = json.loads(raw)
    code, headers, _ = request("/v1/chat/completions", CHAT, key["api_key"])
    expect(code, 200)
    trace = next(v for k, v in headers.items() if k.lower() == "x-agentmesh-trace-id")
    expect(request("/v1/observability/traces/" + trace, token=secret("bootstrap_key"))[0], 404)
    expected = int(redis("GET", balance))
    redis("DEL", balance)
    expect(request("/v1/chat/completions", CHAT, key["api_key"])[0], 503)
    expect(request("/admin/quota/grants", payload, admin)[0], 409)
    expect(request("/readyz")[0], 503)
    command("stop", "gateway", "usage-relay", "usage-worker")
    try:
        command("run", "--rm", "--no-deps", "gateway", "quota-rebuild", "--offline", "--tenant", tenant)
    finally:
        command("start", "gateway", "usage-relay", "usage-worker")
    wait_ready()
    assert int(redis("GET", balance)) == expected
    expect(request("/v1/chat/completions", CHAT, key["api_key"])[0], 200)
    expect(request("/admin/api-keys/" + key["key_id"], token=admin, method="DELETE")[0], 204)
    expect(request("/v1/chat/completions", CHAT, key["api_key"])[0], 401)

def budget_cancel_failure_readiness():
    before = int(redis("GET", "agentmesh:quota:v1:balance:operator"))
    request("/control", {"mode": "overrun"}, port=29090)
    try:
        code, _, raw = request("/v1/chat/completions", CHAT, secret("bootstrap_key"))
        expect(code, 503)
        assert b"quota_exhausted" in raw and b"xxxxx" not in raw
    finally:
        request("/control", {"mode": ""}, port=29090)
    assert int(redis("GET", "agentmesh:quota:v1:balance:operator")) == before - 4096
    request("/control", {"mode": "delay"}, port=29090)
    try:
        old = int(sql("SELECT COUNT(*) FROM quota_reservations WHERE state='settled'"))
        body = json.dumps(CHAT).encode()
        connection = socket.create_connection(("127.0.0.1", 29080))
        headers = f"POST /v1/chat/completions HTTP/1.1\r\nHost: localhost\r\nAuthorization: Bearer {secret('bootstrap_key')}\r\nContent-Type: application/json\r\nContent-Length: {len(body)}\r\n\r\n".encode()
        connection.sendall(headers + body)
        time.sleep(0.5)
        connection.close()
        deadline = time.monotonic() + 8
        while time.monotonic() < deadline:
            if int(sql("SELECT COUNT(*) FROM quota_reservations WHERE state='settled'")) > old:
                break
            time.sleep(0.25)
        else:
            raise AssertionError("cancelled request was left open")
        assert int(sql("SELECT COUNT(*) FROM quota_reservations WHERE state IN ('creating','reserved','expired_pending')")) == 0
    finally:
        request("/control", {"mode": ""}, port=29090)
    for dependency in ["redis", "control"]:
        before = int(redis("GET", "agentmesh:quota:v1:balance:operator"))
        calls = json.loads(request("/control", port=29090)[2])["calls"]
        command("stop", dependency)
        try:
            expect(request("/healthz")[0], 200)
            expect(request("/readyz")[0], 503)
            started = time.monotonic()
            assert request("/v1/chat/completions", CHAT, secret("bootstrap_key"))[0] != 200
            assert time.monotonic() - started < 8, "dependency failure did not fail closed promptly"
            assert json.loads(request("/control", port=29090)[2])["calls"] == calls
        finally:
            command("start", dependency)
        wait_ready()
        assert int(redis("GET", "agentmesh:quota:v1:balance:operator")) == before

def crash_and_queue_recovery():
    delivery = "crash-" + uuid.uuid4().hex
    request("/control", {"mode": "delay"}, port=29090)
    expect(request("/jobs", {"delivery_id": delivery, "pr_number": 1, "sql": QUERY}, secret("sentinel_token"), port=29081)[0], 202)
    wait_job(delivery, "RUNNING")
    command("kill", "-s", "SIGKILL", "sentinel-worker")
    command("stop", "sentinel-worker")
    request("/control", {"mode": ""}, port=29090)
    try:
        wait_job(delivery, "RETRYING")
        assert int(sql("SELECT COUNT(*) FROM sql_sentinel_job_history h JOIN sql_sentinel_jobs j ON j.job_id=h.job_id WHERE j.delivery_id='" + delivery + "' AND h.reason='delivery_recovered'", "sentinel_control")) > 0
    finally:
        command("start", "sentinel-worker")
    job = wait_job(delivery)
    assert job["attempts"] >= 2
    command("stop", "sentinel-worker")
    try:
        queued = "retention-" + uuid.uuid4().hex
        expect(request("/jobs", {"delivery_id": queued, "pr_number": 1, "sql": QUERY, "candidate_spec": SPEC}, secret("sentinel_token"), port=29081)[0], 202)
        deadline = time.monotonic() + 10
        while time.monotonic() < deadline:
            if sql("SELECT COUNT(*) FROM sql_sentinel_outbox o JOIN sql_sentinel_jobs j ON j.job_id=o.job_id WHERE j.delivery_id='" + queued + "' AND o.published_at IS NOT NULL", "sentinel_control") == "1":
                break
            time.sleep(0.25)
        sql("UPDATE sql_sentinel_jobs SET updated_at=UTC_TIMESTAMP(6)-INTERVAL 6 MINUTE WHERE delivery_id='" + queued + "'", "sentinel_control")
        wait_job(queued, "RETRYING")
        assert int(sql("SELECT COUNT(*) FROM sql_sentinel_outbox o JOIN sql_sentinel_jobs j ON j.job_id=o.job_id WHERE j.delivery_id='" + queued + "'", "sentinel_control")) >= 2
    finally:
        command("start", "sentinel-worker")
    wait_job(queued)

def backup_restore_probe():
    dump = command("exec", "-T", "control", "sh", "-c", 'MYSQL_PWD=$(cat /run/secrets/mysql_root) exec mysqldump -uroot --single-transaction --hex-blob --set-gtid-purged=OFF --no-tablespaces sentinel_control')
    # Restore only to a new acceptance database. Production databases are never
    # overwritten by this acceptance check.
    probe = "acceptance_restore_" + uuid.uuid4().hex
    count = int(sql("SELECT COUNT(*) FROM sql_sentinel_jobs", "sentinel_control"))
    sql("CREATE DATABASE `" + probe + "`")
    try:
        sql(dump, probe)
        assert int(sql("SELECT COUNT(*) FROM sql_sentinel_jobs", probe)) == count
        assert int(sql("SELECT COUNT(*) FROM sql_sentinel_reports", probe)) > 0
    finally:
        sql("DROP DATABASE `" + probe + "`")

def comment_replay_and_stale_head():
    published = sql("SELECT s.job_id FROM sql_sentinel_sources s WHERE s.comment_state='published' ORDER BY s.comment_published_at DESC LIMIT 1", "sentinel_control")
    assert published
    before = json.loads(request("/control", port=29090)[2])["comments"]
    assert sum(published in c["body"] for c in before) == 1
    # Simulate the SQL acknowledgement being lost after the API accepted POST.
    sql("UPDATE sql_sentinel_sources SET comment_published_at=NULL,comment_state='' WHERE job_id='" + published + "'", "sentinel_control")
    deadline = time.monotonic() + 25
    while time.monotonic() < deadline:
        if sql("SELECT comment_state FROM sql_sentinel_sources WHERE job_id='" + published + "'", "sentinel_control") == "published":
            break
        time.sleep(0.5)
    else:
        raise AssertionError("comment replay was not acknowledged")
    after = json.loads(request("/control", port=29090)[2])["comments"]
    assert len(after) == len(before) and sum(published in c["body"] for c in after) == 1
    request("/control", {"head": "2" * 40}, port=29090)
    try:
        sql("UPDATE sql_sentinel_sources SET comment_published_at=NULL,comment_state='' WHERE job_id='" + published + "'", "sentinel_control")
        deadline = time.monotonic() + 25
        while time.monotonic() < deadline:
            if sql("SELECT comment_state FROM sql_sentinel_sources WHERE job_id='" + published + "'", "sentinel_control") == "superseded":
                break
            time.sleep(0.5)
        else:
            raise AssertionError("stale head was not fenced")
        assert json.loads(request("/control", port=29090)[2])["comments"] == after
    finally:
        request("/control", {"head": "1" * 40}, port=29090)

def concurrent_shadow_jobs():
    deliveries = []
    for i in range(4):
        delivery = "parallel-" + uuid.uuid4().hex
        expect(request("/jobs", {"delivery_id": delivery, "pr_number": 1, "sql": QUERY, "candidate_spec": SPEC}, secret("sentinel_token"), port=29081)[0], 202)
        deliveries.append(delivery)
    for delivery in deliveries:
        wait_job(delivery)
    assert sql("SELECT COUNT(DISTINCT INDEX_NAME) FROM information_schema.STATISTICS WHERE TABLE_SCHEMA='sentinel' AND TABLE_NAME='orders' AND INDEX_NAME!='PRIMARY'", "sentinel", "candidate") == "0"
    assert sql("SELECT COUNT(DISTINCT INDEX_NAME) FROM information_schema.STATISTICS WHERE TABLE_SCHEMA='sentinel' AND TABLE_NAME='orders' AND INDEX_NAME!='PRIMARY'", "sentinel", "baseline") == "0"

def main():
    for name, function in [("health_auth_and_verified_local_tls", health_auth), ("chat_embeddings_quota_trace_usage_projection", chat_embedding), ("manual_and_eino_model_jobs_reports_index_cleanup", manual_and_model), ("native_github_full_sql_idempotency_comments", native_webhook), ("tenant_grants_trace_isolation_key_revocation_offline_wallet_rebuild", tenant_grants_isolation_rebuild), ("output_budget_client_cancel_dependency_fault_readiness_AOF", budget_cancel_failure_readiness), ("worker_SIGKILL_lease_expiry_durable_requeue_and_retention_repair", crash_and_queue_recovery), ("backup_restore_to_new_database", backup_restore_probe)]:
        check(name, function)
    check("comment_replay_without_duplicate_and_stale_head_suppression", comment_replay_and_stale_head)
    check("four_queued_jobs_shadow_serialization_baseline_and_cleanup", concurrent_shadow_jobs)
    (ROOT / "acceptance-results.json").write_text(json.dumps({"scope": "isolated Linux containers; provider/GitHub protocol fixtures", "checks": RESULTS}, ensure_ascii=False, indent=2), encoding="utf-8")

if __name__ == "__main__":
    main()
