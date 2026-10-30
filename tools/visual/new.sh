#!/usr/bin/env bash
# Новый сервер (zafirogo) для визуального сравнения: отдельное окружение compose (tools/visual/compose.yml),
# goose up, демо-данные (server/cmd/visual-seed), бинарник со вшитым фронтом, фиксированное «сейчас»
# (ZAFIRO_DEV_FIXED_NOW, только dev/test), сервер на 127.0.0.1:$VISUAL_NEW_PORT.
#
#   tools/visual/new.sh start            # окружение + миграции + данные + сборка + запуск; ждёт /readyz
#   tools/visual/new.sh start --no-build # без пересборки фронта и бинарника (если уже собраны)
#   tools/visual/new.sh stop             # остановить сервер и окружение (данные в tmpfs пропадают)
#   tools/visual/new.sh seed             # пересоздать базу и загрузить демо-данные (сервер перезапустить!)
#
# Переменные: VISUAL_CACHE (~/.cache/zafirogo-visual), VISUAL_NEW_PORT (8091), VISUAL_PG_PORT (55432),
# VISUAL_VALKEY_PORT (56379), VISUAL_NATS_PORT (54222), GOOSE (goose из PATH или ~/go/bin).
set -euo pipefail

MODE="${1:-start}"
BUILD=1
[ "${2:-}" = "--no-build" ] && BUILD=0
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO="$(cd "$HERE/../.." && pwd)"
CACHE="${VISUAL_CACHE:-$HOME/.cache/zafirogo-visual}"
WORK="$CACHE/new"
PORT="${VISUAL_NEW_PORT:-8091}"
export VISUAL_PG_PORT="${VISUAL_PG_PORT:-55432}" VISUAL_VALKEY_PORT="${VISUAL_VALKEY_PORT:-56379}" VISUAL_NATS_PORT="${VISUAL_NATS_PORT:-54222}"
FIXTURE="$HERE/fixtures/demo.json"
BIN="$WORK/zafiro"
COMPOSE=(docker compose -f "$HERE/compose.yml")

log() { echo "[visual:new] $*" >&2; }

mkdir -p "$WORK"
# Пароли окружения: случайные, один раз на машину (не в репозитории).
if [ ! -f "$WORK/secrets.env" ]; then
  umask 077
  {
    echo "VISUAL_PG_PASSWORD=$(openssl rand -hex 16)"
    echo "VISUAL_APP_PASSWORD=$(openssl rand -hex 16)"
    echo "VISUAL_PLATFORM_PASSWORD=$(openssl rand -hex 16)"
    echo "VISUAL_VALKEY_PASSWORD=$(openssl rand -hex 16)"
    echo "VISUAL_SIGNING_KEY=visual:$(openssl rand -hex 32)"
  } > "$WORK/secrets.env"
fi
set -a; . "$WORK/secrets.env"; set +a

OWNER_DSN="postgres://zafiro:$VISUAL_PG_PASSWORD@127.0.0.1:$VISUAL_PG_PORT/zafiro_visual?sslmode=disable"
APP_DSN="postgres://zafiro_app_login:$VISUAL_APP_PASSWORD@127.0.0.1:$VISUAL_PG_PORT/zafiro_visual?sslmode=disable"
PLATFORM_DSN="postgres://zafiro_platform_login:$VISUAL_PLATFORM_PASSWORD@127.0.0.1:$VISUAL_PG_PORT/zafiro_visual?sslmode=disable"

goose_bin() {
  if [ -n "${GOOSE:-}" ]; then echo "$GOOSE"; elif command -v goose >/dev/null; then command -v goose
  elif [ -x "$(go env GOPATH)/bin/goose" ]; then echo "$(go env GOPATH)/bin/goose"
  else log "нет goose: task tools"; exit 2; fi
}

stop_server() {
  if [ -f "$WORK/zafiro.pid" ]; then
    kill "$(cat "$WORK/zafiro.pid")" 2>/dev/null || true
    rm -f "$WORK/zafiro.pid"
  fi
}

