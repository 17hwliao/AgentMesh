"""Isolated configuration regression. No model calls or operational mutations."""
from pathlib import Path
import argparse
import hashlib
import json
import shutil
import subprocess
import sys
import tempfile

root = Path(__file__).resolve().parent
parser = argparse.ArgumentParser()
parser.add_argument("--compose-binary", help="optional standalone Compose binary for version compatibility tests")
args = parser.parse_args()
compose = [args.compose_binary] if args.compose_binary else ["docker", "compose"]

def operational_hashes():
    files = [root / ".env", *(root / "secrets").glob("*")]
    return {str(p): hashlib.sha256(p.read_bytes()).hexdigest() for p in files if p.is_file()}

before = operational_hashes()
with tempfile.TemporaryDirectory(prefix="qwen-config-") as temporary:
    base = Path(temporary)
    key = base / "provider-key"
    key.write_text("fixture-private-key\n", encoding="utf-8")

    def prepare(name, arguments, success=True):
        folder = base / name
        folder.mkdir(exist_ok=True)
        for file in ["setup.py", "compose.yml", "qwen.yml", "qwen-billing.yml"]:
            shutil.copyfile(root / file, folder / file)
        result = subprocess.run([sys.executable, str(folder / "setup.py"), *arguments], capture_output=True, text=True)
        assert (result.returncode == 0) == success, name + " unexpected setup result"
        assert "fixture-private-key" not in result.stdout + result.stderr, "credential was printed"
        return folder

    common = ["--domain", "api.example.test", "--ollama-url", "http://ollama:11434"]
    qwen = [*common, "--chat-provider", "qwen", "--qwen-key-file", str(key), "--qwen-base-url", "https://workspace.cn-beijing.maas.aliyuncs.com/compatible-mode/v1"]
    folder = prepare("qwen", qwen)
    assert not (folder / "secrets/ark_key").exists()
    assert (folder / "secrets/compatible_key").read_text().strip() == "fixture-private-key"
    command = [*compose, "--project-directory", str(folder), "--env-file", str(folder / ".env"), "-f", str(folder / "compose.yml"), "-f", str(folder / "qwen.yml")]

    def configuration(extra=()):
        result = subprocess.run([*command, *extra, "config", "--format", "json"], capture_output=True, text=True)
        assert result.returncode == 0, "Compose configuration failed"
        return json.loads(result.stdout)

    config = configuration()
    services = config["services"]
    gateway = services["gateway"]
    assert gateway["command"][-1] == "compatible"
    aliases = json.loads(gateway["environment"]["COMPATIBLE_MODELS"])
    assert aliases == {"sql-review": "qwen-plus", "domestic-chat": "qwen-plus"}
    routes = json.loads(services["am-migrate"]["environment"]["AGENTMESH_BOOTSTRAP_MODEL_ROUTES"])
    assert routes == {"sql-review": ["compatible"], "domestic-chat": ["compatible"], "embedding": ["ollama"]}
    for name in ["gateway", "usage-relay", "usage-worker"]:
        service = services[name]
        targets = [item.get("target", item["source"]) for item in service["secrets"]]
        mounts = {target if target.startswith("/") else "/run/secrets/" + target for target in targets}
        for variable in ["ARK_API_KEY_FILE", "ARK_BASE_URL", "ARK_MODEL"]:
            assert variable not in service["environment"], name + " retained inactive provider configuration: " + variable
        assert "ark_key" not in {item["source"] for item in service["secrets"]}
        for variable, value in service["environment"].items():
            if variable.endswith("_FILE") and value:
                assert value in mounts, name + " missing secret mount: " + variable + " targets=" + repr(sorted(mounts))
    money = configuration(["-f", str(folder / "qwen-billing.yml")])
    mounts = {item["source"] for item in money["services"]["gateway"]["secrets"]}
    assert mounts == {"am_dsn", "redis_url", "admin_token", "compatible_key", "billing_catalog"}
    assert "AGENTMESH_BILLING_CATALOG_FILE" not in gateway["environment"]
    saved = {p.name: p.read_bytes() for p in (folder / "secrets").iterdir()}
    env = (folder / ".env").read_bytes()
    key.write_text("another-private-key\n", encoding="utf-8")
    prepare("qwen", qwen)
    assert env == (folder / ".env").read_bytes()
    assert saved == {p.name: p.read_bytes() for p in (folder / "secrets").iterdir()}
    key.write_text("fixture-private-key\n", encoding="utf-8")
    legacy = prepare("ark", [*common, "--ark-key-file", str(key), "--ark-model", "endpoint-id"])
    result = subprocess.run([*compose, "--project-directory", str(legacy), "--env-file", str(legacy / ".env"), "-f", str(legacy / "compose.yml"), "config", "--format", "json"], capture_output=True, text=True)
    assert result.returncode == 0, "legacy Compose configuration failed"
    assert json.loads(result.stdout)["services"]["gateway"]["command"][-1] == "ark,ollama"
    original = (legacy / ".env").read_bytes()
    prepare("ark", qwen, success=False)
    assert (legacy / ".env").read_bytes() == original and not (legacy / "secrets/compatible_key").exists()
    prepare("acceptance", ["--acceptance"])
    for index, endpoint in enumerate(["http://workspace/compatible-mode/v1", "https://{WorkspaceId}.cn-beijing.maas.aliyuncs.com/compatible-mode/v1", "https://workspace/compatible-mode/v1?token=invalid", "https://workspace:bad/compatible-mode/v1"]):
        args = qwen.copy()
        args[-1] = endpoint
        invalid = prepare("invalid" + str(index), args, success=False)
        assert not (invalid / "secrets").exists()
assert operational_hashes() == before, "operational configuration changed"
print("PASS Qwen/legacy/fixture setup, alias routes, secret mounts, optional billing, preservation and invalid configuration; no live requests.")
