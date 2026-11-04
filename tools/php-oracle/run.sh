#!/usr/bin/env bash
# Pest-оракул: прогоняет сценарии GAP-001…GAP-036 (tools/php-oracle/tests/*.php) на одноразовой
# копии старого Laravel-проекта Zafiro. Сам старый проект не меняется.
#
#   tools/php-oracle/run.sh            # GAP-тесты (по умолчанию)
#   tools/php-oracle/run.sh all        # полный набор Pest старого проекта + GAP-тесты (базовая линия)
#   tools/php-oracle/run.sh eval < calls.jsonl > results.jsonl   # дифференциальный оракул eval.php
#   tools/php-oracle/run.sh settle     # заполнить поле "php" в server/internal/billing/session/testdata/settle_oracle.json
#   tools/php-oracle/run.sh shell      # подготовить копию и напечатать её путь (ORACLE_KEEP=1)
#
# Переменные окружения:
#   ZAFIRO_SRC          старый проект (по умолчанию ../Zafiro рядом с репозиторием)
#   ORACLE_WORK         рабочая папка копии (по умолчанию mktemp -d; удаляется, если не ORACLE_KEEP=1)
#   ORACLE_KEEP=1       не удалять копию (для повторных прогонов укажите тот же ORACLE_WORK)
#   ORACLE_VENDOR_CACHE кэш vendor/ по хешу composer.lock (по умолчанию ~/.cache/zafirogo-php-oracle)
#   ORACLE_GAP_RESULT   файл для фактических результатов GAP (JSONL); без него печатаются в конце
#   PHP_IMAGE           образ PHP, если локальный php < 8.4 (composer.lock старого проекта требует PHP >= 8.4)
#
# Нужны: composer (только для скачивания пакетов), PHP >= 8.4 с pdo_sqlite или Docker.
set -euo pipefail

MODE="${1:-gap}"
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO="$(cd "$HERE/../.." && pwd)"
SRC="${ZAFIRO_SRC:-$REPO/../Zafiro}"
PHP_IMAGE="${PHP_IMAGE:-mirror.gcr.io/library/php:8.4.27-cli}"
CACHE_ROOT="${ORACLE_VENDOR_CACHE:-$HOME/.cache/zafirogo-php-oracle}"

log() { echo "[php-oracle] $*" >&2; }

[ -f "$SRC/artisan" ] || { log "нет старого проекта в $SRC (задайте ZAFIRO_SRC)"; exit 2; }

if [ -n "${ORACLE_WORK:-}" ]; then
  WORK="$ORACLE_WORK"; mkdir -p "$WORK"
else
  WORK="$(mktemp -d "${TMPDIR:-/tmp}/zafiro-oracle.XXXXXX")"
fi
APP="$WORK/app"
cleanup() { if [ "${ORACLE_KEEP:-0}" != "1" ] && [ -z "${ORACLE_WORK:-}" ]; then rm -rf "$WORK"; fi; }
trap cleanup EXIT

# 1. Копия без .git, мобильных приложений, агента и зависимостей.
log "копирую $SRC → $APP"
mkdir -p "$APP"
tar -C "$SRC" --exclude=./.git --exclude=./mobileclient --exclude=./mobileemployee --exclude=./WindowsAgent \
    --exclude=./node_modules --exclude=./vendor -cf - . | tar -C "$APP" -xf -

# 2. Зависимости: из кэша по хешу composer.lock, иначе composer install.
LOCK_HASH="$(sha256sum "$APP/composer.lock" | cut -c1-16)"
CACHED="$CACHE_ROOT/vendor-$LOCK_HASH"
if [ -d "$CACHED" ]; then
  log "vendor из кэша $CACHED"
  cp -a "$CACHED" "$APP/vendor"
else
  log "composer install (dist; если GitHub-архивы недоступны — из исходников без кэша)"
  export COMPOSER_ALLOW_SUPERUSER=1
  # Платформу PHP не проверяем: composer здесь только скачивает, код исполняет PHP >= 8.4.
  if ! (cd "$APP" && composer install --no-interaction --no-progress --no-scripts --prefer-dist --ignore-platform-req=php >&2 </dev/null); then
    rm -rf "$APP/vendor"
    (cd "$APP" && composer install --no-cache --prefer-source --no-interaction --no-progress --no-scripts --ignore-platform-req=php >&2 </dev/null)
  fi
  # Клоны из исходников весят гигабайты: история пакетов не нужна.
  find "$APP/vendor" -name .git -type d -prune -exec rm -rf {} +
  mkdir -p "$CACHE_ROOT" && cp -a "$APP/vendor" "$CACHED.tmp" && mv "$CACHED.tmp" "$CACHED"
