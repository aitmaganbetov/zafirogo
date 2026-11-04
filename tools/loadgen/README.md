# Нагрузочный тест (Этап 6, O4)

Генератор — `server/cmd/loadgen` (тег сборки `loadgen`, в CI не собирается). Результаты и выводы — `docs/load-test.md`.

## Что делает

- **Агенты**: N ПК, у каждого свой клиентский сертификат (mTLS, ECDSA P-256), отдельное TCP-соединение через nginx L4
  (PROXY protocol) → `Connect`: `Hello` → `Welcome` → `Status` раз в 30 с, итог каждой команды (`CommandResult`),
  `Drain` — переподключение в случайный момент окна, обрыв — full jitter `random(0, min(60 с, 2^n))` (как агент,
  `docs/agent-protocol.md` §2). Клуб ходит со своего адреса источника (`net.sh`) — лимиты по IP на клуб.
- **Гости** (доля `-guest-share`, 60 %): вход → покупка минут или тарифа (ключ идемпотентности; повтор тем же ключом
  при обрыве; 3 % покупок повторяются нарочно — результат должен совпасть) → игра 4–12 мин, в половине сессий заказ в
  магазине → завершение сессии (или время выходит — сессию закрывает воркер `expire-sessions`) → выход → пауза (среднее
  8 мин). В клубах с автобиллингом (чётные) 15 % гостей входят и ждут, пока сервер купит время сам.
- **Админка**: владелец и кассир каждого клуба — WebSocket + запрос списка раз в ~30 с (карта, ПК, смена, X-отчёт,
  клиенты, заказы, журнал, дашборд); кассир — пополнения (раз в ~3 мин) и продажи на кассе (раз в ~6 мин).
- **Фоновые**: истечение сессий, автобиллинг, outbox, вебхуки (10 клубов → заглушка `legacy`).
- **Сценарии** (`run`): `ramp` → `steady` (а) → `peak` (в: доп. денежные операции, итого ×`-peak-mult`) →
  `restart-graceful` (б: `docker stop app1`) → `restart-kill` (б: `docker kill app2`, шторм переподключений).
- **Замеры**: p50/p95/p99 каждой операции по фазам, итоги (ok / бизнес-отказ / ошибка), раз в 10 с — `docker stats`,
  `/metrics` инстансов (порт 9090 в сети staging), Postgres (TPS, WAL, ожидания блокировок, UPDATE `club_computers`),
  Valkey `INFO`, NATS `/varz`.

## Запуск

```bash
task staging:image && task staging:up
C="docker compose -f deploy/staging/compose.yaml -f tools/loadgen/compose.load.yaml --env-file deploy/staging/.env.staging"
$C up -d                                    # лимит дескрипторов nginx, вебхуки на заглушку
sh tools/loadgen/net.sh add 172.29.66.100 50
cd server && go build -tags loadgen -o /tmp/loadgen ./cmd/loadgen
/tmp/loadgen fixture -clubs 50 -pcs 100 -out /tmp/load.json
. ../deploy/staging/.env.staging && go run ./cmd/visual-seed -fixture /tmp/load.json \
  -db "postgres://$STG_DB_OWNER_USER:$STG_DB_OWNER_PASSWORD@127.0.0.1:15432/$STG_POSTGRES_DB?sslmode=disable"
/tmp/loadgen prepare -staging ../deploy/staging -certs /tmp/load-certs.json -src 172.29.66.100
/tmp/loadgen run -staging ../deploy/staging -certs /tmp/load-certs.json -src 172.29.66.100 -out /tmp/load-run
/tmp/loadgen check -staging ../deploy/staging -keys /tmp/load-run/keys.json
cd .. && task staging:destroy
```

`prepare` выпускает сертификаты ПК **напрямую** от staging-CA (`secrets/pki/agent-ca.*`, как делает `Enroll`:
`pki.CA.Issue` + строка `agent_certificates`) — 5 000 ПК за ≈ 3 с без обхода лимитов регистрации в коде; настоящий путь
(код через REST → `Enroll` через nginx) меряется выборкой `-enroll-sample` (50). Пароли набора — Argon2id (иначе первый
вход каждого гостя — bcrypt cost 12 и перехеширование). Файл `-certs` содержит закрытые ключи — только локально.

Флаги `run -h`: `-pcs`/`-clubs` (меньше ПК), `-steady`, `-peak`, `-peak-mult`, `-restart graceful,kill|""`,
`-guest-share`, `-guest-idle`, `-play-min/-play-max`, `-status-every`, `-read-every`, `-topup-every`.

`check`: сумма проводок каждой операции = 0, балансы = ledger (основной и бонусный), нет отрицательных балансов и дублей
ключей идемпотентности, каждая успешная денежная операция генератора — ровно одна операция ledger.
