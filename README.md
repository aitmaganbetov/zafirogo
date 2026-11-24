<div align="center">

# zafirogo

**Биллинг компьютерных клубов Zafiro на Go + React**

Облачный SaaS: касса, карта клуба, тарифы, брони, склад, клиенты, агент ПК.
Каждый клуб — отдельный тенант (`club_id`, Row-Level Security в Postgres).

![Go](https://img.shields.io/badge/Go-1.27-00ADD8?logo=go&logoColor=white)
![React](https://img.shields.io/badge/React-19-61DAFB?logo=react&logoColor=black)
![TypeScript](https://img.shields.io/badge/TypeScript-Vite_8-3178C6?logo=typescript&logoColor=white)
![PostgreSQL](https://img.shields.io/badge/PostgreSQL-18-4169E1?logo=postgresql&logoColor=white)
![Valkey](https://img.shields.io/badge/Valkey-9-DC382D?logo=redis&logoColor=white)
![NATS](https://img.shields.io/badge/NATS-JetStream-27AAE1?logo=natsdotio&logoColor=white)

</div>

---

Это **перенос** старого Zafiro с Laravel, а не редизайн: экраны, Tailwind-классы, формулы и суммы до тиына — как в
старом проекте. Источник правды — <https://github.com/aitmaganbetov/Zafiro> (локально `../Zafiro`).

| Документ | О чём |
|---|---|
| [`CLAUDE.md`](CLAUDE.md) | правила разработки: деньги, тенантность, безопасность, локализация |
| [`docs/dev-setup.md`](docs/dev-setup.md) | окружение подробно: все переменные `ZAFIRO_*`, роли Postgres, PKI агентов |
| [`tools/migrate-from-mysql/README.md`](tools/migrate-from-mysql/README.md) | перенос клубов из MySQL старого проекта |
| [`FULL_SYSTEM_TEST_REPORT.md`](FULL_SYSTEM_TEST_REPORT.md) | последняя полная проверка системы |

## Содержание

- [Зависимости](#зависимости)
- [Быстрый старт](#быстрый-старт)
- [Как открыть](#как-открыть)
- [Команды](#команды)
- [Перенос данных из MySQL](#перенос-данных-из-mysql)
- [Структура репозитория](#структура-репозитория)
- [Частые проблемы](#частые-проблемы)

## Зависимости

### Обязательно

| Что | Версия | Зачем | Установка |
|---|---|---|---|
| **Go** | ≥ 1.27.4 (`server/go.mod`) | сервер, генераторы, линтеры | <https://go.dev/dl/>; без root — распаковать в `~/.local/go` |
| **Node.js + npm** | ≥ 22.22.2, npm 10 (в CI — 22.23.3) | фронт (Vite), тесты | <https://nodejs.org/dist/>; без root — в `~/.local/node` |
| **Docker + Compose v2** | любой актуальный | Postgres 18.6, Valkey 9.1.3, NATS 2.15.2 | `sudo apt install docker.io docker-compose-v2` |
| **go-task** | 3.54.0 | все команды проекта | `go install github.com/go-task/task/v3/cmd/task@v3.54.0` |
| **git**, **curl** | — | — | обычно уже есть |

> [!IMPORTANT]
> `$(go env GOPATH)/bin` (обычно `~/go/bin`) должен стоять в `PATH` **раньше** системных каталогов — иначе
> подхватится старый `golangci-lint` или чужой `task`. Пример для `~/.bashrc`:
> ```bash
> export PATH=$HOME/go/bin:$HOME/.local/go/bin:$HOME/.local/node/bin:$PATH
> ```

Пакет `task` из `snap`/`apt` (`taskwarrior`) — **другая программа**, не ставьте его.

### Ставит `task tools` (версии — `tools/versions.env`)

| Инструмент | Версия | Для чего |
|---|---|---|
| sqlc | 1.31.1 | `db/queries` → Go-код `server/internal/store` |
| goose | 3.29.0 | миграции `db/migrations` |
| oapi-codegen | 2.8.1 | `api/*.yaml` → Go-сервер REST |
| buf, protoc-gen-go, protoc-gen-go-grpc | 1.75.0, 1.36.12, 1.6.2 | `proto/` → gRPC агента |
| golangci-lint | 2.15.0 | линтер Go |
| govulncheck, gosec | 1.9.0, 2.30.0 | проверка уязвимостей |

Пакеты фронта (`web/node_modules`) ставит `task dev` или `npm ci` в `web/`.

### Только для тестов

| Что | Когда нужно | Установка |
|---|---|---|
| Chromium для Playwright | `task test:e2e`, `task visual:qa` | `cd web && npx playwright install chromium` |
| Системные библиотеки Chromium | если браузер не стартует (`libatk-1.0.so.0: cannot open…`) | `sudo npx playwright install-deps chromium` (без root — см. [Частые проблемы](#частые-проблемы)) |
| Docker | `task test:integration` (testcontainers) | см. выше |

### По желанию

| Что | Для чего |
|---|---|
| .NET SDK 10.0.100+ (`agent/global.json`) | сборка агента ПК (`agent/`, C#) |
| PHP 8.4 в Docker, composer | `task visual` — попиксельное сравнение со старым Laravel |
| MySQL-клиент и доступ к базе старого проекта | перенос клубов из Laravel |

## Быстрый старт

```bash
git clone https://github.com/aitmaganbetov/zafirogo.git
cd zafirogo

task tools          # генераторы и линтеры нужных версий
cp .env.example .env   # dev-пароли (в git не попадает)
task db:up          # Postgres, Valkey, NATS в Docker; ждёт healthcheck
task db:migrate     # миграции базы
task dev            # сервер Go :8080 (+ gRPC агентов :8443) и Vite :5173
```

Проверка:

```bash
curl localhost:8080/healthz   # {"status":"ok"}
curl localhost:8080/readyz    # проверяет Postgres (оба логина), Valkey и NATS
```

> [!TIP]
> В `.env` задайте постоянные `ZAFIRO_AUTH_SIGNING_KEYS`, `ZAFIRO_AGENT_ENROLL_CODE_PEPPER` и
> `ZAFIRO_SECURITY_DATA_KEYS` (`kid:секрет`, секрет ≥ 32 байт). Пустые — случайные до перезапуска: сессии
> сбрасываются, а второй фактор суперадмина и перенесённые секреты (вебхуки, Telegram) перестают расшифровываться.
> ```bash
> openssl rand -base64 36 | tr -d '/+=\n'   # секрет
> ```

## Как открыть

| Адрес | Что |
|---|---|
| <http://localhost:5173> | интерфейс (Vite, горячая перезагрузка; `/api` проксируется на :8080) |
| <http://localhost:5173/superadmin> | кабинет суперадмина (вход + код TOTP) |
| <http://localhost:8080> | собранный фронт из бинарника (после `task build`) |

> [!WARNING]
> **Открывайте через `localhost`, а не по IP.** Cookie сессии — `__Host-`, `Secure`: браузер принимает их только
> по HTTPS или на `localhost`. По `http://10.0.x.x:5173` вход работает до перезагрузки страницы, потом сбрасывается.
> С другой машины — пробросьте порт: в VS Code вкладка **Ports → Forward a Port → 5173**, или
> `ssh -L 5173:localhost:5173 user@server`. Чтобы Vite был виден в сети, запускайте его с `--host`
> (`cd web && npm run dev -- --host`).

## Команды

Полный список — `task --list`.

| Команда | Что делает |
|---|---|
| **Окружение** | |
| `task tools` | поставить закреплённые версии генераторов и линтеров |
| `task db:up` / `task db:down` | поднять / остановить Postgres, Valkey, NATS |
| `task db:migrate` | применить миграции (`goose up`) |
| `task db:logins` | создать логины сервера на уже существующем томе Postgres |
| `task dev` | окружение + сервер + Vite |
| `task dev:server` / `task dev:web` | только сервер / только Vite |
| **Генерация** | |
| `task gen` | sqlc, oapi-codegen, buf, openapi-typescript (результат коммитится) |
| `task gen:check` | сгенерированное совпадает с исходниками, `sqlc vet`, `buf lint` — как в CI |
| **Тесты и проверки** | |
| `task test` | юнит-тесты Go (`-race`) и фронта (Vitest) |
| `task test:integration` | Go-тесты с настоящими Postgres, Valkey, NATS (нужен Docker) |
| `task test:e2e` | Playwright против `vite preview` (мок API) |
| `task visual:qa` | 86 проверок против настоящего сервера: все страницы под своими ролями, касса, CRUD |
| `task visual` | попиксельное сравнение страниц со старым Laravel |
| `task lint` | go vet, golangci-lint, govulncheck, gosec, eslint, tsc |
| **Сборка** | |
| `task build` | `bin/zafiro` — один бинарник со вшитым фронтом |
| `task image` | Docker-образ |

Сбросить базу разработки полностью: `task db:down && docker compose -f docker-compose.dev.yml --env-file .env down -v`.

## Перенос данных из MySQL

Клубы переходят со старого Laravel **по одному**. Перенос одного клуба — одна транзакция со сверкой; MySQL только
читается. Подробно — [`tools/migrate-from-mysql/README.md`](tools/migrate-from-mysql/README.md).

```bash
export ZAFIRO_MIGRATE_MYSQL_DSN='user:пароль@tcp(127.0.0.1:3306)/zafiro'
export ZAFIRO_MIGRATE_PG_DSN='postgres://zafiro:…@127.0.0.1:5432/zafiro?sslmode=disable'
export ZAFIRO_MIGRATE_LARAVEL_APP_KEY='base64:…'          # APP_KEY старого .env
export ZAFIRO_SECURITY_DATA_KEYS='dev:…'                    # тот же ключ, что у сервера
cd server
go run ./cmd/migrate-from-mysql platform apply --laravel-storage ../../Zafiro/storage/app/private --translations-seed ../db/seed
go run ./cmd/migrate-from-mysql plan   --club <id|slug> --laravel-storage … --thaw-subscription active
go run ./cmd/migrate-from-mysql apply  --club <id|slug> --laravel-storage … --thaw-subscription active
go run ./cmd/migrate-from-mysql verify --club <id|slug> --laravel-storage …
go run ./cmd/migrate-from-mysql go-live --club <id|slug>    # открыть клуб в zafirogo
```

После `apply` клуб в состоянии `imported`: разделы отвечают «Подписка клуба не активна» до `go-live`.
Перед боевым переездом клуб нужно заморозить в Laravel — иначе продажи во время переноса не попадут в новую базу.

## Структура репозитория

```
server/                      Go-модуль github.com/aitmaganbetov/zafirogo/server
  cmd/zafiro/                единый бинарник: HTTP API, gRPC агентов, воркеры, вшитый фронт
  cmd/migrate-from-mysql/    поклубный перенос MySQL → Postgres
  internal/config            koanf: умолчания → YAML → env ZAFIRO_*
  internal/http              chi-роутер, middleware, /healthz, /readyz
  internal/http/sections/    REST по разделам (касса, склад, карта клуба, суперадмин, …)
  internal/http/api          сгенерировано oapi-codegen (не править)
  internal/{agent,auth,billing,cash,ledger,money,stock,worker,…}   доменные пакеты
  internal/store             сгенерировано sqlc (не править)
  internal/webui             embed.FS со сборкой web/dist
  gen/                       сгенерировано buf из proto/ (не править)
web/                         React 19 + TypeScript + Vite 8 + Tailwind 4, TanStack Router/Query
  e2e/                       Playwright; e2e/visual — стенд и проверки против настоящего сервера
api/<раздел>.yaml            REST-контракт (источник для Go-сервера и TS-типов)
proto/agent/v1/              gRPC-контракт агента ПК
db/migrations/               миграции goose
db/queries/                  запросы sqlc
db/seed/                     переводы новых строк
agent/                       агент ПК (C#, .NET 10)
deploy/                      staging (compose, nginx), бэкапы, наблюдаемость
tools/                       versions.env, стенд tools/visual, перенос из MySQL
docs/                        документация
```

## Частые проблемы

<details>
<summary><b><code>task: command not found</code></b></summary>

`task` ставится в `~/go/bin`. Добавьте его в `PATH` (раздел [Зависимости](#зависимости)) и откройте новый
терминал или выполните `source ~/.bashrc`. Не ставьте `task` через snap/apt.
</details>

<details>
<summary><b><code>permission denied … docker.sock</code></b></summary>

Пользователь не в группе `docker` или сеанс открыт до добавления:

```bash
sudo usermod -aG docker $USER   # один раз
newgrp docker                   # или перелогиниться
```
</details>

<details>
<summary><b>После обновления страницы снова просит войти</b></summary>

Сайт открыт по IP без HTTPS — откройте через `localhost` (раздел [Как открыть](#как-открыть)).
</details>

<details>
<summary><b>Суперадмин: код TOTP не принимается после перезапуска</b></summary>

`ZAFIRO_SECURITY_DATA_KEYS` в `.env` пустой — ключ менялся при каждом запуске. Задайте постоянный ключ и сбросьте
второй фактор: `cd server && go run ./cmd/zafiro superadmin-totp-reset -phone +7…`.
</details>

<details>
<summary><b>Docker Hub отвечает 429</b></summary>

Возьмите образы из зеркала:

```bash
docker pull mirror.gcr.io/library/postgres:18.6 && docker tag mirror.gcr.io/library/postgres:18.6 postgres:18.6
```

То же для `valkey/valkey:9.1.3-alpine` и `library/nats:2.15.2-alpine`.
</details>

<details>
<summary><b>Playwright без root: браузер не стартует</b></summary>

Скачайте недостающие библиотеки в домашний каталог и укажите путь к ним:

```bash
mkdir -p ~/.local/pwlibs && cd ~/.local/pwlibs
apt-get download libatk1.0-0t64 libatk-bridge2.0-0t64 libatspi2.0-0t64 libxdamage1 libasound2t64 libxres1
for d in *.deb; do dpkg -x "$d" root; done
export LD_LIBRARY_PATH=$HOME/.local/pwlibs/root/usr/lib/x86_64-linux-gnu
```

Каких библиотек не хватает, покажет
`ldd ~/.cache/ms-playwright/chromium_headless_shell-*/chrome-headless-shell-linux64/chrome-headless-shell | grep 'not found'`.
</details>

<details>
<summary><b>Golangci-lint: «Go language version … is lower than the targeted»</b></summary>

Подхватился старый системный `golangci-lint`. Поставьте `~/go/bin` в `PATH` раньше системных каталогов и выполните
`task tools`.
</details>