seed() {
  # Чистая схема: tmpfs-база пересоздаётся целиком.
  docker exec -i zafirogo-visual-postgres-1 psql -q -v ON_ERROR_STOP=1 -U zafiro -d zafiro_visual -c 'SET client_min_messages = warning' \
    -c 'DROP SCHEMA public CASCADE' -c 'CREATE SCHEMA public' -c 'GRANT USAGE ON SCHEMA public TO PUBLIC' >/dev/null
  GOOSE_DRIVER=postgres GOOSE_DBSTRING="$OWNER_DSN" "$(goose_bin)" -dir "$REPO/db/migrations" up >/dev/null 2>&1
  (cd "$REPO/server" && go run ./cmd/visual-seed -fixture "$FIXTURE" -db "$OWNER_DSN") >&2
  log "демо-данные загружены"
}

build() {
  log "сборка фронта и бинарника"
  if ! (cd "$REPO/web" && { [ -d node_modules ] || npm ci --no-audit --no-fund; } && npm run build) > "$WORK/build.log" 2>&1; then
    tail -30 "$WORK/build.log" >&2
    log "сборка фронта не удалась (журнал: $WORK/build.log)"
    exit 1
  fi
  find "$REPO/server/internal/webui/dist" -mindepth 1 ! -name .gitkeep -exec rm -rf {} +
  cp -R "$REPO/web/dist/." "$REPO/server/internal/webui/dist/"
  (cd "$REPO/server" && CGO_ENABLED=0 go build -trimpath -o "$BIN" ./cmd/zafiro)
}

start() {
  command -v docker >/dev/null || { log "нужен Docker"; exit 2; }
  stop_server
  "${COMPOSE[@]}" up -d --wait >/dev/null
  seed
  if [ "$BUILD" = 1 ] || [ ! -x "$BIN" ]; then build; fi
  local now; now="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["now"])' "$FIXTURE")"
  # Сервер: dev, фиксированное «сейчас», сверка присутствия ПК раз в сутки (статусы ПК из демо-данных не сбрасываются),
  # без лимита входа в панель (каждый снимок входит заново).
  env -i PATH="$PATH" HOME="$HOME" \
    ZAFIRO_ENV=dev ZAFIRO_HTTP_ADDR="127.0.0.1:$PORT" \
    ZAFIRO_DATABASE_URL="$APP_DSN" ZAFIRO_DATABASE_PLATFORM_URL="$PLATFORM_DSN" \
    ZAFIRO_VALKEY_ADDR="127.0.0.1:$VISUAL_VALKEY_PORT" ZAFIRO_VALKEY_PASSWORD="$VISUAL_VALKEY_PASSWORD" \
    ZAFIRO_NATS_URL="nats://127.0.0.1:$VISUAL_NATS_PORT" \
    ZAFIRO_AUTH_SIGNING_KEYS="$VISUAL_SIGNING_KEY" \
    ZAFIRO_AGENT_GRPC_LISTEN="127.0.0.1:0" ZAFIRO_AGENT_PKI_DEV_DIR="$WORK/pki" \
    ZAFIRO_AGENT_SWEEPER_RECONCILE_EVERY=24h ZAFIRO_RATELIMIT_DISABLE=web-login \
    ZAFIRO_DEV_FIXED_NOW="$now" ZAFIRO_LOG_LEVEL=warn ZAFIRO_LOG_FORMAT=text \
    nohup "$BIN" > "$WORK/zafiro.log" 2>&1 &
  echo $! > "$WORK/zafiro.pid"
  for _ in $(seq 1 60); do
    if curl -fsS -o /dev/null "http://localhost:$PORT/readyz" 2>/dev/null; then
      log "новый Zafiro: http://localhost:$PORT («сейчас» = $now)"
      return
    fi
    if ! kill -0 "$(cat "$WORK/zafiro.pid")" 2>/dev/null; then break; fi
    sleep 1
  done
  tail -20 "$WORK/zafiro.log" >&2
  log "сервер не ответил на /readyz (журнал: $WORK/zafiro.log)"
  exit 1
}

case "$MODE" in
  start) start ;;
  stop) stop_server; "${COMPOSE[@]}" down >/dev/null 2>&1 || true ;;
  seed) "${COMPOSE[@]}" up -d --wait >/dev/null; seed ;;
  build) build ;;
  *) echo "usage: $0 start [--no-build]|stop|seed|build" >&2; exit 2 ;;
esac
