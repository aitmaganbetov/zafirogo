# Выкладка zafirogo

Этап 6, блок O1. Файлы: `Dockerfile`, `.dockerignore`, `deploy/staging/**` (compose, nginx, инициализация),
`.github/workflows/release.yml`, `deploy/trivy-ignore.yaml`, задачи `task image`, `task staging:*`.
Резервные копии — `deploy/backup` (O2), наблюдаемость — `deploy/observability` (O3). Вопросы и принятые решения —
[`open-questions/stage6-deploy.md`](open-questions/stage6-deploy.md).

## 1. Образ

Один образ на всё (`Dockerfile`, многоступенчатая сборка):

| Стадия | База (тег + digest, PD-4) | Что делает |
|---|---|---|
| `web` | `node:22.23.3-alpine3.24` | `npm ci --ignore-scripts` → `npm run build` (Vite) |
| `go` | `golang:1.27.4-alpine3.24` (та же версия, что `server/go.mod` и `GO_VERSION` в `tools/versions.env`; совпадение проверяет `go.yml`, аудит SEC-4) | `CGO_ENABLED=0 go build -trimpath -tags timetzdata -ldflags "-s -w -X main.buildVersion=…"` со вшитым фронтом (`internal/webui/dist`); `goose` v3.29.0 (`GOOSE_VERSION` из `tools/versions.env`) только с драйвером Postgres; `migrate-from-mysql`; `zafiro-migrate` и `zafiro-healthcheck` (модуль `deploy/ops`) |
| итог | `gcr.io/distroless/static-debian13:nonroot` | без shell и пакетного менеджера, пользователь `65532:65532`, CA-сертификаты и tzdata из distroless (tzdata ещё и вшит в бинарники) |

Содержимое: `/usr/local/bin/zafiro` (ENTRYPOINT), `/usr/local/bin/goose`, `/usr/local/bin/zafiro-migrate` (job
миграций, §7), `/usr/local/bin/zafiro-healthcheck`, `/usr/local/bin/migrate-from-mysql`, `/migrations`
(`db/migrations`, `GOOSE_MIGRATION_DIR`), `/seed` (`db/seed`). Переменные по умолчанию: `ZAFIRO_ENV=prod`,
`ZAFIRO_LOG_FORMAT=json`, `GOOSE_DRIVER=postgres`. Порты: 8080 (HTTP), 8443 (gRPC агентов).

`HEALTHCHECK` — `zafiro-healthcheck` (GET `http://127.0.0.1:8080/healthz`, таймаут 2 с, каждые 10 с; в distroless нет
shell/curl, поэтому отдельный статический бинарник; аудит OPS-15). Это liveness: недоступный Postgres не делает
контейнер «нездоровым» — из балансировки его выводит readiness (`/readyz`: оба пула Postgres, Valkey, NATS; во время
остановки — 503). Другой адрес — `ZAFIRO_HEALTHCHECK_URL`. k8s `HEALTHCHECK` не использует (пробы — §13). Одноразовые
запуски образа (job миграций, `migrate-from-mysql`) — с `--no-healthcheck` / `healthcheck: {disable: true}`.

| Замер (локально, 4 vCPU) | Значение |
|---|---|
| Размер образа | **29,8 МБ** сжатый (скачивание), ≈ 83 МБ распакованный; 117 МБ в хранилище Docker вместе со сжатыми слоями (zafiro 48 МБ, migrate-from-mysql 20 МБ, goose 12,6 МБ, distroless ≈ 2 МБ — несжатые) |
| Сборка с кэшем модулей Go и npm (BuildKit cache mounts) | ≈ 105 с: фронт 33 с, Go 66 с |
| Холодная сборка (без кэша, CI) | ≈ 3–4 мин (оценка: + скачивание модулей и npm) |
| Скан trivy 0.76.0 (HIGH/CRITICAL) | 0 находок (debian 13.7: 6 пакетов; 3 Go-бинарника) |

Сборка: `task image IMAGE=zafiro:dev` (`VERSION` — `git describe`). За прокси с подменой TLS корневой сертификат прокси
передаётся секретом сборки и в слои не попадает: `task image BUILD_CA=/путь/ca-bundle.crt`.

## 2. Архитектура выкладки

```
                   ┌──────────────────────── балансировщик (nginx 1.30.5) ─────────────────────────┐
браузер, SPA,      │ :443  TLS терминируется здесь, HSTS, X-Forwarded-For := адрес клиента        │
мобильные, /api/v1 │       app./superadmin./wiki. → zafirogo; основной домен → Laravel, кроме     │──► zafiro ×N :8080
                   │       /api/(auth|guest|staff|v1)/ → zafirogo (чужие клубы — прокси в Laravel)  │
агенты ПК (mTLS)   │ :8443 L4-проброс TCP без расшифровки + PROXY protocol v1                      │──► zafiro ×N :8443
                   └───────────────────────────────────────────────────────────────────────────────┘
                                 zafiro ×N ──► Postgres (2 логина: zafiro_app, zafiro_platform)
                                           ──► Valkey (сессии, лимиты, присутствие ПК)
                                           ──► NATS JetStream (события, команды агентам между инстансами)
job миграций (goose, роль-владелец схемы) ──► Postgres          старый Laravel ◄── zafiro (mobile.upstream)
```

Инстансы приложения не хранят состояния: сессии и лимиты — в Valkey, события и команды между инстансами — NATS,
деньги — Postgres (ledger). Поток агента живёт на одном инстансе; владельца ПК знает Valkey
(`docs/agent-protocol.md` §13), команды с других инстансов приходят через NATS.

## 3. Порты и домены

| Порт | Где | Кто ходит |
|---|---|---|
| 443/tcp (+ 80 → 301) | балансировщик, публично | браузеры, SPA, WebSocket `/ws`, REST, мобильные приложения, интеграции `/api/v1` |
| 8443/tcp | балансировщик, публично | агенты ПК (gRPC, mTLS) |
| 8080/tcp | инстанс, внутренняя сеть | балансировщик (HTTP), проверки оркестратора `/healthz`, `/readyz` |
| 8443/tcp | инстанс, внутренняя сеть | балансировщик (L4, PROXY protocol) |
| `observability.metrics_listen` (например 9090) | инстанс, внутренняя сеть | Prometheus (O3, `docs/observability.md`); на балансировщике `/metrics` закрыт |
| 5432, 6379, 4222 | внутренняя сеть | инстансы, job миграций |

| Домен (прод / staging) | Куда | Настройка сервера |
|---|---|---|
| `app.zafiro.kz` / `app.staging.zafiro.test` | zafirogo: панель клуба (по общему адресу балансировщик не знает клуба сотрудника до входа — README переноса, п. 8) | — |
| `superadmin.zafiro.kz` | zafirogo: кабинет суперадмина с корня | `superadmin.domain` (Q-069) |
| `wiki.zafiro.kz` | zafirogo: wiki с корня | `http.wiki_host` (Q-069) |
| `resseler.zafiro.kz` / `resseler.staging.zafiro.test` | zafirogo: кабинет реселлера с корня (написание — как `RESELLER_DOMAIN` в `config/app.php` старого проекта, UQ-21); входят только реселлеры, панели клуба и регистрации на нём нет. Нужны запись DNS и сертификат на это имя; добавить хост в `superadmin.ssl_hosts` (экран «Сервер» следит за сроком сертификата). До переноса последнего клуба кабинет на новом сервере не открывать — в проде `reseller.domain` задаётся только после переноса последнего клуба (UQ-3, UR-Q1; решение владельца 16.11.2026): реселлеры, счета и выплаты — данные платформы, источник правды — старый сервер | `reseller.domain` (`ZAFIRO_RESELLER_DOMAIN`) |
| `zafiro.kz` | старый Laravel, пока не переведены все клубы, кроме путей ниже | — |
| `zafiro.kz/api/(auth\|guest\|staff)/` | zafirogo; чужие токены и телефоны он сам проксирует в Laravel | `mobile.upstream`, `mobile.upstream_host` (`docs/mobile-compat.md` §3) |
| `zafiro.kz/api/v1/` | zafirogo; ключи, которых нет в его базе, он сам проксирует в Laravel | `integrations.legacy_upstream`, `integrations.legacy_upstream_host` (пусто — `mobile.upstream`, `mobile.upstream_host`; S6-O1-4) |
| `agent.zafiro.kz:8443` | zafirogo, gRPC агентов | `agent.grpc.endpoint` (уходит агенту в ответе Enroll) |

