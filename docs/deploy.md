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
| `go` | `golang:1.27.3-alpine3.24` | `CGO_ENABLED=0 go build -trimpath -tags timetzdata -ldflags "-s -w -X main.buildVersion=…"` со вшитым фронтом (`internal/webui/dist`); `goose` v3.29.0 только с драйвером Postgres; `migrate-from-mysql` |
| итог | `gcr.io/distroless/static-debian13:nonroot` | без shell и пакетного менеджера, пользователь `65532:65532`, CA-сертификаты и tzdata из distroless (tzdata ещё и вшит в бинарники) |

Содержимое: `/usr/local/bin/zafiro` (ENTRYPOINT), `/usr/local/bin/goose`, `/usr/local/bin/migrate-from-mysql`,
`/migrations` (`db/migrations`, `GOOSE_MIGRATION_DIR`), `/seed` (`db/seed`). Переменные по умолчанию:
`ZAFIRO_ENV=prod`, `ZAFIRO_LOG_FORMAT=json`, `GOOSE_DRIVER=postgres`. Порты: 8080 (HTTP), 8443 (gRPC агентов).

`HEALTHCHECK` не задан (в distroless нечем выполнить проверку): оркестратор проверяет `/healthz` (жив ли процесс) и
`/readyz` (оба пула Postgres, Valkey, NATS; во время остановки — 503).

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
| `zafiro.kz` | старый Laravel, пока не переведены все клубы, кроме путей ниже | — |
| `zafiro.kz/api/(auth\|guest\|staff)/` | zafirogo; чужие токены и телефоны он сам проксирует в Laravel | `mobile.upstream`, `mobile.upstream_host` (`docs/mobile-compat.md` §3) |
| `zafiro.kz/api/v1/` | zafirogo; ключи, которых нет в его базе, он сам проксирует в Laravel | `integrations.legacy_upstream`, `integrations.legacy_upstream_host` (пусто — `mobile.upstream`, `mobile.upstream_host`; S6-O1-4) |
| `agent.zafiro.kz:8443` | zafirogo, gRPC агентов | `agent.grpc.endpoint` (уходит агенту в ответе Enroll) |

Go-live клуба не меняет балансировщик: `/api/(auth|guest|staff|v1)/` всегда идут в zafirogo, и он решает сам —
токен или ключ клуба есть в его базе (в том числе выпущенный уже в zafirogo) → ответ здесь, нет → обратный прокси
в Laravel без изменений (`server/internal/http/sections/v1/upstream.go`, `server/internal/http/legacy`). Свой ключ,
даже отозванный или с неверным секретом, обслуживается здесь (401, как ответил бы Laravel); лимит неудачных проверок
по IP для проксируемых ключей считает Laravel. Когда переведены все клубы — `mobile.upstream` и
`integrations.legacy_upstream` очистить (прокси выключится). Откат клуба до первой операции —
`tools/migrate-from-mysql/README.md`, п. 10.

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
- Внутренние соединения (Postgres, Valkey, NATS) в staging — без TLS внутри изолированной сети. Прод: Postgres —
  `sslmode=verify-full` в DSN, Valkey/NATS — TLS, если сеть общая с другими системами (вопрос S6-O1-6).

## 5. Секреты и ключи

Секреты не в образе, не в репозитории, не в переменных compose-файла. Сервер читает их из YAML-файла конфигурации
(`ZAFIRO_CONFIG_FILE`, смонтирован как секрет) или из переменных `ZAFIRO_*` (k8s Secret → env); PKI — файлы.
В логе конфигурация печатается с маской (`config.Secret`, у DSN виден только хост и база).

