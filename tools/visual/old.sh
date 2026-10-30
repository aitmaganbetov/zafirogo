#!/usr/bin/env bash
# Старый Laravel (Zafiro) для визуального сравнения: копия ../Zafiro в кэше, PHP 8.4 в Docker, sqlite,
# демо-данные tools/visual/fixtures/demo.json, фиксированное «сейчас», собранный фронт, сервер на 127.0.0.1:$VISUAL_OLD_PORT.
# Сам старый проект не меняется: все правки (.env, public/index.php, база) — только в копии.
#
#   tools/visual/old.sh start    # подготовить (если нужно) и запустить; ждёт ответа /login
#   tools/visual/old.sh stop     # остановить контейнер
#   tools/visual/old.sh seed     # пересоздать базу копии и загрузить демо-данные (сервер можно не останавливать)
#
# Переменные: ZAFIRO_SRC (../Zafiro), VISUAL_CACHE (~/.cache/zafirogo-visual), VISUAL_OLD_PORT (8001),
# PHP_IMAGE (mirror.gcr.io/library/php:8.4.27-cli), ORACLE_VENDOR_CACHE (как у tools/php-oracle/run.sh).
set -euo pipefail

MODE="${1:-start}"
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO="$(cd "$HERE/../.." && pwd)"
SRC="${ZAFIRO_SRC:-$REPO/../Zafiro}"
CACHE="${VISUAL_CACHE:-$HOME/.cache/zafirogo-visual}"
WORK="$CACHE/old"
APP="$WORK/app"
PORT="${VISUAL_OLD_PORT:-8001}"
PHP_IMAGE="${PHP_IMAGE:-mirror.gcr.io/library/php:8.4.27-cli}"
CONTAINER="zafirogo-visual-old"
FIXTURE="$HERE/fixtures/demo.json"

log() { echo "[visual:old] $*" >&2; }

php_run() {
  docker run --rm --network none -v "$APP":/app -v "$HERE":/visual:ro -w /app "$PHP_IMAGE" php "$@"
}

# Отпечаток исходника: коммит + незакоммиченные изменения старого проекта.
src_stamp() {
  if git -C "$SRC" rev-parse HEAD >/dev/null 2>&1; then
    { git -C "$SRC" rev-parse HEAD; git -C "$SRC" status --porcelain; } | sha256sum | cut -c1-16
  else
    find "$SRC/app" "$SRC/resources" "$SRC/routes" "$SRC/config" "$SRC/database" -type f -newer "$SRC/composer.lock" 2>/dev/null \
      | sort | xargs -r sha256sum | sha256sum | cut -c1-16
  fi
}