Адрес сайта для ссылок вне ответа на запрос (оповещения Telegram без `superadmin.domain`, `og:image`, адрес входа
реселлеров) — `http.public_url` (`ZAFIRO_HTTP_PUBLIC_URL=https://zafiro.kz`): из заголовка `Host` он не берётся; не задан —
оповещения уходят без ссылки (`docs/DECISIONS.md`, «HTTP и второй фактор»).

Go-live клуба не меняет балансировщик: `/api/(auth|guest|staff|v1)/` всегда идут в zafirogo, и он решает сам —
токен или ключ клуба есть в его базе (в том числе выпущенный уже в zafirogo) → ответ здесь, нет → обратный прокси
в Laravel без изменений (`server/internal/http/sections/v1/upstream.go`, `server/internal/http/legacy`). Свой ключ,
даже отозванный или с неверным секретом, обслуживается здесь (401, как ответил бы Laravel); лимит неудачных проверок
по IP для проксируемых ключей считает Laravel. Когда переведены все клубы — `mobile.upstream` и
`integrations.legacy_upstream` очистить (прокси выключится). Откат клуба до первой операции —
`tools/migrate-from-mysql/README.md`, п. 10.

### Балансировщик: сжатие и журнал доступа

Конфигурация — `deploy/staging/nginx/nginx.conf` (блок `http`); проверка без поднятого staging —
`task staging:nginx-test` (`deploy/staging/nginx-test.sh`, в CI — `.github/workflows/deploy.yml`): `nginx -t` в
закреплённом образе nginx из `compose.yaml`, заглушка вместо инстансов.

- **Сжатие** (аудит PB-11): `gzip on`, уровень 5, ответы от 1 КБ, типы — JSON, JS, CSS, текст, XML, SVG, манифест
  (HTML — всегда). `Accept-Encoding` клиента уходит в инстанс как есть: ответ, который сервер сжал сам (есть
  `Content-Encoding`), nginx передаёт без изменений — **дважды не сжимается**; несжатый — сжимает nginx.
  `gzip_proxied any` (запросы через прокси/CDN), `gzip_vary on` (`Vary: Accept-Encoding`). `gzip_static` не включён:
  статику отдаёт инстанс из `embed.FS`, файлов на диске балансировщика нет. Замер на заглушке: JSON 21 298 байт →
  2 549 байт. WebSocket `/ws` и gRPC агентов (`stream`) сжатие не затрагивает.
- **Журнал доступа без секретов** (аудит A-05): формат `zafiro` — путь **без строки запроса** (билет `/ws?ticket=…` и
  любые параметры не пишутся), сегмент после `/invitations/`, `/api/invitations/`, `/pc-login/`, `/api/pc-login/`,
  `/api/guest/qr-login/` заменён на `***` (токен приглашения сотрудника, код входа на ПК по QR), `Referer` не пишется
  (страница приглашения шлёт его с токеном). Сервер маскирует те же сегменты в своём журнале
  (`server/internal/http/logpath.go`). Новый маршрут с секретом в пути — добавить в `map $request_uri $log_uri` и в
  проверку. `error_log` nginx (уровень `warn`) печатает строку запроса как есть — во внешние системы сбора логов его
  отдавать только с фильтром.

## 4. TLS

- **Веб** — TLS на балансировщике (TLS 1.2/1.3, ECDHE + AEAD), HSTS ставит балансировщик (сервер ставит HSTS
  только при TLS в процессе, Q-071). Сервер доверяет `X-Forwarded-For` только от адресов
  `http.trusted_proxies`; nginx **заменяет** заголовок адресом клиента (не дописывает). Cookie сессии — всегда
  `Secure`, поэтому панель работает только по HTTPS. Прод — сертификат публичного CA (ACME на балансировщике).
- **Агенты** — сквозной TLS: балансировщик пробрасывает TCP (`stream`), mTLS заканчивается в zafiro (сервер должен
  видеть клиентский сертификат ПК). Серверный сертификат gRPC подписан **корнем** CA агентов (агент доверяет только
  встроенному корню — pinning, `docs/agent-protocol.md` §12), SAN = `agent.<домен>`.
- **PROXY protocol** (`server/internal/grpc/proxyproto.go`): без него сервер видит адрес балансировщика, и лимиты
  регистрации по IP (`agent-enroll`) и подсети ключа образа (Q-037) за балансировщиком не работают.
  - `agent.grpc.proxy_trusted` (`ZAFIRO_AGENT_GRPC_PROXY_TRUSTED`) — IP/CIDR балансировщиков; от них заголовок
    **обязателен** (соединение без него закрывается), от остальных адресов не читается (прямые подключения — как есть);
  - v1 (nginx stream `proxy_protocol on`) и v2 (HAProxy `send-proxy-v2`, облачные NLB); LOCAL/UNKNOWN — проверки
    самого балансировщика, берётся адрес соединения; `agent.grpc.proxy_header_timeout` (5 с) — на заголовок;
  - адрес из заголовка получают все, кто читает `peer` (`grpcserver.PeerIP`, журнал регистрации, `remote_ip` ПК).
  - Проверено на staging: журнал `computer.registered` содержит адрес клиента до nginx (`172.29.66.1`), не `172.29.66.10`.
- Внутренние соединения (Postgres, Valkey, NATS) в staging — без TLS (`sslmode=disable`, `valkey.tls` выключен) и
  останутся такими осознанно: staging — одна машина, все соединения идут внутри отдельной bridge-сети compose `zafiro`
  (`172.29.66.0/24`), в которой только контейнеры проекта; наружу опубликованы лишь порты на `127.0.0.1` хоста. Общей
  сети с другими системами нет — шифровать внутри неё нечего, а TLS потребовал бы ещё одного CA и секретов только ради
  staging. Сервер при старте предупреждает об этом в журнале (`sslmode ниже require`, `valkey.tls выключен`) — на
  staging это ожидаемые предупреждения. Если staging переедет на несколько машин или в общую сеть — перевести на
  `sslmode=verify-full` (сертификат Postgres от своего CA, `sslrootcert`), `valkey.tls=true` (`tls_ca_file`,
  `tls_server_name`) и TLS NATS.
- **Прод:** Postgres — `sslmode=verify-full` в DSN; Valkey — `valkey.password` **обязателен** (без него сервер с
  `ZAFIRO_ENV=prod` не стартует), TLS (`valkey.tls`, `valkey.tls_ca_file`, `valkey.tls_server_name`, при ACL —
  `valkey.username`), если сеть общая с другими системами; NATS — учётные данные в `nats.url` (без них — предупреждение:
  публиковать события клубов может любой в сети NATS) и TLS при общей сети (вопрос S6-O1-6).
