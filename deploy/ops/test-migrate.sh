#!/bin/bash
# Проверка обёртки job миграций zafiro-migrate на настоящем Postgres (аудит OPS-7/DB-1/OPS-20):
#   1. два job одновременно на пустую базу — оба успешны, миграции применены один раз (голый goose: один падает);
#   2. DDL за долгой транзакцией: обёртка с lock_timeout не держит очередь — обычный SELECT к таблице не ждёт,
#      миграция откатывается (55P03) и проходит повтором после конца транзакции (голый goose: SELECT ждёт всю транзакцию).
# Нужны: Docker, goose (GOOSE=путь, иначе из PATH или ~/go/bin), psql. CI — .github/workflows/deploy.yml (job ops).
#
#   deploy/ops/test-migrate.sh
set -euo pipefail

here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo=$(cd "$here/../.." && pwd)
image=${PG_IMAGE:-postgres:18.6@sha256:979bccfde35445db618c65107d27d08c6c21339d8dc3ad4c830c6633947be0e4}
goose_bin=${GOOSE:-$(command -v goose || echo "$HOME/go/bin/goose")}
work=$(mktemp -d)
name="zafiro-migrate-test-$$"
cleanup() { docker rm -f "$name" >/dev/null 2>&1 || true; rm -rf "$work"; }
trap cleanup EXIT
fail() { echo "FAIL: $*" >&2; exit 1; }

(cd "$here" && go build -o "$work/zafiro-migrate" ./cmd/zafiro-migrate)
docker run -d --name "$name" -e POSTGRES_PASSWORD=test -p 127.0.0.1::5432 "$image" >/dev/null
port=$(docker port "$name" 5432/tcp | head -1 | sed 's/.*://')
admin="postgres://postgres:test@127.0.0.1:$port/postgres?sslmode=disable"
for _ in $(seq 60); do psql -X -q "$admin" -c 'select 1' >/dev/null 2>&1 && break; sleep 1; done
# Как в проде: init-скрипт создаёт логин владельца схемы и роли до миграций (deploy/staging/postgres-init, db/init).
psql -X -q -v ON_ERROR_STOP=1 "$admin" -c "CREATE DATABASE m1" -c "CREATE DATABASE m2" -c "CREATE DATABASE m3"
dsn() { echo "postgres://postgres:test@127.0.0.1:$port/$1?sslmode=disable"; }

# --- 1. Параллельный запуск (OPS-20) ---------------------------------------------------------------------------------
echo "== 1. два job миграций одновременно (db/migrations)"
printf '%s' "$(dsn m1)" > "$work/dsn"
codes=()
for i in 1 2; do
  ( rc=0; GOOSE_DRIVER=postgres GOOSE_DBSTRING_FILE="$work/dsn" GOOSE_MIGRATION_DIR="$repo/db/migrations" \
      ZAFIRO_MIGRATE_GOOSE="$goose_bin" "$work/zafiro-migrate" up >"$work/par$i.log" 2>&1 || rc=$?; echo "$rc" >"$work/par$i.code" ) &
done
wait
for i in 1 2; do codes+=("$(cat "$work/par$i.code")"); done
[ "${codes[*]}" = "0 0" ] || { cat "$work"/par*.log >&2; fail "обёртка: коды ${codes[*]}, ожидалось 0 0"; }
grep -q "другой job" "$work/par1.log" "$work/par2.log" || fail "второй job не ждал блокировки"
latest=$(ls "$repo/db/migrations" | sed -n 's/^0*\([0-9]*\)_.*\.sql$/\1/p' | sort -n | tail -1)
applied=$(psql -X -A -t "$(dsn m1)" -c "SELECT max(version_id), count(*) FILTER (WHERE version_id > 0) FROM goose_db_version")
[ "$applied" = "$latest|$(ls "$repo/db/migrations" | grep -c '\.sql$')" ] || fail "goose_db_version: $applied"
echo "   обёртка: оба кода 0, второй ждал; применено $applied (версия|число)"
# Для сравнения — голый goose CLI (без блокировки): один из двух падает.
bare=0
for i in 1 2; do
  ( rc=0; "$goose_bin" -env=none -dir "$repo/db/migrations" postgres "$(dsn m2)" up >"$work/bare$i.log" 2>&1 || rc=$?; echo "$rc" >"$work/bare$i.code" ) &
