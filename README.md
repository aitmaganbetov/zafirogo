# zafirogo

Перенос биллинга компьютерных клубов **Zafiro** с Laravel на **Go + React**.
Облачный SaaS: каждый клуб — тенант (`club_id`). Дизайн и бизнес-логика сохраняются
один в один; старый проект — источник правды: <https://github.com/aitmaganbetov/Zafiro>.

Правила разработки — [`CLAUDE.md`](CLAUDE.md). Как поднять окружение — [`docs/dev-setup.md`](docs/dev-setup.md).

## Структура

```
server/                      Go-модуль github.com/aitmaganbetov/zafirogo/server
  cmd/zafiro/                единый бинарник: HTTP API, gRPC агентов, воркеры, вшитый фронт
  cmd/migrate-from-mysql/    поклубный перенос MySQL → Postgres (tools/migrate-from-mysql/README.md)
  internal/config            koanf: умолчания → YAML → env ZAFIRO_*
  internal/logging           slog (json/text)
  internal/http              chi-роутер, middleware, /healthz, /readyz
  internal/http/api          сгенерировано oapi-codegen из api/<раздел>.yaml (api/gen.sh; не править)
  internal/webui             embed.FS со сборкой web/dist, SPA fallback
  internal/{agent,auth,billing,cash,ledger,money,shop,stock,worker,…}   доменные пакеты
  internal/store             сгенерировано sqlc (не править)
  gen/                       сгенерировано buf из proto/ (не править)
web/                         React 19 + TypeScript + Vite 8 + Tailwind 4
api/<раздел>.yaml            REST-контракт по разделам (источник для Go-сервера и TS-типов web/src/api/gen)
proto/agent/v1/              gRPC-контракт агента ПК
db/migrations/               миграции goose
db/queries/                  запросы sqlc
agent/                       агент ПК (C#, .NET 10)
deploy/                      staging (compose, nginx), бэкап, наблюдаемость
tools/migrate-from-mysql/    описание переноса данных
tools/versions.env           закреплённые версии инструментов (Taskfile, CI, Dockerfile)
docs/                        документация
```

## Команды ([Taskfile](https://taskfile.dev), `task --list`)

| Команда | Что делает |
|---|---|
| `task tools` | установить закреплённые версии sqlc, goose, oapi-codegen, buf, линтеров |
| `task db:up` / `task db:down` | Postgres, Valkey, NATS из `docker-compose.dev.yml` |
| `task db:migrate` | `goose up` по `db/migrations` |
| `task dev` | окружение + сервер Go (:8080) + Vite (:5173) |
| `task gen` | sqlc, oapi-codegen, buf, openapi-typescript (результат коммитится) |
| `task gen:check` | сгенерированный код совпадает с исходниками + `sqlc vet`, `buf lint` (то же делает CI) |
| `task test` | `go test -race ./...` + Vitest |
| `task test:integration` | Go-тесты с реальным Postgres (testcontainers / `ZAFIRO_TEST_DATABASE_URL`) |
| `task test:e2e` | Playwright против `vite preview` |
| `task lint` | go vet, golangci-lint, govulncheck, gosec, eslint, tsc |
| `task build` | `bin/zafiro` — один бинарник со вшитым фронтом |

Проверка: `./bin/zafiro` → `curl localhost:8080/healthz` → `{"status":"ok"}`.
