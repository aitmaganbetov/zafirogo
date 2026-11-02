#!/usr/bin/env bash
# Go-сервер zafirogo для интеграционных тестов агента .NET (agent/test.sh integration, CI agent.yml).
#
#   agent/tests/integration/server.sh start   # окружение (compose, tmpfs) + goose up + демо-клуб + сборка + запуск;
#                                              # печатает путь к env-файлу с ZAFIRO_IT_* (единственная строка stdout)
#   agent/tests/integration/server.sh stop    # остановить сервер и окружение
#
# Демо-данные — фикстура визуального сравнения (tools/visual/fixtures/demo.json, server/cmd/visual-seed): клуб 1
# «Zafiro Arena», владелец +77010000002, гость +77011000012 (баланс 6 400 ₸), тариф 1 «1 час» (зоны 1, 2), пароль всех —
# login.password. Подписка клуба в фикстуре кончается 01.11.2026 — в копии фикстуры продлевается до 2099 года.
#
# Переменные: AGENT_IT_CACHE (~/.cache/zafirogo-agent-it), AGENT_IT_HTTP_PORT (18091), AGENT_IT_GRPC_PORT (18444),
# AGENT_IT_PG_PORT (55532), AGENT_IT_VALKEY_PORT (56479), AGENT_IT_NATS_PORT (54322), GOOSE (goose из PATH или ~/go/bin).
set -euo pipefail

MODE="${1:-start}"
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO="$(cd "$HERE/../../.." && pwd)"
WORK="${AGENT_IT_CACHE:-$HOME/.cache/zafirogo-agent-it}"
HTTP_PORT="${AGENT_IT_HTTP_PORT:-18091}"
GRPC_PORT="${AGENT_IT_GRPC_PORT:-18444}"
export AGENT_IT_PG_PORT="${AGENT_IT_PG_PORT:-55532}" AGENT_IT_VALKEY_PORT="${AGENT_IT_VALKEY_PORT:-56479}" AGENT_IT_NATS_PORT="${AGENT_IT_NATS_PORT:-54322}"
COMPOSE=(docker compose -f "$HERE/compose.yml")

log() { echo "[agent-it] $*" >&2; }

mkdir -p "$WORK"
chmod 700 "$WORK"
if [ ! -f "$WORK/secrets.env" ]; then
  ( umask 077
    {
      echo "AGENT_IT_PG_PASSWORD=$(openssl rand -hex 16)"
      echo "AGENT_IT_APP_PASSWORD=$(openssl rand -hex 16)"
      echo "AGENT_IT_PLATFORM_PASSWORD=$(openssl rand -hex 16)"
      echo "AGENT_IT_VALKEY_PASSWORD=$(openssl rand -hex 16)"
      echo "AGENT_IT_SIGNING_KEY=it:$(openssl rand -hex 32)"
      echo "AGENT_IT_PEPPER=$(openssl rand -hex 32)"
    } > "$WORK/secrets.env" )
fi
set -a; . "$WORK/secrets.env"; set +a

DB=zafiro_agent_it
OWNER_DSN="postgres://zafiro:$AGENT_IT_PG_PASSWORD@127.0.0.1:$AGENT_IT_PG_PORT/$DB?sslmode=disable"
APP_DSN="postgres://zafiro_app_login:$AGENT_IT_APP_PASSWORD@127.0.0.1:$AGENT_IT_PG_PORT/$DB?sslmode=disable"
PLATFORM_DSN="postgres://zafiro_platform_login:$AGENT_IT_PLATFORM_PASSWORD@127.0.0.1:$AGENT_IT_PG_PORT/$DB?sslmode=disable"

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
  if ! "$(goose_bin)" -env=none -dir "$REPO/db/migrations" postgres "$OWNER_DSN" up > "$WORK/goose.log" 2>&1; then cat "$WORK/goose.log" >&2; exit 1; fi
  python3 - "$REPO/tools/visual/fixtures/demo.json" "$WORK/demo.json" <<'PY'
import json, sys
d = json.load(open(sys.argv[1], encoding="utf-8"))
for c in d["clubs"]:
    sub = c.get("subscription")
    if sub:
        sub["current_period_ends_at"] = "2099-01-01T00:00:00+05:00"