| Секрет | Ключ конфигурации | Где хранить | Ротация |
|---|---|---|---|
| Пароли логинов Postgres | `database.url`, `database.platform_url` | секрет-менеджер → файл конфигурации | новый пароль `ALTER ROLE … PASSWORD` → обновить секрет → rolling restart |
| Владелец схемы (миграции, перенос) | DSN job миграций (`GOOSE_DBSTRING`), `ZAFIRO_MIGRATE_PG_DSN` | секрет-менеджер, **только** в job миграций и у оператора переноса | как выше; приложению не выдаётся |
| Valkey, NATS | `valkey.password`, `nats.url` | секрет-менеджер | на сервере с двумя паролями (Valkey ACL, NATS users) → обновить секрет → restart → убрать старый |
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
2. **Job миграций** тем же образом: `goose up` под владельцем схемы (staging: `task staging:migrate`). Миграции
   обязаны быть совместимы со **старой** версией приложения, которая ещё работает (expand → contract): новые колонки
   — с умолчанием или NULL; удаление/переименование — отдельным выпуском, когда старый код уже не работает.
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
zafirogo (401); `/api/v1` с префиксом из карты → zafirogo, без — Laravel; неизвестный хост — отказ TLS; `/metrics`
снаружи — 404; HTTP → 301 на HTTPS.

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
- Dependabot: ecosystem `docker` для `Dockerfile` (cooldown 14 дней, PD-4).

## 11. Масштабирование

- **Инстансы** — горизонтально, без состояния. Агенты: ≈ 90 КБ RSS на поток с mTLS (§13 `docs/agent-protocol.md`),
  10–25 тыс. потоков на инстанс. Балансировщик агентов — `least_conn` (выравнивает только новые соединения); после
  добавления или возврата инстанса потоки выравниваются **сами**: перегруженный инстанс понемногу просит простаивающие
  ПК переподключиться (`agent.stream.rebalance_*`, по умолчанию до 100 ПК за 15 с с инстанса; ПК с гостем и сессией
  не трогаются) — `docs/agent-protocol.md` §13.6. Ручной `Drain`/перезапуск для этого не нужен; ход —
  `zafiro_agent_stream_rebalance_requested_total` и «Потоки по инстансам» на дашборде «Агенты».
- **Балансировщик** — 2 дескриптора на соединение агента (`worker_connections`, `LimitNOFILE`/`ulimits` ≥ 2 × ПК на
  узел); прод — пара узлов с VRRP/облачный NLB с PROXY protocol v2.
- **NATS** — прод: кластер из 3 узлов JetStream, `events.stream.replicas=3` (поток `zafiro-events`), отдельные
  пользователи для приложения; потеря кворума не теряет событий (outbox).
- **Valkey** — основной + реплика (Sentinel) или управляемый сервис; `noeviction` (лимиты и сессии нельзя вытеснять).
- **Postgres** — основной + синхронная/асинхронная реплика (O2); `max_connections` ≥ инстансы × (пул app + пул
  platform) + запас (пулы pgx по умолчанию — 4 × CPU на пул).

## 12. Ресурсы

По нагрузочному тесту O4 (`docs/load-test.md`: 5 000 ПК с mTLS в 50 клубах через nginx, гости, админка, фоновые
задачи; staging на одной машине 4 vCPU / 16 ГБ) и замерам `docs/agent-protocol.md` §13. «Ядро» — 100 % одного vCPU.

| Что | 5 000 ПК (замер) | 50 000 ПК (оценка ×10, линейно — §6 отчёта) |
|---|---|---|
| Денежных операций в обычный час / в пиковую минуту | 6,5/с / 65/с | ≈ 65/с / ≈ 650/с |
| Инстанс zafiro: RSS | ≈ 0,47 ГБ на 2 500 потоков; ≈ 0,8 ГБ, когда один держит все 5 000 | ≈ 92–97 КБ на поток + ≈ 0,1 ГБ |
| Инстанс zafiro: CPU (все инстансы вместе) | 0,4 ядра обычно, 0,9 в пик; один инстанс на 5 000 — до 1,1 | ≈ 4 ядра обычно, ≈ 9–10 в пик |
| Postgres | 0,15 ядра, 160 транз./с, WAL ≈ 0,24 МБ/с (0,85 ГБ/ч); пик — 0,5 ядра, 540 транз./с, 1,1 МБ/с | ≈ 1,5 ядра / 1 600 транз./с, пик ≈ 5 ядер / 5 400; WAL ≈ 2,4 МБ/с (≈ 8,5 ГБ/ч), пик ≈ 11 МБ/с |
| Valkey | 0,05 ядра, 1 500 команд/с (пик 2 900), 15–20 МБ | ≈ 15 000 команд/с (пик ≈ 30 000), ≈ 0,2 ГБ |
| NATS | 0,03 ядра, 40/85 сообщ./с (вход/выход), 50 МБ | ≈ 400/850 сообщ./с, пик ≈ 3 000/6 500 |
| Балансировщик (nginx) | 0,04 ядра, 0,2 ГБ | ≈ 0,4 ядра; дескрипторы ≥ 2 × ПК на узел |

