#!/bin/sh
# Run on Linux in the deployed release directory. Quiesce writers for a
# coherent control-plane + wallet backup. Never use `down -v` on production.
set -eu
cd "$(dirname "$0")"
umask 077
project=${COMPOSE_PROJECT_NAME:-internship-backend}
destination=${1:-backups/$(date -u +%Y%m%dT%H%M%SZ)}
mkdir -p "$destination"
compose() {
  if [ -n "${COMPOSE_FILE:-}" ]; then
    docker compose -p "$project" "$@"
  elif [ "${BACKUP_EXTENSIONS:-false}" = true ]; then
    docker compose -p "$project" -f compose.yml -f extensions.yml --profile extensions "$@"
  else docker compose -p "$project" -f compose.yml "$@"; fi
}
# Only restore services that were running before the backup. In particular a
# disabled worker must not become active as an accidental backup side effect.
running=$(compose ps --status running --services)
actors=''
for service in $running; do
  case "$service" in gateway|usage-relay|usage-worker|sentinel-api|sentinel-relay|sentinel-worker|model-runner|verifier) actors="$actors $service";; esac
done
resume() { if [ -n "$actors" ]; then compose start $actors >/dev/null; fi; }
trap resume EXIT
if [ -n "$actors" ]; then compose stop $actors; fi
for pair in 'control agentmesh' 'control sentinel_control' 'baseline sentinel' 'candidate sentinel'; do
  set -- $pair
  service=$1
  database=$2
  compose exec -T "$service" sh -c 'MYSQL_PWD=$(cat /run/secrets/mysql_root) exec mysqldump -uroot --single-transaction --hex-blob --set-gtid-purged=OFF --no-tablespaces "$1"' sh "$database" > "$destination/$service-$database.sql"
done
compose exec -T redis sh -c 'REDISCLI_AUTH=$(cat /run/secrets/redis_password) redis-cli --rdb /tmp/backup.rdb >/dev/null'
# Stream the file from the running namespace. Docker archive/cp cannot
# reliably access a tmpfs file on every daemon/storage-driver combination.
compose exec -T redis cat /tmp/backup.rdb > "$destination/redis.rdb"
sha256sum "$destination"/*.sql "$destination/redis.rdb" > "$destination/SHA256SUMS"
printf 'Backup complete: %s\n' "$destination"
# Copy the private secrets directory separately into encrypted off-host
# storage. SQL text and connection credentials are confidential backup data.
