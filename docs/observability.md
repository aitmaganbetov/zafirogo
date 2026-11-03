# Наблюдаемость (Этап 6, O3)

Метрики Prometheus, структурные журналы, трассировка OpenTelemetry, алерты и дашборды для дежурных.
Код — `server/internal/observability`, развёртывание — `deploy/observability`, решения — `docs/open-questions/stage6-observability.md`.

Пакет `server/internal/monitoring` (экран «Сервер» суперадминки, перенос старого мониторинга, PD-10, алерты бота
Telegram Q-060) не меняется и работает параллельно: он пишет показатели в базу для суперадмина, этот стек — для эксплуатации.

## 1. Правило меток

В метках — только конечные наборы значений, заданные кодом: шаблон маршрута chi (`/api/cash/shifts/{id}`), метод gRPC,
имя запроса sqlc, вид операции ledger, имя фоновой задачи, код ответа, имя пула. **Никогда** — `club_id`, `user_id`,
номер ПК, телефон, путь с id, IP, суммы. Каждая метка проходит ограничитель (`labelGuard`): после предела различных
значений новые склеиваются в `other`. Проверки: `TestMetricLabelsHaveNoTenantCardinality` (regex по именам и значениям
меток) и интеграционный `TestObservabilityMetrics` (в выдаче `/metrics` нет id клуба, пользователя и ПК теста).

Показатели «по клубам» (загрузка, запросы клуба) остаются на экране суперадминки (`monitoring`), не в Prometheus.

## 2. Конфигурация

| Ключ (env `ZAFIRO_OBSERVABILITY_…`) | По умолчанию | Что делает |
|---|---|---|
| `observability.metrics_listen` | пусто (выключено) | Адрес внутреннего порта `/metrics` (`127.0.0.1:9090`; в сети контейнеров `:9090`). Отдельный HTTP-сервер, не публичный роутер: без аутентификации, только `GET /metrics`, ≤ 4 одновременных сбора. Наружу не публиковать. |
| `observability.otlp_endpoint` | пусто (выключено) | OTLP/HTTP коллектор трасс (`http://otel-collector:4318`). Без логина в URL. |
| `observability.trace_sample_ratio` | `0.1` | Доля трасс, которые начинает инстанс (0..1). |
| `observability.log_sample_initial` / `log_sample_thereafter` | `100` / `100` | Выборка шумных записей debug/info: за секунду по одному сообщению — первые 100, затем каждая 100-я. `0` — без выборки. warn/error не отбрасываются. |

Порт метрик открывается в `Build` (конфликт портов — ошибка запуска), обслуживается в `Run` и закрывается после
остановки воркеров — метрики видны во время graceful shutdown.

## 3. Метрики

Все имена — с префиксом `zafiro_`. Счётчики пишет код; показатели с пометкой «при сборе» читаются при каждом сборе
(запросы к базе и Valkey кэшируются на 10 с, таймаут 3 с; ошибка — ряд пропадает, растёт `observability_collect_errors_total{source}`).
Показатели «по всей платформе» каждый инстанс отдаёт одинаковыми — в правилах берётся `max`, не `sum`.

