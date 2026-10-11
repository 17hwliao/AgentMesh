#!/bin/sh
# Explicit recovery operation after ES data loss. Source of truth is MySQL.
# Run only while API/verifier writers are stopped, then restart them.
set -eu
if [ "${1:-}" != '--ack-history-rebuild' ]; then
  printf 'Use --ack-history-rebuild after stopping API/verifier writers.\n' >&2
  exit 1
fi
project=${COMPOSE_PROJECT_NAME:-internship-backend}
docker compose -p "$project" -f compose.yml -f extensions.yml --profile extensions exec -T control sh -c '
MYSQL_PWD=$(cat /run/secrets/mysql_root) exec mysql -uroot sentinel_control --batch -e "UPDATE sql_sentinel_prior_outbox SET published_at=NULL,attempts=0,available_at=UTC_TIMESTAMP(6) WHERE expires_at>UTC_TIMESTAMP(6)"
'
