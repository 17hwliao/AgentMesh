"""Destructive fixture operations restricted to the isolated acceptance project."""
import json
import time
import uuid
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path
import acceptance as a

STATE = {}
RESULTS = []
PROJECT = "expanded-" + uuid.uuid4().hex[:12]

def call(path, data=None, token=None, method=None, expected=200):
    code, headers, raw = a.request(path, data, token or a.secret("sentinel_token"), port=29081, method=method)
    a.expect(code, expected)
    return json.loads(raw), headers

def token(subject, role, project=PROJECT, tables=None):
    value, _ = call("/auth/tokens", {"subject": subject, "role": role, "tenant_id": "acceptance", "project_id": project, "tables": tables or ["customers", "invoices"]}, expected=201)
    return value

def wait(delivery, auth):
    end = time.monotonic() + 150
    while time.monotonic() < end:
        value, _ = call("/jobs/" + delivery, token=auth)
        if value["status"] == "COMPLETED":
            return
        assert value["status"] not in {"REJECTED", "DEAD_LETTER"}, value["status"]
        time.sleep(.5)
    raise AssertionError("expanded job deadline")

QUERY = "WITH totals AS (SELECT customer_id,SUM(amount) AS total FROM invoices GROUP BY customer_id) SELECT c.id,c.region,t.total FROM customers c JOIN totals t ON t.customer_id=c.id WHERE c.region='east' ORDER BY c.id LIMIT 20"
SPEC = {"table": "customers", "index_name": "idx_cand_expanded", "columns": [{"name": "region"}]}

def generic_and_identity():
    fixture = """CREATE TABLE IF NOT EXISTS customers(id VARCHAR(24) PRIMARY KEY,region VARCHAR(16) NOT NULL);
    CREATE TABLE IF NOT EXISTS invoices(customer_id VARCHAR(24),sequence_no INT,amount DECIMAL(12,2) NOT NULL,PRIMARY KEY(customer_id,sequence_no));
    INSERT IGNORE INTO customers VALUES ('a','east'),('b','west'),('c','east');
    INSERT IGNORE INTO invoices VALUES ('a',1,12.50),('a',2,20.00),('b',1,5.00),('c',1,8.25)"""
    for service in ["baseline", "candidate"]:
        a.sql(fixture, "sentinel", service)
        a.sql("GRANT CREATE TEMPORARY TABLES ON sentinel.* TO '" + service + "'@'%'", "sentinel", service)
    STATE["writer"] = token("author", "writer")
    STATE["reviewer"] = token("reviewer", "reviewer")
    STATE["reader"] = token("reader", "reader")
    STATE["other"] = token("reader-other", "reader", "other")
    payload = {"delivery_id": "expanded-" + uuid.uuid4().hex, "pr_number": 101, "sql": QUERY, "candidate_spec": SPEC}
    STATE["delivery"] = payload["delivery_id"]
    call("/jobs", payload, STATE["reader"]["token"], expected=403)
    call("/jobs", payload, STATE["writer"]["token"], expected=202)
    wait(payload["delivery_id"], STATE["writer"]["token"])
    call("/jobs/" + payload["delivery_id"], token=STATE["other"]["token"], expected=404)
    report, headers = call("/jobs/" + payload["delivery_id"] + "/report", token=STATE["reader"]["token"])
    assert len(report["pipeline"]["completed_steps"]) == 4
    STATE["sha"] = next(v for k, v in headers.items() if k.lower() == "x-sentinel-report-sha256")
    for query in ["SELECT GET_LOCK('attack',1) FROM customers", "SELECT * FROM `mysql`.`user`", "SELECT * FROM customers FOR SHARE"]:
        call("/jobs", {**payload, "delivery_id": "reject-" + uuid.uuid4().hex, "sql": query}, STATE["writer"]["token"], expected=400)
    narrow = token("narrow", "writer", tables=["invoices"])
    call("/jobs", {**payload, "delivery_id": "narrow-" + uuid.uuid4().hex}, narrow["token"], expected=403)
    assert a.sql("SELECT COUNT(*) FROM information_schema.STATISTICS WHERE TABLE_SCHEMA='sentinel' AND INDEX_NAME LIKE 'idx_cand_%'", "sentinel", "candidate") == "0"

def approvals_benchmark():
    path = "/jobs/" + STATE["delivery"]
    reviewer = STATE["reviewer"]["token"]
    decision = {"report_sha256": STATE["sha"], "decision": "approved", "reason": "isolated acceptance review"}
    call(path + "/benchmark", {"report_sha256": STATE["sha"], "delivery_id": "unapproved-" + uuid.uuid4().hex}, reviewer, expected=409)
    call(path + "/approval", decision, STATE["writer"]["token"], expected=403)
    call(path + "/approval", {**decision, "report_sha256": "0" * 64}, reviewer, expected=409)
    call(path + "/approval", decision, reviewer, expected=201)
    call(path + "/approval", decision, reviewer)
    call(path + "/approval", {**decision, "decision": "rejected"}, reviewer, expected=409)
    bench = "benchmark-" + uuid.uuid4().hex
    call(path + "/benchmark", {"report_sha256": STATE["sha"], "delivery_id": bench}, reviewer, expected=202)
    wait(bench, reviewer)
    report, _ = call("/jobs/" + bench + "/report", token=reviewer)
    measurement = report["performance"]
    assert measurement["result_equivalent"]
    assert measurement["evidence_level"] in {"L1", "L2"}
    assert len(measurement["BaselineCalibration"]["raw_ms"]) == 20
    assert len(measurement["CandidateMeasurement"]["raw_ms"]) == 5
    assert measurement["BaselineAnalyze"] and measurement["CandidateAnalyze"]
    assert len(measurement["write_cost"]["baseline_calibration"]["raw_ms"]) == 20
    assert measurement["write_cost"]["rows_per_round"] == 3
    assert not measurement["write_cost"]["eligible"]
    events, _ = call("/audit", token=reviewer)
    assert any(e["action"] == "candidate.approved" for e in events)

