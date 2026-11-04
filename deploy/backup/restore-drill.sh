#!/usr/bin/env bash
# Учения восстановления (task backup:restore-drill): чистый Postgres из образа бэкапа ← копия WAL-G, проверка.
#
#   deploy/backup/restore-drill.sh [--target-time "2026-11-02 21:30:00+00"] [--pitr-only]
#                                  [--expect-fingerprint файл] [--expect-latest-fingerprint файл] [--keep]
#
# Прогоны (каждый — новый контейнер с пустым томом, без доступа к боевой базе):
#   1. latest — последняя копия + весь архив WAL (сценарий «потеря инстанса БД»);
#   2. pitr   — копия, законченная раньше T, + WAL до T (сценарий «порча данных»). T — --target-time,
#               по умолчанию «5 минут назад» (DRILL_PITR_AGO_MINUTES); пропуск — DRILL_PITR_AGO_MINUTES=0.
# Проверки каждого прогона: goose status/version = последняя миграция репозитория; deploy/backup/verify.sql
# (деньги: балансы = Σ проводок, Σ проводок операции = 0; партиции; RLS); pg_amcheck (heap + btree);
# для PITR — ни одной операции ledger позже T; отпечаток денег (fingerprint.sql) совпадает с исходным:
# --expect-fingerprint — для PITR (состояние на T), --expect-latest-fingerprint — для latest (последний WAL).
#
# Окружение (как у сайдкара бэкапа, но с ЗАКРЫТЫМ ключом):
#   BACKUP_SECRETS_DIR  — s3_access_key, s3_secret_key, walg_pgp_private.asc, walg_pgp_passphrase
#   WALG_S3_PREFIX, WALG_S3_ENDPOINT, WALG_S3_REGION — хранилище копий (только чтение достаточно)
#   DRILL_NETWORK       — сеть Docker, из которой виден S3 (staging: <проект>_default; по умолчанию bridge)
#   BACKUP_PG_IMAGE     — образ (deploy/backup/Dockerfile); DRILL_DB_USER/DRILL_DB_NAME — zafiro/zafiro
#   DRILL_TIMEOUT       — предел ожидания восстановления, с (3600); DRILL_REPORT — файл отчёта (Markdown)
#   GOOSE               — бинарник goose (task tools); DRILL_AMCHECK=0 — без pg_amcheck
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO="$(cd "$HERE/../.." && pwd)"
IMAGE="${BACKUP_PG_IMAGE:-zafiro-postgres-walg:18.6-3.0.9}"
SECRETS="${BACKUP_SECRETS_DIR:?задайте BACKUP_SECRETS_DIR}"
NET="${DRILL_NETWORK:-bridge}"
DB_USER="${DRILL_DB_USER:-zafiro}"
DB_NAME="${DRILL_DB_NAME:-zafiro}"
TIMEOUT="${DRILL_TIMEOUT:-3600}"
AGO="${DRILL_PITR_AGO_MINUTES:-5}"

TARGET=""
PITR_ONLY=0
EXPECT_FP=""
EXPECT_LATEST_FP=""
KEEP=0
while [ $# -gt 0 ]; do
  case "$1" in
    --target-time) TARGET="$2"; shift 2 ;;
    --pitr-only) PITR_ONLY=1; shift ;;
    --expect-fingerprint) EXPECT_FP="$2"; shift 2 ;;
    --expect-latest-fingerprint) EXPECT_LATEST_FP="$2"; shift 2 ;;
    --keep) KEEP=1; shift ;;
    *) echo "usage: $0 [--target-time T] [--pitr-only] [--expect-fingerprint файл] [--expect-latest-fingerprint файл] [--keep]" >&2
       exit 2 ;;
  esac
done
if [ -z "$TARGET" ] && [ "$AGO" != 0 ]; then
  TARGET="$(date -u -d "-$AGO minutes" '+%Y-%m-%d %H:%M:%S+00')"
fi

for f in s3_access_key s3_secret_key walg_pgp_private.asc walg_pgp_passphrase; do
  [ -r "$SECRETS/$f" ] || { echo "restore-drill: нет $SECRETS/$f" >&2; exit 2; }
