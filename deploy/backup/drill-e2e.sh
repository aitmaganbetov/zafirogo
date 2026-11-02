#!/usr/bin/env bash
# Сквозные учения бэкапа на одноразовом окружении (CI и локально; task backup:drill-e2e):
#
#   1. Postgres 18.6 + WAL-G + SeaweedFS (docker-compose.dev.yml + deploy/backup/compose.backup.yml, свой проект);
#   2. миграции goose + демо-данные (server/cmd/visual-seed: клиенты, кассовые смены, продажи, пополнения);
#   3. базовая копия (backup-run.sh: копия, хранение, проверка архива WAL);
#   4. «метка» — операция ledger, отпечаток денег, момент T; затем «порча» — ошибочное списание всего баланса
#      клиента и удаление кассовых операций клуба;
#   5. restore-drill.sh: latest (всё до последнего WAL, вместе с порчей: отпечаток = исходному на конец) и
#      PITR на T (отпечаток = исходному на T до тиына, порчи нет); в обоих — инварианты денег, партиции, RLS.
#
#   deploy/backup/drill-e2e.sh [--keep]
#
# Переменные: DRILL_WORK (рабочий каталог; по умолчанию временный), DRILL_PROJECT (zafiro-backup-drill),
# DRILL_PG_PORT (55433), BACKUP_SKIP_BUILD=1 (образ уже собран), VISUAL_SEED_BIN (готовый бинарник
# visual-seed; иначе go run), GOOSE, S3_MIN_FREE_SPACE (порог свободного места SeaweedFS, по умолчанию 1 %).
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO="$(cd "$HERE/../.." && pwd)"
KEEP=0
[ "${1:-}" = "--keep" ] && KEEP=1
WORK="${DRILL_WORK:-$(mktemp -d)}"
mkdir -p "$WORK"
export COMPOSE_PROJECT_NAME="${DRILL_PROJECT:-zafiro-backup-drill}"
export BACKUP_SECRETS_DIR="$WORK/secrets"
export BACKUP_PG_IMAGE="${BACKUP_PG_IMAGE:-zafiro-postgres-walg:18.6-3.0.9}"
export POSTGRES_PORT="${DRILL_PG_PORT:-55433}"
# Пароли одноразового окружения — случайные; Valkey/NATS базового файла не запускаются.
POSTGRES_PASSWORD="$(openssl rand -hex 16)"
ZAFIRO_DB_APP_PASSWORD="$(openssl rand -hex 16)"
ZAFIRO_DB_PLATFORM_PASSWORD="$(openssl rand -hex 16)"
VALKEY_PASSWORD="$(openssl rand -hex 16)"
export POSTGRES_PASSWORD ZAFIRO_DB_APP_PASSWORD ZAFIRO_DB_PLATFORM_PASSWORD VALKEY_PASSWORD
export POSTGRES_USER=zafiro POSTGRES_DB=zafiro
export WALG_S3_PREFIX="s3://zafiro-backup/pg/drill" WALG_S3_ENDPOINT="http://s3:8333"
export DRILL_NETWORK="${COMPOSE_PROJECT_NAME}_default"
COMPOSE=(docker compose --env-file /dev/null -f "$REPO/docker-compose.dev.yml" -f "$HERE/compose.backup.yml")
GOOSE_BIN="${GOOSE:-$(command -v goose || echo "$(go env GOPATH)/bin/goose")}"
OWNER_DSN="postgres://zafiro:$POSTGRES_PASSWORD@127.0.0.1:$POSTGRES_PORT/zafiro?sslmode=disable"

log() { printf '%s e2e: %s\n' "$(date -u +%H:%M:%S)" "$*" >&2; }
cleanup() {
  if [ "$KEEP" = 1 ]; then
    log "окружение оставлено (проект $COMPOSE_PROJECT_NAME, каталог $WORK)"
  else
    "${COMPOSE[@]}" down -v --remove-orphans >/dev/null 2>&1 || true
    if [ -z "${DRILL_WORK:-}" ]; then rm -rf "$WORK"; fi
  fi
  return 0
}
trap cleanup EXIT
psql_src() { "${COMPOSE[@]}" exec -T postgres psql -X -q -v ON_ERROR_STOP=1 -U zafiro -d zafiro "$@"; }