- **Keepalive Postgres.** Клиент (pgx) — TCP keepalive 30 с / 10 с × 3 и `TCP_USER_TIMEOUT` 60 с: обрыв сети до
  Postgres (failover, NAT) обнаруживается за ≈ 1 мин, а не за часы. Сервер — те же значения
  (`tcp_keepalives_idle=30`, `tcp_keepalives_interval=10`, `tcp_keepalives_count=3`, `tcp_user_timeout=60000`, staging —
  `command` Postgres): «мёртвые» сессии исчезших клиентов не держат блокировки и слоты `max_connections`.

## 5. Секреты и ключи

Секреты не в образе, не в репозитории, не в переменных compose-файла и не в аргументах команд (аргументы и переменные
контейнера видны в `docker inspect`, `ps` хоста и в конфигурации завершённого job — аудит OPS-14: пароли Valkey и NATS,
DSN владельца схемы теперь файлы-секреты). Сервер читает их из YAML-файла конфигурации
(`ZAFIRO_CONFIG_FILE`, смонтирован как секрет) или из переменных `ZAFIRO_*` (k8s Secret → env); PKI — файлы.
В логе конфигурация печатается с маской (`config.Secret`, у DSN виден только хост и база).

| Секрет | Ключ конфигурации | Где хранить | Ротация |
|---|---|---|---|
| Пароли логинов Postgres | `database.url`, `database.platform_url` | секрет-менеджер → файл конфигурации | новый пароль `ALTER ROLE … PASSWORD` → обновить секрет → rolling restart |
| Владелец схемы (миграции, перенос) | DSN job миграций — **файл** `GOOSE_DBSTRING_FILE` (`zafiro-migrate`; staging — секрет `goose_dsn`), `ZAFIRO_MIGRATE_PG_DSN` | секрет-менеджер, **только** в job миграций и у оператора переноса | как выше; приложению не выдаётся |
| Valkey, NATS | `valkey.password`, `nats.url`; на стороне серверов Valkey/NATS — файлы конфигурации (staging: секреты `valkey_conf` — `requirepass`, `nats_conf` — `authorization`) | секрет-менеджер | на сервере с двумя паролями (Valkey ACL, NATS users) → обновить секрет → restart → убрать старый |
| Ключи подписи access-токенов | `auth.signing_keys` (`kid:секрет`, ≥ 32 байт) | секрет-менеджер, общий для инстансов | новый ключ **первым** (подписывает), старый — вторым (проверяет) → rolling restart → через `auth.access_ttl` (10 мин) старый убрать |
| Ключи шифрования данных | `security.data_keys` | секрет-менеджер; **копия офлайн** (потеря = потеря секретов вебхуков и Telegram, ключей TOTP суперадминов — сброс ниже) | новый первым → воркер `reencrypt-secrets` перешифровывает → старый убрать (`docs/schema-changes.md`, «Шифрование секретов в базе») |
| Перец кодов регистрации | `agent.enroll.code_pepper` | секрет-менеджер, общий | смена обнуляет выданные коды (живут 15 мин) — менять в тихое окно |
| Устаревший ключ Этапа 3 | `superadmin.secret_key` | — | только расшифровка; после `reencrypt-secrets` убрать |
| Корень CA агентов | — (на серверах **нет**) | офлайн (HSM/сейф), ключ не на серверах | 10 лет; смена корня = переустановка агентов (корень вшит в установщик) |
| Выпускающий CA агентов | `agent.pki.ca_cert_file` (+ цепочка до корня), `ca_key_file`; прежний — `agent.pki.extra_trust_files` | секрет (файл), только инстансы | 5 лет (менять за ≥ 60 дней до конца); без обрыва ПК — «Ротация CA агентов» ниже |
| Серверный сертификат gRPC | `agent.pki.server_cert_file`, `server_key_file` | секрет (файл) | ≤ 397 дней, подписывает корень (офлайн-церемония); rolling restart |
| TLS веба | балансировщик | секрет балансировщика | ACME, автоматически |
| Ключ подписи обновлений агента (Ed25519) | серверу — только открытый `agent.update.public_keys` | закрытый — только в секретах CI (`agent-release`) | новый key_id добавить в `public_keys` до выпуска, старый убрать после |

### Ротация CA агентов

Агент закрепляет **корень** CA (`RootCaPem`/`RootCaFile` в настройках агента, `docs/agent-protocol.md` §12), а не
выпускающий CA: цепочку до корня он получает в `ca_chain_pem` ответа Enroll/RenewCertificate и проверяет по
закреплённому корню. Поэтому смена выпускающего CA под тем же корнем не требует ничего от агентов.
Сервер доверяет клиентским сертификатам по выпускающим CA (не по корню): текущий + `agent.pki.extra_trust_files`.

1. [ ] Офлайн-церемония с ключом корня: новый выпускающий CA (ECDSA P-256, `CA:TRUE, pathlen:0`, EKU clientAuth,
       5 лет) — сертификат + цепочка до корня (тот же формат, что текущий `ca_cert_file`) и ключ.
2. [ ] Секреты инстансов: новый CA → `agent.pki.ca_cert_file`/`ca_key_file`; прежний файл сертификата (без ключа) →
       `agent.pki.extra_trust_files` (`ZAFIRO_AGENT_PKI_EXTRA_TRUST_FILES`, список через запятую). Ключ прежнего CA
       серверам больше не нужен — убрать из секретов.
3. [ ] Rolling restart. В журнале старта — `agent PKI: previous issuing CA is still trusted (rotation)`. Новые
       регистрации и продления выдаёт новый CA; ПК с сертификатами прежнего подключаются как раньше и на продлении
       (в первой трети срока до конца, `renew_after`) получают сертификат нового CA и его цепочку.
4. [ ] Через `agent.cert.lifetime` (30 дней) + запас на выключенные ПК: проверить, что сертификатов прежнего CA не
       осталось (`SELECT count(*) FROM agent_certificates WHERE revoked_at IS NULL AND not_after > now() AND
       issued_at < <дата шага 3>` — 0; бездисковые живут 24 ч). ПК, выключенный дольше срока сертификата, всё равно
       регистрируется заново (сертификат истёк) — это не связано с ротацией.
5. [ ] Убрать прежний CA из `extra_trust_files` → rolling restart.

Смена **корня** (10 лет): агент принимает несколько корней (PEM-список в `RootCaPem`/`RootCaFile`; склейка файлов
без перевода строки между ними тоже читается). Сначала новый
корень добавляется агентам вторым в список (подписанное обновление агента/установщик), затем выпускающий CA и
серверный сертификат gRPC переводятся на новый корень (как выше), затем старый корень убирается из агентов.
Проверено тестами: `server/internal/pki` `TestRotateIssuingCA`, `server/internal/app` `TestAgentCARotation`
(интеграционный: ПК со старым сертификатом подключается и продлевается на новый CA; без `extra_trust_files` — отказ),
агент `CredentialTests` (несколько закреплённых корней).

Staging: `deploy/staging/init.sh` генерирует всё это локально (`deploy/staging/secrets/`, `.env.staging` — вне git).

### Аварийный сброс второго фактора суперадмина

Вход суперадмина — пароль и код TOTP (Этап 7, `docs/open-questions/stage7-totp.md`). Обычный путь, если суперадмин
потерял телефон и коды восстановления: другой суперадмин в кабинете — «Пользователи» → «Сбросить 2FA» (с
подтверждением; не из режима просмотра клуба). Команда ниже — когда войти некому: суперадмин один, или потерян ключ
данных `security.data_keys` (ключи TOTP не расшифровываются — вход отвечает «обратитесь к другому суперадмину»).

