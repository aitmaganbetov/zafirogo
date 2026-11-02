# deploy/ — выкладка zafirogo

Подробно — [`docs/deploy.md`](../docs/deploy.md) (архитектура, порты, домены, TLS, секреты и ротация, порядок выкладки,
откат, масштабирование, ресурсы, перенос в Kubernetes).

| Каталог / файл | Что | Блок |
|---|---|---|
| `../Dockerfile`, `../.dockerignore` | образ: `zafiro` + `goose` + `migrate-from-mysql`, distroless, nonroot | O1 |
| `staging/` | staging на docker compose: 2 инстанса, Postgres, Valkey, NATS, job миграций, nginx | O1 |
| `trivy-ignore.yaml` | исключения скана образа (CI `release.yml`) — с обоснованием и сроком | O1 |
| `backup/` | резервные копии Postgres (базовые + WAL), проверка восстановления | O2 |
| `observability/` | Prometheus, алерты, дашборды | O3 |

## Staging за 4 команды

Нужны Docker (Compose v2), openssl, go-task, Go (для `staging:seed` и `staging:smoke`).

```bash
task staging:image   # docker build → zafiro:staging (за прокси с подменой TLS: BUILD_CA=/путь/ca.crt)
task staging:up      # deploy/staging/init.sh (пароли, PKI агентов, TLS веба) → compose up → ждёт /readyz обоих инстансов
task staging:seed    # демо-данные: вход +77010000002 / zafiro-demo-2026
task staging:smoke   # вход, WebSocket между инстансами, агент через L4 + PROXY protocol + mTLS, остановка инстанса
```

Открыть панель: в `/etc/hosts` — `127.0.0.1 app.staging.zafiro.test superadmin.staging.zafiro.test
wiki.staging.zafiro.test staging.zafiro.test agent.staging.zafiro.test`, доверить `staging/secrets/web/ca.pem`,
затем <https://app.staging.zafiro.test>. Агенту — корень `staging/secrets/pki/root-ca.pem`, адрес
`agent.staging.zafiro.test:8443`.

Прочее: `task staging:ps`, `task staging:logs -- app1`, `task staging:migrate` (job миграций повторно),
`task staging:reload-lb` (после правки конфигурации nginx), `task staging:down` (graceful, тома сохраняются),
`task staging:destroy` (с томами).

## Что где в `staging/`

| Файл | Назначение |
|---|---|
| `compose.yaml` | сервисы, сеть `172.29.66.0/24` (nginx — `172.29.66.10`, единственный доверенный прокси), секреты-файлы |
| `init.sh` | генерация `.env.staging` и `secrets/` (идемпотентно; в git не попадают) |
| `.env.staging.example` | шаблон переменных (`STG_*`: образы с digest, порты, имена логинов) |
| `postgres-init/20-zafiro-owner.sh` | роль-владелец схемы без SUPERUSER для миграций (PD-9); логины — `db/init/10-zafiro-logins.sh` |
| `nginx/nginx.conf`, `nginx/http.d/zafiro.conf` | HTTPS: домены, мобильные пути и `/api/v1` → zafirogo (чужие токены/ключи он проксирует в Laravel), WebSocket, HSTS |
| `nginx/stream.d/agents.conf` | L4-проброс gRPC агентов + `proxy_protocol on` |
| `legacy/default.conf` | заглушка старого Laravel (отвечает `laravel <путь>`) |

При 429 Docker Hub — в `staging/.env.staging` образы зависимостей с `mirror.gcr.io/library/…` (тот же digest).
