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
task db:migrate     # goose up по db/migrations
task dev            # сервер :8080 + Vite :5173 (прокси /api, /healthz, /readyz → :8080)
```

Открыть <http://localhost:5173>. Проверка бэкенда: `curl localhost:8080/healthz`, `curl localhost:8080/readyz`.

Порты окружения (только на 127.0.0.1): Postgres 5432, Valkey 6379, NATS 4222, мониторинг NATS 8222.

## Конфигурация сервера

Источники по возрастанию приоритета: умолчания → YAML-файл (`-config path` или `ZAFIRO_CONFIG_FILE`) →
переменные `ZAFIRO_*`. Имя переменной: `ZAFIRO_<СЕКЦИЯ>_<КЛЮЧ>`.

| Переменная | По умолчанию | Назначение |
|---|---|---|
| `ZAFIRO_ENV` | `dev` | dev / test / prod |
| `ZAFIRO_HTTP_ADDR` | `:8080` | адрес HTTP |
| `ZAFIRO_HTTP_READ_HEADER_TIMEOUT` | `5s` | также `_READ_TIMEOUT` 30s, `_WRITE_TIMEOUT` 60s, `_IDLE_TIMEOUT` 120s |
| `ZAFIRO_HTTP_REQUEST_TIMEOUT` | `30s` | дедлайн контекста обработчика |
| `ZAFIRO_HTTP_TRUSTED_PROXIES` | пусто | IP/CIDR через запятую; только от них принимается `X-Forwarded-For` |
| `ZAFIRO_DATABASE_URL` | пусто | DSN Postgres; пусто — `/readyz` базу не проверяет |
| `ZAFIRO_VALKEY_ADDR`, `ZAFIRO_VALKEY_PASSWORD` | `localhost:6379` | пока не используется |
| `ZAFIRO_NATS_URL` | `nats://localhost:4222` | пока не используется |
| `ZAFIRO_LOG_LEVEL` | `info` | debug / info / warn / error |
| `ZAFIRO_LOG_FORMAT` | `json` | json / text |
| `ZAFIRO_SHUTDOWN_TIMEOUT` | `15s` | время на graceful shutdown после SIGINT/SIGTERM |

Секреты в логах маскируются (`config.Config` реализует `slog.LogValuer`).

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
task test:integration   # go test -tags=integration ./... — testcontainers поднимет Postgres
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
./bin/zafiro  # :8080, фронт на /, API на /api/*, /healthz, /readyz
```

Без собранного фронта `go build` тоже работает: в `server/internal/webui/dist` в git лежит только
`.gitkeep`, и на `/` отдаётся страница-заглушка.

## Сброс окружения

```bash
task db:down
docker compose -f docker-compose.dev.yml --env-file .env down -v   # удалить и данные
```
