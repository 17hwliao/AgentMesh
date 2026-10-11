#!/bin/sh
set -eu
# Docker Desktop bind mounts cannot honor secret mode=0400. Copy only the
# mounted bootstrap password into an owned private file before ES entrypoint.
umask 077
cat /run/secrets/es_password > /tmp/es-bootstrap-password
export ELASTIC_PASSWORD_FILE=/tmp/es-bootstrap-password
exec /bin/tini -- /usr/local/bin/docker-entrypoint.sh eswrapper