"$HERE/gen-secrets.sh" "$BACKUP_SECRETS_DIR" >/dev/null 2>&1
if [ "${BACKUP_SKIP_BUILD:-0}" != 1 ]; then
  log "сборка образа $BACKUP_PG_IMAGE"
  docker build -q -t "$BACKUP_PG_IMAGE" "$HERE" >/dev/null
fi

log "окружение: postgres + backup + s3"
"${COMPOSE[@]}" up -d --wait postgres backup >/dev/null

log "миграции и демо-данные"
GOOSE_DRIVER=postgres GOOSE_DBSTRING="$OWNER_DSN" "$GOOSE_BIN" -dir "$REPO/db/migrations" up >/dev/null
if [ -n "${VISUAL_SEED_BIN:-}" ]; then
  "$VISUAL_SEED_BIN" -fixture "$REPO/tools/visual/fixtures/demo.json" -db "$OWNER_DSN" >/dev/null
else
  (cd "$REPO/server" && go run ./cmd/visual-seed -fixture "$REPO/tools/visual/fixtures/demo.json" -db "$OWNER_DSN") >/dev/null
fi

log "базовая копия"
t0=$(date +%s)
"${COMPOSE[@]}" exec -T backup backup-run.sh
BACKUP_S=$(( $(date +%s) - t0 ))
DB_SIZE="$(psql_src -At -c "SELECT pg_size_pretty(pg_database_size(current_database()))")"

log "метка до T: пополнение 12 345 тиынов"
psql_src -v kind=owner_top_up -v amount=12345 -v key="drill-marker-$(date +%s)" < "$HERE/drill-ops.sql"
psql_src -At < "$HERE/fingerprint.sql" > "$WORK/fingerprint-T.txt"
sleep 1
T="$(psql_src -At -c "SELECT to_char(clock_timestamp() AT TIME ZONE 'UTC', 'YYYY-MM-DD HH24:MI:SS.US') || '+00'")"
sleep 1
log "T = $T; порча после T: списание всего баланса клиента и удаление кассовых операций клуба 1"
psql_src -v kind=owner_write_off -v amount=all -v key="drill-damage-$(date +%s)" < "$HERE/drill-ops.sql"
psql_src -c "DELETE FROM cash_transactions WHERE club_id = 1"
if diff -q "$WORK/fingerprint-T.txt" <(psql_src -At < "$HERE/fingerprint.sql") >/dev/null; then
  log "ОШИБКА: порча не изменила отпечаток — учения ничего не докажут"
  exit 1
fi

psql_src -At < "$HERE/fingerprint.sql" > "$WORK/fingerprint-latest.txt"

# Закрыть текущий сегмент WAL и дождаться его архивации — иначе PITR на T не дойдёт до цели.
seg="$(psql_src -At -c "SELECT pg_walfile_name(pg_switch_wal())")"
for _ in $(seq 1 60); do
  last="$(psql_src -At -c "SELECT COALESCE(last_archived_wal, '') FROM pg_stat_archiver")"
  [[ "$last" > "$seg" || "$last" == "$seg" ]] && break
  sleep 1
done
[[ "$last" > "$seg" || "$last" == "$seg" ]] || { log "ОШИБКА: сегмент $seg не заархивирован за 60 с"; exit 1; }
log "архив WAL догнал $seg"

export DRILL_REPORT="$WORK/report.md"
status=0
"$HERE/restore-drill.sh" --target-time "$T" --expect-fingerprint "$WORK/fingerprint-T.txt" \
  --expect-latest-fingerprint "$WORK/fingerprint-latest.txt" || status=$?
{
  echo
  echo "Исходная база: $DB_SIZE; базовая копия (backup-run.sh: копия + хранение + проверка архива): $BACKUP_S с."
} >> "$DRILL_REPORT"
cat "$DRILL_REPORT"
if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then cat "$DRILL_REPORT" >> "$GITHUB_STEP_SUMMARY"; fi
exit "$status"
