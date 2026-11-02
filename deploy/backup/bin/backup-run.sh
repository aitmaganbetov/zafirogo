#!/bin/bash
# Один цикл бэкапа Postgres (WAL-G): базовая копия → месячная «постоянная» копия → политика хранения →
# проверка непрерывности архива WAL → метрики. Запускается расписанием (backup-scheduler.sh) или вручную:
#
#   docker compose ... exec backup backup-run.sh
#
# Политика (docs/runbooks/restore.md «Политика хранения», BK-3):
#   * базовая копия раз в сутки; каждая 7-я — полная, между ними — дельты (WALG_DELTA_MAX_STEPS=6);
#   * хранятся BACKUP_RETAIN_FULL (5) последних полных цепочек — это ≥ 28 суток PITR на любую секунду;
#   * первая копия каждого месяца помечается постоянной и хранится BACKUP_KEEP_MONTHLY (12) месяцев;
#   * архив WAL непрерывный (archive_timeout = 60 с): RPO ≤ 1 минуты.
# Ошибка любого шага — код выхода ≠ 0 и метрика zafiro_backup_last_run_success 0 (алерт — O3).
set -euo pipefail

: "${PGDATA:?PGDATA не задан}"
RETAIN_FULL="${BACKUP_RETAIN_FULL:-5}"
KEEP_MONTHLY="${BACKUP_KEEP_MONTHLY:-12}"
METRICS_DIR="${BACKUP_METRICS_DIR:-/var/lib/walg-metrics}"
export WALG_DELTA_MAX_STEPS="${WALG_DELTA_MAX_STEPS:-6}"
export WALG_VERIFY_PAGE_CHECKSUMS="${WALG_VERIFY_PAGE_CHECKSUMS:-true}"

log() { printf '%s backup: %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*" >&2; }

started=$(date +%s)
ok=0
verify_ok=0
write_metrics() {
  local now; now=$(date +%s)
  mkdir -p "$METRICS_DIR"
  local tmp="$METRICS_DIR/.walg.prom.$$"
  {
    echo "# HELP zafiro_backup_last_run_timestamp_seconds Время окончания последнего цикла бэкапа."
    echo "# TYPE zafiro_backup_last_run_timestamp_seconds gauge"
    echo "zafiro_backup_last_run_timestamp_seconds $now"
    echo "# HELP zafiro_backup_last_run_success 1 — последний цикл бэкапа успешен."
    echo "# TYPE zafiro_backup_last_run_success gauge"
    echo "zafiro_backup_last_run_success $ok"
    echo "# HELP zafiro_backup_last_run_duration_seconds Длительность последнего цикла бэкапа."
    echo "# TYPE zafiro_backup_last_run_duration_seconds gauge"
    echo "zafiro_backup_last_run_duration_seconds $((now - started))"
    echo "# HELP zafiro_backup_wal_verify_ok 1 — архив WAL непрерывен от старейшей копии до текущего сегмента."
    echo "# TYPE zafiro_backup_wal_verify_ok gauge"
    echo "zafiro_backup_wal_verify_ok $verify_ok"
    if [ "$ok" = 1 ]; then
      echo "# HELP zafiro_backup_last_success_timestamp_seconds Время последней успешной базовой копии."
      echo "# TYPE zafiro_backup_last_success_timestamp_seconds gauge"
      echo "zafiro_backup_last_success_timestamp_seconds $now"
    elif [ -f "$METRICS_DIR/walg.prom" ]; then
      grep '^zafiro_backup_last_success_timestamp_seconds\|^# .*zafiro_backup_last_success' "$METRICS_DIR/walg.prom" || true
    fi
  } > "$tmp"
  mv -f "$tmp" "$METRICS_DIR/walg.prom"
}
trap write_metrics EXIT

# 1. Базовая копия (полная или дельта). WAL-G сам проверяет контрольные суммы страниц (WALG_VERIFY_PAGE_CHECKSUMS).
log "базовая копия: старт"
walg backup-push "$PGDATA"
log "базовая копия: готово за $(( $(date +%s) - started )) с"

# 2. Месячная постоянная копия: первая копия месяца (UTC). Старые постоянные — снять пометку,
#    после чего их удалит политика хранения.
list_json="$(walg backup-list --detail --json)"
month="$(date -u +%Y-%m)"
cutoff="$(date -u -d "-${KEEP_MONTHLY} months" +%Y-%m-%dT%H:%M:%SZ)"
# Вывод: «mark <имя>» — пометить, «unmark <имя>» — снять пометку.
actions="$(printf '%s' "$list_json" | perl -MJSON::PP -e '
  my ($month, $cutoff) = @ARGV;
  local $/; my $list = decode_json(<STDIN>) // [];
  my @b = sort { $a->{start_time} cmp $b->{start_time} } @$list;
  my $have = grep { $_->{is_permanent} && substr($_->{start_time}, 0, 7) eq $month } @b;
  print "mark $b[-1]{backup_name}\n" if !$have && @b;
  for (@b) { print "unmark $_->{backup_name}\n" if $_->{is_permanent} && $_->{start_time} lt $cutoff }
' "$month" "$cutoff")"
while read -r act name; do
  [ -n "${act:-}" ] || continue
  if [ "$act" = mark ]; then
    log "постоянная копия месяца $month: $name"
    walg backup-mark "$name"
  else
    log "снимаю пометку постоянной копии старше $KEEP_MONTHLY мес.: $name"
    walg backup-mark -i "$name"
  fi
done <<< "$actions"

# 3. Хранение: последние RETAIN_FULL полных цепочек (с дельтами и WAL между ними) + постоянные копии.
#    BACKUP_RETENTION=0 — пропустить (прод: удаляет отдельная задача с учёткой S3 «удаление», BK-5).
if [ "${BACKUP_RETENTION:-1}" = 1 ]; then
  log "хранение: оставить $RETAIN_FULL полных цепочек"
  walg delete retain FULL "$RETAIN_FULL" --confirm
fi

# 4. Целостность архива: нет пропусков WAL от старейшей копии до текущего сегмента, линия времени та же.
verify_json="$(walg wal-verify integrity timeline --json)"
status="$(printf '%s' "$verify_json" | perl -MJSON::PP -e '
  local $/; my $r = decode_json(<STDIN>);
  print join(" ", map { "$_=" . ($r->{$_}{status} // "?") } sort keys %$r);
')"
log "проверка архива WAL: $status"
case " $status " in
  *"integrity=OK"*"timeline=OK"*) verify_ok=1 ;;
  *) log "ОШИБКА: архив WAL не прошёл проверку"; printf '%s\n' "$verify_json" >&2; exit 1 ;;
esac

ok=1
log "цикл завершён за $(( $(date +%s) - started )) с"