1. [ ] На машине с конфигурацией сервера (тот же `ZAFIRO_CONFIG_FILE`/`ZAFIRO_*`: нужны `database.platform_url`,
       `valkey.*`, `auth.signing_keys`) тем же образом:
       ```bash
       docker compose exec app1 /usr/local/bin/zafiro superadmin-totp-reset -phone +77010000001          # compose (staging: app1)
       kubectl exec deploy/zafiro -- /usr/local/bin/zafiro superadmin-totp-reset -phone +77010000001    # k8s
       ```
       (бинарник: `zafiro [-config файл] superadmin-totp-reset -phone …`; телефон — `+7` и 10 цифр, скобки и пробелы
       допускаются).
2. [ ] Команда удаляет ключ TOTP и коды восстановления, завершает сеансы пользователя, пишет `superadmin.totp_reset`
       в журнал платформы (от имени платформы, `details.via = superadmin-totp-reset`) и `audit_log`
       (`actor_kind = system`). Ответ: «Второй фактор пользователя … сброшен» или «не настроен — менять нечего».
3. [ ] Суперадмин входит паролем и сразу настраивает TOTP заново (новый ключ и новые коды восстановления).
4. [ ] Проверить журнал платформы: неожиданные `superadmin.totp_failed` / `superadmin.totp_reset` — повод сменить
       пароль суперадмина.

После выкладки Этапа 7 все сессии суперадминов, открытые до неё, недействительны: каждый суперадмин входит заново и
проходит настройку (ключ — в приложение-аутентификатор, 10 кодов восстановления — в надёжное место).

## 6. Роли Postgres

| Кто | Роль | Права |
|---|---|---|
| job миграций, `migrate-from-mysql` | владелец схемы (`zafiro_owner`): `LOGIN NOSUPERUSER CREATEROLE NOBYPASSRLS`, владелец базы и схемы `public`, член `zafiro_platform` (PD-9) | DDL; data-миграции — `SET ROLE zafiro_platform` |
| инстансы, запросы клуба | логин ∈ `zafiro_app` | RLS по `app.club_id` |
| инстансы, фоновые задачи и суперадминка | логин ∈ `zafiro_platform` | все клубы |

Сервер при старте отказывается работать под суперпользователем или ролью с `BYPASSRLS`. Логины и владельца создаёт
инфраструктура (staging — `db/init/10-zafiro-logins.sh` и `deploy/staging/postgres-init/20-zafiro-owner.sh`).

## 7. Выкладка новой версии

Порядок: **миграции → новые инстансы → остановка старых**.

1. Образ из CI (тег `vX.Y.Z`, ссылка с digest — `ghcr.io/aitmaganbetov/zafirogo:vX.Y.Z@sha256:…`; подпись cosign —
   §10).
2. **Job миграций** тем же образом: `zafiro-migrate up` (обёртка над `goose up`, модуль `deploy/ops`) под владельцем
   схемы (staging: `task staging:migrate`). Миграции обязаны быть совместимы со **старой** версией приложения, которая
   ещё работает (expand → contract): новые колонки — с умолчанием или NULL; удаление/переименование — отдельным
   выпуском, когда старый код уже не работает. Что добавляет обёртка (аудит OPS-7, DB-1, OPS-20, OPS-14):
   - **advisory-блокировка** на время работы goose (ключ — тот же, что у `goose.Provider`): второй job (повтор хука
     helm, ручной запуск во время выкладки) ждёт первого до `ZAFIRO_MIGRATE_LOCK_WAIT` (10 мин), а не применяет те же
     миграции параллельно (голый goose CLI блокировки не берёт — один из двух падает посреди применения);
   - **`lock_timeout`** (`ZAFIRO_MIGRATE_LOCK_TIMEOUT`, 5 с) и **`statement_timeout`** (`ZAFIRO_MIGRATE_STATEMENT_TIMEOUT`,
     30 мин; 0 — без предела) через `PGOPTIONS`: DDL не встаёт в очередь за долгой транзакцией (отчёт, перенос клуба),
     держа за собой все запросы к таблице. Замер (`deploy/ops/test-migrate.sh`): транзакция держит таблицу 6 с, голый
     goose — обычный `SELECT` к таблице ждал **4,1 с**; с обёрткой — **0,16 с**, миграция откатилась (55P03) и прошла
     повтором после конца транзакции;
   - **повтор при 55P03** — до `ZAFIRO_MIGRATE_RETRIES` (5) раз через `ZAFIRO_MIGRATE_RETRY_DELAY` (15 с): миграция в
     транзакции откатывается целиком, повтор безопасен (файл `NO TRANSACTION` — только идемпотентные команды,
     `docs/schema-changes.md` §19.1). Не прошла за все повторы — job падает с кодом goose, выкладка останавливается до
     новых инстансов; смотреть `pg_stat_activity` (кто держит таблицу) и повторить в тихое время;
   - **DSN из файла** `GOOSE_DBSTRING_FILE` (секрет), а не из переменной контейнера; `GOOSE_DBSTRING` — запасной вариант.

   Известная долгая миграция (перестройка таблицы, VALIDATE на больших данных) — поднять
   `ZAFIRO_MIGRATE_STATEMENT_TIMEOUT` на время этого job, не убирая `lock_timeout`.
3. **Новые инстансы** по одному: старт → `/readyz` 200 → в балансировщик.
4. **Старые инстансы** по одному: SIGTERM → `/readyz` 503 (балансировщик выводит), HTTP дорабатывает запросы, агентам
   уходит `Drain` (переподключение в окне `agent.stream.drain_within` 10 с с jitter — на оставшиеся инстансы),
   воркеры останавливаются, процесс выходит с кодом 0 в пределах `shutdown_timeout` (30 с). Оркестратор ждёт дольше
   (compose `stop_grace_period: 45s`, k8s `terminationGracePeriodSeconds: 45`).
5. Фоновые задачи с лидерством (outbox, партиции, вебхуки, sweeper) переходят на другой инстанс сами (advisory lock /
   лок в Valkey) — дважды не выполняются.

Проверено на staging (`task staging:smoke`): остановка инстанса, держащего поток агента, — агент получает `Drain`,
переподключается ко второму инстансу через балансировщик, повтор покупки с тем же ключом идемпотентности возвращает
прежний результат, баланс списан ровно один раз; `docker compose stop` завершается за ≈ 0,5–1 с с кодом 0.

## 8. Откат

- **Приложение** — предыдущий образ (тег/digest) тем же порядком, без job миграций. Возможен, пока последняя миграция
  совместима со старым кодом (правило expand/contract выше).
- **Миграции** вперёд-only: `goose down` в проде не используется (данные денег не откатываются). Ошибочная миграция —
  исправляется новой миграцией. Перед миграцией, меняющей данные, — точка восстановления (O2, `deploy/backup`).
- **Valkey** потеря не теряет денег: сессии сотрудников (повторный вход), лимиты, присутствие ПК (сверка sweeper
  раз в 5 мин, агенты переподключаются). AOF включён.
- **NATS** — события восстанавливаются из outbox (`published_at IS NULL`), команды — из `agent_commands`.
- **Клуб после go-live** — вручную (README переноса, п. 10).

## 9. Staging

```bash
task staging:image   # образ zafiro:staging (или STG_IMAGE=ghcr.io/…:тег@sha256:… в deploy/staging/.env.staging)
task staging:up      # init (пароли, PKI, TLS) → Postgres/Valkey/NATS → job миграций → app1, app2 → nginx; ждёт /readyz
task staging:seed    # демо-данные (tools/visual/fixtures/demo.json), подписки активны
task staging:smoke   # проверки (ниже)
task staging:down    # graceful-остановка (тома сохраняются); staging:destroy — с томами
```

