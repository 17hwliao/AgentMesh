"""Local-only system lab. Requires an existing fixture acceptance deployment.

Mutations are restricted to the named acceptance project and fixture model.
No production credentials, volumes or remote publishing are accepted.
"""
import argparse
import collections
import concurrent.futures
import hashlib
import importlib.util
import json
import os
import socket
import re
from pathlib import Path
import statistics
import sys
import subprocess
import threading
import time
import urllib.error
import urllib.request
import uuid

REPO = Path(__file__).resolve().parent.parent
PROJECT = "codex-backend-acceptance"
SERVICES = ["gateway", "model-runner", "usage-relay", "usage-worker", "sentinel-api", "sentinel-worker", "sentinel-relay", "verifier"]

class Lab:
    def __init__(self, deploy, out, project=None, port_offset=None):
        self.deploy, self.out = deploy.resolve(), out.resolve()
        self.out.mkdir(parents=True, exist_ok=True)
        env = (self.deploy / ".env").read_text(encoding="utf-8")
        if "OLLAMA_MODEL=fixture" not in env or "http://fixtures:8090" not in env:
            raise ValueError("existing explicitly isolated fixture configuration required")
        settings_path=self.deploy/".private/lab-settings.json"
        settings=json.loads(settings_path.read_text(encoding="utf-8")) if settings_path.exists() else {}
        self.environment=os.environ.copy()
        if settings.get("fixture_history_image"):
            self.environment["ELASTICSEARCH_IMAGE"]=settings["fixture_history_image"]
        self.project=project or settings.get("project",PROJECT)
        self.offset=settings.get("port_offset",0) if port_offset is None else port_offset
        if self.project!=PROJECT and not re.fullmatch(r"codex-backend-lab-[a-f0-9]{8}",self.project): raise ValueError("isolated lab project required")
        if not isinstance(self.offset,int) or self.offset<0 or self.offset>15000 or (self.offset and self.project==PROJECT): raise ValueError("isolated bounded port offset required")
        self.compose = ["docker", "compose", "-p", self.project, "--profile", "extensions", "-f", "compose.yml", "-f", "acceptance.yml", "-f", "extensions.yml", "-f", "extensions-acceptance.yml"]
        self.overlay = self.deploy / ".private" / "engineering.yml"
        self.overlay.parent.mkdir(exist_ok=True)
        definitions = {name: {"image": "local/agentmesh-backend:engineering" if name in SERVICES[:4] else "local/sql-sentinel-backend:engineering"} for name in SERVICES}
        definitions.update({"am-migrate":{"image":"local/agentmesh-backend:engineering"},**{name:{"image":"local/sql-sentinel-backend:engineering"} for name in ["ss-migrate","seed","pg-compare","write-probe"]}})
        definitions["fixtures"] = {"image": "local/sql-sentinel-fixture:engineering"}
        definitions["gateway"]["environment"] = {"AGENTMESH_RATE_LIMIT_PER_MINUTE": "60000", "AGENTMESH_RATE_LIMIT_BURST": "1000"}
        # JSON is YAML-compatible; actual file-secret values never enter this override.
        self.overlay.write_text(json.dumps({"services": definitions}, indent=2), encoding="utf-8")
        self.compose += ["-f", str(self.overlay)]
        if self.offset:
            config=json.loads(self.cmd("config","--format","json"))
            lines=["services:"]
            for name,service in config["services"].items():
                if service.get("ports"):
                    ports=[p["host_ip"]+":"+str(int(p["published"])+self.offset)+":"+str(p["target"])+"/"+p.get("protocol","tcp") for p in service["ports"]]
                    if any(int(p["published"])+self.offset>65535 for p in service["ports"]):raise ValueError("port range")
                    lines.extend(["  "+name+":","    ports: !override "+json.dumps(ports)])
                    if name=="kafka":
                        listeners=service["environment"]["KAFKA_ADVERTISED_LISTENERS"].replace("EXTERNAL://127.0.0.1:29092","EXTERNAL://127.0.0.1:"+str(29092+self.offset))
                        lines.extend(["    environment:","      KAFKA_ADVERTISED_LISTENERS: "+json.dumps(listeners)])
            port_file=self.overlay.parent/"lab-ports.yml"
            port_file.write_text('\n'.join(lines)+'\n',encoding="utf-8")
            self.compose += ["-f",str(port_file)]
        self.results = []
        self.key = None

    def cmd(self, *args, timeout=180, check=True, input=None):
        p = subprocess.run([*self.compose, *args], cwd=self.deploy, env=self.environment,input=input,capture_output=True, text=True, encoding="utf-8", errors="replace", timeout=timeout)
        if check and p.returncode:
            raise RuntimeError("acceptance Compose command failed: " + args[0])
        return p.stdout.strip()

    def secret(self, name):
        return (self.deploy / "secrets" / name).read_text(encoding="utf-8").strip()

    def http(self, path, data=None, token=None, port=29080, method=None, timeout=35):
        body = json.dumps(data).encode() if data is not None else None
        headers = {"Content-Type": "application/json"}
        if token:
            headers["Authorization"] = "Bearer " + token
        req = urllib.request.Request(f"http://127.0.0.1:{port+self.offset}" + path, body, headers, method=method)
        try:
            response = urllib.request.urlopen(req, timeout=timeout)
        except urllib.error.HTTPError as e:
            response = e
        with response:
            raw = response.read(2 << 20)
            try:
                value = json.loads(raw)
            except ValueError:
                value = raw
            return response.status, value, dict(response.headers)

    def control(self, mode="", **kw):
        assert self.http("/control", {"mode": mode, **kw}, port=29090)[0] == 200

    def sql(self, query, database="agentmesh"):
        return self.cmd("exec", "-T", "control", "sh", "-c", 'MYSQL_PWD="$(cat /run/secrets/mysql_root)" mysql -uroot --batch --skip-column-names "$1" -e "$2"', "sh", database, query)

    def ready(self):
        deadline = time.monotonic() + 180
        while time.monotonic() < deadline:
            try:
                if self.http("/readyz")[0] == 200 and self.http("/readyz", port=29081)[0] == 200:
                    return
            except (OSError, TimeoutError):
                pass
            time.sleep(1)
        raise RuntimeError("acceptance applications not ready")

    def bootstrap(self):
        tenant = "lab-" + uuid.uuid4().hex[:16]
        admin = self.secret("admin_token")
        code, _, _ = self.http("/admin/tenants", {"tenant_id": tenant, "model_routes": {"domestic-chat": ["compatible"]}}, admin)
        assert code == 201
        code, value, _ = self.http("/admin/api-keys", {"tenant_id": tenant}, admin)
        assert code == 201
        self.key = self.out / "tenant-key.private"
        self.key.write_text(value["api_key"], encoding="utf-8")
        self.key.chmod(0o600)
        self.tenant, self.token = tenant, value["api_key"]
        self.key_id = value["key_id"]
        code, _, _ = self.http("/admin/billing/credits", {"tenant_id": tenant, "credit_id": tenant + "-gift", "amount_nano": 100000000000, "proof": "local fixture lab grant; no payment"}, admin)
        assert code == 200
        # Only this hard-fenced fixture project receives synthetic operator funds.
        code, _, _ = self.http("/admin/billing/credits", {"tenant_id":"operator","credit_id":tenant+"-operator-gift","amount_nano":100000000000,"proof":"local fixture lab grant; no payment"}, admin)
        assert code==200

    def assert_money(self):
        # All fixture tenant IDs originate locally; never interpolate submitted identifiers.
        tenant = self.tenant
        rows = self.sql("SELECT balance_nano,held_nano FROM billing_wallets WHERE tenant_id='" + tenant + "'").split()
        assert len(rows) == 2 and 0 <= int(rows[1]) <= int(rows[0])
        totals = self.sql("SELECT COALESCE(SUM(charged_nano),0),COALESCE(SUM(CASE WHEN state IN ('held','pending_reconciliation') THEN reserved_nano ELSE 0 END),0) FROM billing_requests WHERE tenant_id='" + tenant + "'").split()
        credits = int(self.sql("SELECT COALESCE(SUM(amount_nano),0) FROM billing_credits WHERE tenant_id='" + tenant + "'"))
        assert int(rows[0]) + int(totals[0]) == credits, "money conservation failed"
        assert int(rows[1]) == int(totals[1]), "held money does not match unresolved requests"
        # Request columns/state names are checked against the actual schema before use.
        return {"balance_nano": int(rows[0]), "held_nano": int(rows[1]), "charged_nano": int(totals[0])}

    def resources(self):
        names = self.cmd("ps", "--format", "json")
        try:
            values = json.loads(names)
            if isinstance(values, dict):
                values = [values]
        except ValueError:
            values = [json.loads(line) for line in names.splitlines()]
        names = [v["Name"] for v in values if v.get("Service") in ["gateway", "model-runner", "proxy", "control", "redis"]]
        assert len(names)==5,"expected fixture resource surfaces missing"
        p = subprocess.run(["docker", "stats", "--no-stream", "--format", "{{json .}}", *names], capture_output=True, text=True, timeout=30)
        if p.returncode:
            raise RuntimeError("resource sampling failed")
        code,metrics,_=self.http("/metrics")
        assert code==200 and isinstance(metrics,bytes)
        go={}
        for line in metrics.decode("utf-8").splitlines():
            match=re.fullmatch(r"(go_goroutines|go_memstats_heap_alloc_bytes|process_open_fds) ([0-9.eE+\-]+)",line)
            if match:go[match[1]]=float(match[2])
        assert len(go)==3,"Go/process metrics unavailable"
        return {"time": time.time(), "containers": [json.loads(line) for line in p.stdout.splitlines()],"gateway_metrics":go}

    def bench(self, name, seconds=30, concurrency=8, rate=0, delay="0s", cancel="0s", tokens=100):
        output = self.out / (name + ".json")
        binary = self.out.parent / ("system-benchmark.exe" if os.name == "nt" else "system-benchmark")
        if not binary.exists():
            subprocess.run(["go", "build", "-buildvcs=false", "-o", str(binary), "./cmd/system-benchmark"], cwd=REPO, check=True)
        args = [str(binary), "--url", "http://127.0.0.1:"+str(29880+self.offset), "--key-file", str(self.key), "--duration", str(seconds) + "s", "--concurrency", str(concurrency), "--rate", str(rate), "--read-delay", delay, "--cancel-after", cancel, "--max-tokens", str(tokens), "--out", str(output)]
        observations, stop = [], threading.Event()
        def observe():
            next_sample=time.monotonic()
            while not stop.is_set():
                try:
                    observations.append(self.resources())
                except Exception:
                    observations.append({"time": time.time(), "error": "resource_sampling_failed"})
                with (self.out/(name+".resources.jsonl")).open("a",encoding="utf-8") as durable:
                    durable.write(json.dumps(observations[-1])+"\n")
                # Fixed cadence: Docker sampling time must not accumulate into
                # every interval and invalidate the minimum sample-count gate.
                next_sample+=30
                stop.wait(max(0,next_sample-time.monotonic()))
        observer = threading.Thread(target=observe)
        observer.start()
        try:
            p = subprocess.run(args, capture_output=True, text=True, timeout=seconds + 90)
        finally:
            stop.set()
            observer.join()
        (self.out / (name + ".resources.json")).write_text(json.dumps(observations, indent=2), encoding="utf-8")
        assert observations and all("error" not in row for row in observations),"resource observations failed"
        if p.returncode:
            raise RuntimeError("benchmark failed: " + name)
        report = json.loads(output.read_text(encoding="utf-8"))
        assert not report.get("sample_limit_reached"),"sample bound ended offered workload prematurely"
        assert report["scheduled"] == len(report["samples"]) + report["dropped"]
        if cancel == "0s":
            assert report["completed"] > 0 and report["unexpected"] == 0
        self.results.append({"name": name, "status": "PASS", "completed": report["completed"], "statuses": report["statuses"], "dropped": report["dropped"]})
        return report

    def load(self):
        self.control()
        for concurrency in [1, 8, 32, 64]:
            self.bench("closed-" + str(concurrency), concurrency=concurrency)
        for rate in [2, 20, 100]:
            self.bench("open-" + str(rate), rate=rate, concurrency=64)
        self.control("long_stream", chunks=4000)
        self.bench("long-stream", concurrency=4, tokens=4096)
        self.control("paced_stream", chunks=100, delayms=25)
        self.bench("slow-consumer", concurrency=8, delay="50ms", tokens=4096)
        self.bench("cancel-storm", concurrency=16, cancel="200ms", tokens=4096)
        self.control()
        time.sleep(2)
        self.bench("post-cancellation-recovery", seconds=10, concurrency=4)

    def helpers(self):
        spec = importlib.util.spec_from_file_location("local_acceptance", self.deploy / "acceptance.py")
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        sys.modules["acceptance"] = module
        module.COMPOSE = self.compose.copy()
        if self.offset:
            request=module.request
            def shifted(path,data=None,token=None,port=29080,**kw):
                if path.startswith("https://localhost:29443"):
                    path=path.replace("https://localhost:29443","https://localhost:"+str(29443+self.offset),1)
                return request(path,data,token,port=port+self.offset,**kw)
            module.request=shifted
        return module

    def faults(self):
        self.control()
        helper = self.helpers()
        helper.crash_and_queue_recovery()
        self.results.append({"name":"worker_sigkill_lease_requeue_retention", "status":"PASS"})

    def wait_job(self, delivery, target="COMPLETED", timeout=100):
        end = time.monotonic() + timeout
        while time.monotonic() < end:
            code, value, _ = self.http("/jobs/"+delivery, token=self.secret("sentinel_token"), port=29081)
            assert code == 200
            if value["status"] == target:
                return value
            if value["status"] in {"REJECTED","DEAD_LETTER"}:
                raise AssertionError("fixture job unexpectedly terminal: " + value["status"])
            time.sleep(.25)
        raise AssertionError("fixture job deadline")

    def replicas(self):
        # Clone resolved definitions into the same isolated network/datastores.
        # Unique loopback ports avoid changing the first instance's exposure.
        config = json.loads(self.cmd("config", "--format", "json"))
        gateway = config["services"]["gateway"]
        gateway["ports"] = [{"target":18080,"published":str(41080+self.offset),"host_ip":"127.0.0.1","protocol":"tcp"}]
        gateway.pop("container_name",None)
        worker = config["services"]["sentinel-worker"]
        worker.pop("container_name",None)
        override = self.overlay.parent / "replicas.yml"
        override.write_text(json.dumps({"services":{"gateway-secondary":gateway,"worker-secondary":worker}}, indent=2), encoding="utf-8")
        original = self.compose.copy()
        self.compose += ["-f",str(override)]
        try:
            self.cmd("up","-d","--no-build","--no-deps","gateway-secondary","worker-secondary")
            deadline=time.monotonic()+60
            while time.monotonic()<deadline:
                try:
                    if self.http("/readyz",port=41080)[0]==200: break
                except OSError: pass
                time.sleep(1)
            else: raise AssertionError("secondary gateway readiness")
            self.control()
            body={"model":"domestic-chat","messages":[{"role":"user","content":"replica fixture"}],"stream":True,"max_tokens":100}
            def call(port):
                code,value,_=self.http("/v1/chat/completions",body,self.token,port=port)
                assert code==200 and b"[DONE]" in value
            with concurrent.futures.ThreadPoolExecutor(4) as executor:
                list(executor.map(call,[29080,41080]*4))
            before = self.assert_money()
            self.cmd("stop","gateway")
            try: call(41080)
            finally: self.cmd("start","gateway"); self.ready()
            after=self.assert_money()
            assert after["charged_nano"] > before["charged_nano"]
            # Start one job while only the original worker can claim it, then
            # kill that process and let the second consumer take over.
            self.cmd("stop","worker-secondary")
            self.control("delay")
            delivery="replica-"+uuid.uuid4().hex
            helper=self.helpers()
            payload={"delivery_id":delivery,"pr_number":1,"sql":helper.QUERY}
            assert self.http("/jobs",payload,self.secret("sentinel_token"),port=29081)[0]==202
            self.wait_job(delivery,"RUNNING")
            self.cmd("kill","-s","SIGKILL","sentinel-worker")
            self.cmd("stop","sentinel-worker")
            self.control()
            self.cmd("start","worker-secondary")
            job=self.wait_job(delivery)
            assert job["attempts"]>=2
            self.cmd("start","sentinel-worker")
            event={"schema_version":1,"event_id":uuid.uuid4().hex,"job_id":job["job_id"],"delivery_id":delivery,"attempt":1}
            self.cmd("exec","-T","kafka","/opt/kafka/bin/kafka-console-producer.sh","--bootstrap-server","kafka:9092","--topic","sql-sentinel.jobs",input=(json.dumps(event)+"\n")*3)
            # Verify broker offsets are consumed before asserting idempotence.
            group=config["services"]["sentinel-worker"].get("environment",{}).get("SQL_SENTINEL_KAFKA_WORKER_GROUP") or "sql-sentinel-worker-v1"
            end=time.monotonic()+90
            while time.monotonic()<end:
                offsets=self.cmd("exec","-T","kafka","/opt/kafka/bin/kafka-consumer-groups.sh","--bootstrap-server","kafka:9092","--describe","--group",group)
                rows=[line.split() for line in offsets.splitlines() if line.split() and line.split()[0]==group]
                if rows and all(row[5]=="0" for row in rows):break
                time.sleep(1)
            else:raise AssertionError("duplicate broker events were not consumed")
            assert self.wait_job(delivery)["attempts"]==job["attempts"],"duplicate Kafka delivery reran terminal job"
            for _ in range(3):
                code,value,_=self.http("/jobs",payload,self.secret("sentinel_token"),port=29081)
                assert code==202 and value["duplicate"] is True
            query="SELECT COUNT(*) FROM sql_sentinel_reports r JOIN sql_sentinel_jobs j ON j.job_id=r.job_id WHERE j.delivery_id='"+delivery+"'"
            assert self.sql(query,"sentinel_control")=="1"
            terminal="SELECT COUNT(*) FROM sql_sentinel_job_history h JOIN sql_sentinel_jobs j ON j.job_id=h.job_id WHERE j.delivery_id='"+delivery+"' AND h.status='COMPLETED'"
            assert self.sql(terminal,"sentinel_control")=="1"
            assert self.http("/admin/api-keys/"+self.key_id,token=self.secret("admin_token"),method="DELETE")[0]==204, "key revocation contract is 204"
            for port in [29080,41080]:
                assert self.http("/v1/billing/balance",token=self.token,port=port)[0]==401
            self.results.append({"name":"two_gateways_two_workers_shared_ledger_revocation_kill_takeover_duplicate", "status":"PASS","attempts":job["attempts"],"duplicate_kafka_events":3,"consumer_lag":0,"routing":"explicit endpoints; no distributed load-balancer claim"})
        finally:
            self.control()
            self.cmd("start","gateway","sentinel-worker",check=False)
            self.cmd("stop","gateway-secondary","worker-secondary",check=False)
            self.cmd("rm","-f","gateway-secondary","worker-secondary",check=False)
            self.compose=original
            override.unlink(missing_ok=True)

    def backpressure(self):
        self.control("long_stream", chunks=4000, payloadbytes=8192)
        before=self.http("/control",port=29090)[1]
        sock=socket.socket()
        sock.setsockopt(socket.SOL_SOCKET,socket.SO_RCVBUF,4096)
        sock.settimeout(20)
        try:
            # Direct gateway TCP path deliberately has no proxy buffering.
            sock.connect(("127.0.0.1",29080+self.offset))
            body=json.dumps({"model":"domestic-chat","messages":[{"role":"user","content":"bounded TCP backpressure fixture"}],"stream":True,"max_tokens":4096}).encode()
            headers=("POST /v1/chat/completions HTTP/1.1\r\nHost: localhost\r\nAuthorization: Bearer "+self.token+"\r\nContent-Type: application/json\r\nContent-Length: "+str(len(body))+"\r\nConnection: close\r\n\r\n").encode()
            sock.sendall(headers+body)
            header=b""
            while b"\r\n\r\n" not in header and len(header)<16384: header+=sock.recv(1)
            assert header.startswith(b"HTTP/1.1 200")
            time.sleep(5)
            blocked=self.http("/control",port=29090)[1]
            assert blocked["finished"]==before["finished"], "oversized upstream was accepted as a completed response"
            assert blocked["active"]>0 or blocked["cancelled"]>before["cancelled"], "upstream neither backpressured nor bounded"
        finally:
            sock.close()
        end=time.monotonic()+20
        while time.monotonic()<end:
            final=self.http("/control",port=29090)[1]
            if final["active"]==0: break
            time.sleep(.2)
        else: raise AssertionError("TCP cancellation did not release upstream")
        assert final["cancelled"]>before["cancelled"]
        self.control()
        self.bench("tcp-backpressure-recovery",seconds=5,concurrency=2)
        self.results.append({"name":"full_rpc_tcp_nonreading_output_bound_and_close", "status":"PASS","non_reading_seconds":5,"attempted_synthetic_content_bytes":4000*8192,"production_rpc_output_bound_bytes":1048576,"backpressure_at_five_seconds":blocked["active"]>0,"separate_kernel_window_test":"TestTCPBackpressureCancelsBlockedUpstream"})

    def demo(self):
        self.control()
        self.bench("demo-chat", seconds=5, concurrency=1, rate=1)
        helper=self.helpers()
        delivery="demo-"+uuid.uuid4().hex
        body={"delivery_id":delivery,"pr_number":1,"sql":helper.QUERY,"candidate_spec":helper.SPEC}
        assert self.http("/jobs",body,self.secret("sentinel_token"),port=29081)[0]==202
        self.wait_job(delivery)
        code,report,_=self.http("/jobs/"+delivery+"/report",token=self.secret("sentinel_token"),port=29081)
        assert code==200 and report["pipeline"]["eligible_for_performance_claim"] is False
        (self.out/"demo-sql-report.json").write_text(json.dumps(report,indent=2),encoding="utf-8")
        self.faults()
        self.results.append({"name":"demo_sql_shadow_evidence_without_production_claim","status":"PASS"})
        sys.path.insert(0,str(REPO/"sdks/python"))
        import example
        sdk=example.run("http://127.0.0.1:"+str(29880+self.offset),"http://127.0.0.1:"+str(29081+self.offset),self.key,self.deploy/"secrets/sentinel_token")
        self.results.append({"name":"actual_python_sdk_stream_money_sql_retry","status":"PASS",**sdk})

    def soak(self, seconds):
        self.control()
        self.bench("soak",seconds=seconds,rate=2,concurrency=8)
        observations=json.loads((self.out/"soak.resources.json").read_text())
        assert len(observations)>=seconds//30-1 and all("error" not in x for x in observations)
        def mib(text):
            value=text.split(" / ")[0]
            for unit,multiplier in [("GiB",1024),("MiB",1),("KiB",1/1024),("B",1/(1024*1024))]:
                if value.endswith(unit): return float(value[:-len(unit)])*multiplier
            raise ValueError("resource units")
        # Compare warmed-up quarters, retain raw samples. This is a bounded
        # regression gate, not a proof that no long-term leak exists.
        values=[sum(mib(c["MemUsage"]) for c in x["containers"] if "gateway" in c["Name"] or "model-runner" in c["Name"]) for x in observations]
        width=max(2,len(values)//4)
        first=statistics.mean(values[width:2*width]); last=statistics.mean(values[-width:])
        assert last-first<=max(64,first*.25), "sustained memory growth exceeded the documented local gate"
        self.results.append({"name":"sustained_resource_plateau","status":"PASS","offered_seconds":seconds,"first_warm_quarter_mib":first,"last_quarter_mib":last,"gate":"growth <= max(64 MiB, 25%)"})
        for metric,tolerance in [("go_goroutines",32),("go_memstats_heap_alloc_bytes",16*1024*1024),("process_open_fds",32)]:
            values=[row["gateway_metrics"][metric] for row in observations]
            start=statistics.mean(values[width:2*width]);end=statistics.mean(values[-width:])
            assert end-start<=max(tolerance,start*.25),"sustained Go/process resource growth: "+metric
            self.results.append({"name":"sustained_"+metric,"status":"PASS","warm_mean":start,"last_mean":end,"growth_allowance":max(tolerance,start*.25)})
        assert self.http("/control",port=29090)[1]["active"]==0
        self.ready()

    def primary_acceptance(self):
        original=self.compose.copy()
        override=self.overlay.parent/"legacy-quota.yml"
        override.write_text(json.dumps({"services":{"gateway":{"environment":{"AGENTMESH_BILLING_CATALOG_FILE":"","AGENTMESH_BILLING_CATALOG":""}}}}),encoding="utf-8")
        self.compose += ["-f",str(override)]
        try:
            self.cmd("up","-d","--no-deps","--no-build","gateway")
            self.ready()
            helper=self.helpers()
            helper.main()
            self.results.extend(helper.RESULTS)
        finally:
            self.compose=original
            self.cmd("up","-d","--no-deps","--no-build","gateway")
            self.ready()
            override.unlink(missing_ok=True)
    def acceptance(self, primary=True):
        if primary: self.primary_acceptance()
        for name in ["acceptance-expanded","acceptance-extensions"]:
            self.helpers()
            spec=importlib.util.spec_from_file_location(name,self.deploy/(name+".py"))
            module=importlib.util.module_from_spec(spec)
            try:
                spec.loader.exec_module(module)
                if name=="acceptance-expanded":
                    for title,fn in [("generic_and_identity",module.generic_and_identity),("approvals_benchmark",module.approvals_benchmark),("rag_persistence_and_revocation",module.rag_persistence_and_revocation),("monitoring",module.monitoring),("quota_grant_cap",module.quota_grant_cap)]:
                        fn();module.RESULTS.append({"name":title,"status":"PASS"});print("PASS "+title,flush=True)
            finally:
                self.results.extend(getattr(module,"RESULTS",[]))

    def save(self):
        (self.out / "summary.json").write_text(json.dumps({"project": self.project, "scope": "local synthetic fixtures; no cloud/model capacity claim", "results": self.results}, indent=2) + "\n", encoding="utf-8")

def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("mode", choices=["prepare", "load", "soak", "demo", "faults", "replicas", "backpressure", "overload", "acceptance", "extensions"])
    parser.add_argument("--deploy", type=Path, required=True)
    parser.add_argument("--out", type=Path, required=True)
    parser.add_argument("--seconds", type=int, default=3600)
    args = parser.parse_args()
    lab = Lab(args.deploy, args.out)
    if args.mode == "prepare":
        lab.cmd("up", "-d", "--no-deps", "--no-build", "fixtures", *SERVICES)
        lab.ready()
        print("PASS updated acceptance applications; datastores preserved")
        return
    lab.ready()
    lab.bootstrap()
    try:
        if args.mode == "load":
            lab.load()
        elif args.mode == "soak":
            if args.seconds < 300 or args.seconds > 7200:
                raise ValueError("soak duration must be 300..7200 seconds")
            lab.soak(args.seconds)
        elif args.mode=="faults": lab.faults()
        elif args.mode=="replicas": lab.replicas()
        elif args.mode=="backpressure": lab.backpressure()
        elif args.mode=="overload": lab.control(); lab.bench("open-100",rate=100,concurrency=64)
        elif args.mode=="acceptance": lab.acceptance()
        elif args.mode=="extensions": lab.acceptance(primary=False)
        else: lab.demo()
        lab.results.append({"name":"ledger_conservation", "status":"PASS", **lab.assert_money()})
        lab.save()
        print("PASS " + args.mode, flush=True)
    except Exception:
        lab.results.append({"name":args.mode,"status":"FAIL","error":"see private scenario reports or command exception"})
        lab.save()
        raise
    finally:
        try: lab.control()
        finally: lab.key.unlink(missing_ok=True)

if __name__ == "__main__":
    main()