fi

# 3. Как исполнять PHP: локальный, если >= 8.4, иначе Docker.
if command -v php >/dev/null && php -r 'exit(PHP_VERSION_ID >= 80400 && extension_loaded("pdo_sqlite") ? 0 : 1);'; then
  run_php() { (cd "$APP" && php "$@"); }
  ORACLE_DIR="$HERE"
else
  command -v docker >/dev/null || { log "нужен PHP >= 8.4 с pdo_sqlite или Docker"; exit 2; }
  log "локальный PHP < 8.4 — запускаю в $PHP_IMAGE"
  run_php() { docker run --rm -i --network none -e ORACLE_GAP_OUT=/app/storage/logs/oracle-gap.jsonl \
      -v "$APP":/app -v "$HERE":/oracle:ro -w /app "$PHP_IMAGE" php "$@"; }
  ORACLE_DIR=/oracle
fi

# 4. .env и ключ; автозагрузка пакетов Laravel (composer запускался с --no-scripts).
cp "$APP/.env.example" "$APP/.env"
run_php artisan package:discover --ansi >/dev/null </dev/null
run_php artisan key:generate --force >/dev/null </dev/null
run_php artisan config:clear >/dev/null </dev/null

case "$MODE" in
  eval)
    run_php "$ORACLE_DIR/eval.php" --app "$( [ "$ORACLE_DIR" = /oracle ] && echo /app || echo "$APP" )"
    exit 0 ;;
  settle)
    FIXTURE="$REPO/server/internal/billing/session/testdata/settle_oracle.json"
    if [ "$ORACLE_DIR" = /oracle ]; then
      docker run --rm --network none -v "$APP":/app -v "$HERE":/oracle:ro -v "$(dirname "$FIXTURE")":/fixture -w /app "$PHP_IMAGE" \
        php /oracle/fill-settle-fixture.php --app /app --fixture /fixture/settle_oracle.json
    else
      run_php "$HERE/fill-settle-fixture.php" --app "$APP" --fixture "$FIXTURE"
    fi
    exit 0 ;;
  shell)
    ORACLE_KEEP=1; echo "$APP"; exit 0 ;;
esac

# 5. GAP-тесты в копию.
mkdir -p "$APP/tests/Feature/Oracle"
cp "$HERE"/tests/*.php "$APP/tests/Feature/Oracle/"
rm -f "$APP/storage/logs/oracle-gap.jsonl"
export ORACLE_GAP_OUT="$APP/storage/logs/oracle-gap.jsonl"

status=0
if [ "$MODE" = "all" ]; then
  # Страницы со @vite падают без public/build/manifest.json: собираем фронт, если есть npm.
  if command -v npm >/dev/null; then
    log "npm ci && npm run build (Vite-манифест для страниц)"
    (cd "$APP" && npm ci --no-audit --no-fund >/dev/null && npm run build >/dev/null && rm -rf node_modules) || log "сборка фронта не удалась — страницы упадут на Vite"
  fi
  log "полный набор Pest (базовая линия)"
  run_php vendor/bin/pest --colors=never > "$WORK/pest-all.log" 2>&1 || status=$?
  grep -E 'FAIL|⨯|✗' "$WORK/pest-all.log" | head -60 || true
else
  log "GAP-тесты"
  run_php vendor/bin/pest --colors=never tests/Feature/Oracle > "$WORK/pest-gap.log" 2>&1 || status=$?
  cat "$WORK/pest-gap.log"
fi

# 6. Итог.
echo
echo "== Итог =="
grep -E '^\s*Tests:' "$WORK"/pest-*.log | tail -2 || true
if [ -f "$APP/storage/logs/oracle-gap.jsonl" ]; then
  if [ -n "${ORACLE_GAP_RESULT:-}" ]; then
    cp "$APP/storage/logs/oracle-gap.jsonl" "$ORACLE_GAP_RESULT"; echo "Факты GAP: $ORACLE_GAP_RESULT"
  else
    echo "== Факты GAP (JSONL) =="; cat "$APP/storage/logs/oracle-gap.jsonl"
  fi
fi
exit "$status"
