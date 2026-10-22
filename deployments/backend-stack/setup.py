"""Generate local Compose secrets. Existing files are preserved, never rotated."""
import argparse
import json
import os
from pathlib import Path
import secrets
import re
from urllib.parse import urlparse

root = Path(__file__).resolve().parent
parser = argparse.ArgumentParser()
parser.add_argument("--acceptance", action="store_true")
parser.add_argument("--domain")
parser.add_argument("--chat-provider", choices=["ark", "qwen"], default="ark")
parser.add_argument("--ark-key-file", type=Path)
parser.add_argument("--ark-model")
parser.add_argument("--qwen-key-file", type=Path)
parser.add_argument("--qwen-base-url", help="exact HTTPS OpenAI-compatible Base URL from your Bailian workspace")
parser.add_argument("--qwen-model", default="qwen-plus")
parser.add_argument("--qwen-vision",action="store_true",help="explicitly enable images for domestic-chat after verifying the selected model")
parser.add_argument("--qwen-tools",action="store_true",help="explicitly enable client-side function calls for domestic-chat after verifying the selected model")
parser.add_argument("--qwen-use-max-completion-tokens", action="store_true", help="only for models documented to support max_completion_tokens")
parser.add_argument("--ollama-url", help="reachable existing Ollama endpoint; no model service is silently provisioned")
parser.add_argument("--ollama-model", default="qwen2.5:7b")
parser.add_argument("--embedding-provider", choices=["ollama", "compatible"], default="ollama")
parser.add_argument("--embedding-model")
parser.add_argument("--embedding-dimension",type=int)
args = parser.parse_args()
if (args.qwen_vision or args.qwen_tools) and (args.acceptance or args.chat_provider!="qwen"):
    parser.error("vision requires the production Qwen profile")
args.embedding_model = args.embedding_model or ("qwen3.7-text-embedding-flash" if args.embedding_provider=="compatible" else "nomic-embed-text")
args.embedding_dimension = args.embedding_dimension if args.embedding_dimension is not None else (512 if args.embedding_provider=="compatible" else 768)
if args.embedding_provider=="compatible" and (args.acceptance or args.chat_provider!="qwen"):
    parser.error("compatible embeddings require a fresh production Qwen profile")
if args.acceptance and args.chat_provider != "ark":
    parser.error("acceptance fixtures cannot be combined with the production Qwen profile")
if not args.acceptance:
    if not args.domain or (args.embedding_provider=="ollama" and not args.ollama_url):
        parser.error("production requires --domain; Ollama embeddings also require --ollama-url")
    if args.chat_provider == "ark" and (not args.ark_key_file or not args.ark_model):
        parser.error("Ark requires --ark-key-file and --ark-model")
    if args.chat_provider == "qwen" and (not args.qwen_key_file or not args.qwen_base_url):
        parser.error("Qwen requires --qwen-key-file and --qwen-base-url")
args.ollama_url = args.ollama_url or "http://127.0.0.1:11434"
if not 1 <= args.embedding_dimension <= 4096:
    parser.error("embedding dimension must be 1..4096")
if not args.acceptance:
    if not re.fullmatch(r"(?=.{1,253}$)[A-Za-z0-9](?:[A-Za-z0-9.-]*[A-Za-z0-9])?", args.domain):
        parser.error("domain must be a hostname")
    endpoint = urlparse(args.ollama_url)
    if endpoint.scheme not in {"http", "https"} or not endpoint.hostname or endpoint.username or endpoint.password:
        parser.error("Ollama URL must be http(s) without embedded credentials")
    model_values = [args.ark_model] if args.chat_provider == "ark" else [args.qwen_model, args.qwen_base_url]
    for value in [args.domain, args.ollama_url, args.ollama_model, args.embedding_model, *model_values]:
        if not value or any(c in value for c in "\r\n\x00$#'\"\\") or value != value.strip():
            parser.error("configuration values must be single safe lines")
    if args.chat_provider == "qwen":
        endpoint = urlparse(args.qwen_base_url)
        try:
            endpoint_port = endpoint.port
        except ValueError:
            parser.error("invalid Qwen endpoint port")
        if (endpoint.scheme != "https" or not endpoint.hostname or endpoint.username or endpoint.password
                or endpoint.query or endpoint.fragment or endpoint_port not in {None, 443}
                or endpoint.path.rstrip("/") != "/compatible-mode/v1"
                or any(c in args.qwen_base_url for c in "{}<>")):
            parser.error("Qwen Base URL must be an actual HTTPS compatible-mode/v1 workspace endpoint")
        if not re.fullmatch(r"[A-Za-z0-9._/-]{1,128}", args.qwen_model):
            parser.error("Qwen model must be a valid model ID, not a display name")
        if not re.fullmatch(r"[A-Za-z0-9._/-]{1,128}", args.embedding_model):
            parser.error("embedding model must be a valid model ID")
        if args.embedding_provider=="compatible" and args.embedding_model=="qwen3.7-text-embedding-flash" and args.embedding_dimension not in {256,512,768,1024}:
            parser.error("qwen3.7-text-embedding-flash supports 256,512,768,1024 dimensions")
        if (root / ".env").exists() and "CHAT_PROVIDER=qwen" not in (root / ".env").read_text(encoding="utf-8").splitlines():
            parser.error("use a fresh deployment directory; existing configuration is not migrated or overwritten")
    key_path = args.ark_key_file if args.chat_provider == "ark" else args.qwen_key_file
    try:
        api_key = key_path.read_text(encoding="utf-8-sig").strip()
    except (OSError, UnicodeError):
        parser.error("API key file cannot be read")
    if not api_key or len(api_key) > 16384 or any(c.isspace() or ord(c) < 32 for c in api_key):
        parser.error("API key file must contain one nonempty credential")
