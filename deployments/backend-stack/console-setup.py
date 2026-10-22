"""Configure two private consoles after backend setup; never prints credentials."""
import argparse
import base64
import json
import os
from pathlib import Path
import re
from urllib.parse import urlsplit


def host(value):
    if not re.fullmatch(r"[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?", value) or "." not in value or ".." in value or value.endswith(("example.com", "example.net", ".invalid")):
        raise ValueError("use a real lowercase DNS hostname without scheme or path")
    return value


def validate(config):
    origins = []
    for key in ("origin", "peer_url"):
        u = urlsplit(config[key])
        if u.scheme != "https" or u.username or u.password or u.path or u.query or u.fragment or u.port:
            raise ValueError("production console origins require HTTPS DNS hosts")
        origins.append(host(u.hostname))
    if origins[0] == origins[1] or origins[0].split(".", 1)[1] != origins[1].split(".", 1)[1]:
        raise ValueError("use two distinct subdomains of the same configured root domain")
    issuer = urlsplit(config["clerk_issuer"])
    key = config["clerk_publishable_key"]
    if not key.startswith("pk_live_") or issuer.scheme != "https" or issuer.path or issuer.query or issuer.fragment or issuer.username or issuer.password:
        raise ValueError("production requires a live Clerk instance and HTTPS issuer origin")
    try:
        encoded = key[len("pk_live_"):]
        actual = base64.b64decode(encoded + "=" * (-len(encoded) % 4), altchars=b'-_', validate=True).decode().removesuffix("$")
    except Exception as exc:
        raise ValueError("invalid Clerk publishable key encoding") from exc
    if actual != issuer.hostname:
        raise ValueError("Clerk publishable key and issuer must belong to one instance")


def secure(path, directory=False):
    path.chmod(0o700 if directory else 0o600)
    if os.name != "nt" and os.geteuid() == 0:
        os.chown(path, 1000, 1000)


def read_secret(path):
    value = path.read_text(encoding="utf-8-sig").strip()
    if not value or len(value) > 16384 or "\n" in value or "\r" in value:
        raise ValueError("private credential file is empty or malformed")
    return value


def configure(args):
    root = args.deploy_dir.resolve()
    if not (root / "compose.yml").is_file() or not (root / ".env").is_file():
        raise ValueError("run backend setup first; compose.yml and its private .env must exist")
    if os.name != "nt" and os.geteuid() not in (0, 1000):
        raise ValueError("run as root or UID 1000 so container UID 1000 can read generated private files")
    mesh, sentinel, api = map(host, (args.mesh_domain, args.sentinel_domain, args.api_domain))
    if len({mesh, sentinel, api}) != 3:
        raise ValueError("API and the two console hostnames must be distinct")
    for value in (args.user_id, args.tenant_id, args.project_id, *args.tables):
        if not re.fullmatch(r"[A-Za-z0-9_-]{1,64}", value):
            raise ValueError("identity, scope and tables require bounded identifiers")
    if not args.tables or len(args.tables) > 16 or not args.user_id.startswith("user_"):
        raise ValueError("provide Clerk user ID and 1..16 concrete allowed tables")
    common = dict(production=True, clerk_publishable_key=args.clerk_publishable_key,
                  clerk_issuer=args.clerk_issuer.rstrip("/"), clerk_secret_file="/run/console-private/clerk_secret",
                  members_file="/run/console-private/members.json", backend_admin_file="/run/console-private/admin_token")
    configs = {
        "mesh": dict(common, origin="https://" + mesh, peer_url="https://" + sentinel,
                     public_api_url="https://" + api + "/v1", backend_url="http://gateway:18080"),
        "sentinel": dict(common, origin="https://" + sentinel, peer_url="https://" + mesh,
                         backend_url="http://sentinel-api:8080"),
    }
    for config in configs.values():
        validate(config)
    credentials = {"clerk_secret": read_secret(args.clerk_secret_file),
                   "gateway_key": read_secret(args.gateway_key_file),
                   "mesh_admin": read_secret(args.gateway_admin_file),
                   "sentinel_admin": read_secret(args.sentinel_bootstrap_file)}
    if not credentials['clerk_secret'].startswith('sk_live_'):
        raise ValueError("production requires a live Clerk secret key")
    base = root / "console-private"
    if base.exists():
        raise ValueError("console-private already exists; preserve it and update its configs explicitly")
    # All inputs are validated before the first private write.
    base.mkdir(mode=0o700); secure(base, True)
    members = {
        "mesh": {args.user_id: dict(enabled=True, tenant_id=args.tenant_id,
                    gateway_key_file="/run/console-private/gateway_key", can_manage_keys=args.platform_admin,
                    platform_admin=args.platform_admin)},
        "sentinel": {args.user_id: dict(enabled=True, tenant_id=args.tenant_id, project_id=args.project_id,
                    role=args.sql_role, tables=args.tables)},
    }
    for product in configs:
        directory = base / product; directory.mkdir(mode=0o700); secure(directory, True)
        files = {"config.json": json.dumps(configs[product], ensure_ascii=False, indent=2) + "\n",
                 "members.json": json.dumps(members[product], ensure_ascii=False, indent=2) + "\n",
                 "clerk_secret": credentials["clerk_secret"],
                 "admin_token": credentials["mesh_admin" if product == "mesh" else "sentinel_admin"]}
        if product == "mesh": files["gateway_key"] = credentials["gateway_key"]
        for name, content in files.items():
            file = directory / name
            with file.open("x", encoding="utf-8", newline="\n") as stream: stream.write(content)
            secure(file)
    env = root / ".env"; text = env.read_text(encoding="utf-8")
    for key, value in dict(APP_DOMAIN=api, MESH_CONSOLE_DOMAIN=mesh, SENTINEL_CONSOLE_DOMAIN=sentinel).items():
        pattern = re.compile(r"^" + key + r"=.*$", re.M)
        text = pattern.sub(key + "=" + value, text) if pattern.search(text) else text.rstrip() + "\n" + key + "=" + value + "\n"
    temporary = root / ".env.console-tmp"
    with os.fdopen(os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600), "w", encoding="utf-8", newline="\n") as stream: stream.write(text)
    temporary.chmod(0o600); os.replace(temporary, env)
    return {"status": "configured", "identity": "one Clerk app, two independent product grants",
            "live_login_verified": False, "cloud_deployed": False}


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--deploy-dir", type=Path, default=Path.cwd())
    for name in ("mesh-domain", "sentinel-domain", "api-domain", "clerk-publishable-key", "clerk-issuer", "user-id", "tenant-id", "project-id"):
        parser.add_argument("--" + name, required=True)
    for name in ("clerk-secret-file", "gateway-key-file", "gateway-admin-file", "sentinel-bootstrap-file"):
        parser.add_argument("--" + name, required=True, type=Path)
    parser.add_argument("--tables", nargs="+", required=True)
    parser.add_argument("--sql-role", choices=("reader", "writer", "reviewer", "admin"), default="writer")
    parser.add_argument("--platform-admin", action="store_true", help="explicitly grant this operator platform administration")
    try:
        print(json.dumps(configure(parser.parse_args()), ensure_ascii=False))
    except (ValueError, OSError):
        raise SystemExit("Console setup refused: check inputs, private file access and existing output; credentials were not printed.")
