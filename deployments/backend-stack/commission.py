"""Non-destructive real-domain smoke test. No GitHub comments are published."""
import argparse
import json
from pathlib import Path
import time
import urllib.error
import urllib.request
import uuid

parser = argparse.ArgumentParser()
parser.add_argument("--url", required=True, help="https://your-domain")
args = parser.parse_args()
if not args.url.startswith("https://"):
    parser.error("a real HTTPS endpoint is required")
root = Path(__file__).resolve().parent

def call(path, data=None, token=None):
    head = {"Content-Type": "application/json"}
    if token:
        head["Authorization"] = "Bearer " + token
    raw = json.dumps(data).encode() if data is not None else None
    with urllib.request.urlopen(urllib.request.Request(args.url.rstrip("/") + path, raw, head), timeout=150) as response:
        return response.status, response.read()

def secret(name):
    return (root / "secrets" / name).read_text(encoding="utf-8").strip()

assert call("/readyz")[0] == 200
assert call("/sentinel/readyz")[0] == 200
code, raw = call("/v1/chat/completions", {"model": "sql-review", "messages": [{"role": "user", "content": "Reply with OK."}], "stream": True, "max_tokens": 32}, secret("bootstrap_key"))
assert code == 200 and b"[DONE]" in raw and b'"error"' not in raw
assert call("/v1/embeddings", {"model": "embedding", "input": "deployment verification"}, secret("bootstrap_key"))[0] == 200
delivery = "commission-" + uuid.uuid4().hex
assert call("/sentinel/jobs", {"delivery_id": delivery, "pr_number": 1, "sql": "SELECT id FROM orders WHERE user_id=42 AND status='paid' ORDER BY created_at DESC LIMIT 20"}, secret("sentinel_token"))[0] == 202
deadline = time.monotonic() + 150
while time.monotonic() < deadline:
    _, raw = call("/sentinel/jobs/" + delivery, token=secret("sentinel_token"))
    status = json.loads(raw)["status"]
    if status == "COMPLETED":
        assert call("/sentinel/jobs/" + delivery + "/report", token=secret("sentinel_token"))[0] == 200
        print("PASS real HTTPS, real chat/embedding providers, model-generated SQL shadow report.")
        break
    assert status not in {"REJECTED", "DEAD_LETTER"}, "real model/shadow job failed"
    time.sleep(1)
else:
    raise AssertionError("real-domain job deadline exceeded")