json.dump(d, open(sys.argv[2], "w", encoding="utf-8"), ensure_ascii=False)
PY
  (cd "$REPO/server" && go run ./cmd/visual-seed -fixture "$WORK/demo.json" -db "$OWNER_DSN") >&2
  log "демо-данные загружены"
}

build() {
  (cd "$REPO/server" && CGO_ENABLED=0 go build -trimpath -o "$WORK/zafiro" ./cmd/zafiro)
}

start() {
  command -v docker >/dev/null || { log "нужен Docker"; exit 2; }
  stop_server
  "${COMPOSE[@]}" down >/dev/null 2>&1 || true
  if ! "${COMPOSE[@]}" up -d --wait > "$WORK/compose.log" 2>&1; then cat "$WORK/compose.log" >&2; exit 1; fi
  seed
  build
  rm -rf "$WORK/pki"
  env -i PATH="$PATH" HOME="$HOME" \
    ZAFIRO_ENV=dev ZAFIRO_HTTP_ADDR="127.0.0.1:$HTTP_PORT" \
    ZAFIRO_DATABASE_URL="$APP_DSN" ZAFIRO_DATABASE_PLATFORM_URL="$PLATFORM_DSN" \
    ZAFIRO_VALKEY_ADDR="127.0.0.1:$AGENT_IT_VALKEY_PORT" ZAFIRO_VALKEY_PASSWORD="$AGENT_IT_VALKEY_PASSWORD" \
    ZAFIRO_NATS_URL="nats://127.0.0.1:$AGENT_IT_NATS_PORT" \
    ZAFIRO_AUTH_SIGNING_KEYS="$AGENT_IT_SIGNING_KEY" ZAFIRO_AGENT_ENROLL_CODE_PEPPER="$AGENT_IT_PEPPER" \
    ZAFIRO_AGENT_GRPC_LISTEN="127.0.0.1:$GRPC_PORT" ZAFIRO_AGENT_GRPC_ENDPOINT="localhost:$GRPC_PORT" \
    ZAFIRO_AGENT_PKI_DEV_DIR="$WORK/pki" ZAFIRO_RATELIMIT_DISABLE=web-login \
    ZAFIRO_DEV_DISABLE_WORKERS=monitoring-collect,monitoring-flush,monitoring-clubs \
    ZAFIRO_LOG_LEVEL=info ZAFIRO_LOG_FORMAT=text \
    nohup "$WORK/zafiro" > "$WORK/zafiro.log" 2>&1 &
  echo $! > "$WORK/zafiro.pid"
  for _ in $(seq 1 60); do
    if curl -fsS -o /dev/null "http://127.0.0.1:$HTTP_PORT/readyz" 2>/dev/null && [ -f "$WORK/pki/root-ca.pem" ]; then
      chmod 644 "$WORK/pki/root-ca.pem"
      cat > "$WORK/it.env" <<EOF
ZAFIRO_IT_HTTP=http://127.0.0.1:$HTTP_PORT
ZAFIRO_IT_ENROLL_ENDPOINT=localhost:$GRPC_PORT
ZAFIRO_IT_ROOT_CA=$WORK/pki/root-ca.pem
ZAFIRO_IT_WORK=$WORK
ZAFIRO_IT_SERVER_LOG=$WORK/zafiro.log
ZAFIRO_IT_OWNER_PHONE=+77010000002
ZAFIRO_IT_GUEST_PHONE=+77011000012
ZAFIRO_IT_PASSWORD=zafiro-demo-2026
ZAFIRO_IT_ZONE_ID=2
ZAFIRO_IT_TARIFF_ID=1
EOF
      log "сервер: http://127.0.0.1:$HTTP_PORT, gRPC localhost:$GRPC_PORT (журнал: $WORK/zafiro.log)"
      echo "$WORK/it.env"
      return
    fi
    if ! kill -0 "$(cat "$WORK/zafiro.pid")" 2>/dev/null; then break; fi
    sleep 1
  done
  tail -30 "$WORK/zafiro.log" >&2
  log "сервер не ответил на /readyz"
  exit 1
}

case "$MODE" in
  start) start ;;
  stop) stop_server; "${COMPOSE[@]}" down >/dev/null 2>&1 || true ;;
  *) echo "usage: $0 start|stop" >&2; exit 2 ;;
esac
