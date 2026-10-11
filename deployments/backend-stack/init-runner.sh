#!/bin/sh
set -eu
reader=$(cat /run/secrets/runner_password)
MYSQL_PWD=$(cat /run/secrets/mysql_root) mysql -hcontrol -uroot <<SQL
CREATE USER IF NOT EXISTS 'runner_reader'@'%' IDENTIFIED BY '$reader';
GRANT SELECT ON agentmesh.tenants TO 'runner_reader'@'%';
GRANT SELECT ON agentmesh.tenant_model_routes TO 'runner_reader'@'%';
SQL
