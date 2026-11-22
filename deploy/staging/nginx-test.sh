#!/usr/bin/env bash
# Проверка конфигурации балансировщика staging (deploy/staging/nginx) в закреплённом образе nginx из compose.yaml:
#   1. `nginx -t`;
#   2. сжатие (аудит PB-11): несжатый JSON сжимается (gzip, Vary: Accept-Encoding), ответ, уже сжатый сервером,
#      не сжимается второй раз, мелкий ответ и клиент без Accept-Encoding получают ответ как есть;
#   3. журнал доступа (аудит A-05): токен приглашения, код входа на ПК и строка запроса (/ws?ticket=…) в него не попадают;
#   4. служебное: /healthz и /readyz снаружи — 404 (аудит OPS-13), /nginx-health — 200 только с 127.0.0.1 (healthcheck
#      compose, OPS-15), HTTP → 301.
# Вместо zafiro и Laravel — заглушка на том же образе. Нужны docker, openssl. CI: .github/workflows/deploy.yml.
#
#   deploy/staging/nginx-test.sh            # образ — из compose.yaml (STG_NGINX_IMAGE переопределяет)
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
IMAGE="${STG_NGINX_IMAGE:-$(sed -n 's/.*STG_NGINX_IMAGE:-\([^}]*\)}.*/\1/p' "$HERE/compose.yaml" | head -1)}"
[ -n "$IMAGE" ] || { echo "не найден образ nginx в compose.yaml" >&2; exit 2; }

ID="zafiro-nginx-test-$$"
WORK="$(mktemp -d)"
cleanup() {
  docker rm -f "$ID-lb" "$ID-up" >/dev/null 2>&1 || true
  docker network rm "$ID" >/dev/null 2>&1 || true
  rm -rf "$WORK"
}
trap cleanup EXIT

fail() { echo "nginx-test: ОШИБКА: $*" >&2; echo "--- журнал lb ---" >&2; docker logs "$ID-lb" 2>&1 | tail -30 >&2 || true; exit 1; }

openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -days 1 -subj "/CN=staging.zafiro.test" \
  -keyout "$WORK/key.pem" -out "$WORK/cert.pem" >/dev/null 2>&1
chmod 0644 "$WORK/key.pem"

# Заглушка upstream: /api/big — 20 КБ JSON без сжатия; /api/pre — тот же JSON, сжатый самим «сервером»;
# /api/small — короткий JSON; остальное — 200.
mkdir -p "$WORK/up/www/api"
{ printf '{"items":['; for i in $(seq 1 500); do printf '{"id":%d,"name":"компьютер %d"},' "$i" "$i"; done; printf '{}]}'; } > "$WORK/up/www/api/big"
cp "$WORK/up/www/api/big" "$WORK/up/www/api/pre"
printf '{"ok":true}' > "$WORK/up/www/api/small"
BIG_SIZE=$(wc -c < "$WORK/up/www/api/big")
cat > "$WORK/up/default.conf" <<'CONF'
server {
    listen 80 default_server;
    listen 8080 default_server;
    root /www;
    default_type application/json;
    location = /api/pre { gzip on; gzip_types application/json; gzip_min_length 1; gzip_proxied any; gzip_http_version 1.0; }
    location /api/ { try_files $uri @ok; }
    location / { return 200 '{"stub":true}'; }
    location @ok { return 200 '{"stub":true}'; }
}
CONF
chmod -R a+rX "$WORK"

docker network create "$ID" >/dev/null
docker run -d --name "$ID-up" --network "$ID" --network-alias app1 --network-alias app2 --network-alias legacy \
  -v "$WORK/up/default.conf:/etc/nginx/conf.d/default.conf:ro" -v "$WORK/up/www:/www:ro" "$IMAGE" >/dev/null

lb_args=(--network "$ID" --tmpfs /tmp
  -v "$HERE/nginx/nginx.conf:/etc/nginx/nginx.conf:ro" -v "$HERE/nginx/http.d:/etc/nginx/http.d:ro"
  -v "$HERE/nginx/stream.d:/etc/nginx/stream.d:ro" -v "$HERE/nginx/snippets:/etc/nginx/snippets:ro"
  -v "$WORK/cert.pem:/run/secrets/web_tls_cert:ro" -v "$WORK/key.pem:/run/secrets/web_tls_key:ro")

echo "nginx-test: образ $IMAGE"
docker run --rm "${lb_args[@]}" "$IMAGE" nginx -t 2>&1 | sed 's/^/  /'
docker run -d --name "$ID-lb" "${lb_args[@]}" "$IMAGE" >/dev/null

# curl внутри контейнера балансировщика (в образе nginx:alpine он есть): порты наружу не публикуются.
req() { # req <host> <path> [curl-параметры…] → заголовки ответа в $WORK/h, тело в $WORK/b (внутри контейнера — /tmp)
  local host="$1" path="$2"; shift 2
  docker exec "$ID-lb" curl -sk --resolve "$host:443:127.0.0.1" -D /tmp/h -o /tmp/b "$@" "https://$host$path"
  docker exec "$ID-lb" cat /tmp/h | tr -d '\r' > "$WORK/h"
  docker exec "$ID-lb" cat /tmp/b > "$WORK/b"
}
header() { grep -ci "^$1" "$WORK/h" || true; }
APP=app.staging.zafiro.test

