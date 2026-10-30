#!/bin/sh
# Dev/CI: логины сервера — члены групповых ролей zafiro_app и zafiro_platform (db/migrations/0004_rls.sql).
#
# Миграции (goose, владелец схемы) создают только групповые роли NOLOGIN; логины с паролями — дело
# развёртывания. Этот скрипт:
#  * выполняется автоматически при первой инициализации тома Postgres (docker-entrypoint-initdb.d);
#  * идемпотентен: `task db:logins` запускает его повторно на уже существующем томе (меняет пароли).
# Пароли и имена приходят из окружения (.env → docker-compose.dev.yml), в репозитории их нет.
# Не для prod: там логины создаёт инфраструктура (Terraform/секрет-менеджер).
set -eu

: "${ZAFIRO_DB_APP_USER:?нужен ZAFIRO_DB_APP_USER}"
: "${ZAFIRO_DB_APP_PASSWORD:?нужен ZAFIRO_DB_APP_PASSWORD}"
: "${ZAFIRO_DB_PLATFORM_USER:?нужен ZAFIRO_DB_PLATFORM_USER}"
: "${ZAFIRO_DB_PLATFORM_PASSWORD:?нужен ZAFIRO_DB_PLATFORM_PASSWORD}"
for u in "$ZAFIRO_DB_APP_USER" "$ZAFIRO_DB_PLATFORM_USER"; do
	if [ "$u" = "${POSTGRES_USER:-postgres}" ] || [ "$u" = zafiro_app ] || [ "$u" = zafiro_platform ]; then
		echo "zafiro: логин сервера $u не может совпадать с владельцем или групповой ролью" >&2
		exit 1
	fi
done
if [ "$ZAFIRO_DB_APP_USER" = "$ZAFIRO_DB_PLATFORM_USER" ]; then
	echo "zafiro: логины zafiro_app и zafiro_platform должны различаться" >&2
	exit 1
fi

# Значения передаются переменными psql и экранируются format(%I/%L) — без склейки строк в SQL.
psql -v ON_ERROR_STOP=1 --no-psqlrc \
	--username "${POSTGRES_USER:-postgres}" --dbname "${POSTGRES_DB:-postgres}" \
	-v app_user="$ZAFIRO_DB_APP_USER" -v app_password="$ZAFIRO_DB_APP_PASSWORD" \
	-v platform_user="$ZAFIRO_DB_PLATFORM_USER" -v platform_password="$ZAFIRO_DB_PLATFORM_PASSWORD" <<'SQL'
-- Групповые роли — как в 0004_rls.sql (миграция их не пересоздаёт: IF NOT EXISTS).
SELECT 'CREATE ROLE zafiro_app NOLOGIN NOBYPASSRLS'
WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'zafiro_app') \gexec
SELECT 'CREATE ROLE zafiro_platform NOLOGIN NOBYPASSRLS'
WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'zafiro_platform') \gexec

SELECT format('CREATE ROLE %I LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS', :'app_user')
WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = :'app_user') \gexec
SELECT format('ALTER ROLE %I WITH LOGIN NOSUPERUSER NOBYPASSRLS PASSWORD %L', :'app_user', :'app_password') \gexec
SELECT format('GRANT zafiro_app TO %I', :'app_user') \gexec

SELECT format('CREATE ROLE %I LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOBYPASSRLS', :'platform_user')
WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = :'platform_user') \gexec
SELECT format('ALTER ROLE %I WITH LOGIN NOSUPERUSER NOBYPASSRLS PASSWORD %L', :'platform_user', :'platform_password') \gexec
SELECT format('GRANT zafiro_platform TO %I', :'platform_user') \gexec
SQL
echo "zafiro: логины $ZAFIRO_DB_APP_USER (zafiro_app) и $ZAFIRO_DB_PLATFORM_USER (zafiro_platform) готовы"