done
GOOSE_BIN="${GOOSE:-$(command -v goose || echo "$(go env GOPATH 2>/dev/null)/bin/goose")}"
[ -x "$GOOSE_BIN" ] || { echo "restore-drill: нет goose (task tools)" >&2; exit 2; }
EXPECTED_VERSION="$(find "$REPO/db/migrations" -name '[0-9]*.sql' -printf '%f\n' | cut -d_ -f1 | sort -n | tail -1)"
EXPECTED_VERSION=$((10#$EXPECTED_VERSION))

log() { printf '%s drill: %s\n' "$(date -u +%H:%M:%S)" "$*" >&2; }
REPORT=()
report() { REPORT+=("$*"); log "$*"; }
CONTAINERS=()
cleanup() {
  [ "$KEEP" = 1 ] && { log "контейнеры оставлены: ${CONTAINERS[*]:-}"; return; }
  for c in "${CONTAINERS[@]:-}"; do
    if [ -n "$c" ]; then docker rm -f -v "$c" >/dev/null 2>&1 || true; fi
  done
  return 0
}
trap cleanup EXIT

psql_in() { local c="$1"; shift; docker exec -i "$c" psql -X -q -v ON_ERROR_STOP=1 -U "$DB_USER" -d "$DB_NAME" "$@"; }

# $1 — метка прогона, $2 — время цели (пусто — до конца архива).
run() {
  local label="$1" target="$2" c="zafiro-drill-$1-$$" t0 t1 waited=0
  CONTAINERS+=("$c")
  log "[$label] старт контейнера восстановления${target:+ (PITR на $target)}"
  t0=$(date +%s)
  docker run -d --name "$c" --network "$NET" -p 127.0.0.1::5432 \
    -e WALG_S3_PREFIX="${WALG_S3_PREFIX:?задайте WALG_S3_PREFIX}" \
    -e AWS_ENDPOINT="${WALG_S3_ENDPOINT:?задайте WALG_S3_ENDPOINT}" \
    -e AWS_REGION="${WALG_S3_REGION:-us-east-1}" -e AWS_S3_FORCE_PATH_STYLE=true \
    -e WALG_S3_ACCESS_KEY_FILE=/run/secrets/walg_s3_access_key \
    -e WALG_S3_SECRET_KEY_FILE=/run/secrets/walg_s3_secret_key \
    -e WALG_PGP_KEY_PATH=/run/secrets/walg_pgp_private \
    -e WALG_PGP_PASSPHRASE_FILE=/run/secrets/walg_pgp_passphrase \
    -e WALG_DOWNLOAD_CONCURRENCY="${WALG_DOWNLOAD_CONCURRENCY:-8}" \
    -e POSTGRES_USER="$DB_USER" -e POSTGRES_DB="$DB_NAME" \
    -v "$SECRETS/s3_access_key:/run/secrets/walg_s3_access_key:ro" \
    -v "$SECRETS/s3_secret_key:/run/secrets/walg_s3_secret_key:ro" \
    -v "$SECRETS/walg_pgp_private.asc:/run/secrets/walg_pgp_private:ro" \
    -v "$SECRETS/walg_pgp_passphrase:/run/secrets/walg_pgp_passphrase:ro" \
    "$IMAGE" restore-into.sh auto "$target" >/dev/null

  # Ждём конца восстановления: promote (pg_is_in_recovery() = false).
  until [ "$(docker exec "$c" psql -X -At -U "$DB_USER" -d "$DB_NAME" -c 'SELECT NOT pg_is_in_recovery()' 2>/dev/null || true)" = t ]; do
    if [ "$(docker inspect -f '{{.State.Running}}' "$c" 2>/dev/null)" != true ]; then
      docker logs --tail 40 "$c" >&2 || true
      report "[$label] ОШИБКА: контейнер восстановления остановился"
      return 1
    fi
    if [ "$waited" -ge "$TIMEOUT" ]; then
      docker logs --tail 40 "$c" >&2 || true
      report "[$label] ОШИБКА: восстановление дольше $TIMEOUT с"
      return 1
    fi
    sleep 2; waited=$((waited + 2))
  done
  t1=$(date +%s)
  local fetch_s
  fetch_s="$(docker logs "$c" 2>&1 | sed -n 's/.*копия загружена за \([0-9]*\) с.*/\1/p' | tail -1)"
  report "[$label] восстановлено за $((t1 - t0)) с (загрузка копии ${fetch_s:-?} с, остальное — WAL и запуск)"

  # goose — с хоста по TCP под временной ролью учений (кластер одноразовый).
  local pass port dsn out
  pass="$(openssl rand -hex 16)"
  psql_in "$c" -c "CREATE ROLE zafiro_drill LOGIN SUPERUSER PASSWORD '$pass'" >/dev/null
  port="$(docker port "$c" 5432/tcp | head -1 | sed 's/.*://')"
  dsn="postgres://zafiro_drill:$pass@127.0.0.1:$port/$DB_NAME?sslmode=disable"
  # DSN — через окружение goose, не аргументом: пароль не попадает в вывод и список процессов.
  out="$(GOOSE_DRIVER=postgres GOOSE_DBSTRING="$dsn" "$GOOSE_BIN" -dir "$REPO/db/migrations" version 2>&1 | tail -1)"
  if ! printf '%s' "$out" | grep -q "version $EXPECTED_VERSION\$"; then
    report "[$label] ОШИБКА: goose: «$out», ожидается версия $EXPECTED_VERSION"
    return 1
  fi
  if GOOSE_DRIVER=postgres GOOSE_DBSTRING="$dsn" "$GOOSE_BIN" -dir "$REPO/db/migrations" status 2>&1 | grep -q 'Pending'; then
    report "[$label] ОШИБКА: goose status: есть неприменённые миграции"
    return 1
  fi
  report "[$label] goose: версия $EXPECTED_VERSION, неприменённых миграций нет"

  local v0; v0=$(date +%s)
  if ! psql_in "$c" -v expected_version="$EXPECTED_VERSION" < "$HERE/verify.sql" >&2; then
    report "[$label] ОШИБКА: verify.sql (деньги/партиции/RLS)"
    return 1
  fi
  report "[$label] verify.sql: деньги сошлись, партиции и RLS на месте ($(( $(date +%s) - v0 )) с)"

  if [ "${DRILL_AMCHECK:-1}" = 1 ]; then
    v0=$(date +%s)
    if ! docker exec "$c" pg_amcheck -U "$DB_USER" -d "$DB_NAME" --install-missing >&2; then
      report "[$label] ОШИБКА: pg_amcheck нашёл повреждения"
      return 1
    fi
    report "[$label] pg_amcheck: повреждений нет ($(( $(date +%s) - v0 )) с)"
  fi

  if [ -z "$target" ] && [ -n "$EXPECT_LATEST_FP" ]; then
    if ! diff -u "$EXPECT_LATEST_FP" <(psql_in "$c" -At < "$HERE/fingerprint.sql") >&2; then
      report "[$label] ОШИБКА: отпечаток денег не совпал с исходной базой (потеряны заархивированные операции)"
      return 1
    fi
    report "[$label] отпечаток денег совпал с исходной базой на последний заархивированный WAL"
  fi
  if [ -n "$target" ]; then
    local late
    late="$(psql_in "$c" -At -c "SELECT count(*) FROM ledger_transactions WHERE created_at > '$target'::timestamptz")"
    if [ "$late" != 0 ]; then
      report "[$label] ОШИБКА: $late операций ledger позже цели $target"
      return 1
    fi
    report "[$label] PITR: операций ledger позже $target нет"
    if [ -n "$EXPECT_FP" ]; then
      if ! diff -u "$EXPECT_FP" <(psql_in "$c" -At < "$HERE/fingerprint.sql") >&2; then
        report "[$label] ОШИБКА: отпечаток денег не совпал с исходной базой на момент T"
        return 1
      fi
      report "[$label] отпечаток денег совпал с исходной базой на момент T ($(wc -l < "$EXPECT_FP") строк)"
    fi
  fi
  if [ "$KEEP" != 1 ]; then docker rm -f -v "$c" >/dev/null 2>&1 || true; fi
}

status=0
if [ "$PITR_ONLY" = 0 ]; then
  run latest "" || status=1
fi
if [ "$status" = 0 ] && [ -n "$TARGET" ]; then
  run pitr "$TARGET" || status=1
fi

{
  echo "## Учения восстановления $(date -u '+%Y-%m-%d %H:%M UTC')"
  echo
  echo "Хранилище: \`${WALG_S3_PREFIX}\`, образ \`$IMAGE\`, итог: $([ "$status" = 0 ] && echo 'УСПЕХ' || echo 'ОШИБКА')"
  echo
  for l in "${REPORT[@]}"; do echo "- $l"; done
} > "${DRILL_REPORT:-/dev/stderr}"
exit "$status"