Состав (`deploy/staging/compose.yaml`): `postgres` 18.6 (+ init-скрипты логинов и владельца), `valkey` 9.1.3 (AOF,
пароль), `nats` 2.15.2 (JetStream, одиночный, пользователь/пароль), `migrate` (job), `app1`/`app2` (read-only FS,
`cap_drop: ALL`, `no-new-privileges`, секреты файлами), `legacy` (заглушка Laravel), `lb` (nginx, фиксированный адрес
`172.29.66.10` — единственный доверенный прокси). Домены `*.staging.zafiro.test` — `--resolve`/hosts на адрес
балансировщика, доверить `deploy/staging/secrets/web/ca.pem`.

`task staging:smoke` (`server/internal/app/staging_test.go`, тег `staging`) проверяет:

- [x] `/readyz` обоих инстансов из образа; миграции применены job'ом (код выхода 0);
- [x] вход в панель через nginx (`https://app.staging.zafiro.test/api/auth/login`, `/api/auth/me`);
- [x] WebSocket через nginx и напрямую на каждый инстанс: задача создана на `app2` — `todo.changed` пришёл на сокет
      `app1` и на сокет через nginx (outbox → NATS → инстанс сокета);
- [x] агент: код регистрации через REST → `Enroll` через L4-проброс (TLS до zafiro) → `Connect` по mTLS → `Welcome`;
      адрес ПК в журнале — из PROXY protocol;
- [x] остановка инстанса-владельца потока: `Drain` → переподключение ко второму → повтор покупки с тем же ключом
      идемпотентности не задваивает списание, прежняя покупка не теряется; graceful-выход с кодом 0.

Ручные проверки маршрутизации (`curl --resolve …`): основной домен → Laravel; `/api/auth/me` основного домена →
zafirogo (401); `/api/v1` с префиксом из карты → zafirogo, без — Laravel; неизвестный хост — отказ TLS; `/metrics`,
`/healthz`, `/readyz` снаружи — 404 (проверки — напрямую на :8080 инстанса); HTTP → 301 на HTTPS. Автоматически —
`deploy/staging/nginx-test.sh` (CI `deploy.yml`).

## 10. CI: образ (`.github/workflows/release.yml`)

- **PR** (изменения образа, сервера, фронта, миграций): сборка (BuildKit, кэш `gha`) → смоук (`-version`, goose,
  `/healthz` в режиме без зависимостей, пользователь 65532) → **trivy 0.76.0** (`--severity HIGH,CRITICAL
  --exit-code 1`, уязвимости и секреты; исключения — `deploy/trivy-ignore.yaml` с обоснованием и `expired_at`) →
  SBOM CycloneDX (артефакт, 90 дней). Ничего не публикуется; `permissions: contents: read`.
- **Тег `v*`**: то же + job `publish` (окружение `release`, единственное место с `packages: write`): в GHCR уходит
  **ровно просканированный** образ (`docker save` → artifact → `docker load` → `push`), тег = версия, ссылка с digest —
  в summary.
- **Подпись cosign (keyless, по желанию)**: переменная репозитория `COSIGN_SIGN=true` → `cosign sign` (OIDC GitHub →
  Fulcio, запись в Rekor; `id-token: write` только в `publish`) и `cosign attest --type cyclonedx` (SBOM к digest).
  Проверка перед выкладкой:
  `cosign verify ghcr.io/aitmaganbetov/zafirogo@sha256:… --certificate-identity-regexp '^https://github.com/aitmaganbetov/zafirogo/.github/workflows/release.yml@refs/tags/v' --certificate-oidc-issuer https://token.actions.githubusercontent.com`.
  В k8s — политика admission (Kyverno/Sigstore policy-controller) на те же identity/issuer.
- Dependabot (`.github/dependabot.yml`, cooldown 14 дней, PD-4): `docker` — `Dockerfile` и `deploy/backup/Dockerfile`;
  `docker-compose` — все compose-файлы (корень, `deploy/staging`, `deploy/backup`, `deploy/observability`,
  `tools/visual`, `tools/loadgen`, `agent/tests/integration`); `github-actions`; `dotnet-sdk` — `agent/global.json`.
- **Действия GitHub закреплены SHA коммита** с комментарием версии (`uses: owner/action@<40 символов> # vX.Y.Z`,
  аудит A-03) во всех workflow — тег действия можно передвинуть, а задачи выпуска держат ключи подписи. Dependabot
  обновляет SHA и комментарий вместе. Версия — последняя в мажорной ветке, выпущенная ≥ 14 дней назад.
- **Версии инструментов** (goose, sqlc, buf, oapi-codegen, линтеры, task, Node.js для CI) — один файл
  `tools/versions.env`: Taskfile (dotenv), workflow (шаг «versions» → `$GITHUB_ENV`), Dockerfile (стадия `go` читает
  `GOOSE_VERSION`). `tools/check-versions.sh` (CI, задача `lint` в `go.yml`) ловит дубли и расхождение с образом node.
  Раннеры — конкретные образы (`ubuntu-24.04`, `windows-2025`), не `*-latest`.

- **Другие проверки CI** (аудит OPS-17): у каждой задачи — `timeout-minutes`; `deploy.yml` — `nginx-test.sh`,
  `docker compose config -q` всех compose-файлов и их сочетаний, `deploy/observability/check.sh` (promtool/amtool,
  тесты алертов, в т.ч. бэкапа), `actionlint` с shellcheck (`ACTIONLINT_VERSION`), `deploy/ops` (тесты и
  `test-migrate.sh` на Postgres); `go.yml` — одна версия Go в `go.mod`, `tools/versions.env` и обоих Dockerfile;
  `image-scan.yml` — раз в неделю trivy последнего выпущенного тега `v*` из GHCR (новые CVE в работающей версии;
  красный прогон — повод для патч-выпуска).

### 10.1. Секреты окружений GitHub и защита выпуска (аудит OPS-10)

Самые ценные секреты проекта лежат в окружениях GitHub: `agent-release` — ключ Ed25519 подписи обновлений агента
(подпись принимают **все** ПК всех клубов) и сертификат Authenticode; `backup-drill` — **закрытый** ключ OpenPGP всех
копий базы и ключи S3 на чтение (self-hosted раннер в сети хранилища); `release` — публикация образа в GHCR.
`workflow_dispatch` можно запустить с любой ветки, и выполняется код этой ветки — поэтому защита двухслойная.

**В коде (сделано):**

| Workflow | Что проверяется |
|---|---|
| `agent-release.yml` | сборка и тесты — job `build` **без окружения** (restore NuGet/npm не видит ключей); job `guard`: только `refs/heads/main` или тег `agent-v*` на коммите из `main` (`git merge-base --is-ancestor`), не из форка; `package` (Authenticode, установщик) и `sign` (Ed25519, выпуск) — отдельные чистые раннеры с окружением `agent-release`, `if:` по ref и повторная проверка ref первым шагом |
| `release.yml` | job `guard`: тег `v*` на коммите из `main`, не из форка; `publish` (окружение `release`, `packages: write`) — только после `build` и `guard` |
| `backup-drill.yml` | `drill-prod` (окружение `backup-drill`, self-hosted) — только `github.ref == refs/heads/main`, не на PR, не из форка; ref ещё раз проверяется первым шагом |
| все | `permissions: contents: read` по умолчанию, права выше — только в job, которой нужны (`contents: write` — создание выпуска агента, `packages: write`/`id-token: write` — публикация образа); действия закреплены SHA; `persist-credentials: false` |
| `CODEOWNERS` | владелец — обязательный ревьюер `.github/`, `deploy/`, `Dockerfile`, `server/cmd/sign-update/`, `agent/installer/`, `db/migrations/` |