| Группа | Метрика | Метки | Откуда |
|---|---|---|---|
| HTTP | `http_requests_total`, `http_request_duration_seconds`, `http_requests_in_flight` | `method`, `route`, `code` | middleware `Observer.HTTPMiddleware` сразу после `middleware.RequestID`; `route` — шаблон chi, `spa` (страницы фронта), `unmatched_api` (неизвестный `/api`, старый REST мобильных до маршрутизации). WebSocket `/ws` — без гистограммы. |
| gRPC | `grpc_requests_total`, `grpc_request_duration_seconds` (unary), `grpc_streams_active`, `grpc_stream_messages_received_total` | `method`, `code` | внешние перехватчики (`grpcserver.Options.OuterUnary/OuterStream`) — до проверки сертификата: отказы UNAUTHENTICATED и паники видны |
| Агенты | `agent_streams_active` (инстанс, при сборе), `agent_online_computers` (платформа, при сборе: `ZCOUNT` zset присутствия), `agent_stream_rebalance_requested_total` (счётчик инстанса: ПК, которых попросили переподключиться ради перебалансировки) | — | `stream.Service.LocalStreams`, `stream.Service.RebalanceRequested`, presence |
| Автобиллинг | `agent_auto_billing_due_computers`, `agent_auto_billing_oldest_due_age_seconds` (платформа, при сборе) | — | zset `zafiro:guest:ab:due` (`guestapi.DefaultKeyPrefix`) (`guestapi.Server.AutoBillingBacklog`: `ZCOUNT` по сроку ≤ сейчас и самый ранний срок) — сколько ПК ждут пересмотра и насколько просрочен старейший срок |
| Команды агентам | `agent_commands_total` | `event` = issued / redelivered / accepted / completed / expired, `result` (для completed: succeeded / failed / duplicate) | трассировщик pgx по запросам `InsertAgentCommand`, `ListPendingAgentCommands`, `AcceptAgentCommand`, `CompleteAgentCommand`, `ExpireAgentCommands` |
| Outbox | `outbox_pending_events`, `outbox_oldest_unpublished_age_seconds` (при сборе), `outbox_publish_failures_total`, `nats_messages_published_total` | — | `outbox_events` (индекс неопубликованных), запросы `MarkOutboxAttemptFailed` / `MarkOutboxPublished` |
| Вебхуки | `webhook_deliveries_pending`, `webhook_deliveries_retrying`, `webhook_oldest_pending_age_seconds` (при сборе), `webhook_delivery_attempts_total` | `result` = delivered / retry / failed / skipped | `webhook_deliveries` (status=pending), запросы `SaveWebhookAttempt`, `SkipWebhookDelivery` |
| Воркеры | `worker_runs_total`, `worker_run_duration_seconds`, `worker_last_run_timestamp_seconds` | `job` (имя лока задачи: `club-computers:expire-sessions`, `agent:auto-billing`, …) | захват/снятие advisory lock `hashtext('job:…')` (worker/leader, Q-086), видимые трассировщику pgx |
| | `worker_errors_total` | `job` | записи журнала warn/error с атрибутом `job` или `worker`; завершение горутины с ошибкой |
| | `worker_up` | `worker` (имя горутины app: `outbox-publisher`, `webhook-deliverer`, …) | обёртка `TrackWorker` |
| | `worker_leader` (при сборе) | `job` | `pg_locks`: держит ли соединение этого инстанса лок задачи (лидер постоянного воркера — публикатора outbox) |
| Пулы Postgres | `db_pool_{acquired,idle,total,max}_connections`, `db_pool_acquire_waits_total`, `db_pool_acquire_wait_seconds_total`, `db_pool_acquire_canceled_total` (при сборе) | `pool` = app / platform | `pgxpool.Stat()` |
| Запросы Postgres | `db_queries_total`, `db_query_errors_total`, `db_query_duration_seconds` | `pool`, `query` (имя sqlc, `begin`/`commit`/`rollback`, `raw`), `sqlstate` | трассировщик pgx |
| Деньги | `ledger_operations_total` | `kind` (вид операции ledger, неизвестный — other), `result` = committed / rolled_back | `InsertLedgerTransaction` в транзакции + исход COMMIT/ROLLBACK этого соединения |
| | `ledger_errors_total` | `query`, `reason` (имя ограничения или sqlstate) | ошибки запросов `*Ledger*`. Нехватку средств ledger проверяет до записи (`checkBalances`) — здесь в норме 0 |
| Лимиты | `rate_limited_total` | `channel` = http / grpc, `route` | HTTP 429, gRPC RESOURCE_EXHAUSTED |
| Вход | `auth_logins_total` | `result` = succeeded / failed, `reason` (`auth.Reason*`) | обёртка `auth.Events` (вход в панель) |
| Valkey / NATS | `valkey_commands_total`, `valkey_errors_total{kind=server|timeout|network}`, `nats_errors_total{kind=async|disconnect|publish}`, `nats_connected`, `nats_reconnects_total`, `nats_messages_consumed_total{consumer}` | | обёртки клиента Valkey и JetStream, обработчики соединения NATS |
| Сертификаты | `tls_certificate_expiry_timestamp_seconds` | `cert` = agent_ca / agent_server | CA агентов и серверный TLS gRPC при запуске. HTTPS веба терминирует балансировщик (Q-071) — его срок проверяйте экспортёром на балансировщике (blackbox) |
| Журнал | `log_records_dropped_total{level}` | | выборка шумных записей |
| Процесс | `go_*`, `process_*`, `zafiro_instance_info{instance_id}` | | стандартные сборщики client_golang |

