"""Repeatable, isolated local follow-up verification. Never publish or touch operational services.

Modes: persistent (disposable MySQL), linux (whole-source race/vet), host
(whole-source test/vet), bench (Linux mock resources + SQL/RAG microbench), fuzz.
All Go test results keep test/package PASS/FAIL/SKIP counts separate.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import secrets
import subprocess
import tempfile
import time
import uuid

ROOT = Path(__file__).resolve().parent.parent
OUT = ROOT / "validation" / "local-improvements-2026-10-11"
OUT.mkdir(parents=True, exist_ok=True)
PROJECTS = [("agentmesh", ROOT / "AgentMesh", "local/agentmesh-verify:roadmap"),
            ("sentinel", ROOT / "sql-sentinel", "local/sql-sentinel-verify:roadmap")]

def call(args, **kw):
    return subprocess.run(args, check=True, capture_output=True, text=True, encoding="utf-8", errors="replace", **kw).stdout.strip()

def record(name, args, cwd=None):
    start = time.monotonic()
    logfile = OUT / (name + ".log")
    print("START " + name, flush=True)
    with logfile.open("w", encoding="utf-8") as f:
        p = subprocess.run(args, cwd=cwd, stdout=f, stderr=subprocess.STDOUT)
    counts = {level: {s: 0 for s in ("pass", "fail", "skip")} for level in ("tests", "packages")}
    skipped = []
    for line in logfile.read_text(encoding="utf-8").splitlines():
        try:
            row = json.loads(line)
        except (ValueError, TypeError):
            continue
        if not isinstance(row, dict):
            continue
        action = row.get("Action")
        if action in counts["tests"]:
            level = "tests" if row.get("Test") else "packages"
            counts[level][action] += 1
            if action == "skip":
                skipped.append({"package": row.get("Package"), "test": row.get("Test")})
    result = {"name": name, "status": "PASS" if p.returncode == 0 else "FAIL", "exit_code": p.returncode,
              "elapsed_seconds": round(time.monotonic()-start, 3), "counts": counts, "skipped": skipped,
              "log_sha256": hashlib.sha256(logfile.read_bytes()).hexdigest()}
    (OUT / (name + ".result.json")).write_text(json.dumps(result, indent=2)+"\n", encoding="utf-8")
    print(json.dumps(result, ensure_ascii=False), flush=True)
    if p.returncode:
        print(logfile.read_text(encoding="utf-8")[-12000:], flush=True)
        raise RuntimeError("verification failed: " + name)

def go_container(repo, image, command, network="none", extra=None):
    return ["docker", "run", "--rm", "--network", network, "--mount", f"type=bind,source={repo},target=/src,readonly",
            "-w", "/src", *(extra or []), "--entrypoint", "sh", image, "-c", command]

def persistent():
    suffix = uuid.uuid4().hex[:12]
    network = "local-improvement-" + suffix
    container = "local-improvement-mysql-" + suffix
    created_network = created_container = False
    try:
        with tempfile.TemporaryDirectory(prefix="backend-improvement-") as temp:
            directory = Path(temp).resolve()
            password = secrets.token_hex(24)
            (directory / "mysql_password").write_text(password, encoding="utf-8")
            (directory / "init.sql").write_text("CREATE DATABASE agentmesh_improvement;\nCREATE DATABASE sentinel_improvement;\n", encoding="utf-8")
            for name, database in (("am_dsn","agentmesh_improvement"),("ss_dsn","sentinel_improvement")):
                (directory / name).write_text(f"root:{password}@tcp({container}:3306)/{database}?parseTime=true&loc=UTC&timeout=5s&readTimeout=10s&writeTimeout=10s", encoding="utf-8")
            call(["docker", "network", "create", "--internal", "--label", "purpose=local-improvement-test", network])
            created_network = True
            call(["docker", "run", "-d", "--name", container, "--network", network,
                  "--label", "purpose=local-improvement-test", "--memory", "768m",
                  "--tmpfs", "/var/lib/mysql:rw,size=512m", "--mount", f"type=bind,source={directory},target=/run/test-secrets,readonly",
                  "--mount", f"type=bind,source={directory / 'init.sql'},target=/docker-entrypoint-initdb.d/init.sql,readonly",
                  "-e", "MYSQL_ROOT_PASSWORD_FILE=/run/test-secrets/mysql_password", "-e", "MYSQL_ROOT_HOST=%", "mysql:8.4.11"])
            created_container = True
            ready = False
            for _ in range(60):
                p = subprocess.run(["docker","exec",container,"sh","-c",'MYSQL_PWD="$(cat /run/test-secrets/mysql_password)" mysql -uroot -N -e "SELECT COUNT(*) FROM information_schema.schemata WHERE schema_name IN (\'agentmesh_improvement\',\'sentinel_improvement\')"'], capture_output=True, text=True)
                if p.returncode == 0 and p.stdout.strip() == "2":
                    ready = True
                    break
                time.sleep(1)
            if not ready:
                raise RuntimeError("isolated MySQL initialization failed")
            print("Isolated MySQL ready, no host port or operational volume", flush=True)
            extra = ["--mount", f"type=bind,source={directory},target=/run/test-secrets,readonly"]
            for name, repo, image in PROJECTS:
                if name == "agentmesh":
                    command = 'export AGENTMESH_TEST_MYSQL_DSN="$(cat /run/test-secrets/am_dsn)"; CGO_ENABLED=1 go test -race -json -count=1 ./internal/billing -run TestPersistentMoneyStateInvariants && CGO_ENABLED=1 go test -race -json -count=1 ./internal/billing -run TestRealMySQLMoneySettlementAndRecovery'
                else:
                    command = 'export SQL_SENTINEL_TEST_MYSQL_DSN="$(cat /run/test-secrets/ss_dsn)"; CGO_ENABLED=1 go test -race -json -count=1 ./internal/kafka ./internal/resultset -run "TestPersistentWorkflowStateInvariants|TestPersistentSQLCounterexamples"'
                record(name+"-persistent",go_container(repo,image,command,network,extra))
    finally:
        if created_container:
            call(["docker","rm","-f",container])
        if created_network:
            call(["docker","network","rm",network])
        print("Disposable MySQL/network cleanup complete", flush=True)

def linux():
    for name, repo, image in PROJECTS:
        record(name+"-linux-race",go_container(repo,image,"CGO_ENABLED=1 go test -race -json -count=1 ./... && go vet ./..."))

def host():
    for name,repo,_ in PROJECTS:
        record(name+"-host",["go","test","-json","-count=1","./..."],repo)
        record(name+"-host-vet",["go","vet","./..."],repo)

def bench():
    name,repo,image = PROJECTS[0]
    revision = call(["git","-C",str(repo),"rev-parse","HEAD"])
    extra = ["--mount",f"type=bind,source={OUT},target=/evidence", "-e", "LOCAL_BENCHMARK_OUTPUT_DIR=/evidence", "-e", "LOCAL_BENCHMARK_REVISION="+revision]
    record("agentmesh-resource-baseline",go_container(repo,image,"go test -json -count=1 ./internal/localbench -run TestLocalResourceEvidence",extra=extra))
    name,repo,image = PROJECTS[1]
    record("sentinel-microbench",go_container(repo,image,"go test -run '^$' -bench 'BenchmarkSQLAdmissionCorpus|BenchmarkScopedRetrieval' -benchmem -benchtime=1s -count=3 ./internal/sqladmit ./internal/rag"))

def fuzz():
    _,repo,image=PROJECTS[0]
    record("sse-fuzz",go_container(repo,image,"go test ./internal/sse -run '^$' -fuzz FuzzEventReader -fuzztime=3s -parallel=2"))

def contracts():
    record("qwen-setup-regression",["python",str(ROOT / "deploy" / "test-qwen-setup.py")])
    deploy = ROOT / "deploy"
    record("compose-extensions-config",["docker","compose","--profile","extensions","--env-file",str(deploy / ".env"),"-f",str(deploy / "compose.yml"),"-f",str(deploy / "acceptance.yml"),"-f",str(deploy / "extensions.yml"),"-f",str(deploy / "extensions-acceptance.yml"),"config","--quiet"],deploy)
    record("benchmark-cli-smoke",["go","run","./cmd/local-benchmark","--warmup","2","--rounds","1","--requests","8","--concurrency","2","--rate-requests","20","--rate-burst","2","--stream-chunks","4","--chunk-bytes","16","--read-delay-ms","1","--out",str(OUT / "cli-smoke.json")],PROJECTS[0][1])
    record("rag-contrast-cli",["go","run","./cmd/rag-demo","--in","docs/backend-roadmap.md","--question","What is the retention policy?","--contrast"],PROJECTS[1][1])
    demo=json.loads((OUT / "rag-contrast-cli.log").read_text(encoding="utf-8"))
    conditions=demo["contrast"]["cases"]
    if [c["mode"] for c in conditions] != ["without_context","retrieved_context","irrelevant_context"] or any(c["answer_status"] != "skipped_no_answerer" or "correct" in c for c in conditions):
        raise RuntimeError("RAG demo evidence contract failed")
    formatting()

def formatting():
    formatting_counts={}
    for name,repo,_ in PROJECTS:
        files = call(["rg","--files","-g","*.go"],cwd=repo).splitlines()
        output = call(["gofmt","-l",*files],cwd=repo)
        if output:
            raise RuntimeError("Go formatting failed: "+name)
        formatting_counts[name]=len(files)
    (OUT / "go-format.result.json").write_text(json.dumps({"name":"go-format","status":"PASS","files_checked":formatting_counts},indent=2)+"\n",encoding="utf-8")
    print("PASS all Go formatting",flush=True)

if __name__ == "__main__":
    parser=argparse.ArgumentParser()
    parser.add_argument("mode",choices=["persistent","linux","host","bench","fuzz","contracts","formatting"])
    args=parser.parse_args()
    globals()[args.mode]()
