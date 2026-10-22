"""One-command fixture interview demo. No cloud credentials or remote writes."""
import argparse
import importlib.util
import os
from pathlib import Path
import subprocess
import sys
import time
import json
import uuid

ROOT=Path(__file__).resolve().parent.parent

def run(args,cwd=ROOT,timeout=900):
    completed=subprocess.run([str(x) for x in args],cwd=cwd,capture_output=True,text=True,encoding="utf-8",errors="replace",timeout=timeout)
    if completed.returncode:
        # Setup/provision diagnostics can contain credentials. Do not replay them.
        raise RuntimeError("local demo step failed: "+str(args[0])+"; inspect the private fixture deployment")

def main():
    p=argparse.ArgumentParser()
    p.add_argument("--deploy",type=Path,default=ROOT.parent/"deploy")
    p.add_argument("--out",type=Path,default=ROOT/".private/interview-demo")
    p.add_argument("--reuse-images",action="store_true",help="explicitly reuse current locally built engineering images")
    a=p.parse_args();a.out=a.out.resolve();deploy=a.deploy.resolve()
    fresh=not deploy.exists()
    if fresh:
        run([sys.executable,ROOT/"scripts/export-backend-stack.py","--out",deploy])
        run([sys.executable,"setup.py","--acceptance"],deploy)
        run([sys.executable,"setup-extensions.py","--fixture-pricing"],deploy)
        (deploy/".private").mkdir(mode=0o700,exist_ok=True)
        (deploy/".private/lab-settings.json").write_text(json.dumps({"project":"codex-backend-lab-"+uuid.uuid4().hex[:8],"port_offset":1000,"fixture_history_image":"docker.elastic.co/elasticsearch/elasticsearch:8.19.17@sha256:aa6ee0ea2d708cea22a24ed44a420c5bbde9853d58a872e1e870f2086d04f652"}),encoding="utf-8")
    # Lab validation refuses production configurations before starting/building.
    spec=importlib.util.spec_from_file_location("backend_lab",ROOT/"scripts/backend-lab.py")
    lab_module=importlib.util.module_from_spec(spec);spec.loader.exec_module(lab_module)
    lab=lab_module.Lab(deploy,a.out)
    if not a.reuse_images:
        print("Building current local source images",flush=True)
        run(["docker","build","-t","local/agentmesh-backend:engineering","."])
        run(["docker","build","-t","local/sql-sentinel-backend:engineering","."],ROOT.parent/"sql-sentinel")
        run(["docker","build","-t","local/sql-sentinel-fixture:engineering","-f","Dockerfile.fixture","."],ROOT.parent/"sql-sentinel")
    if fresh:
        print("Bootstrapping isolated fixture datastores and migrations",flush=True)
        lab.cmd("build","proxy",timeout=900)
        lab.cmd("up","-d","control","baseline","candidate","redis","kafka",timeout=240)
        for service in ["am-migrate","ss-migrate","topics","runner-grants"]:
            lab.cmd("run","--rm",service,timeout=240)
        lab.cmd("--profile","setup","run","--rm","seed",timeout=240)
        lab.cmd("up","-d","elasticsearch","postgres","write-shadow","jaeger-init","jaeger","alertmanager",timeout=240)
        for _ in range(60):
            try:
                run([sys.executable,"bootstrap-history.py","--url","https://localhost:"+str(29200+lab.offset)],deploy,timeout=30)
                break
            except RuntimeError:time.sleep(2)
        else:raise RuntimeError("secured history bootstrap deadline")
    lab.cmd("up","-d","--no-build","fixtures",*lab_module.SERVICES,"proxy","prometheus","otel-collector",timeout=240)
    lab.ready()
    print("Running stream, actual ledger, SQL evidence, SIGKILL recovery and Python SDK demo",flush=True)
    # --out reports contain job/SQL evidence, never keys. Tenant key is removed.
    run([sys.executable,ROOT/"scripts/backend-lab.py","demo","--deploy",deploy,"--out",a.out],timeout=600)
    print("PASS local interview demo; report: "+str(a.out.resolve()/"summary.json"),flush=True)

if __name__=="__main__":main()
