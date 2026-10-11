#!/bin/sh
set -eu
password=$(cat /run/secrets/candidate_password)
MYSQL_PWD=$(cat /run/secrets/mysql_root) mysql --protocol=socket -uroot <<SQL
CREATE DATABASE IF NOT EXISTS sentinel;
CREATE USER IF NOT EXISTS 'candidate'@'%' IDENTIFIED BY '$password';
GRANT SELECT,INSERT,INDEX,CREATE TEMPORARY TABLES ON sentinel.* TO 'candidate'@'%';
SQL
