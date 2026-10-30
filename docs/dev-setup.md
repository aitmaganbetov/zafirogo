# Как поднять окружение разработки

## Что нужно

| Инструмент | Версия | Примечание |
|---|---|---|
| Go | 1.27.3 (`server/go.mod`) | при `GOTOOLCHAIN=auto` (по умолчанию) нужная версия скачается сама |
| Node.js | ≥ 22.22.2, npm 10 | jsdom 30 требует 22.22.2+ |
| Docker + Compose v2 | любой актуальный | Postgres, Valkey, NATS |
| go-task | v3.54.0 | `go install github.com/go-task/task/v3/cmd/task@v3.54.0` |

`$(go env GOPATH)/bin` должен быть в `PATH` **раньше** системных каталогов: иначе может подхватиться
старый `golangci-lint` (собранный Go < 1.27 он откажется работать: «Go language version … is lower than the targeted»).

## Первый запуск

```bash
task tools          # sqlc, goose, oapi-codegen, buf, protoc-gen-go(-grpc), golangci-lint, govulncheck, gosec
cp .env.example .env   # dev-пароли; .env не коммитится (task env делает то же)
task db:up          # Postgres 18.6, Valkey 9.1.3, NATS 2.15.2 (-js), ждёт healthcheck
task db:migrate     # goose up по db/migrations — под владельцем схемы (GOOSE_DBSTRING)
task dev            # сервер :8080 (+ gRPC агентов :8443) + Vite :5173 (прокси /api, /healthz, /readyz → :8080)
```

Открыть <http://localhost:5173>. Проверка бэкенда: `curl localhost:8080/healthz`, `curl localhost:8080/readyz`
(`/readyz` проверяет оба пула Postgres, Valkey и NATS; во время остановки отвечает 503).

Порты окружения (только на 127.0.0.1): Postgres 5432, Valkey 6379, NATS 4222, мониторинг NATS 8222.

### Роли Postgres и два DSN

| Кто | Роль | Откуда |
|---|---|---|
| goose (`task db:migrate`) | владелец схемы (`POSTGRES_USER`, в dev — суперпользователь контейнера) | `GOOSE_DBSTRING` |
| сервер, запросы клуба | логин `ZAFIRO_DB_APP_USER` ∈ `zafiro_app` (RLS по `app.club_id`) | `ZAFIRO_DATABASE_URL` |
| сервер, фоновые задачи и суперадминка | логин `ZAFIRO_DB_PLATFORM_USER` ∈ `zafiro_platform` | `ZAFIRO_DATABASE_PLATFORM_URL` |

Групповые роли `zafiro_app`/`zafiro_platform` (NOLOGIN) создаёт миграция `0004_rls.sql`. Логины с паролями — не
миграции: в dev их создаёт `db/init/10-zafiro-logins.sh` при **первой** инициализации тома Postgres (пароли — из
`.env`, в репозитории их нет). Том создан раньше (или поменяли пароли в `.env`) — `task db:logins` (идемпотентно).
Сервер при старте проверяет, что логины состоят в нужных ролях и **не обходят RLS** (суперпользователь/BYPASSRLS —
ошибка запуска). В prod логины создаёт инфраструктура.

### Агенты (gRPC + mTLS) в dev

При первом запуске сервер создаёт dev-CA и серверный сертификат в `ZAFIRO_AGENT_PKI_DEV_DIR` (`task dev:server` —
`<корень>/.dev/pki`; файлы `*.pem`/`*.key` в git не попадают). Агенту нужен корень `root-ca.pem` оттуда.
Проверить TLS: `openssl s_client -connect localhost:8443 -servername localhost -CAfile .dev/pki/root-ca.pem </dev/null`.
Код регистрации ПК выдаёт сотрудник (REST — волна B); сквозной сценарий «код → Enroll → Connect → online» —
`go test -tags=integration ./internal/app/`.

### Режим без зависимостей

Пустой `ZAFIRO_DATABASE_URL` — сервер поднимает только HTTP (`/healthz`, `/readyz`, фронт) без Postgres, Valkey,
NATS и gRPC. Нужен для смоук-теста собранного бинарника в CI; в `ZAFIRO_ENV=prod` запрещён.

## Конфигурация сервера

Источники по возрастанию приоритета: умолчания → YAML-файл (`-config path` или `ZAFIRO_CONFIG_FILE`) →
переменные `ZAFIRO_*`. Имя переменной: `ZAFIRO_<СЕКЦИЯ>_<КЛЮЧ>`.

Полный список ключей с умолчаниями — `Defaults()` в `server/internal/config/config.go`; ниже основные.
Списки в окружении — через запятую.

