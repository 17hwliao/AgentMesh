#!/bin/sh
set -eu
password=$(cat /run/secrets/write_password)
MYSQL_PWD=$(cat /run/secrets/mysql_root) mysql --protocol=socket -uroot <<SQL
CREATE DATABASE IF NOT EXISTS sentinel_write;
CREATE USER IF NOT EXISTS 'write_probe'@'%' IDENTIFIED BY '$password';
GRANT SELECT,INSERT,UPDATE,DELETE,CREATE,DROP,INDEX ON sentinel_write.* TO 'write_probe'@'%';
SQL
