#!/bin/sh
set -eu
password=$(cat /run/secrets/redis_password)
cat > /tmp/redis.conf <<CONFIG
bind 0.0.0.0
protected-mode yes
requirepass $password
appendonly yes
appendfsync always
dir /data
maxmemory 256mb
maxmemory-policy noeviction
CONFIG
chown redis:redis /tmp/redis.conf
chmod 600 /tmp/redis.conf
exec /usr/local/bin/docker-entrypoint.sh redis-server /tmp/redis.conf