prepare() {
  [ -f "$SRC/artisan" ] || { log "нет старого проекта в $SRC (задайте ZAFIRO_SRC)"; exit 2; }
  local stamp; stamp="$(src_stamp)"
  if [ -f "$WORK/.stamp" ] && [ "$(cat "$WORK/.stamp")" = "$stamp" ] && [ -f "$APP/public/build/manifest.json" ]; then
    log "копия актуальна ($stamp)"
    return
  fi
  rm -rf "$WORK"
  mkdir -p "$WORK"
  # Копия + vendor (кэш по composer.lock) + .env + ключ — общий механизм оракула.
  ORACLE_WORK="$WORK" ORACLE_KEEP=1 ZAFIRO_SRC="$SRC" PHP_IMAGE="$PHP_IMAGE" "$REPO/tools/php-oracle/run.sh" shell >/dev/null

  # .env копии: sqlite-файл, файловые сессии и кэш, синхронная очередь, без отладочной панели.
  sed -i \
    -e 's/^APP_NAME=.*/APP_NAME=Zafiro/' -e 's/^APP_ENV=.*/APP_ENV=production/' -e 's/^APP_DEBUG=.*/APP_DEBUG=false/' \
    -e "s#^APP_URL=.*#APP_URL=http://localhost:$PORT#" \
    -e 's/^APP_LOCALE=.*/APP_LOCALE=ru/' -e 's/^APP_FALLBACK_LOCALE=.*/APP_FALLBACK_LOCALE=ru/' \
    -e 's/^DB_CONNECTION=.*/DB_CONNECTION=sqlite/' -e 's#^DB_DATABASE=.*#DB_DATABASE=/app/database/visual.sqlite#' \
    -e 's/^SESSION_DRIVER=.*/SESSION_DRIVER=file/' -e 's/^CACHE_STORE=.*/CACHE_STORE=file/' \
    -e 's/^QUEUE_CONNECTION=.*/QUEUE_CONNECTION=sync/' -e 's/^LOG_LEVEL=.*/LOG_LEVEL=warning/' \
    "$APP/.env"
  # Cookie сессии — до закрытия браузера: срок cookie Laravel считает от замороженного «сейчас» (в прошлом для
  # браузера), и cookie с таким сроком браузер сразу удаляет.
  printf 'PHP_CLI_SERVER_WORKERS=4\nSESSION_EXPIRE_ON_CLOSE=true\n' >> "$APP/.env"

  # Фиксированное «сейчас» для каждого запроса: Carbon::setTestNow из storage/framework/visual-now (только копия).
  python3 - "$APP/public/index.php" <<'PY'
import sys
p = sys.argv[1]
s = open(p, encoding="utf-8").read()
marker = "require __DIR__.'/../vendor/autoload.php';"
patch = marker + """

// zafirogo tools/visual: фиксированное «сейчас» (правка только в копии старого проекта).
if (is_file($visualNow = __DIR__.'/../storage/framework/visual-now')) {
    $visualAt = \\Carbon\\Carbon::parse(trim((string) file_get_contents($visualNow)));
    \\Carbon\\Carbon::setTestNow($visualAt);
    \\Carbon\\CarbonImmutable::setTestNow($visualAt->toImmutable());
}
"""
if "visual-now" not in s:
    s = s.replace(marker, patch, 1)
    open(p, "w", encoding="utf-8").write(s)
PY

  # Фронт старого проекта (Vite-манифест нужен страницам с @vite): кэш public/build по хешу исходников фронта.
  local fe_hash; fe_hash="$(cd "$APP" && cat package-lock.json vite.config.js $(find resources/css resources/js -type f | sort) | sha256sum | cut -c1-16)"
  local fe_cache="$CACHE/old-build-$fe_hash"
  if [ ! -f "$fe_cache/manifest.json" ]; then
    log "npm ci && npm run build (фронт старого проекта)"
    (cd "$APP" && npm ci --no-audit --no-fund >/dev/null && npm run build >/dev/null)
    rm -rf "$fe_cache.tmp" && cp -a "$APP/public/build" "$fe_cache.tmp" && mv "$fe_cache.tmp" "$fe_cache"
    rm -rf "$APP/node_modules"
  else
    log "фронт из кэша $fe_cache"
    rm -rf "$APP/public/build" && cp -a "$fe_cache" "$APP/public/build"
  fi
  echo "$stamp" > "$WORK/.stamp"
}

seed() {
  local now; now="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["now"])' "$FIXTURE")"
  rm -f "$APP/database/visual.sqlite" && touch "$APP/database/visual.sqlite"
  rm -rf "$APP"/storage/framework/sessions/* "$APP"/storage/framework/cache/data/*
  php_run artisan migrate:fresh --force >/dev/null
  php_run /visual/old/seed.php --app /app --fixture /visual/fixtures/demo.json >&2
  echo "$now" > "$APP/storage/framework/visual-now"
  log "демо-данные загружены, «сейчас» = $now"
}

stop() { docker rm -f "$CONTAINER" >/dev/null 2>&1 || true; }

start() {
  command -v docker >/dev/null || { log "нужен Docker"; exit 2; }
  stop
  prepare
  # Кабинет суперадмина — на своём домене (Q-069), как в проде: страницы pages.json с "host" (и для копии из кэша).
  sed -i '/^SUPERADMIN_DOMAIN=/d' "$APP/.env"
  printf 'SUPERADMIN_DOMAIN=%s\n' "${VISUAL_SUPERADMIN_HOST:-admin.localhost}" >> "$APP/.env"
  seed
  # php artisan serve внутри контейнера: PHP_CLI_SERVER_WORKERS из .env копии (параллельные запросы Playwright).
  # Сеть хоста: старый видит клиента как 127.0.0.1, как новый (IP в журналах — одинаковой ширины на снимке).
  docker run -d --name "$CONTAINER" --network host -v "$APP":/app -w /app "$PHP_IMAGE" \
    php artisan serve --host=127.0.0.1 --port="$PORT" --no-reload >/dev/null
  for _ in $(seq 1 60); do
    if curl -fsS -o /dev/null "http://localhost:$PORT/login" 2>/dev/null; then
      log "старый Zafiro: http://localhost:$PORT"
      return
    fi
    sleep 1
  done
  docker logs "$CONTAINER" 2>&1 | tail -20 >&2
  log "сервер не ответил на /login"
  exit 1
}

case "$MODE" in
  start) start ;;
  stop) stop ;;
  seed) prepare; seed ;;
  prepare) prepare ;;
  *) echo "usage: $0 start|stop|seed|prepare" >&2; exit 2 ;;
esac
