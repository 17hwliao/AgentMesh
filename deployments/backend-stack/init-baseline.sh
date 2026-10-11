#!/bin/sh
set -eu
password=$(cat /run/secrets/baseline_password)
MYSQL_PWD=$(cat /run/secrets/mysql_root) mysql --protocol=socket -uroot <<SQL
CREATE DATABASE IF NOT EXISTS sentinel;
CREATE USER IF NOT EXISTS 'baseline'@'%' IDENTIFIED BY '$password';
GRANT SELECT,CREATE TEMPORARY TABLES ON sentinel.* TO 'baseline'@'%';
SQL