Денежные маршруты для `zafiro:http_money_latency_p95:5m`: изменяющие методы на `/api/cash/*`, `/api/billing/*`,
`/api/club-computers/*`, `/api/shop-orders/*`, `/api/customers/*`, `/api/v1/*`.

**Почему события предметной области считает трассировщик pgx.** Метрики ledger, команд, вебхуков, outbox и запусков
задач получены без правок этих пакетов: каждый запрос sqlc начинается с `-- name: Имя`, трассировщик видит имя,
аргументы и тег результата. Позиции аргументов проверяет `pgx_test.go` на настоящем коде `store` — переименование
запроса ломает тест, а не метрику молча. Текст SQL в метки не попадает.

## 4. Журналы и трассировка

- Формат — slog JSON (`log.format`). Обёртка `observability.NewLogHandler` (подключается в `app.Build`):
  - `request_id` (HTTP — из `X-Request-Id`/chi, gRPC — новый на вызов и поток) и `trace_id`/`span_id` в каждой записи,
    сделанной с контекстом (`InfoContext`, `LogAttrs(ctx, …)`); записи без контекста — без них;
  - маскирование: значения атрибутов с ключами `password`, `token`, `secret`, `authorization`, `cookie`, `pepper`,
    `dsn`, `pin`, `otp`, `api_key`, `private_key`, … и строки вида `Bearer …`, `Basic …`, `zfk_…`, PEM → `[REDACTED]`;
  - выборка шумных debug/info (раздел 2).
- Правило проекта не меняется: тела запросов, заголовки, query-строки не логируются (`RequestLogger` пишет путь без query).
  Тест `TestRequestLogsDoNotLeakSecrets` прогоняет запросы с Authorization, cookie, `?token=`, паролем в теле через настоящий роутер.
- Трассировка (OpenTelemetry SDK, OTLP/HTTP, выключена по умолчанию): спаны HTTP (имя — метод + шаблон маршрута),
  unary gRPC, транзакции Postgres (`db.tx`, для операций с ledger — `db.tx ledger` с видами операций) и запросы внутри
  записываемой трассы, публикация в JetStream (outbox) и обработка сообщений потребителями (вебхуки, WebSocket).
  Поток `Connect` агента спаном не покрывается (живёт часами). Входящие `traceparent` не принимаются (публичный край),
  в вебхуки не пропагируются. Коллектор (Tempo/Jaeger/OTel Collector) — вне этого репозитория; адрес — `observability.otlp_endpoint`.

## 5. Алерты и первые шаги

Правила — `deploy/observability/prometheus/rules/zafiro-alerts.rules.yml`, записи — `zafiro-records.rules.yml`,
модульные тесты — `prometheus/tests/alerts.test.yml`. Проверка: `deploy/observability/check.sh` (promtool и amtool в Docker).
`critical` — будит дежурного; `warning` — в рабочее время.

<a id="rb-instance-down"></a>
### ZafiroInstanceDown (critical)
Инстанс 2 минуты не отдаёт метрики.
1. `docker compose ps` / systemd — процесс жив? Последние записи журнала (`level=ERROR`, `worker failed`, `shutdown timeout`).
2. `/readyz` инстанса: какая проверка не проходит (postgres, valkey, nats).
3. Агенты переподключаются к остальным инстансам сами; проверьте `zafiro:agent_online_computers` и нагрузку оставшихся.

<a id="rb-high-5xx"></a>
### ZafiroHigh5xxRate (critical), ZafiroGRPCServerErrors (warning)
Больше 5% ответов — ошибка сервера.
1. Дашборд «обзор» → «Маршруты с 5xx»: один маршрут или все.
2. Все — зависимости: `/readyz`, `ZafiroDBPool*`, `ZafiroNATSDisconnected`, ошибки Valkey. Один — журнал по `route`/`request_id`, трасса по `trace_id`.
3. После выкладки — откат образа на предыдущий тег.

