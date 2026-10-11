"""Optional protocols on the isolated acceptance project; no external writes."""
import json,os,time,uuid
from datetime import datetime,timedelta,timezone
from urllib.parse import urlencode
from pathlib import Path
import acceptance as a
ROOT=Path(__file__).resolve().parent
a.COMPOSE += ["-f","extensions.yml","-f","extensions-acceptance.yml","--profile","extensions"]
os.environ["ELASTICSEARCH_IMAGE"]="docker.elastic.co/elasticsearch/elasticsearch:8.19.17@sha256:aa6ee0ea2d708cea22a24ed44a420c5bbde9853d58a872e1e870f2086d04f652"
RESULTS=[];STATE={};PROJECT="roadmap-"+uuid.uuid4().hex[:12]
def call(path,data=None,token=None,port=29080,expected=200,headers=None,method=None):
    code,head,raw=a.request(path,data,token,port,headers=headers,method=method)
    a.expect(code,expected)
    return (json.loads(raw) if raw and not raw.startswith(b"data:") else raw),head
def admin(path,data,expected=200):return call(path,data,a.secret("admin_token"),expected=expected)
def wait(delivery,target="COMPLETED",token=None):
    end=time.monotonic()+150
    while time.monotonic()<end:
        value,_=call("/jobs/"+delivery,token=token or a.secret("sentinel_token"),port=29081)
        if value["status"]==target:return value
        assert value["status"] not in {"DEAD_LETTER","REJECTED","COMPLETED"},value["status"]
        time.sleep(.5)
    raise AssertionError("extension job deadline")
def check(name,fn):
    started=time.monotonic();fn();RESULTS.append(dict(name=name,status="PASS",seconds=round(time.monotonic()-started,3)));print("PASS "+name,flush=True)
def balance(token):return call("/v1/billing/balance",token=token)[0]
def money_cache_third_provider():
    tenant="money-"+uuid.uuid4().hex[:12]
    admin("/admin/tenants",{"tenant_id":tenant,"model_routes":{"domestic-chat":["compatible"]}},201)
    key=admin("/admin/api-keys",{"tenant_id":tenant},201)[0]["api_key"]
    STATE.update(tenant=tenant,key=key)
    assert balance(key)["available_nano"]==0
    chat={**a.CHAT,"model":"domestic-chat","messages":[{"role":"user","content":"fixture domestic model question 42"}]}
    call("/v1/chat/completions",chat,key,expected=429)
    credit={"tenant_id":tenant,"credit_id":"credit-"+uuid.uuid4().hex,"amount_nano":2000000000,"proof":"isolated acceptance credit; no external payment"}
    call("/admin/billing/credits",credit,key,expected=401)
    admin("/admin/billing/credits",credit);admin("/admin/billing/credits",credit)
    assert balance(key)["balance_nano"]==2000000000
    cache={"X-AgentMesh-Cache":"semantic","X-AgentMesh-Cache-Namespace":"fixture-context-v1"}
    raw,head=call("/v1/chat/completions",chat,key,headers=cache)
    assert b"fixture response" in raw
    first=balance(key);assert first["balance_nano"]==2000000000-180000 and first["held_nano"]==0
    count=json.loads(a.request("/control",port=29090)[2])["calls"]
    raw,head=call("/v1/chat/completions",chat,key,headers=cache)
    assert any(k.lower()=="x-agentmesh-cache" and v=="hit" for k,v in head.items()) and b'"charged_nano":0' in raw
    assert balance(key)==first
    assert json.loads(a.request("/control",port=29090)[2])["calls"]==count
    # Namespace/context invalidation makes a new paid upstream request.
    call("/v1/chat/completions",chat,key,headers={**cache,"X-AgentMesh-Cache-Namespace":"fixture-context-v2"})
    assert balance(key)["balance_nano"]==first["balance_nano"]-180000
    ledger,_=call("/v1/billing/requests",token=key);assert sum(r["state"]=="cache_hit" for r in ledger)==1
    admin("/admin/billing/credits",{**credit,"amount_nano":2000000001},409)
    assert a.request("/admin/billing/credits",credit,a.secret("admin_token"),port=29880)[0]==404
    # Supply fixture-only credit for SQL model/RAG commissioning.
    admin("/admin/billing/credits",{"tenant_id":"operator","credit_id":"operator-"+uuid.uuid4().hex,"amount_nano":2000000000,"proof":"isolated acceptance SQL/RAG calls"})