**Настройки GitHub — обязательные шаги владельца** (кодом не задаются; проверить при каждом выпуске):

1. *Settings → Environments → `agent-release`*: **Required reviewers** — владелец (и/или второй ответственный),
   «Prevent self-review»; **Deployment branches and tags** — «Selected»: ветка `main` и теги `agent-v*`. Секреты
   `AGENT_UPDATE_SIGNING_KEY`, `AGENT_CODESIGN_PFX`, `AGENT_CODESIGN_PASSWORD` — **только** в этом окружении, не в
   секретах репозитория.
2. *Environments → `release`*: required reviewers — владелец; deployment tags — `v*`.
3. *Environments → `backup-drill`*: required reviewers (или, для еженедельного расписания без человека, — только ветка
   `main` и «Wait timer» 0, но тогда пункты 4–5 обязательны); deployment branches — только `main`. Секреты
   `BACKUP_DRILL_*` — только здесь.
4. *Settings → Branches → `main`* (ruleset): запрет force-push и удаления, PR с одобрением, «Require review from Code
   Owners», обязательные проверки `go`, `web`, `deploy`; *Rules → Tags*: создавать `v*` и `agent-v*` может только
   владелец (ruleset на теги), удалять/перемещать — никто.
5. *Settings → Actions → Runners*: self-hosted раннер `zafiro-backup-drill` — в отдельной **группе раннеров**, доступной
   только репозиторию zafirogo и только workflow `backup-drill.yml` («Selected workflows»); раннер не обслуживает PR.
6. *Settings → Actions → General*: «Fork pull request workflows» — требовать одобрения для всех внешних участников;
   `GITHUB_TOKEN` по умолчанию — read-only.
7. Ключ подписи обновлений агента — со временем перенести в HSM/KMS (подпись через API облачного KMS, ключ не
   покидает HSM), секрет в окружении — только учётные данные с правом «sign» (решение владельца, вне аудита).

## 11. Масштабирование

- **Инстансы** — горизонтально, без состояния. Агенты: для планирования — **≈ 190–260 КБ RSS на поток** с mTLS (замер
  аудита на 1 000 ПК, §12 «Память на поток»; ранние 90 КБ из §13 `docs/agent-protocol.md` — нижняя граница при большой
  доле простаивающих потоков), 10–25 тыс. потоков на инстанс при `GOMEMLIMIT`. Балансировщик агентов — `least_conn` (выравнивает только новые соединения); после
  добавления или возврата инстанса потоки выравниваются **сами**: перегруженный инстанс понемногу просит простаивающие
  ПК переподключиться (`agent.stream.rebalance_*`, по умолчанию до 100 ПК за 15 с с инстанса; ПК с гостем и сессией
  не трогаются) — `docs/agent-protocol.md` §13.6. Ручной `Drain`/перезапуск для этого не нужен; ход —
  `zafiro_agent_stream_rebalance_requested_total` и «Потоки по инстансам» на дашборде «Агенты».
- **Балансировщик** — 2 дескриптора на соединение агента (`worker_connections`, `LimitNOFILE`/`ulimits` ≥ 2 × ПК на
  узел); прод — пара узлов с VRRP/облачный NLB с PROXY protocol v2.
- **NATS** — прод: кластер из 3 узлов JetStream, `events.stream.replicas=3` (поток `zafiro-events`), отдельные
  пользователи для приложения; потеря кворума не теряет событий (outbox). Диск JetStream на каждом узле — **не меньше
  `events.stream.max_bytes` (по умолчанию 0 — без предела, как до аудита; задать явно по расчёту до выкладки) с запасом**, в сумме по кластеру ≥ `max_bytes × replicas`: JetStream
  резервирует `max_bytes` при создании потока, на меньшем диске поток не создастся. Пропавший поток (пустой NATS,
  потеря хранилища) инстансы создают заново сами, без перезапуска.
- **Valkey** — основной + реплика (Sentinel) или управляемый сервис; `noeviction` (лимиты и сессии нельзя вытеснять).
- **Postgres** — основной + синхронная/асинхронная реплика (O2); `max_connections` ≥ инстансы × (пул app + пул
  platform + пул лидерства `database.lock_pool_max_conns`) + запас (§12 «Пулы Postgres»).

## 12. Ресурсы

По нагрузочному тесту O4 (`docs/load-test.md`: 5 000 ПК с mTLS в 50 клубах через nginx, гости, админка, фоновые
задачи; staging на одной машине 4 vCPU / 16 ГБ) и замерам `docs/agent-protocol.md` §13. «Ядро» — 100 % одного vCPU.

| Что | 5 000 ПК (замер) | 50 000 ПК (оценка ×10, линейно — §6 отчёта) |
|---|---|---|
| Денежных операций в обычный час / в пиковую минуту | 6,5/с / 65/с | ≈ 65/с / ≈ 650/с |
| Инстанс zafiro: RSS | ≈ 0,47 ГБ на 2 500 потоков; ≈ 0,8 ГБ, когда один держит все 5 000 | ≈ 190–260 КБ на поток + ≈ 0,1 ГБ (см. «Память на поток» ниже) |
| Инстанс zafiro: CPU (все инстансы вместе) | 0,4 ядра обычно, 0,9 в пик; один инстанс на 5 000 — до 1,1 | ≈ 4 ядра обычно, ≈ 9–10 в пик |
| Postgres | 0,15 ядра, 160 транз./с, WAL ≈ 0,24 МБ/с (0,85 ГБ/ч); пик — 0,5 ядра, 540 транз./с, 1,1 МБ/с | ≈ 1,5 ядра / 1 600 транз./с, пик ≈ 5 ядер / 5 400; WAL ≈ 2,4 МБ/с (≈ 8,5 ГБ/ч), пик ≈ 11 МБ/с |
| Valkey | 0,05 ядра, 1 500 команд/с (пик 2 900), 15–20 МБ | ≈ 15 000 команд/с (пик ≈ 30 000), ≈ 0,2 ГБ |
| NATS | 0,03 ядра, 40/85 сообщ./с (вход/выход), 50 МБ | ≈ 400/850 сообщ./с, пик ≈ 3 000/6 500 |
| Балансировщик (nginx) | 0,04 ядра, 0,2 ГБ | ≈ 0,4 ядра; дескрипторы ≥ 2 × ПК на узел |

| Масштаб | Инстансы zafiro | Postgres | Valkey | NATS | Балансировщик |
|---|---|---|---|---|---|
| 5 000 ПК (сейчас) | 2 × (2 vCPU, 1,5 ГБ) — каждый выдерживает все 5 000 при потере второго | 2 vCPU, 4 ГБ, SSD 50 ГБ (+ архив WAL ≈ 20 ГБ/сутки до сжатия) | 1 vCPU, 1 ГБ | 1 узел (staging) / 3 × (0,5 vCPU, 512 МБ) (прод) | 1 vCPU, 512 МБ, `nofile` ≥ 20 000 |
| 50 000 ПК (цель) | 4 × (4 vCPU, **6 ГБ**, `GOMEMLIMIT` ≈ 5,4 ГиБ): пик ≈ 10 ядер держат 3 оставшихся при потере одного; RSS ≈ 2,4–3,3 ГБ на инстанс (12,5 тыс. потоков), ≈ 3,2–4,3 ГБ после перераспределения (16,7 тыс.) | 8 vCPU, 32 ГБ, NVMe, реплика; параметры — таблица ниже | 2 vCPU, 2 ГБ, реплика | 3 × (1 vCPU, 1 ГБ) | 2 × (2 vCPU, 2 ГБ), `nofile` ≥ 131 072 |