| Переменная | По умолчанию | Назначение |
|---|---|---|
| `ZAFIRO_ENV` | `dev` | dev / test / prod (prod требует все секреты и файлы PKI) |
| `ZAFIRO_SHUTDOWN_TIMEOUT` | `30s` | бюджет graceful shutdown: readyz 503 → Drain агентов → gRPC/HTTP → воркеры → подключения |
| `ZAFIRO_HTTP_ADDR` | `:8080` | адрес HTTP |
| `ZAFIRO_HTTP_READ_HEADER_TIMEOUT` | `5s` | также `_READ_TIMEOUT` 30s, `_WRITE_TIMEOUT` 60s, `_IDLE_TIMEOUT` 120s |
| `ZAFIRO_HTTP_REQUEST_TIMEOUT` | `30s` | дедлайн контекста обработчика (кроме `/ws`) |
| `ZAFIRO_HTTP_TRUSTED_PROXIES` | пусто | IP/CIDR через запятую; только от них принимается `X-Forwarded-For` |
| `ZAFIRO_DATABASE_URL` \* | пусто | DSN логина ∈ `zafiro_app`; пусто — режим без зависимостей |
| `ZAFIRO_DATABASE_PLATFORM_URL` \* | пусто | DSN логина ∈ `zafiro_platform`; обязателен вместе с `DATABASE_URL` |
| `ZAFIRO_VALKEY_ADDR`, `ZAFIRO_VALKEY_PASSWORD` \* | `localhost:6379` | Valkey: сессии, лимиты, присутствие ПК |
| `ZAFIRO_NATS_URL` \* | `nats://localhost:4222` | NATS JetStream: события, команды агентам |
| `ZAFIRO_LOG_LEVEL` / `ZAFIRO_LOG_FORMAT` | `info` / `json` | debug…error / json, text |
| `ZAFIRO_AUTH_SIGNING_KEYS` \* | dev: случайный | `kid:секрет[,kid2:секрет2]`, секрет ≥ 32 байт; первый подписывает |
| `ZAFIRO_AUTH_ACCESS_TTL`, `_IDLE_TTL`, `_REMEMBER_TTL`, `_MAX_LIFETIME` | `10m`, `120m`, `720h`, `720h` | сроки токенов и сессий; также `_REFRESH_REUSE_GRACE` 10s, `_TOUCH_EVERY` 1m |
| `ZAFIRO_PASSWORD_MEMORY_KIB`, `_ITERATIONS`, `_PARALLELISM`, `_MAX_CONCURRENT` | `19456`, `2`, `1`, `0` | Argon2id |
| `ZAFIRO_RATELIMIT_ENABLE`, `_DISABLE`, `_FAIL_OPEN` | пусто, пусто, `false` | именованные лимиты (`web-login`, `web-register` выключены по умолчанию) |
| `ZAFIRO_AGENT_INSTANCE_ID` | имя хоста | id инстанса в кластере `[A-Za-z0-9_-]{1,64}` |
| `ZAFIRO_AGENT_GRPC_LISTEN`, `_ENDPOINT` | `:8443`, `localhost:8443` | gRPC агентов; публичный адрес для ответа Enroll |
| `ZAFIRO_AGENT_PKI_CA_CERT_FILE`, `_CA_KEY_FILE`, `_SERVER_CERT_FILE`, `_SERVER_KEY_FILE` | пусто | prod: все четыре обязательны (ключи — вне репозитория) |
| `ZAFIRO_AGENT_PKI_DEV_DIR` | dev/test: `.dev/pki` | dev-CA (в prod запрещён); `_DEV_HOSTS` — имена сертификата |
| `ZAFIRO_AGENT_ENROLL_CODE_PEPPER` \* | dev: случайный | HMAC кодов регистрации (≥ 32 байт), общий для инстансов; `_CODE_TTL` 15m |
| `ZAFIRO_AGENT_CERT_LIFETIME` | `720h` | срок сертификата ПК; кэш: `ZAFIRO_AGENT_CERT_CACHE_REFRESH` 5s, `_FULL_RELOAD` 10m |
| `ZAFIRO_AGENT_STREAM_*` | AS-12 | `PRESENCE_TTL` 90s, `OFFLINE_GRACE` 15s, `HELLO_TIMEOUT` 10s, `STATUS_INTERVAL` 60s, `STATE_DEBOUNCE` 5s, `ACK_TIMEOUT` 30s, `KEY_PREFIX`, `SUBJECT_PREFIX`, `DRAIN_WITHIN` 10s |
| `ZAFIRO_AGENT_GUEST_TOKEN_TTL`, `_IDEMPOTENCY_TTL`, `_QR_BASE_URL`, `_PUBLIC_BASE_URL`, `_QR_POLL_EVERY` | пакета (12h, 24h, `https://zafiro.kz`, = QR, 3s) | API гостя на ПК |
| `ZAFIRO_AGENT_SWEEPER_INTERVAL`, `_RECONCILE_EVERY`, `_COMMAND_RETENTION` | `5s`, `5m`, `168h` | офлайн ПК и очистка команд |
| `ZAFIRO_EVENTS_STREAM_MAX_AGE`, `_DUPLICATES`, `_REPLICAS` | `72h`, `10m`, `1` | поток `zafiro-events` |
| `ZAFIRO_OUTBOX_BATCH_SIZE`, `_INTERVAL`, `_RETENTION` | `100`, `2s`, `24h` | публикатор outbox |
| `ZAFIRO_WEBHOOKS_*` | | `REQUIRE_HTTPS` true, `ALLOW_PRIVATE_HOSTS` false, `TIMEOUT` 5s, `CONNECT_TIMEOUT` 3s, `BACKOFF` 10s,60s,300s,1800s,7200s, `DISABLE_AFTER` 20, `WORKERS` 8, `POLL_INTERVAL` 5s |
| `ZAFIRO_REALTIME_*` | | `MAX_CONNS_PER_USER` 5, `SEND_BUFFER` 64, `PING_INTERVAL` 25s, `PING_TIMEOUT` 10s, `MAX_LIFETIME` 0, `ORIGIN_PATTERNS` |
| `ZAFIRO_WORKERS_EXPIRE_SESSIONS_INTERVAL` | `1m` | снятие истёкших сессий ПК |
| `ZAFIRO_WORKERS_PARTITIONS_INTERVAL`, `_MONTHS` | `24h`, `3` | месячные партиции журналов (Q-081) |