def uncertain_usage():
    a.request("/control",{"mode":"missing_usage"},port=29090)
    before=balance(STATE["key"])
    try:
        raw,head=call("/v1/chat/completions",{**a.CHAT,"model":"domestic-chat"},STATE["key"])
        request_id=next(v for k,v in head.items() if k.lower()=="x-agentmesh-billing-request-id")
        ledger,_=call("/v1/billing/requests",token=STATE["key"])
        row=next(v for v in ledger if v["request_id"]==request_id)
        assert row["state"]=="pending_reconciliation" and row["charged_nano"]==0
        assert balance(STATE["key"])["balance_nano"]==before["balance_nano"]
        proof={"request_id":request_id,"proof":"fixture provider exact usage proof","attempts":[{"ordinal":1,"usage":{"input_tokens":100,"output_tokens":50}}]}
        admin("/admin/billing/resolve",proof);admin("/admin/billing/resolve",proof,409)
        assert balance(STATE["key"])["balance_nano"]==before["balance_nano"]-180000 and balance(STATE["key"])["held_nano"]==0
    finally:a.request("/control",{"mode":""},port=29090)
def rewrite_and_approval():
    original="SELECT id,user_id,status FROM orders WHERE user_id=42 ORDER BY id LIMIT 20"
    good="SELECT id,user_id,status FROM orders WHERE 42=user_id ORDER BY id LIMIT 20"
    delivery="rewrite-"+uuid.uuid4().hex
    body={"delivery_id":delivery,"pr_number":201,"sql":original,"candidate_spec":{"kind":"rewrite","sql":good}}
    call("/jobs",body,a.secret("sentinel_token"),29081,202);wait(delivery)
    report,head=call("/jobs/"+delivery+"/report",token=a.secret("sentinel_token"),port=29081)
    assert report["rewrite"]["result_equivalent"] and report["rewrite"]["ordered_result"]
    sha=next(v for k,v in head.items() if k.lower()=="x-sentinel-report-sha256")
    call("/jobs/"+delivery+"/approval",{"report_sha256":sha,"decision":"approved","reason":"isolated snapshot rewrite review"},a.secret("sentinel_token"),29081,201)
    bench="rewrite-bench-"+uuid.uuid4().hex
    call("/jobs/"+delivery+"/benchmark",{"report_sha256":sha,"delivery_id":bench},a.secret("sentinel_token"),29081,202);wait(bench)
    report,_=call("/jobs/"+bench+"/report",token=a.secret("sentinel_token"),port=29081)
    assert len(report["performance"]["CandidateCalibration"]["raw_ms"])==20
    for bad in ["SELECT id,user_id,status FROM orders WHERE user_id=43 ORDER BY id LIMIT 20","SELECT id,user_id,status FROM orders WHERE user_id=42 ORDER BY id DESC LIMIT 20"]:
        failed="rewrite-bad-"+uuid.uuid4().hex
        call("/jobs",{**body,"delivery_id":failed,"candidate_spec":{"kind":"rewrite","sql":bad}},a.secret("sentinel_token"),29081,202);wait(failed,"REJECTED")
        report,_=call("/jobs/"+failed+"/report",token=a.secret("sentinel_token"),port=29081)
        assert not report["rewrite"]["result_equivalent"]
    assert a.sql("SELECT COUNT(*) FROM information_schema.STATISTICS WHERE TABLE_SCHEMA='sentinel' AND INDEX_NAME LIKE 'idx_cand_%'","sentinel","candidate")=="0"
def historical_relay_scope_and_outage():
    token=call("/auth/tokens",{"subject":"history-reviewer","role":"writer","tenant_id":"acceptance","project_id":PROJECT,"tables":["orders"]},a.secret("sentinel_token"),29081,201)[0]["token"]
    delivery="history-"+uuid.uuid4().hex
    call("/jobs",{"delivery_id":delivery,"pr_number":202,"sql":a.QUERY,"candidate_spec":a.SPEC},token,29081,202);wait(delivery,token=token)
    end=time.monotonic()+95
    while True:
        code,_,raw=a.request("/history/search?table=orders",token=token,port=29081)
        # Optional ES may briefly time out under concurrent image compilation;
        # keep the production three-second deadline and wait one relay retry.
        if code==200 and json.loads(raw)["count"]:
            result=json.loads(raw);break
        assert code in {200,503},"unexpected historical authorization/status"
        assert time.monotonic()<end,"historical outbox did not publish";time.sleep(1)
    assert result["historical_only"] and all(d["project_id"]==PROJECT for d in result["documents"])
    unrelated=call("/auth/tokens",{"subject":"other","role":"reader","tenant_id":"acceptance","project_id":"other-"+uuid.uuid4().hex[:8],"tables":["orders"]},a.secret("sentinel_token"),29081,201)[0]["token"]
    result,_=call("/history/search?table=orders",token=unrelated,port=29081);assert result["count"]==0
    call("/history/search?table=customers",token=token,port=29081,expected=404)
    a.command("stop","elasticsearch")
    try:
        call("/history/search?table=orders",token=token,port=29081,expected=503)
        offline="history-offline-"+uuid.uuid4().hex
        call("/jobs",{"delivery_id":offline,"pr_number":203,"sql":a.QUERY,"candidate_spec":a.SPEC},token,29081,202);wait(offline,token=token)
    finally:a.command("start","elasticsearch")
