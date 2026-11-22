#!/bin/bash
# Метрики архивации WAL для алертов (аудит OPS-1): снимок pg_stat_archiver и очереди невыгруженных сегментов —
# в textfile node_exporter (том backup-metrics, файл archiver.prom). Запускается циклом из backup-scheduler.sh
# (раз в BACKUP_ARCHIVER_METRICS_INTERVAL, 60 с) или вручную:
#
#   docker compose ... exec backup backup-archiver-metrics.sh
#
# Отдельный экспортёр Postgres не нужен: сайдкар уже ходит в Postgres по unix-сокету суперпользователем кластера
# (pg_ls_archive_statusdir и pg_ls_waldir требуют pg_monitor или суперпользователя).
#
# Метрики (алерты — deploy/observability/prometheus/rules/zafiro-backup.rules.yml):
#   zafiro_pg_archiver_up                                  1 — запрос к Postgres прошёл (0 — остальные значения прежние);
#   zafiro_pg_archiver_collect_timestamp_seconds           время снимка (устаревание — сайдкар не работает);
#   zafiro_pg_archiver_archived_count / _failed_count      счётчики pg_stat_archiver;
#   zafiro_pg_archiver_last_archived_timestamp_seconds     последний выгруженный сегмент (0 — ни одного);
#   zafiro_pg_archiver_last_failed_timestamp_seconds       последняя ошибка archive_command (0 — ни одной);
#   zafiro_pg_archiver_ready_segments                      сегменты, ждущие выгрузки (*.ready в pg_wal/archive_status);
#   zafiro_pg_wal_bytes                                    размер pg_wal (растёт, пока архив не работает).
set -euo pipefail

METRICS_DIR="${BACKUP_METRICS_DIR:-/var/lib/walg-metrics}"
out="$METRICS_DIR/archiver.prom"

row=""
up=0
# Без -h: сокет по умолчанию образа postgres (/var/run/postgresql, общий том postgres-run).
if row="$(psql -X -q -A -t -F ' ' -v ON_ERROR_STOP=1 -U "${POSTGRES_USER:-postgres}" -d "${POSTGRES_DB:-postgres}" -c "
  SELECT archived_count, failed_count,
         coalesce(floor(extract(epoch FROM last_archived_time)), 0)::bigint,
         coalesce(floor(extract(epoch FROM last_failed_time)), 0)::bigint,
         (SELECT count(*) FROM pg_ls_archive_statusdir() WHERE name LIKE '%.ready'),
         (SELECT coalesce(sum(size), 0) FROM pg_ls_waldir())
    FROM pg_stat_archiver" 2>/dev/null)"; then
  up=1
fi

mkdir -p "$METRICS_DIR"
chmod 0755 "$METRICS_DIR" 2>/dev/null || true
tmp="$METRICS_DIR/.archiver.prom.tmp"
{
  echo "# HELP zafiro_pg_archiver_up 1 — снимок pg_stat_archiver получен."
  echo "# TYPE zafiro_pg_archiver_up gauge"
  echo "zafiro_pg_archiver_up $up"
  echo "# HELP zafiro_pg_archiver_collect_timestamp_seconds Время последней попытки снимка."
  echo "# TYPE zafiro_pg_archiver_collect_timestamp_seconds gauge"
  echo "zafiro_pg_archiver_collect_timestamp_seconds $(date +%s)"
  if [ "$up" = 1 ]; then
    read -r archived failed last_ok last_fail ready wal_bytes <<< "$row"
    echo "# HELP zafiro_pg_archiver_archived_count Выгружено сегментов WAL (pg_stat_archiver.archived_count)."
    echo "# TYPE zafiro_pg_archiver_archived_count gauge"
    echo "zafiro_pg_archiver_archived_count $archived"
    echo "# HELP zafiro_pg_archiver_failed_count Неудачных вызовов archive_command (pg_stat_archiver.failed_count)."
    echo "# TYPE zafiro_pg_archiver_failed_count gauge"
    echo "zafiro_pg_archiver_failed_count $failed"
    echo "# HELP zafiro_pg_archiver_last_archived_timestamp_seconds Время последней выгрузки сегмента (0 — не было)."
    echo "# TYPE zafiro_pg_archiver_last_archived_timestamp_seconds gauge"
    echo "zafiro_pg_archiver_last_archived_timestamp_seconds $last_ok"
    echo "# HELP zafiro_pg_archiver_last_failed_timestamp_seconds Время последней ошибки archive_command (0 — не было)."
    echo "# TYPE zafiro_pg_archiver_last_failed_timestamp_seconds gauge"
    echo "zafiro_pg_archiver_last_failed_timestamp_seconds $last_fail"
    echo "# HELP zafiro_pg_archiver_ready_segments Сегменты WAL, ждущие выгрузки."
    echo "# TYPE zafiro_pg_archiver_ready_segments gauge"
    echo "zafiro_pg_archiver_ready_segments $ready"
    echo "# HELP zafiro_pg_wal_bytes Размер каталога pg_wal."
    echo "# TYPE zafiro_pg_wal_bytes gauge"
    echo "zafiro_pg_wal_bytes $wal_bytes"
  elif [ -f "$out" ]; then
    # Postgres недоступен: прежние значения остаются (алерт по zafiro_pg_archiver_up), иначе ряды пропадут.
    grep -v -e 'zafiro_pg_archiver_up ' -e 'zafiro_pg_archiver_collect_timestamp_seconds ' "$out" || true
  fi
} > "$tmp"
chmod 0644 "$tmp"
mv -f "$tmp" "$out"
[ "$up" = 1 ]
