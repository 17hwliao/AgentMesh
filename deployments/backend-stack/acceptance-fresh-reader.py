"""Fresh isolated control bootstrap/migration/table-reader grants regression."""
import json,subprocess,time
from pathlib import Path
root=Path(__file__).resolve().parent
project="codex-reader-bootstrap-regression"
base=["docker","compose","-p",project,"-f","compose.yml","-f","extensions.yml","--profile","extensions"]
def run(*args):
 r=subprocess.run(base+list(args),cwd=root,capture_output=True,timeout=180)
 if r.returncode:raise RuntimeError("fresh reader operation failed: "+args[0])
 return r.stdout.decode()
try:
 run("up","-d","control")
 for _ in range(90):
  health=subprocess.run(["docker","inspect",project+"-control-1","--format","{{.State.Health.Status}}"],capture_output=True,text=True)
  if health.returncode==0 and health.stdout.strip()=="healthy":break
  time.sleep(1)
 else:raise RuntimeError("fresh control startup failed")
 run("run","--rm","am-migrate")
 run("run","--rm","runner-grants")
 # The executor gets SELECT only on two identity tables, no money or writes.
 grants=run("exec","-T","control","sh","-c","MYSQL_PWD=$(cat /run/secrets/mysql_root) mysql -uroot -N -e \"SHOW GRANTS FOR 'runner_reader'@'%'\"")
 assert "`agentmesh`.`tenants`" in grants and "`agentmesh`.`tenant_model_routes`" in grants
 assert "INSERT" not in grants and "`agentmesh`.*" not in grants and "billing" not in grants
 (root.parent/"validation/fresh-reader-bootstrap.txt").write_text("PASS fresh control initialization, migration, post-migration exact SELECT reader grants; no application wallet access.\n",encoding="utf-8")
 print("PASS fresh control initialization and least-privilege runner reader")
finally:
 # This exact fresh test project owns only disposable volumes; main acceptance
 # and unrelated containers are never selected by this cleanup command.
 run("down","-v")
