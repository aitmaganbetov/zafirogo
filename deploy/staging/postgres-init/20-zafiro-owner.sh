#!/bin/sh
# Staging: владелец схемы — отдельная роль без SUPERUSER (PD-9), под ней работает job миграций (goose) и
# инструмент переноса (ZAFIRO_MIGRATE_PG_DSN). Выполняется при первой инициализации тома после 10-zafiro-logins.sh
# (групповые роли zafiro_app/zafiro_platform уже есть). В проде роль создаёт инфраструктура (Terraform и т.п.) так же.
set -eu

: "${ZAFIRO_DB_OWNER_USER:?нужен ZAFIRO_DB_OWNER_USER}"
: "${ZAFIRO_DB_OWNER_PASSWORD:?нужен ZAFIRO_DB_OWNER_PASSWORD}"

psql -v ON_ERROR_STOP=1 --no-psqlrc \
	--username "${POSTGRES_USER:-postgres}" --dbname "${POSTGRES_DB:-postgres}" \
	-v owner="$ZAFIRO_DB_OWNER_USER" -v owner_password="$ZAFIRO_DB_OWNER_PASSWORD" -v db="${POSTGRES_DB:-postgres}" <<'SQL'
-- CREATEROLE — для CREATE ROLE IF NOT EXISTS в 0004_rls.sql на пустой базе; NOSUPERUSER/NOBYPASSRLS — наименьшие права.
SELECT format('CREATE ROLE %I LOGIN NOSUPERUSER NOCREATEDB CREATEROLE NOBYPASSRLS', :'owner')
WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = :'owner') \gexec
SELECT format('ALTER ROLE %I WITH LOGIN NOSUPERUSER NOBYPASSRLS PASSWORD %L', :'owner', :'owner_password') \gexec
SELECT format('ALTER DATABASE %I OWNER TO %I', :'db', :'owner') \gexec
SELECT format('ALTER SCHEMA public OWNER TO %I', :'owner') \gexec
-- data-миграции и перенос клубов — под SET ROLE zafiro_platform (PD-9).
SELECT format('GRANT zafiro_platform TO %I', :'owner') \gexec
SQL
echo "zafiro: владелец схемы $ZAFIRO_DB_OWNER_USER готов"