def rpc_service_auth():
    a.command("exec","-T","model-runner","grpc-probe","--target","model-runner:50051","--prefix","AGENTMESH_GRPC")
    a.command("exec","-T","verifier","grpc-probe","--target","verifier:50052","--prefix","SQL_SENTINEL_GRPC")
    import subprocess
    denied=subprocess.run(a.COMPOSE+["exec","-T","-e","AGENTMESH_GRPC_TOKEN="+"z"*32,"-e","AGENTMESH_GRPC_TOKEN_FILE=","model-runner","grpc-probe","--target","model-runner:50051","--prefix","AGENTMESH_GRPC"],cwd=ROOT,capture_output=True,timeout=10)
    assert denied.returncode!=0
    # The Kafka worker has no shadow DB credentials after process separation.
    mounted=json.loads(subprocess.check_output(["docker","inspect","codex-backend-acceptance-sentinel-worker-1"]))[0]["Mounts"]
    assert not any(m["Destination"] in {"/run/secrets/baseline_dsn","/run/secrets/candidate_dsn","/run/secrets/bootstrap_key"} for m in mounted)
def experiments():
    a.command("stop","sentinel-worker","verifier")
    try:
        a.command("run","--rm","pg-compare");a.command("run","--rm","write-probe")
    finally:a.command("start","verifier","sentinel-worker")
    pg=json.loads((ROOT/"experiments/report.json").read_text());writes=json.loads((ROOT/"experiments/write-report.json").read_text())
    assert pg["data_equivalent"] and pg["result_equivalent"] and pg["postgres_version"].startswith("18.6")
    assert len(pg["mysql_calibration"]["rounds_ms"])==20 if "rounds_ms" in pg["mysql_calibration"] else len(pg["mysql_raw_ms"])==5
    assert writes["workers"]==4 and len(writes["baseline"]["calibration"])==20 and len(writes["candidate"]["measurement"])==5
    assert writes["candidate"]["index_build_ms"]>0 and writes["candidate"]["p95_ms"]>0
    assert a.sql("SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA='sentinel_write'","sentinel_write","write-shadow")=="0"
    assert a.command("exec","-T","postgres","psql","-U","postgres","-d","sentinel_compare","-Atc","SELECT COUNT(*) FROM pg_namespace WHERE nspname LIKE 'ss_compare_%'")=="0"
def traces_alerts_persistence():
    end=time.monotonic()+30
    while True:
        code,_,raw=a.request("/api/v3/services",port=29686)
        if code==200 and "agentmesh" in json.loads(raw).get("services",[]):break
        assert time.monotonic()<end,"trace service missing";time.sleep(1)
    now=datetime.now(timezone.utc)
    query=urlencode({"query.service_name":"agentmesh","query.start_time_min":(now-timedelta(hours=1)).isoformat(),"query.start_time_max":(now+timedelta(minutes=1)).isoformat(),"query.search_depth":5})
    code,_,raw=a.request("/api/v3/traces?"+query,port=29686);a.expect(code,200)
    resources=json.loads(raw)["result"]["resourceSpans"];assert resources
    trace_id=resources[0]["scopeSpans"][0]["spans"][0]["traceId"]
    a.command("restart","jaeger")
    for _ in range(30):
        try:
            code,_,raw=a.request("/api/v3/traces/"+trace_id,port=29686)
            if code==200 and json.loads(raw).get("result",{}).get("resourceSpans"):break
        except OSError:pass
        time.sleep(1)
    else:raise AssertionError("persisted trace lost after restart")
    alert={"labels":{"alertname":"AcceptanceOnly","severity":"warning"},"annotations":{"summary":"local routing acceptance"}}
    code,_,_=a.request("/api/v2/alerts",[alert],port=29903);a.expect(code,200)
    code,_,raw=a.request("/api/v2/alerts?active=true",port=29903);assert any(v["labels"].get("alertname")=="AcceptanceOnly" for v in json.loads(raw))
    # Dashboards are private; no notification is sent externally.
    assert a.request("/metrics",port=29880)[0]==404
for name,fn in [("CNY money, third provider and free scoped cache",money_cache_third_provider),("Unknown usage hold and proof-based settlement",uncertain_usage),("Rewrite equivalence, ordering counterexamples and approval benchmark",rewrite_and_approval),("Historical outbox, scoped search and outage tolerance",historical_relay_scope_and_outage),("Separate mTLS executors and service-token denial",rpc_service_auth),("PostgreSQL and concurrent write experiment cleanup",experiments),("Persisted trace query and private alert routing",traces_alerts_persistence)]:check(name,fn)
(ROOT.parent/"validation/extension-acceptance-results.json").write_text(json.dumps(dict(results=RESULTS,limitations=["Elasticsearch protocol acceptance used cached 8.19.17; pulling current 8.19.23 failed with registry EOF.","Model tariffs/credentials are fixtures. No actual payment, cloud, GitHub or external alert delivery."]),indent=2),encoding="utf-8")
print("All optional protocol acceptance groups passed.")