<a id="rb-money-latency"></a>
### ZafiroMoneyLatencyHigh, ZafiroMoneyLatencyRegression (warning)
p95 денежных эндпоинтов > 1 с или в 3 раза выше вчерашнего.
1. Дашборд «деньги» → p95 по маршрутам. Пулы БД (ожидание соединения), `db_query_duration_seconds`.
2. Блокировки в Postgres: `pg_stat_activity` (wait_event Lock), долгие транзакции. Денежные операции сериализуются по балансу клиента (FOR UPDATE).
3. Трассы с `db.tx ledger` — какой запрос медленный.

<a id="rb-agents-disconnect"></a>
### ZafiroAgentsMassDisconnect (critical), ZafiroAgentStreamsImbalance (warning)
Число ПК на связи упало больше чем на 20% за 5 минут (при ≥ 50 ПК).
1. Один инстанс или все: `zafiro_agent_streams_active` по инстансам. Упал инстанс — см. ZafiroInstanceDown.
2. Все: балансировщик L4 (порт агентов 8443, PROXY protocol), сертификат сервера (`ZafiroCertificate*`), сеть провайдера.
3. `grpc_requests_total{method=~".*/Connect", code=…}`: Unauthenticated — сертификаты ПК (отзыв, CA), Unavailable — сервер.
4. Дисбаланс после выкладки: потоки выравниваются автоматически (`agent.stream.rebalance_*`, `docs/agent-protocol.md`
   §13.6) — `zafiro_agent_stream_rebalance_requested_total` перегруженного инстанса растёт. Не растёт: перебалансировка
   выключена (`rebalance_enabled`), инстансы не видят друг друга в Valkey (разный `agent.stream.key_prefix`, предупреждения
   `agent streams publish failed`) или почти все ПК перегруженного инстанса заняты гостями (их не трогают — выровняется
   по мере освобождения). Перезапуск перегруженного инстанса с graceful shutdown разгрузит его сразу.

<a id="rb-agent-commands"></a>
### ZafiroAgentCommandsExpiring (warning)
Команды агентам (сессия, баланс, питание) истекают недоставленными. ПК офлайн или стрим не доставляет: сравните
`issued` и `completed`, проверьте NATS (команды между инстансами идут через NATS) и присутствие.

<a id="rb-outbox-lag"></a>
### ZafiroOutboxLag, ZafiroOutboxNoLeader (critical)
Событие ждёт публикации больше минуты: админка (WebSocket) и вебхуки отстают. Деньги не теряются — события в Postgres.
1. `zafiro_worker_leader{job="outbox-publisher"}`: есть ли лидер (сумма = 1). Нет — журнал `leader:` и `outbox:` на инстансах.
2. `ZafiroNATSDisconnected`, `outbox_publish_failures_total`, `nats_errors_total{kind="publish"}` — недоступен NATS/JetStream (место в хранилище потока).
3. Лидер есть, ошибок нет, отставание растёт — объём: `outbox_pending_events`, размер батча `outbox.batch_size`.

<a id="rb-webhooks"></a>
### ZafiroWebhookQueueGrowing, ZafiroWebhookStale (warning)
Очередь доставок вебхуков растёт или старейшая доставка старше 3 часов.
1. `webhook_delivery_attempts_total{result}`: retry/failed — внешние адреса клиентов не отвечают (их проблема; конечная
   точка отключается после `webhooks.disable_after` ошибок, как в старом). delivered ≈ 0 при пустых ошибках — не работает отправитель (`worker_up{worker="webhook-deliverer"}`).
2. Таймауты и SSRF-отказы — журнал `webhook:`.

<a id="rb-worker-not-running"></a>
### ZafiroWorkerNotRunning, ZafiroWorkerErrors (warning), ZafiroMoneyWorkerMissing (critical)
Задача не выполнялась 3 своих интервала, падает с ошибками или денежная задача (снятие истёкших сессий, автобиллинг)
не запускалась ни разу.
1. Дашборд «воркеры»: последний запуск, ошибки. Журнал по `job`.
2. Ни один инстанс не берёт лок — проверьте, не висит ли сессия Postgres с локом (`pg_locks` advisory + `pg_stat_activity`);
   при необходимости `pg_terminate_backend` зависшего соединения (лок снимется, задачу возьмёт другой инстанс).
