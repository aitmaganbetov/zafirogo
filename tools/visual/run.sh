#!/usr/bin/env bash
# Визуальное сравнение старого Laravel и нового сервера одной командой (task visual). Подробно — tools/visual/README.md.
#
#   tools/visual/run.sh                    # все страницы pages.json
#   tools/visual/run.sh --page dashboard   # одна страница (или несколько через запятую)
#   tools/visual/run.sh --no-build         # не пересобирать фронт и бинарник нового сервера
#   tools/visual/run.sh --keep             # оставить оба сервера запущенными (повторные прогоны: --reuse)
#   tools/visual/run.sh --reuse            # не поднимать серверы: они уже запущены (после --keep)
#
# Итог: tools/visual/report/index.html (старое | новое | разница) и report/summary.json.
# Код выхода 1 — ошибка снимка или перенесённая страница (status: ported) сверх порога.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO="$(cd "$HERE/../.." && pwd)"
PAGES="" BUILD="" KEEP=0 REUSE=0
while [ $# -gt 0 ]; do
  case "$1" in
    --page|--pages) PAGES="${2:?--page <id>}"; shift 2 ;;
    --page=*) PAGES="${1#*=}"; shift ;;
    --no-build) BUILD="--no-build"; shift ;;
    --keep) KEEP=1; shift ;;
    --reuse) REUSE=1; KEEP=1; shift ;;
    -h|--help) sed -n 2,11p "$0"; exit 0 ;;
    *) echo "неизвестный аргумент: $1" >&2; exit 2 ;;
  esac
done

log() { echo "[visual] $*" >&2; }
LOGS="${VISUAL_CACHE:-$HOME/.cache/zafirogo-visual}"
mkdir -p "$LOGS"
started=$(date +%s)

# Зависимости сравнения (pixelmatch, pngjs) и внешних ресурсов старых страниц (Font Awesome, Chart.js).
if [ ! -f "$HERE/node_modules/.package-lock.json" ] || [ "$HERE/package-lock.json" -nt "$HERE/node_modules/.package-lock.json" ]; then
  (cd "$HERE" && npm ci --no-audit --no-fund >/dev/null)
fi
if [ ! -d "$REPO/web/node_modules/@playwright/test" ]; then
  (cd "$REPO/web" && npm ci --no-audit --no-fund >/dev/null)
fi

cleanup() {
  if [ "$KEEP" = 0 ]; then
    "$HERE/old.sh" stop || true
    "$HERE/new.sh" stop || true
  fi
}
trap cleanup EXIT

if [ "$REUSE" = 0 ]; then
  log "поднимаю старый (Laravel, PHP 8.4) и новый (Go) серверы на одних демо-данных"
  "$HERE/old.sh" start > "$LOGS/old.log" 2>&1 &
  old_pid=$!
  "$HERE/new.sh" start $BUILD > "$LOGS/new.log" 2>&1 &
  new_pid=$!
  ok=1
  wait "$old_pid" || { ok=0; log "старый сервер не поднялся:"; tail -30 "$LOGS/old.log" >&2; }
  wait "$new_pid" || { ok=0; log "новый сервер не поднялся:"; tail -30 "$LOGS/new.log" >&2; }
  [ "$ok" = 1 ] || exit 1
  grep -h '\[visual:' "$LOGS/old.log" "$LOGS/new.log" | tail -4 >&2
fi

# Свежие снимки выбранных страниц (снимки других страниц остаются в отчёте).
mkdir -p "$HERE/report/shots"
if [ -n "$PAGES" ]; then
  IFS=',' read -ra ids <<< "$PAGES"
  for id in "${ids[@]}"; do rm -rf "$HERE/report/shots/$id"; done
else
  rm -rf "$HERE/report/shots"
fi

log "снимки (Playwright)"
pw=0
(cd "$REPO/web" && VISUAL_PAGES="$PAGES" npx playwright test -c e2e/visual/playwright.config.ts) || pw=$?

duration=$(( $(date +%s) - started ))
cmp=0
VISUAL_DURATION="$duration" node "$HERE/compare.mjs" ${PAGES:+--page "$PAGES"} || cmp=$?
log "время прогона: ${duration} с"
if [ "$KEEP" = 1 ]; then log "серверы оставлены: старый http://localhost:${VISUAL_OLD_PORT:-8001}, новый http://localhost:${VISUAL_NEW_PORT:-8091} (task visual:stop)"; fi
[ "$pw" = 0 ] || log "Playwright завершился с ошибкой ($pw) — см. вывод выше"
exit $(( pw != 0 || cmp != 0 ))