**Память на поток агента (аудит PERF-4).** Ранняя оценка ≈ 92–97 КБ на поток (5 000 ПК без гостей,
`docs/load-test.md` §3.3) занижена для планирования: прогон аудита на 1 000 ПК — инстанс с 1 потоком 71–105 МБ, с
500 — 197–205 МБ; после `kill` второго инстанса 147 потоков — 107 МБ, 853 — 239 МБ (**наклон ≈ 187 КБ/поток**,
горутин ≈ 5,3 на поток, утечки нет — 2 638 ± 1 за 8 мин). С учётом кучи между сборками мусора считать
**190–260 КБ на поток**. Следствия: 50 000 ПК на 4 инстансах — 2,4–3,3 ГБ на инстанс, после потери одного — до 4,3 ГБ;
заложенных 4 ГБ мало → 6 ГБ на инстанс и `GOMEMLIMIT` ≈ 90 % лимита контейнера (staging: `STG_APP_GOMEMLIMIT`,
лимит `STG_APP_MEMORY`). До выхода на 50 000 ПК — перемерить на 5 000 ПК (`GOMEMLIMIT`, `GODEBUG=madvdontneed=1`,
heap-профиль на поток, `docs/load-test.md` §7) и уточнить эту таблицу.

**Лимиты контейнеров (аудит OPS-4).** В compose staging, бэкапа и наблюдаемости у каждого сервиса — лимит памяти,
CPU и числа процессов (`deploy.resources.limits`) и журнал `json-file` с ротацией (50 МБ × 5; наблюдаемость — 20 МБ × 3).
Значения staging (переопределяются в `.env.staging`): инстанс 4 vCPU / 2 ГБ / `GOMEMLIMIT` 1800 МиБ, Postgres 2 vCPU /
4 ГБ, Valkey 1 vCPU / 1 ГБ с `maxmemory 768mb` (`noeviction` без `maxmemory` не действует: память росла бы без предела;
у предела запись отклоняется — `OOM command not allowed`, сессии и лимиты не вытесняются), NATS 1 vCPU / 512 МБ,
nginx 1 vCPU / 512 МБ, Prometheus 2 ГБ. CPU инстанса — с запасом на волну переподключений: после гибели соседа
один инстанс принимает половину ПК, а Go по лимиту cgroup берёт `GOMAXPROCS` = лимит. Замер аудита (40 клубов × 25 ПК,
`kill` инстанса): при 2 vCPU переподключение ≈ 700 ПК p95 208–389 мс, при 4 vCPU — 37 мс; поэтому по умолчанию 4. Прод — по таблице выше, `maxmemory` Valkey ≈ 75 % его памяти, алерт на
память Valkey — вместе с экспортёром Valkey (пока нет).

**Параметры Postgres (аудит DB-4).** Кроме WAL (O2) настраиваются явно — умолчания Postgres рассчитаны на 128 МБ
памяти и вращающиеся диски (`work_mem` 4 МБ: полная сверка ledger на 1 млн проводок сортировала на диск —
`external merge Disk: 33 МБ`, 882 мс; `ListMemberBalanceDrift` — 3 × external merge по 5,7 МБ, 326 мс).

| Параметр | staging (4 ГБ) | 5 000 ПК (4 ГБ) | 50 000 ПК (8 vCPU, 32 ГБ, NVMe) | Зачем |
|---|---|---|---|---|
| `shared_buffers` | 1GB | 1GB | 8GB | ≈ 25 % памяти |
| `effective_cache_size` | 3GB | 3GB | 24GB | ≈ 75 % памяти: планировщик знает о кэше ОС |
| `work_mem` | 16MB | 16MB | 32–64MB | сортировки сверки и отчётов в памяти; × соединения × узлы плана — не раздувать |
| `maintenance_work_mem` | 256MB | 256MB | 1–2GB | VACUUM, CREATE INDEX CONCURRENTLY |
| `random_page_cost` / `effective_io_concurrency` | 1.1 / 200 | 1.1 / 200 | 1.1 / 200 | SSD/NVMe: индексный доступ не «в 4 раза дороже» |
| `max_wal_size` / `min_wal_size` | 4GB / 1GB | 4GB / 1GB | 8–16GB / 2GB | реже контрольные точки под нагрузкой |
| `wal_compression` | lz4 | lz4 | lz4 | WAL меньше в 2–3 раза (архив S3, реплика) |
| `checkpoint_completion_target` | 0.9 | 0.9 | 0.9 | равномерная запись контрольной точки |
| `autovacuum_vacuum_scale_factor` / `_analyze_` | 0.05 / 0.02 | 0.05 / 0.02 | 0.02 / 0.01 | большие таблицы (`sessions`, `member_balances`) чистятся до раздувания |
| `autovacuum_vacuum_cost_limit` | 2000 | 2000 | 2000–4000 | автовакуум успевает за записью на SSD |
| `log_lock_waits` / `log_min_duration_statement` | on / 1s | on / 1s | on / 500ms | ожидания блокировок и медленные запросы — в журнале |
| `idle_in_transaction_session_timeout` | — | на логинах app/platform: `ALTER ROLE … SET … = '60s'` | то же | брошенная транзакция не держит блокировки и не мешает VACUUM (AP-O7); **на логин владельца схемы и перенос — не ставить** |
| `max_connections` | 300 | ≈ 100–180 | ≈ 180–340 | пулы ниже (app + platform + lock, × 2 на время выкладки) |
| `tcp_keepalives_idle` / `_interval` / `_count`, `tcp_user_timeout` | 30 / 10 / 3, 60000 | то же | то же | обрыв сети до клиента виден за ≈ 1 мин (как у клиента pgx, §4) |

Staging задаёт их в `command` Postgres (`deploy/staging/compose.yaml`; тот же список — в `deploy/backup/compose.backup.yml`,
BK-7). `idle_in_transaction_session_timeout` в staging не включён: это изменение поведения логинов приложения — сначала
проверить, что ни один воркер не держит транзакцию открытой во время внешних вызовов (решение при подготовке прода).

**Пулы Postgres.** Размер по умолчанию — `max(16, 4 × CPU)` соединений на пул (`db.DefaultMaxConns`; умолчание pgx
`max(4, CPU)` давало 4 соединения на 4 vCPU — при 5 000 ПК на одном инстансе пул вставал, ответы 10–30 с, отчёт §4).
Явно — параметрами DSN: `database.url` `…&pool_max_conns=16`, `database.platform_url` `…&pool_max_conns=8`
(занято в пике тестов — 5 из 16). Третий пул — **лидерства** (`database.lock_pool_max_conns`, по умолчанию до 16):
сессионные advisory-блокировки постоянных воркеров держат по соединению на задачу, отдельно от пулов запросов (метрики —
`pool="leader"`). Бюджет на инстанс = app + platform + lock: при умолчаниях до 16 + 8 + 16 = **40**.
`max_connections` Postgres ≥ инстансы × (app + platform + lock) + job миграций и перенос клуба (≈ 5) + служебные
(бэкап, мониторинг, `superuser_reserved_connections`, ≈ 10): 5 000 ПК — 2 × 40 + 15 ≈ **100**; 50 000 ПК —
4 × 40 + 15 ≈ **180** (на время выкладки инстансов вдвое больше — старые и новые одновременно: закладывать
2 × инстансы, т.е. ≈ 180 и ≈ 340; PgBouncer не нужен до ≈ 10 инстансов). Staging: 4 × 40 + 15 < 300 ✓.