\* — секрет: тип `config.Secret`, в логах и `fmt` печатается `***` (у DSN виден только хост и база).
В dev/test незаданные `AUTH_SIGNING_KEYS` и `ENROLL_CODE_PEPPER` заменяются случайными до перезапуска (предупреждение в логе).


## Генерация кода

Сгенерированный код **коммитится**, CI его не генерирует (но проверяет, что TS-типы API актуальны).

```bash
task gen            # всё сразу
task gen:sqlc       # db/queries + db/migrations → server/internal/store
task gen:api        # api/openapi.yaml → server/internal/http/api/api.gen.go (oapi-codegen, chi strict server)
task gen:proto      # proto/ → server/gen (buf)
task gen:web        # api/openapi.yaml → web/src/api/schema.d.ts (openapi-typescript)
```

Новый REST-эндпоинт: описать в `api/openapi.yaml` → `task gen:api gen:web` → реализовать метод
сгенерированного `api.StrictServerInterface` в `server/internal/http`.

## Тесты и линтеры

```bash
task test               # go test -race ./... + vitest (Docker не нужен)
task test:integration   # go test -tags=integration ./... — testcontainers поднимет Postgres, Valkey, NATS
                        # или задайте ZAFIRO_TEST_DATABASE_URL на готовую базу
task test:e2e           # Playwright против vite preview
task lint               # go vet, golangci-lint, govulncheck, gosec, eslint, tsc
```

Playwright: если браузеры уже установлены в системе (`PLAYWRIGHT_BROWSERS_PATH`), `playwright install`
не нужен; путь к своему Chromium можно задать `PW_CHROMIUM_PATH`. Проверить собранный бинарник:
`E2E_BASE_URL=http://localhost:8080 npm run test:e2e`.

Если Docker Hub отвечает 429 (лимит анонимных скачиваний), образы можно взять из зеркала:
`docker pull mirror.gcr.io/library/postgres:18.6 && docker tag mirror.gcr.io/library/postgres:18.6 postgres:18.6`
(то же для `valkey/valkey:9.1.3-alpine`, `library/nats:2.15.2-alpine`, `testcontainers/ryuk:0.14.0`).

## Сборка

```bash
task build    # web build → server/internal/webui/dist → CGO_ENABLED=0 go build -trimpath → bin/zafiro
./bin/zafiro  # :8080 — фронт на /, API на /api/*, WebSocket /ws, /healthz, /readyz; gRPC агентов :8443
```

Без собранного фронта `go build` тоже работает: в `server/internal/webui/dist` в git лежит только
`.gitkeep`, и на `/` отдаётся страница-заглушка.

## Сброс окружения

```bash
task db:down
docker compose -f docker-compose.dev.yml --env-file .env down -v   # удалить и данные
```
