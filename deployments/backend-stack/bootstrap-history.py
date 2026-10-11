"""Explicit local ES index/API-key bootstrap. Never print root or app credentials.

Use after starting ES, before starting API/verifier. Local endpoint can be an
SSH tunnel; CA and DNS identity are verified. No root key is mounted in apps.
"""
import argparse,base64,json,os,ssl,urllib.request
from pathlib import Path
root=Path(__file__).resolve().parent
p=argparse.ArgumentParser();p.add_argument("--url",default="https://localhost:9200");a=p.parse_args()
if a.url not in {"https://localhost:9200","https://localhost:29200"}:p.error("use a verified local SSH tunnel")
tls=ssl.create_default_context(cafile=str(root/"secrets/grpc_ca"))
password=(root/"secrets/es_password").read_text().strip()
auth="Basic "+base64.b64encode(("elastic:"+password).encode()).decode()
def call(method,path,data):
    req=urllib.request.Request(a.url+path,data=json.dumps(data).encode(),method=method,headers={"Authorization":auth,"Content-Type":"application/json"})
    with urllib.request.urlopen(req,context=tls,timeout=15) as r:return json.load(r)
index="sentinel-priors-v1"
properties={key:{"type":"keyword"} for key in ["id","tenant_id","project_id","table","tables","columns","sql_sha256","snapshot_sha256","evidence_level"]}
properties.update({"text":{"type":"text"},"expires_at":{"type":"date"},"measured":{"type":"boolean"},"baseline_median_ms":{"type":"double"},"candidate_median_ms":{"type":"double"},"noise_ratio":{"type":"double"}})
try:call("PUT","/"+index,{"settings":{"number_of_shards":1,"number_of_replicas":0},"mappings":{"dynamic":"strict","properties":properties}})
except urllib.error.HTTPError as e:
    if e.code!=400:raise RuntimeError("history index bootstrap failed") from None
    raw=e.read(8192)
    if b"resource_already_exists_exception" not in raw:raise RuntimeError("history index mapping failed") from None
path=root/"secrets/es_api_key"
if not path.exists():
    result=call("POST","/_security/api_key",{"name":"sentinel-history","expiration":"30d","role_descriptors":{"history":{"cluster":[],"indices":[{"names":[index],"privileges":["read","index","delete"]}]}}})
    path.write_text(result["encoded"],encoding="utf-8")
    if os.name!="nt":path.chmod(0o444)
print("Historical index and expiring least-privilege app key prepared. Rotate the key before 30 days.")