3. `dev.disable_workers` в проде запрещён валидацией — отсутствие задачи в прод-конфигурации невозможно.

<a id="rb-auto-billing-lag"></a>
### ZafiroAutoBillingLag (warning)
Старейший срок пересмотра автобиллинга просрочен больше 30 с в течение 5 минут (`zafiro:auto_billing_oldest_due_seconds`;
в норме < 2 с). Гости после паузы или конца тарифа ждут покупки времени; деньги не теряются (покупка идемпотентна
по ключу ПК + вход + шаг). Тик (`agent:auto-billing`, раз в 1 с, один исполнитель на кластер) выбирает пакеты по 200
ПК, пока просроченные есть и не прошло 800 мс; ПК разных гостей — в 8 потоков (`guestapi.DefaultAutoBillingWorkers`).
1. Задача вообще идёт? `zafiro_worker_last_run_timestamp_seconds{job="agent:auto-billing"}` и `ZafiroWorkerNotRunning`
   / `ZafiroMoneyWorkerMissing`. Нет — см. [rb-worker-not-running](#rb-worker-not-running).
2. Идёт, но не успевает: `zafiro:auto_billing_due` растёт, `worker_run_duration_seconds{job="agent:auto-billing"}` ≈ 1 с.
   Узкое место — тенантные транзакции чтения (по одной на ПК): пул `app` лидера (`ZafiroDBPool*`), `db_query_duration_seconds`,
   CPU Postgres. Замер: ≈ 1 300 ПК/с на стенде 4 vCPU (`docs/load-test.md` «Автобиллинг на 50 000 ПК»).
3. Ошибки тика (`auto billing tick failed`, `worker_errors_total{job="agent:auto-billing"}`) — сбойные ПК повторяются
   через 10 с и не держат очередь; массовые — Valkey или Postgres недоступны.

<a id="rb-db-pool"></a>
### ZafiroDBPoolExhausted (critical), ZafiroDBPoolHigh (warning)
Пул соединений занят, запросы ждут соединения.
1. Какой пул (`app` — запросы клубов, `platform` — фоновые задачи). Долгие транзакции и блокировки — `pg_stat_activity`.
2. Рост нагрузки — увеличить `pool_max_conns` в DSN (с учётом `max_connections` Postgres на все инстансы) или добавить инстанс.

<a id="rb-ledger-errors"></a>
### ZafiroLedgerErrors (critical), ZafiroLedgerRollbacks (warning)
Ошибка запроса журнала денег на стороне базы (нарушение ограничения, сериализация) или много откатов.
1. `zafiro:ledger_errors:increase5m` по `query`/`reason`: `ledger_entries_balanced` — несбалансированная операция (ошибка кода, срочно в разработку);
   `ledger_member_balance_non_negative` — гонка обошла проверку в Go; `40001`/`40P01` — конфликты сериализации/взаимоблокировки.
2. Журнал по времени алерта (`ledger:`), трасса `db.tx ledger`. Ключи идемпотентности защищают от двойного списания при повторе клиента.
3. Сверка балансов (инструмент переноса, отчёт сверки) — если подозрение на расхождение.

<a id="rb-login-failures"></a>
### ZafiroLoginFailuresSpike, ZafiroRateLimitSpike (warning)
Всплеск неудачных входов в панель или отказов по лимиту — перебор паролей или сбой.
1. `auth_logins_total{result="failed"}` по `reason`; журнал клуба `auth.login_failed` (телефоны — там, не в метриках).
2. Источники — журнал `http request` по `remote_ip` (за прокси — из X-Forwarded-For доверенного балансировщика).
3. Блокировка подсети на балансировщике; лимиты входа включены по умолчанию (Q-052).

<a id="rb-certificates"></a>
### ZafiroCertificateExpiresSoon (warning, < 21 дня), ZafiroCertificateExpiresCritical (critical, < 7 дней)
Истекает CA агентов (`agent_ca`) или серверный TLS gRPC (`agent_server`). Истёкший серверный сертификат — массовый обрыв агентов.
1. Серверный: выпустить новый тем же CA, заменить файлы `agent.pki.server_cert_file`/`server_key_file`, перезапустить инстансы по одному.
2. CA (выпускающий «agent-issuing»): ротация с перекрытием — `pki.LoadCAFromPEM(..., extraTrust...)`, прежний CA остаётся в `ClientCAs`, пока живут его сертификаты (≤ 30 дней), docs/agent-protocol.md (раздел про CA, ~строка 367).

<a id="rb-dependencies"></a>
### ZafiroNATSDisconnected (critical), ZafiroDependencyErrors (warning)
Инстанс без NATS: команды агентам между инстансами, WebSocket админки и outbox стоят. Ошибки Valkey: сессии, лимиты, присутствие.
Проверить сервис (`/healthz` NATS на 8222, `valkey-cli ping`), сеть, место на диске JetStream, пароли.

<a id="rb-host"></a>
### HostDiskSpaceLow, HostMemoryLow, ZafiroProcessMemoryHigh (warning), HostDiskWillFillIn24h (critical)
Диск или память хоста. Диск: журналы Docker, WAL Postgres (архивирование WAL-G не успевает — см. docs по бэкапам O2),
JetStream. Память процесса сервера сверх порога — снять профиль (`go tool pprof` на dev-копии) и перезапустить инстанс.

## 6. Дашборды

`deploy/observability/grafana/dashboards` (провижининг из файлов, правки в UI не сохраняются):

- **zafirogo — обзор**: инстансы, 5xx, p95 денег, ПК на связи, outbox, вебхуки; HTTP/gRPC по кодам и маршрутам; процесс, пулы, хост.
- **zafirogo — агенты**: потоки по инстансам, присутствие, команды, регистрация, отказы UNAUTHENTICATED, сроки сертификатов.
- **zafirogo — деньги**: операции ledger по виду, откаты, ошибки, p95 и коды денежных маршрутов, входы и лимиты. Без сумм и клубов.
- **zafirogo — воркеры и очереди**: outbox, NATS, вебхуки, последний запуск и длительность задач, лидер, ошибки, выборка журнала.

## 7. Развёртывание (staging)

```sh
deploy/observability/init.sh   # пароль администратора Grafana → deploy/observability/secrets/ (вне git)
docker compose --env-file deploy/staging/.env.staging \
  -f deploy/staging/compose.yaml -f deploy/observability/compose.observability.yml up -d --wait
deploy/observability/check.sh  # promtool/amtool без запуска стека
```

Оверлей добавляет `ZAFIRO_OBSERVABILITY_METRICS_LISTEN=:9090` инстансам (порт не публикуется), Prometheus
(`127.0.0.1:19090`, хранение 15 дней), Alertmanager (`127.0.0.1:19093`), Grafana (`127.0.0.1:13000`), node-exporter.
Версии — тег + digest, не моложе 14 дней (PD-4): Prometheus v3.15.0, Alertmanager v0.34.1, Grafana 13.1.8, node-exporter v1.12.1.
Alertmanager на staging никуда не шлёт (receiver `null`); для прода — Telegram (`telegram_configs`, токен файлом,
отдельный бот/чат от бота суперадминки) — шаблон закомментирован в `alertmanager/alertmanager.yml`.

**Прод.** Prometheus — отдельный хост/ВМ (не на хосте приложения: падение хоста не должно глушить алерты), цели —
по инстансу (file_sd/DNS), node-exporter на каждом сервере, экспортёры Postgres (postgres_exporter), NATS
(prometheus-nats-exporter, `-m 8222`) и blackbox для HTTPS балансировщика — по мере появления прод-окружения (вопрос OBS-4).

**Loki (опционально).** Журналы уже JSON в stdout контейнера. Вариант без изменений в сервере: Grafana Loki + Promtail/Alloy,
читающий журналы Docker (`/var/lib/docker/containers`), метки — только `service`, `instance`, `level` (не `request_id`
и не `club_id` — кардинальность), поиск по `request_id`/`trace_id` — фильтром строк (`|= "…"`/`| json`). Хранение 14–30 дней.
Секреты в журнал не попадают (раздел 4). В репозиторий не добавлен — решение OBS-3.
