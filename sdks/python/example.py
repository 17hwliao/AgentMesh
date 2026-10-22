"""Executable local/HTTPS example: stream, balance, SQL job and idempotent retry."""
import argparse
import json
import time
import uuid
from pathlib import Path
from backend_client import Client

def run(gateway_url, sentinel_url, gateway_key_file, sentinel_key_file, ca_file=None):
    gateway=Client(gateway_url,gateway_key_file,ca_file=ca_file)
    sentinel=Client(sentinel_url,sentinel_key_file,ca_file=ca_file)
    before=gateway.balance()
    text=[]
    with gateway.chat([{"role":"user","content":"local interview fixture"}]) as chunks:
        for chunk in chunks:
            for choice in chunk.get("choices",[]):
                text.append(choice.get("delta",{}).get("content",""))
    after=gateway.balance()
    assert after["balance_nano"]<=before["balance_nano"] and after["held_nano"]==before["held_nano"]
    delivery="sdk-"+uuid.uuid4().hex
    query="SELECT id, status, created_at FROM orders WHERE user_id=42 AND status='paid' ORDER BY created_at DESC LIMIT 20"
    candidate={"table":"orders","index_name":"idx_cand_sdk","columns":[{"name":"user_id"},{"name":"status"},{"name":"created_at","direction":"DESC"}]}
    sentinel.submit_job(delivery,query,candidate)
    deadline=time.monotonic()+90
    while time.monotonic()<deadline:
        job=sentinel.job(delivery)
        if job["status"]=="COMPLETED":break
        if job["status"] in {"REJECTED","DEAD_LETTER"}:raise RuntimeError("fixture SQL failed")
        time.sleep(.5)
    else:raise TimeoutError("SQL job deadline")
    assert sentinel.submit_job(delivery,query,candidate)["duplicate"] is True
    report=sentinel.request("GET","/jobs/"+delivery+"/report")
    assert report["pipeline"]["eligible_for_performance_claim"] is False
    return {"stream_content_characters":len("".join(text)),"actual_charge_nano":before["balance_nano"]-after["balance_nano"],"currency":after["currency"],"job_status":job["status"],"idempotent_retry":True,"evidence_scope":"local shadow fixture; no L3 or production SQL claim"}

if __name__=="__main__":
    p=argparse.ArgumentParser()
    p.add_argument("--gateway-url",default="http://127.0.0.1:29880")
    p.add_argument("--sentinel-url",default="http://127.0.0.1:29081")
    p.add_argument("--gateway-key-file",type=Path,required=True)
    p.add_argument("--sentinel-key-file",type=Path,required=True)
    p.add_argument("--ca-file",type=Path)
    a=p.parse_args()
    print(json.dumps(run(a.gateway_url,a.sentinel_url,a.gateway_key_file,a.sentinel_key_file,a.ca_file),indent=2))
