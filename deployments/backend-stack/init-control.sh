#!/bin/sh
set -eu
am=$(cat /run/secrets/am_password)
ss=$(cat /run/secrets/ss_password)
migrator=$(cat /run/secrets/migrate_password)
MYSQL_PWD=$(cat /run/secrets/mysql_root) mysql --protocol=socket -uroot <<SQL
CREATE DATABASE IF NOT EXISTS agentmesh;
CREATE DATABASE IF NOT EXISTS sentinel_control;
CREATE USER IF NOT EXISTS 'agentmesh'@'%' IDENTIFIED BY '$am';
CREATE USER IF NOT EXISTS 'sentinel'@'%' IDENTIFIED BY '$ss';
CREATE USER IF NOT EXISTS 'migrator'@'%' IDENTIFIED BY '$migrator';
GRANT SELECT,INSERT,UPDATE,DELETE ON agentmesh.* TO 'agentmesh'@'%';
GRANT SELECT,INSERT,UPDATE,DELETE ON sentinel_control.* TO 'sentinel'@'%';
GRANT SELECT,INSERT,UPDATE,DELETE,CREATE,ALTER,INDEX,REFERENCES ON agentmesh.* TO 'migrator'@'%';
GRANT SELECT,INSERT,UPDATE,DELETE,CREATE,ALTER,INDEX,REFERENCES ON sentinel_control.* TO 'migrator'@'%';
SQL
# The exact runner table grants require migrated tables. The optional
# runner-grants job runs after am-migrate; do not issue them during fresh init.