folder = root / "secrets"
folder.mkdir(mode=0o700, exist_ok=True)

def save(name, value):
    path = folder / name
    if not path.exists():
        path.write_bytes((value + "\n").encode("utf-8"))
        if os.name != "nt":
            # Private 0700 parent protects host access; the explicitly mounted
            # files must be readable by non-root UID 10001 in Linux Compose.
            path.chmod(0o444)
    return path.read_text(encoding="utf-8").strip()

for name in ["control_root", "baseline_root", "candidate_root", "am_password", "ss_password", "migrate_password", "baseline_password", "candidate_password", "redis_password", "admin_token", "sentinel_token", "bootstrap_key", "webhook_secret","jwt_key"]:
    save(name, secrets.token_hex(32))
if args.acceptance or args.chat_provider == "ark":
    save("ark_key", "acceptance-fixture-only" if args.acceptance else api_key)
else:
    save("compatible_key", api_key)
save("github_token", "acceptance-fixture-only" if args.acceptance else "disabled")

def password(name):
    return (folder / name).read_text(encoding="utf-8").strip()

def dsn(user, secret, host, database):
    return f"{user}:{password(secret)}@tcp({host}:3306)/{database}?parseTime=true&loc=UTC&timeout=5s&readTimeout=30s&writeTimeout=10s&time_zone=%27%2B00%3A00%27"

for name, params in {
    "am_dsn": ("agentmesh", "am_password", "control", "agentmesh"),
    "ss_dsn": ("sentinel", "ss_password", "control", "sentinel_control"),
    "am_migrate_dsn": ("migrator", "migrate_password", "control", "agentmesh"),
    "ss_migrate_dsn": ("migrator", "migrate_password", "control", "sentinel_control"),
    "baseline_dsn": ("baseline", "baseline_password", "baseline", "sentinel"),
    "candidate_dsn": ("candidate", "candidate_password", "candidate", "sentinel"),
    "baseline_seed_dsn": ("root", "baseline_root", "baseline", "sentinel"),
    "candidate_seed_dsn": ("root", "candidate_root", "candidate", "sentinel"),
}.items():
    save(name, dsn(*params))
save("redis_url", f"redis://:{password('redis_password')}@redis:6379/0")
env = root / ".env"
if not env.exists():
    values = {
        "APP_DOMAIN": "localhost" if args.acceptance else args.domain,
        "OLLAMA_BASE_URL": "http://fixtures:8090" if args.acceptance else args.ollama_url,
        "OLLAMA_MODEL": "fixture" if args.acceptance else args.ollama_model,
        "OLLAMA_EMBEDDING_MODEL": "fixture" if args.acceptance else args.embedding_model,
        "GITHUB_REPOSITORIES": "17hwliao/sql-sentinel",
        "GITHUB_PUBLISH_COMMENTS": "false",
        "RAG_EMBEDDING_DIMENSION":"3" if args.acceptance else str(args.embedding_dimension),
        "EMBEDDING_PROVIDER":args.embedding_provider,
        "EMBEDDING_CNY_BILLING":"true" if args.embedding_provider=="compatible" else "false",
        "EMBEDDING_BASE_URL":args.qwen_base_url.rstrip("/") if args.embedding_provider=="compatible" else ("http://fixtures:8090" if args.acceptance else args.ollama_url),
        "SQL_SENTINEL_SHADOW_TABLES":"orders",
    }
    if args.acceptance or args.chat_provider == "ark":
        values.update({"ARK_BASE_URL": "http://fixtures:8090" if args.acceptance else "https://ark.cn-beijing.volces.com/api/v3",
                       "ARK_MODEL": "fixture" if args.acceptance else args.ark_model})
    else:
        values.update({"CHAT_PROVIDER": "qwen", "COMPATIBLE_BASE_URL": args.qwen_base_url.rstrip("/"),
                       "COMPATIBLE_MODELS": "'" + json.dumps({"sql-review": args.qwen_model, "domestic-chat": args.qwen_model}, separators=(",", ":")) + "'",
                       "COMPATIBLE_VISION_MODELS": "'[\"domestic-chat\"]'" if args.qwen_vision else "",
                       "COMPATIBLE_TOOL_MODELS": "'[\"domestic-chat\"]'" if args.qwen_tools else "",
                       "QWEN_RESERVATION_UNITS": "65536" if args.qwen_vision else "4096",
                       "COMPATIBLE_DISABLE_THINKING": "true",
                       "COMPATIBLE_USE_MAX_COMPLETION_TOKENS": str(args.qwen_use_max_completion_tokens).lower()})
    env.write_bytes(("\n".join(f"{key}={value}" for key, value in values.items()) + "\n").encode("utf-8"))
print("Compose configuration generated; secret values were not printed.")
