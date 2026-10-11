"""Acceptance-only Go integrations; credentials are never printed."""
from pathlib import Path
import os,subprocess
import acceptance as a

root=Path(__file__).resolve().parent.parent
env=dict(os.environ)
env["AGENTMESH_TEST_MYSQL_DSN"]=a.secret("am_dsn").replace("tcp(control:3306)","tcp(127.0.0.1:29306)")
env["AGENTMESH_TEST_REDIS_URL"]=a.secret("redis_url").replace("@redis:6379/","@127.0.0.1:29379/")
result=subprocess.run(["go","test","-count=1","-v","./internal/billing","./internal/semanticcache","-run","TestReal"],cwd=root/"AgentMesh",env=env,capture_output=True,text=True,timeout=120)
(root/"validation/roadmap-money-cache.txt").write_text(result.stdout,encoding="utf-8")
if result.returncode or "PASS" not in result.stdout:raise RuntimeError("money/cache integration failed; inspect local test log")
print("PASS real MySQL money settlement/replay/holds/recovery and real Redis scoped cache TTL/capacity")