| Масштаб | Инстансы zafiro | Postgres | Valkey | NATS | Балансировщик |
|---|---|---|---|---|---|
| 5 000 ПК (сейчас) | 2 × (2 vCPU, 1,5 ГБ) — каждый выдерживает все 5 000 при потере второго | 2 vCPU, 4 ГБ, SSD 50 ГБ (+ архив WAL ≈ 20 ГБ/сутки до сжатия) | 1 vCPU, 1 ГБ | 1 узел (staging) / 3 × (0,5 vCPU, 512 МБ) (прод) | 1 vCPU, 512 МБ, `nofile` ≥ 20 000 |
| 50 000 ПК (цель) | 4 × (4 vCPU, 4 ГБ): пик ≈ 10 ядер держат 3 оставшихся при потере одного; RSS ≈ 1,5 ГБ на инстанс (≈ 2 ГБ после перераспределения) | 8 vCPU, 32 ГБ, NVMe, реплика; `wal_compression = lz4`, `max_wal_size` ≥ 8 ГБ | 2 vCPU, 2 ГБ, реплика | 3 × (1 vCPU, 1 ГБ) | 2 × (2 vCPU, 2 ГБ), `nofile` ≥ 131 072 |

**Пулы Postgres.** Размер по умолчанию — `max(16, 4 × CPU)` соединений на пул (`db.DefaultMaxConns`; умолчание pgx
`max(4, CPU)` давало 4 соединения на 4 vCPU — при 5 000 ПК на одном инстансе пул вставал, ответы 10–30 с, отчёт §4).
Явно — параметрами DSN: `database.url` `…&pool_max_conns=16`, `database.platform_url` `…&pool_max_conns=8`
(занято в пике тестов — 5 из 16). `max_connections` Postgres ≥ инстансы × (app + platform) + job миграций + запас:
5 000 ПК — 2 × 32 + 20 ≈ 100; 50 000 ПК — 4 × 32 + 20 ≈ 150 (PgBouncer не нужен до ≈ 10 инстансов).

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
- Job миграций: `Job` тем же образом (`command: ["/usr/local/bin/goose", "up"]`), Secret с DSN владельца; запуск —
  helm `pre-upgrade` hook или Argo CD sync wave до Deployment. Приложение DSN владельца не получает.
- Веб: Ingress (TLS, HSTS, `X-Forwarded-For` от контроллера; `http.trusted_proxies` — CIDR подов контроллера).
- Агенты: `Service type: LoadBalancer` на 8443 (TCP) с PROXY protocol (AWS NLB `proxy-protocol: "*"`, v2; либо
  ingress-nginx TCP services с `proxy-protocol` — v1), `externalTrafficPolicy: Local` не нужен (адрес — из заголовка);
  `agent.grpc.proxy_trusted` — подсети балансировщика.
- Карта `/api/v1` и маршрутизация основного домена — на внешнем nginx переходного периода (как в staging) или
  ingress-nginx `configuration-snippet`/отдельный nginx; после перевода всех клубов — не нужна.
- `NetworkPolicy`: в Postgres/Valkey/NATS — только поды zafiro и job миграций; `/metrics` — только Prometheus.