done
wait
for i in 1 2; do [ "$(cat "$work/bare$i.code")" = 0 ] || bare=$((bare + 1)); done
echo "   голый goose: упало $bare из 2 (гонка ожидаема, но не гарантирована на каждом прогоне)"

# --- 2. lock_timeout и повтор (OPS-7, DB-1) --------------------------------------------------------------------------
echo "== 2. ALTER TABLE за долгой транзакцией"
mig="$work/migrations"
mkdir -p "$mig"
printf -- '-- +goose Up\nCREATE TABLE t (id int PRIMARY KEY);\nINSERT INTO t SELECT generate_series(1, 1000);\n-- +goose Down\nDROP TABLE t;\n' >"$mig/0001_t.sql"
printf -- '-- +goose Up\nALTER TABLE t ADD COLUMN c int;\n-- +goose Down\nALTER TABLE t DROP COLUMN c;\n' >"$mig/0002_c.sql"
"$goose_bin" -env=none -dir "$mig" postgres "$(dsn m3)" up-to 1 >/dev/null 2>&1

probe() { # время обычного SELECT к t, пока идёт миграция (мс)
  local s e; s=$(date +%s%N)
  psql -X -q "$(dsn m3)" -c "SELECT count(*) FROM t" >/dev/null
  e=$(date +%s%N); echo $(( (e - s) / 1000000 ))
}
hold() { psql -X -q "$(dsn m3)" -c "BEGIN; SELECT count(*) FROM t; SELECT pg_sleep($1); COMMIT;" >/dev/null; }

# 2a. Голый goose: ALTER ждёт транзакцию 6 с, SELECT встаёт за ним.
hold 6 & holder=$!
sleep 1
"$goose_bin" -env=none -dir "$mig" postgres "$(dsn m3)" up >/dev/null 2>&1 & mig_pid=$!
sleep 1
bare_ms=$(probe)
wait "$mig_pid" "$holder"
"$goose_bin" -env=none -dir "$mig" postgres "$(dsn m3)" down-to 1 >/dev/null 2>&1

# 2b. Обёртка: lock_timeout 1 с → 55P03 → повтор через 3 с; SELECT ждёт не дольше lock_timeout.
hold 6 & holder=$!
sleep 1
printf '%s' "$(dsn m3)" > "$work/dsn3"
GOOSE_DRIVER=postgres GOOSE_DBSTRING_FILE="$work/dsn3" GOOSE_MIGRATION_DIR="$mig" ZAFIRO_MIGRATE_GOOSE="$goose_bin" \
  ZAFIRO_MIGRATE_LOCK_TIMEOUT=1s ZAFIRO_MIGRATE_RETRY_DELAY=3s \
  "$work/zafiro-migrate" up >"$work/retry.log" 2>&1 & mig_pid=$!
sleep 1
wrapped_ms=$(probe)
wait "$holder"
wait "$mig_pid" || { cat "$work/retry.log" >&2; fail "обёртка не применила миграцию после повтора"; }
grep -q "55P03" "$work/retry.log" || { cat "$work/retry.log" >&2; fail "в журнале нет повтора по 55P03"; }
col=$(psql -X -A -t "$(dsn m3)" -c "SELECT count(*) FROM information_schema.columns WHERE table_name = 't' AND column_name = 'c'")
[ "$col" = 1 ] || fail "колонка c не добавлена"
echo "   SELECT во время миграции: голый goose — ${bare_ms} мс, обёртка — ${wrapped_ms} мс; миграция применена повтором"
[ "$wrapped_ms" -lt 2500 ] || fail "SELECT ждал ${wrapped_ms} мс при lock_timeout 1 с"
[ "$bare_ms" -gt 2500 ] || echo "   (голый goose не встал в очередь — транзакция закончилась раньше пробы)"
echo "OK"