for i in $(seq 1 30); do
  if docker exec "$ID-lb" curl -sk --resolve "$APP:443:127.0.0.1" -o /dev/null "https://$APP/api/small" 2>/dev/null; then break; fi
  [ "$i" = 30 ] && fail "балансировщик не отвечает"
  sleep 0.5
done

# --- сжатие ---
req "$APP" /api/big -H 'Accept-Encoding: gzip'
[ "$(header 'content-encoding: gzip')" = 1 ] || fail "/api/big: нет Content-Encoding: gzip"
[ "$(header 'vary: .*accept-encoding')" -ge 1 ] || fail "/api/big: нет Vary: Accept-Encoding"
[ "$(gzip -dc "$WORK/b" | wc -c)" = "$BIG_SIZE" ] || fail "/api/big: после распаковки размер не совпал"
GZ_SIZE=$(wc -c < "$WORK/b")

req "$APP" /api/big
[ "$(header 'content-encoding:')" = 0 ] || fail "/api/big без Accept-Encoding: ответ сжат"
[ "$(wc -c < "$WORK/b")" = "$BIG_SIZE" ] || fail "/api/big без Accept-Encoding: размер не совпал"

req "$APP" /api/pre -H 'Accept-Encoding: gzip'
[ "$(header 'content-encoding: gzip')" = 1 ] || fail "/api/pre: Content-Encoding: gzip должен быть ровно один раз"
[ "$(gzip -dc "$WORK/b" | wc -c)" = "$BIG_SIZE" ] || fail "/api/pre: сжато дважды (после одной распаковки — не исходный JSON)"

req "$APP" /api/small -H 'Accept-Encoding: gzip'
[ "$(header 'content-encoding:')" = 0 ] || fail "/api/small: мелкий ответ сжат"

req staging.zafiro.test /api/big -H 'Accept-Encoding: gzip'
[ "$(header 'content-encoding: gzip')" = 1 ] || fail "Laravel-домен: нет Content-Encoding: gzip"

# --- хост кабинета реселлера (reseller.domain, UQ-21) обслуживает zafirogo, а не «неизвестный хост» ---
req resseler.staging.zafiro.test /api/small
[ "$(header 'HTTP/.* 200')" = 1 ] || fail "resseler.staging.zafiro.test: хост не обслуживается (server_name)"

# --- проверки здоровья: снаружи закрыты (OPS-13), healthcheck балансировщика работает (OPS-15) ---
for path in /healthz /readyz; do
  req "$APP" "$path"
  [ "$(header 'HTTP/.* 404')" = 1 ] || fail "$path доступен через балансировщик"
done
docker exec "$ID-lb" wget -q -O /dev/null http://127.0.0.1/nginx-health || fail "/nginx-health с 127.0.0.1 не отвечает"
code=$(docker exec "$ID-lb" sh -c "curl -s -o /dev/null -w '%{http_code}' http://\$(hostname -i | cut -d' ' -f1)/nginx-health")
[ "$code" = 403 ] || fail "/nginx-health не с 127.0.0.1: $code, ожидался 403"
code=$(docker exec "$ID-lb" curl -s -o /dev/null -w '%{http_code}' -H "Host: $APP" http://127.0.0.1/login)
[ "$code" = 301 ] || fail "HTTP → HTTPS: $code, ожидался 301"

# --- журнал доступа ---
TOKEN=Secr3tInviteT0ken0123456789abcdef
CODE=Secr3tPcLoginC0de
TICKET=Secr3tWsTicket
req "$APP" "/api/invitations/$TOKEN"
req "$APP" "/api/invitations/$TOKEN/accept?x=$TICKET" -X POST -H "Referer: https://$APP/invitations/$TOKEN"
req "$APP" "/invitations/$TOKEN"
req "$APP" "/api/pc-login/$CODE" -X POST
req "$APP" "/pc-login/$CODE"
req staging.zafiro.test "/api/guest/qr-login/$CODE" -X POST
req "$APP" "/ws?ticket=$TICKET"
req staging.zafiro.test "/invitations/$TOKEN?next=$TICKET"
req "$APP" "/api/customers?search=$TICKET"
sleep 0.3
docker logs "$ID-lb" > "$WORK/log" 2>&1
for secret in "$TOKEN" "$CODE" "$TICKET"; do
  if grep -q "$secret" "$WORK/log"; then grep "$secret" "$WORK/log" | head -3 >&2; fail "секрет «$secret» попал в журнал nginx"; fi
done
for want in '"GET /api/invitations/\*\*\* HTTP' '"POST /api/invitations/\*\*\*/accept HTTP' '"GET /invitations/\*\*\* HTTP' \
            '"POST /api/pc-login/\*\*\* HTTP' '"POST /api/guest/qr-login/\*\*\* HTTP' '"GET /pc-login/\*\*\* HTTP' '"GET /ws HTTP' '"GET /api/customers HTTP' '"GET /api/big HTTP'; do
  grep -q "$want" "$WORK/log" || fail "в журнале нет строки $want"
done

echo "nginx-test: ok (nginx -t; gzip: $BIG_SIZE → $GZ_SIZE байт, без двойного сжатия; секретов в журнале нет; /healthz, /readyz закрыты)"