**Лимиты**, проверенные тестом: `grpcserver.DefaultLimits` (окно HTTP/2 64 КБ, буферы 16 КБ, 16 потоков на
соединение) — без ошибок; `agent.stream.drain_within` 10 с — 2 500 потоков ушли на второй инстанс без единого обрыва;
лимиты по IP (`customer-login` 300/мин, `agent-enroll` 300/мин) считаются на клуб (у клуба свой адрес) — в тесте не
срабатывали; лимит гостя (10 покупок/мин) при более частых покупках одного гостя даёт единичные `RATE_LIMITED` (ожидаемо).

**Восстановление** (замер, 5 000 ПК): плановая остановка инстанса — 0 ошибок, `docker stop` ≈ 11 с (окно Drain 10 с);
падение инстанса (`kill -9`) — все 5 000 ПК снова на связи через 7 с (full jitter агента 0–2 с на первую попытку),
денежные операции в эти секунды ждут переподключения и проходят повтором с тем же ключом (p99 ≈ 10 с), ошибок денег
нет. Инстанс после старта готов (`/readyz`) за ≈ 0,6 с. После перезапуска потоки сначала остаются на выжившем
инстансе (`least_conn` выравнивает только новые соединения), затем выравниваются автоматической перебалансировкой
(`agent.stream.rebalance_*`, `docs/agent-protocol.md` §13.6; 5 000 ПК на одном из двух — ≈ 6 мин при умолчаниях,
простаивающими ПК; на staging не перемерено).

## 13. Kubernetes (перенос с compose)

- `Deployment` zafiro (≥ 2 реплики, `topologySpreadConstraints` по узлам, `PodDisruptionBudget minAvailable: 1`):
  `readinessProbe` `/readyz`, `livenessProbe` `/healthz` (порт 8080), `terminationGracePeriodSeconds: 45`,
  `lifecycle.preStop.sleep.seconds: 5` (в distroless нет `sleep` — встроенное действие k8s ≥ 1.30: эндпоинт успевает
  уйти из Service до SIGTERM), `securityContext`: `runAsNonRoot`, `runAsUser: 65532`, `readOnlyRootFilesystem`,
  `allowPrivilegeEscalation: false`, `capabilities.drop: [ALL]`, `seccompProfile: RuntimeDefault`.
- `ZAFIRO_AGENT_INSTANCE_ID` — из `metadata.name` (Downward API).
- Секреты: `Secret` с `zafiro.yaml` → volume (`ZAFIRO_CONFIG_FILE`), PKI — volume с `defaultMode: 0400`
  (`fsGroup`/`runAsUser` 65532); не через env (env видны в `describe`).
- Job миграций: `Job` тем же образом (`command: ["/usr/local/bin/zafiro-migrate", "up"]`, §7), `parallelism: 1`,
  `completions: 1`, `backoffLimit: 2`, `activeDeadlineSeconds` ≥ `ZAFIRO_MIGRATE_STATEMENT_TIMEOUT` + ожидание
  блокировки; Secret с DSN владельца — volume (`GOOSE_DBSTRING_FILE`), не env; запуск — helm `pre-upgrade` hook или
  Argo CD sync wave до Deployment. Два Job одновременно не опасны (advisory-блокировка), но не нужны. Приложение DSN
  владельца не получает.
- Веб: Ingress (TLS, HSTS, `X-Forwarded-For` от контроллера; `http.trusted_proxies` — CIDR подов контроллера).
- Агенты: `Service type: LoadBalancer` на 8443 (TCP) с PROXY protocol (AWS NLB `proxy-protocol: "*"`, v2; либо
  ingress-nginx TCP services с `proxy-protocol` — v1), `externalTrafficPolicy: Local` не нужен (адрес — из заголовка);
  `agent.grpc.proxy_trusted` — подсети балансировщика.
- Карта `/api/v1` и маршрутизация основного домена — на внешнем nginx переходного периода (как в staging) или
  ingress-nginx `configuration-snippet`/отдельный nginx; после перевода всех клубов — не нужна.
- `NetworkPolicy`: в Postgres/Valkey/NATS — только поды zafiro и job миграций; `/metrics` — только Prometheus.
- Ресурсы: `requests`/`limits` по §12 и `GOMEMLIMIT` ≈ 90 % `limits.memory` (env из `resourceFieldRef` не даёт
  процентов — задать значением).
- `/healthz` и `/readyz` — только пробы kubelet на порт пода; через Ingress не публиковать (в staging закрыты на nginx —
  `location = /healthz|/readyz { return 404; }`, аудит OPS-13: `/readyz` раскрывает состав зависимостей и на каждый
  запрос ходит в Postgres/Valkey/NATS).

## 14. Что нужно от владельца до прода

Кодом в репозитории это не закрыть — нужны решения и доступы владельца (аудит 22.11.2026).

**OPS-5. Прод-окружение кодом (CD).** Сейчас кодом описан только staging (`deploy/staging`, одна машина, compose) и
публикация образа (`release.yml`); прод — текстом §7, §13. Ошибки ручной выкладки (забыть `ZAFIRO_ENV=prod`, лимиты,
порядок «миграции → новые → старые») ничем не ловятся. Нужно от владельца:

1. Выбор платформы: managed Kubernetes (helm-чарт или kustomize по §13) **или** compose на ВМ (прод-вариант
   `deploy/staging` + systemd/ansible). От этого зависит формат манифестов.
2. Где Postgres: управляемый сервис (бэкап и реплика — провайдера, `deploy/backup` не нужен) или своя ВМ с WAL-G
   (`deploy/backup`, `docs/runbooks/restore.md`). Аналогично Valkey и NATS.
3. Окружение GitHub `production` (required reviewers — владелец, ветка `main`/теги `v*`) и учётные данные выкладки
   (kubeconfig сервис-аккаунта с правами на один namespace или SSH-ключ деплой-пользователя) — в нём.
4. После выбора — задача CD: ручное одобрение → job миграций (`zafiro-migrate`) → rolling update → проверка `/readyz` →
   автоматический откат образа при провале; `helm lint`/`kubeconform` (или `docker compose config`) в CI.

**OPS-11. Откат клуба на Laravel после первой операции.** Возврат клуба на старый сервер возможен только до первой
операции в zafirogo (`tools/migrate-from-mysql/README.md` п. 10); обратного переноса Postgres → MySQL нет. Если на
первых клубах всплывёт блокирующий дефект, вернуть клуб без потери операций нельзя. Решение владельца (записать в
`docs/DECISIONS.md`):

- (а) **принять риск**: пилот на 1–2 клубах с усиленным мониторингом (дежурный на связи в первые смены, сверка X/Z и
  балансов после каждой смены), исправления — вперёд, без отката клуба; или
- (б) **инструмент обратного переноса денег**: остатки балансов клиентов (основной/бонусный), открытые смены и кассы,
  без истории операций, в MySQL — для окна N дней после go-live клуба; цена — отдельная разработка и тест на сверку
  до тиына.

**Ещё от владельца** (уже перечислено выше): настройки GitHub §10.1 (окружения, rulesets, группа раннеров); ключи S3 и
бакет с Object Lock для копий (`docs/runbooks/restore.md` §2); получатели алертов Alertmanager (`deploy/observability`).
