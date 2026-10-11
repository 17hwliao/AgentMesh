"""Regression: primary profile survives persisted inactive provider routes."""
from pathlib import Path
import json
import acceptance as a

tenant = a.sql("SELECT tenant_id FROM tenant_model_routes WHERE provider='compatible' AND model='domestic-chat' LIMIT 1")
assert tenant, "extension acceptance must have created a compatible-only route"
code, _, raw = a.request("/admin/api-keys", {"tenant_id": tenant}, a.secret("admin_token"))
a.expect(code, 201)
identity = json.loads(raw)
key = identity["api_key"]
before = json.loads(a.request("/control", port=29090)[2])["calls"]
try:
    a.expect(a.request("/readyz")[0], 200)
    a.expect(a.request("/v1/chat/completions", {**a.CHAT, "model": "domestic-chat"}, key)[0], 403)
    assert json.loads(a.request("/control", port=29090)[2])["calls"] == before
finally:
    # The fixture-only key is never logged or delivered.
    a.expect(a.request("/admin/api-keys/" + identity["key_id"], token=a.secret("admin_token"), method="DELETE")[0], 204)
(Path(__file__).parent.parent / "validation/inactive-provider-regression.txt").write_text(
    "PASS primary profile starts with existing compatible-only routes; inactive route returns 403 before any provider call; no route mutation or automatic fallback.\n", encoding="utf-8")
print("PASS inactive provider route startup and no fallback")
