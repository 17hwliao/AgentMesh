"""Generate internal mTLS secrets without replacing existing operational keys.

Run after setup.py; no cloud/payment/provider credentials are guessed. Optional
fixture pricing is explicitly selected and is never a live tariff.
"""
from pathlib import Path
import argparse,datetime,json,secrets,subprocess,os

root=Path(__file__).resolve().parent
parser=argparse.ArgumentParser()
parser.add_argument("--fixture-pricing",action="store_true")
args=parser.parse_args()
directory=root/"secrets";directory.mkdir(mode=0o700,exist_ok=True)
def write(name,value):
    path=directory/name
    if not path.exists():
        path.write_text(value,encoding="utf-8");path.chmod(0o444 if os.name != "nt" else 0o600)
    return path
def openssl(*parts):
    result=subprocess.run(["openssl",*map(str,parts)],capture_output=True,cwd=root,timeout=30)
    if result.returncode:raise RuntimeError("internal certificate generation failed")
ca=directory/"grpc_ca";ca_key=directory/"grpc_ca_key"
if ca.exists()!=ca_key.exists():raise RuntimeError("partial CA requires operator recovery")
if not ca.exists():openssl("req","-x509","-newkey","ed25519","-nodes","-subj","/CN=AgentMesh internal CA","-days","90","-addext","basicConstraints=critical,CA:TRUE","-addext","keyUsage=critical,keyCertSign,cRLSign","-keyout",ca_key,"-out",ca)
for name,host in [("model_runner","model-runner"),("verifier","verifier"),("gateway","gateway"),("worker","sentinel-worker"),("elasticsearch_rsa","elasticsearch")]:
    cert,key=directory/(name+"_cert"),directory/(name+"_key")
    if cert.exists()!=key.exists():raise RuntimeError("partial leaf certificate requires operator recovery")
    if cert.exists():continue
    csr=directory/(name+"_csr");ext=directory/(name+"_extensions")
    ext.write_text(f"basicConstraints=critical,CA:FALSE\nkeyUsage=critical,digitalSignature\nextendedKeyUsage=serverAuth,clientAuth\nsubjectAltName=DNS:{host},DNS:localhost,IP:127.0.0.1\n",encoding="utf-8")
    openssl("req","-new","-newkey","rsa:2048" if name=="elasticsearch_rsa" else "ed25519","-nodes","-subj",f"/CN={host}","-keyout",key,"-out",csr)
    openssl("x509","-req","-in",csr,"-CA",ca,"-CAkey",ca_key,"-set_serial","0x"+secrets.token_hex(16),"-days","80","-extfile",ext,"-out",cert)
    csr.unlink();ext.unlink()
for path in directory.iterdir():
    # Compose file secrets are bind mounts; UID 10001 needs read access.
    # The private 0700 host directory protects access outside containers.
    if path.is_file() and os.name != "nt":path.chmod(0o600 if path.name=="grpc_ca_key" else 0o444)
for name in ["am_grpc_token","ss_grpc_token","runner_password","pg_password","es_password","write_password","write_root"]:write(name,secrets.token_urlsafe(36))
runner_password=(directory/"runner_password").read_text().strip()
write("runner_dsn",f"runner_reader:{runner_password}@tcp(control:3306)/agentmesh?parseTime=true&loc=UTC&timeout=3s&readTimeout=3s&writeTimeout=3s")
pg_password=(directory/"pg_password").read_text().strip()
write("pg_dsn",f"postgresql://sentinel_compare:{pg_password}@postgres:5432/sentinel_compare?sslmode=disable")
write_password=(directory/"write_password").read_text().strip()
write("write_dsn",f"write_probe:{write_password}@tcp(write-shadow:3306)/sentinel_write?parseTime=true&loc=UTC&timeout=3s&readTimeout=5s&writeTimeout=5s")
if args.fixture_pricing:
    expires=(datetime.datetime.now(datetime.timezone.utc)+datetime.timedelta(days=7)).isoformat()
    prices=[dict(provider="ark",model="sql-review",upstream_model="fixture",max_input_tokens=65536,input_nano_per_million=800000000,output_nano_per_million=2000000000,source="acceptance fixture; not a live tariff"),dict(provider="compatible",model="domestic-chat",upstream_model="fixture",max_input_tokens=65536,input_nano_per_million=800000000,output_nano_per_million=2000000000,source="acceptance fixture; not a live tariff")]
    write("billing_catalog",json.dumps(dict(currency="CNY",expires_at=expires,prices=prices),indent=2))
    write("compatible_key","fixture-compatible-key")
else:
    example=dict(currency="CNY",expires_at=(datetime.datetime.now(datetime.timezone.utc)+datetime.timedelta(days=7)).isoformat(),prices=[dict(provider="compatible",model="domestic-chat",upstream_model="qwen-plus",max_input_tokens=32768,input_nano_per_million=800000000,output_nano_per_million=2000000000,cached_input_nano_per_million=160000000,source="https://help.aliyun.com/zh/model-studio/qwen-plus; Beijing, non-thinking, <=128k, checked 2026-10-08; verify account tariff before enabling")])
    # An example is reviewable; it does not enable billing or create credentials.
    (root/"billing-catalog.example.json").write_text(json.dumps(example,ensure_ascii=False,indent=2)+"\n",encoding="utf-8")
print("Internal mTLS credentials and optional datastore secrets prepared; existing files preserved. Live billing catalog and compatible key must be supplied before extensions.yml starts.")
