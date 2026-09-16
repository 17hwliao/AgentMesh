#!/bin/sh
set -eu

: "${MYSQL_HOST:=mysql}"
: "${MYSQL_PORT:=3306}"
: "${MYSQL_DATABASE:=agentmesh_control}"
: "${MYSQL_ROOT_PASSWORD:?MYSQL_ROOT_PASSWORD is required}"
: "${AGENTMESH_BOOTSTRAP_API_KEY:?AGENTMESH_BOOTSTRAP_API_KEY is required}"
: "${AGENTMESH_BOOTSTRAP_TENANT_ID:?AGENTMESH_BOOTSTRAP_TENANT_ID is required}"
: "${AGENTMESH_BOOTSTRAP_MODEL:=mock-model}"
: "${AGENTMESH_BOOTSTRAP_EMBEDDING_MODEL:=embed-model}"

export MYSQL_PWD="$MYSQL_ROOT_PASSWORD"
mysql_cmd="mysql --protocol=TCP -h $MYSQL_HOST -P $MYSQL_PORT -u root"

until $mysql_cmd -e 'SELECT 1' >/dev/null 2>&1; do
  echo "waiting for mysql"
  sleep 1
done

$mysql_cmd "$MYSQL_DATABASE" -e 'CREATE TABLE IF NOT EXISTS schema_migrations (version VARCHAR(255) NOT NULL PRIMARY KEY, applied_at DATETIME(6) NOT NULL) ENGINE=InnoDB'

for migration in $(find /migrations -maxdepth 1 -type f -name '*.sql' | sort); do
  version=$(basename "$migration")
  applied=$($mysql_cmd -N -s "$MYSQL_DATABASE" -e "SELECT COUNT(*) FROM schema_migrations WHERE version='${version}'")
  if [ "$applied" = "0" ]; then
    echo "applying migration $version"
    $mysql_cmd "$MYSQL_DATABASE" < "$migration"
    $mysql_cmd "$MYSQL_DATABASE" -e "INSERT INTO schema_migrations (version, applied_at) VALUES ('${version}', UTC_TIMESTAMP(6))"
  fi
done

case "$AGENTMESH_BOOTSTRAP_API_KEY" in
  *[!A-Za-z0-9_-]* | ????????)
    echo 'AGENTMESH_BOOTSTRAP_API_KEY must be at least 12 alphanumeric, underscore, or dash characters' >&2
    exit 1
    ;;
esac
case "$AGENTMESH_BOOTSTRAP_TENANT_ID" in
  *[!A-Za-z0-9_-]* | '')
    echo 'AGENTMESH_BOOTSTRAP_TENANT_ID must be alphanumeric, underscore, or dash characters' >&2
    exit 1
    ;;
esac
for model in "$AGENTMESH_BOOTSTRAP_MODEL" "$AGENTMESH_BOOTSTRAP_EMBEDDING_MODEL"; do
  case "$model" in
    *[!A-Za-z0-9._:-]* | '')
      echo 'bootstrap model names must use only alphanumeric, dot, underscore, colon, or dash characters' >&2
      exit 1
      ;;
  esac
done
if [ "${#AGENTMESH_BOOTSTRAP_API_KEY}" -lt 12 ]; then
  echo 'AGENTMESH_BOOTSTRAP_API_KEY must be at least 12 characters' >&2
  exit 1
fi

key_prefix=$(printf '%s' "$AGENTMESH_BOOTSTRAP_API_KEY" | cut -c 1-8)
$mysql_cmd "$MYSQL_DATABASE" <<SQL
INSERT INTO tenants (tenant_id, enabled, created_at, updated_at)
VALUES ('${AGENTMESH_BOOTSTRAP_TENANT_ID}', TRUE, UTC_TIMESTAMP(6), UTC_TIMESTAMP(6))
ON DUPLICATE KEY UPDATE enabled=TRUE, updated_at=UTC_TIMESTAMP(6);
INSERT INTO tenant_model_routes (tenant_id, model, ordinal, provider)
VALUES ('${AGENTMESH_BOOTSTRAP_TENANT_ID}', '${AGENTMESH_BOOTSTRAP_MODEL}', 1, 'mock')
ON DUPLICATE KEY UPDATE ordinal=VALUES(ordinal);
INSERT INTO tenant_model_routes (tenant_id, model, ordinal, provider)
VALUES ('${AGENTMESH_BOOTSTRAP_TENANT_ID}', '${AGENTMESH_BOOTSTRAP_EMBEDDING_MODEL}', 1, 'mock')
ON DUPLICATE KEY UPDATE ordinal=VALUES(ordinal);
INSERT INTO api_keys (key_id, tenant_id, key_prefix, key_hash, enabled, created_at, revoked_at)
SELECT UUID(), '${AGENTMESH_BOOTSTRAP_TENANT_ID}', '${key_prefix}', UNHEX(SHA2('${AGENTMESH_BOOTSTRAP_API_KEY}', 256)), TRUE, UTC_TIMESTAMP(6), NULL
WHERE NOT EXISTS (SELECT 1 FROM api_keys WHERE key_prefix='${key_prefix}');
SQL

echo "migrations complete"
