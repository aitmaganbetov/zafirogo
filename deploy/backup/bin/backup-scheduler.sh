#!/bin/bash
# Расписание бэкапа для сайдкара `backup` (compose.backup.yml): ждёт Postgres, делает первую копию, если в
# хранилище её ещё нет, затем запускает backup-run.sh раз в сутки в BACKUP_AT (UTC, ЧЧ:ММ).
# При ошибке — повтор через BACKUP_RETRY_SECONDS (до BACKUP_RETRIES раз за сутки).
#
# Один экземпляр на кластер (рядом с primary): две копии одновременно WAL-G не запрещает, но это двойная нагрузка.
# В Kubernetes вместо этого цикла — CronJob с concurrencyPolicy: Forbid (docs/runbooks/restore.md).
set -euo pipefail

AT="${BACKUP_AT:-21:17}"           # 02:17 по Астане (UTC+5): ночью нагрузка клубов минимальна
RETRY="${BACKUP_RETRY_SECONDS:-1800}"
RETRIES="${BACKUP_RETRIES:-3}"

log() { printf '%s scheduler: %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*" >&2; }

if ! [[ "$AT" =~ ^([01][0-9]|2[0-3]):[0-5][0-9]$ ]]; then
  log "BACKUP_AT должен быть ЧЧ:ММ (UTC), получено «$AT»"
  exit 2
fi

until pg_isready -q -h "${PGHOST:-/var/run/postgresql}" -U "${POSTGRES_USER:-postgres}"; do
  log "жду Postgres"
  sleep 5
done

run_with_retries() {
  local i
  for ((i = 1; i <= RETRIES; i++)); do
    if backup-run.sh; then
      return 0
    fi
    log "цикл бэкапа не удался (попытка $i из $RETRIES)"
    [ "$i" -lt "$RETRIES" ] && sleep "$RETRY"
  done
  return 1
}

# Нет ни одной копии (новый кластер или новое хранилище) — сразу: PITR возможен только после первой копии.
existing="$(walg backup-list --json 2>/dev/null | tr -d '[:space:]' || true)"
if [ -z "$existing" ] || [ "$existing" = "[]" ] || [ "$existing" = "null" ]; then
  log "копий нет — делаю первую"
  run_with_retries || log "первая копия не удалась; следующая попытка — по расписанию"
fi

while true; do
  now=$(date -u +%s)
  next=$(date -u -d "today $AT" +%s)
  [ "$next" -le "$now" ] && next=$(date -u -d "tomorrow $AT" +%s)
  log "следующая копия: $(date -u -d "@$next" +%Y-%m-%dT%H:%M:%SZ)"
  sleep $((next - now))
  run_with_retries || log "ОШИБКА: бэкап за сутки не сделан (алерт по zafiro_backup_last_success_timestamp_seconds)"
done
