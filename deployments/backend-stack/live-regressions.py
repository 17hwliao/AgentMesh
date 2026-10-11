"""Run Go regressions against acceptance-only Redis/Kafka; never print credentials."""
import os
from pathlib import Path
import subprocess
import acceptance as a

root=Path(__file__).resolve().parent.parent
env=dict(os.environ)
env["AGENTMESH_TEST_REDIS_URL"]=a.secret("redis_url").replace("@redis:6379/","@127.0.0.1:29379/")
env["AGENTMESH_TEST_KAFKA_BROKER"]="127.0.0.1:29092"
checks=[("real-Redis-expanded",["go","test","-count=1","-v","./internal/reservation","-run","TestRealRedis"]), ("real-Kafka-expanded",["go","test","-count=1","-v","./internal/usagekafka","-run","TestRealKafka"])]
for name,cmd in checks:
    result=subprocess.run(cmd,cwd=root/"AgentMesh",env=env,capture_output=True,text=True,timeout=120)
    if result.returncode or "PASS" not in result.stdout:
        raise RuntimeError(name+" failed")
    (root/"validation"/(name+".txt")).write_text(result.stdout,encoding="utf-8")
    print("PASS "+name)
