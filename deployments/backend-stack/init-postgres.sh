#!/bin/sh
set -eu
password=$(cat /run/secrets/pg_password)
psql -v ON_ERROR_STOP=1 --username postgres --dbname postgres --set password="$password" <<'SQL'
CREATE USER sentinel_compare PASSWORD :'password';
CREATE DATABASE sentinel_compare OWNER sentinel_compare;
REVOKE ALL ON DATABASE sentinel_compare FROM PUBLIC;
SQL