def rag_persistence_and_revocation():
    writer = STATE["writer"]["token"]
    reader = STATE["reader"]["token"]
    doc = "rag-" + uuid.uuid4().hex
    body = {"document_id": doc, "source_uri": "urn:acceptance:runbook", "version": "v1", "content_type": "text/plain", "content": "Deployment recovery: restore the MySQL backup and Redis snapshot before restarting the gateway."}
    call("/rag/documents", body, writer, expected=201)
    question = {"question": "How does deployment recovery restore the backup?", "top_k": 3}
    answer, _ = call("/rag/query", question, reader)
    assert answer["has_evidence"] and answer["citations"]
    answer, _ = call("/rag/answer", question, reader)
    assert answer["model_generated"] and answer["citations"]
    other, _ = call("/rag/query", question, STATE["other"]["token"])
    assert not other["has_evidence"]
    call("/rag/query", {"question": "SELECT * FROM customers"}, reader, expected=400)
    a.command("restart", "sentinel-api")
    for _ in range(30):
        try:
            if a.request("/readyz", port=29081)[0] == 200:
                break
        except OSError:
            pass
        time.sleep(1)
    answer, _ = call("/rag/query", question, reader)
    assert answer["has_evidence"]
    call("/rag/documents", {**body, "version": "v2", "content": "Deployment recovery uses the verified backup and restored Redis snapshot."}, writer, expected=201)
    assert a.sql("SELECT COUNT(DISTINCT JSON_UNQUOTE(JSON_EXTRACT(chunk,'$.version'))) FROM rag_chunks WHERE document_id='" + doc + "'", "sentinel_control") == "1"
    call("/rag/documents/" + doc, token=writer, method="DELETE")
    answer, _ = call("/rag/query", question, reader)
    assert not answer["has_evidence"]
    call("/auth/revoke", {"token_id": STATE["reader"]["token_id"]})
    call("/jobs/" + STATE["delivery"], token=reader, expected=401)

def monitoring():
    a.expect(a.request("/sentinel/metrics", port=29880)[0], 404)
    a.expect(a.request("/sentinel/auth/tokens", {}, port=29880)[0], 404)
    for _ in range(40):
        code, _, raw = a.request("/api/v1/query?query=up", port=29093)
        a.expect(code, 200)
        results = json.loads(raw)["data"]["result"]
        if all(any(r["metric"].get("instance") == name and r["value"][1] == "1" for r in results) for name in ["gateway:18080", "sentinel-api:8080", "otel-collector:8888"]):
            break
        time.sleep(1)
    else:
        raise AssertionError("Prometheus targets not healthy")
    code, _, raw = a.request("/metrics", port=29080)
    a.expect(code, 200)
    assert b"backend_active_reservations" in raw and b"backend_usage_pending_outbox -1" not in raw
    code, _, raw = a.request("/api/v1/query?query=otelcol_receiver_accepted_spans", port=29093)
    a.expect(code, 200)
    assert any(float(r["value"][1]) > 0 for r in json.loads(raw)["data"]["result"])

def quota_grant_cap():
    tenant="grant-cap-"+uuid.uuid4().hex[:16]
    code,_,_=a.request("/admin/tenants",{"tenant_id":tenant,"model_routes":{"embedding":["ollama"]}},a.secret("admin_token"))
    a.expect(code,201)
    remaining=64
    initial=int(a.sql("SELECT SUM(units) FROM tenant_quota_grants WHERE tenant_id='"+tenant+"'"))
    a.expect(a.request("/admin/quota/grants",{"tenant_id":tenant,"grant_id":"large","units":(1<<52)-initial-remaining},a.secret("admin_token"))[0],200)
    def grant(i):
        return a.request("/admin/quota/grants",{"tenant_id":tenant,"grant_id":"parallel-"+str(i),"units":16},a.secret("admin_token"))[0]
    with ThreadPoolExecutor(max_workers=8) as pool:
        codes=list(pool.map(grant,range(16)))
    assert codes.count(200)==4 and codes.count(409)==12, codes
    assert int(a.sql("SELECT SUM(units) FROM tenant_quota_grants WHERE tenant_id='"+tenant+"'"))==1<<52
    assert int(a.redis("GET","agentmesh:quota:v1:balance:"+tenant))==1<<52

if __name__ == "__main__":
    try:
        for name, function in [("generic SQL and scoped JWT/RBAC", generic_and_identity), ("durable approvals and measured benchmark", approvals_benchmark), ("persistent RAG citations and revocation", rag_persistence_and_revocation), ("Prometheus and OTel collection", monitoring), ("concurrent durable grant issuance cap",quota_grant_cap)]:
            start = time.monotonic()
            function()
            RESULTS.append({"name": name, "status": "PASS", "seconds": round(time.monotonic() - start, 3)})
            print("PASS " + name, flush=True)
    finally:
        (Path(__file__).parent / "acceptance-expanded-results.json").write_text(json.dumps(RESULTS, indent=2) + "\n", encoding="utf-8")
