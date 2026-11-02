#!/bin/bash
# Восстановление кластера из WAL-G в пустой PGDATA и запуск Postgres (точка входа контейнера восстановления).
#
#   restore-into.sh [LATEST|<имя копии>|auto] [время цели PITR]
#
#   * без времени — последняя (или указанная) копия + весь архив WAL до конца (потеря инстанса БД);
#   * со временем (например, 2026-11-02 21:30:00+00) — PITR: берётся последняя копия, ЗАКОНЧЕННАЯ раньше цели
#     (auto), и WAL проигрывается до цели; затем promote (порча данных).
#
# Восстановленный кластер НЕ архивирует WAL (archive_mode=off): иначе копия-двойник писала бы в то же хранилище.
# Боевое восстановление включает архив после проверки — шаги в docs/runbooks/restore.md.
# Нужны: WALG_* / AWS_* (как у сервера БД), WALG_PGP_KEY_PATH = ЗАКРЫТЫЙ ключ, WALG_PGP_PASSPHRASE_FILE.
set -euo pipefail

if [ "$(id -u)" = 0 ]; then
  install -d -o postgres -g postgres -m 0700 "$PGDATA"
  exec gosu postgres "$0" "$@"
fi

NAME="${1:-auto}"
TARGET="${2:-}"
log() { printf '%s restore: %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*" >&2; }

if [ -n "$(ls -A "$PGDATA" 2>/dev/null)" ]; then
  log "PGDATA $PGDATA не пуст — восстановление только в пустой каталог"
  exit 2
fi

if [ "$NAME" = auto ]; then
  if [ -z "$TARGET" ]; then
    NAME=LATEST
  else
    target_epoch="$(date -u -d "$TARGET" +%s)" || { log "не понимаю время цели «$TARGET»"; exit 2; }
    NAME="$(walg backup-list --detail --json | perl -MJSON::PP -MTime::Piece -e '
      my $t = shift; local $/; my $list = decode_json(<STDIN>) // [];
      my ($best, $bt);
      for (@$list) {
        (my $f = $_->{finish_time}) =~ s/\.\d+//; $f =~ s/Z$//; $f =~ s/[+-]\d\d:\d\d$//;
        my $e = Time::Piece->strptime($f, "%Y-%m-%dT%H:%M:%S")->epoch;
        if ($e < $t && (!defined $bt || $e > $bt)) { ($best, $bt) = ($_->{backup_name}, $e) }
      }
      print $best // "";
    ' "$target_epoch")"
    if [ -z "$NAME" ]; then
      log "нет копии, законченной раньше $TARGET — PITR на этот момент невозможен"
      exit 3
    fi
  fi
fi

started=$(date +%s)
log "загрузка копии $NAME"
walg backup-fetch "$PGDATA" "$NAME"
chmod 0700 "$PGDATA"
touch "$PGDATA/recovery.signal"
log "копия загружена за $(( $(date +%s) - started )) с; проигрываю WAL${TARGET:+ до $TARGET}"

args=(
  -c archive_mode=off
  -c "restore_command=/usr/local/bin/walg wal-fetch %f %p"
  -c recovery_target_action=promote
)
# Восстановление прерывается, если эти параметры меньше, чем на исходном сервере («insufficient parameter
# settings»): берём их из pg_control копии (staging, например, работает с max_connections=300).
while IFS=: read -r key value; do
  value="$(echo "$value" | tr -d ' ')"
  case "$key" in
    "max_connections setting") args+=(-c "max_connections=$value") ;;
    "max_worker_processes setting") args+=(-c "max_worker_processes=$value") ;;
    "max_wal_senders setting") args+=(-c "max_wal_senders=$value") ;;
    "max_prepared_xacts setting") args+=(-c "max_prepared_transactions=$value") ;;
    "max_locks_per_xact setting") args+=(-c "max_locks_per_transaction=$value") ;;
  esac
done < <(pg_controldata "$PGDATA")
if [ -n "$TARGET" ]; then
  args+=(-c "recovery_target_time=$TARGET" -c recovery_target_inclusive=true)
fi
exec docker-entrypoint.sh postgres "${args[@]}"
