# 50. Фоновые задачи, очередь, события

> Раздел инвентаризации Этапа 0. Ссылки — на старый репозиторий `../Zafiro`.
> Открытые вопросы — `docs/open-questions/settings-jobs-integrations.md`.

## 50.0. Как это устроено сейчас

- [ ] **Планировщик** — `routes/console.php` (Laravel Scheduler). Запускается cron'ом раз в минуту: `* * * * * cd /home/admzafiro/Zafiro && php artisan schedule:run` (`CLAUDE.md`, раздел «Выкладка»). Демонов нет.
- [ ] **Очередь** — драйвер `database` (`QUEUE_CONNECTION=database`, таблицы `jobs`, `failed_jobs`; `config/queue.php`, `retry_after = 90`). Воркер — короткоживущий `queue:work --stop-when-empty --max-time=50 --sleep=1`, запускается планировщиком каждую минуту. Единственный job-класс — `App\Jobs\DeliverWebhook`.
- [ ] **Блокировки от двойного запуска**: `withoutOverlapping(N)` — мьютекс в кэше (`CACHE_STORE=database`, таблица `cache_locks`) с истечением через N минут. **`onOneServer()` нигде не используется** — архитектура рассчитана на один сервер. `runInBackground()` нет: задачи одной минуты идут **последовательно** в одном процессе `schedule:run`, поэтому порядок в файле важен (комментарий в `routes/console.php`: `monitoring:collect` первым, чтобы его не задерживал воркер очереди до 50 с).
- [ ] **Events/Listeners/Observers**: каталогов `app/Events`, `app/Listeners`, `app/Observers` нет. Есть только Eloquent-хуки моделей (п. 50.4) и `terminate()`-middleware (п. 50.3).
- [ ] **Сторож планировщика**: `app/Services/Monitoring/SchedulerWatchdog.php` — на HTTP-запросах (не чаще раза в минуту, маркер-файл) проверяет, что последняя строка `server_metrics` не старше `monitoring.scheduler_silent_minutes = 5`; иначе Telegram-алерт «Планировщик молчит…».

### Общая схема в Go (предложение)

- [ ] Пакет `internal/jobs`: реестр задач `{name, interval/cron, timeout, singleton bool, fn}`; запуск в том же бинарнике (`zafiro serve` поднимает воркеры) и опционально отдельной ролью (`zafiro worker`).
- [ ] **Один исполнитель на кластер** для периодических задач — Postgres advisory lock: в начале тика `SELECT pg_try_advisory_lock(hashtext('job:<name>'))` на выделенном соединении (или `pg_try_advisory_xact_lock` внутри транзакции задачи). Не взял — пропускает тик. Это заменяет `withoutOverlapping` + `onOneServer` и не требует отдельного leader election. Альтернатива — лиза в NATS JetStream KV (`kv.Create` с TTL) — если захотим не держать соединение БД. Рекомендация: advisory lock (Postgres есть всегда, при падении процесса лок освобождается сам).
- [ ] **Тикер** — `time.Ticker` с выравниванием на границу минуты + джиттер; метрики `job_runs_total`, `job_duration_seconds`, `job_last_success_timestamp` (замена `SchedulerWatchdog`: алерт, если `last_success` старше порога).
- [ ] **Очередь** — без отдельной таблицы `jobs`: доменные таблицы-«outbox» (`webhook_deliveries`) + выборка `FOR UPDATE SKIP LOCKED` по `next_attempt_at`; NATS JetStream используется для «разбудить воркер сейчас» и для событий между инстансами (`club.settings.changed`, `computer.state.changed`). Так ни одно сообщение не теряется при падении NATS (источник правды — Postgres), а несколько инстансов не берут одну доставку.
- [ ] Graceful shutdown: задачи получают `ctx`, при SIGTERM дорабатывают текущую порцию (батч) и выходят.

## 50.1. Расписание `routes/console.php`

- [ ] **`monitoring:collect`** — `everyMinute()->withoutOverlapping(5)`. Класс `app/Console/Commands/CollectServerMetrics.php`.
  - Делает: (1) `RequestRecorder::drain(<текущая минута>)` — читает файлы `storage/framework/metrics/requests-<minute>.log` завершившихся минут, суммирует по каналам (`panel|agent|api|guest`) и клубам, удаляет файлы; (2) пишет `request_metrics` (upsert «прибавить», по `minute+channel`) и `club_request_metrics` (по `club_id+minute`, вставка пачками по 500, только существующие клубы); (3) `ServerProbe::snapshot()` → `insertOrIgnore` в `server_metrics` (load avg, CPU, RAM из `/proc`, диск, uptime, размер БД из `information_schema`, длина очереди из `jobs`/`failed_jobs`); (4) алерты через `Alerts::update()`: `scheduler` (восстановлен), `disk` (> 85 %), `errors` (5xx ≥ 5 % и ≥ 10 ошибок за 5 мин), `queue` (≥ 100 ждут или старейшая ≥ 10 мин).
  - Таблицы: `server_metrics` (1 строка/мин), `request_metrics` (≤ 4 строки/мин), `club_request_metrics` (≤ 1 строка на активный клуб в минуту: при 1 000 клубов ≈ 1,44 млн строк/сутки, хранится 2 суток).
  - Go: тикер 1 мин, singleton. Счётчики запросов — **в памяти каждого инстанса** (atomic/шардированная map), сброс раз в минуту `INSERT ... ON CONFLICT (minute, channel) DO UPDATE SET requests = requests + EXCLUDED.requests` — каждый инстанс пишет сам (не singleton), файлов нет. Снимок сервера — singleton по хосту (а не по кластеру): метрики хоста лучше отдавать в Prometheus, а `server_metrics` оставить для страницы суперадминки (Q-J3). Размер БД — `pg_database_size()`, очередь — число `webhook_deliveries` в `pending` и возраст старейшей.
- [ ] **`monitoring:clubs`** — `everyFiveMinutes()->withoutOverlapping(5)`. `app/Console/Commands/SnapshotClubLoad.php`.
  - Делает: агрегат по `club_computers` всех клубов (`COUNT`, online = `last_seen_at >= now-90с`, busy = есть сессия и `session_ends_at > now`) → `club_load_snapshots` (одна строка на клуб: `weekday`, `hour` в часовом поясе клуба — для тепловой карты); алерт `club-offline.<id>`: все ПК клуба офлайн, если 2–12 мин назад было ≥ 3 онлайн и подписка активна; «снова в сети» — при восстановлении.
  - Объём: 288 строк/клуб/сутки; при 1 000 клубов ≈ 8,6 млн строк за 30 дней хранения.
  - Go: тикер 5 мин, singleton. Онлайн-статус ПК в новой архитектуре живёт в памяти/Valkey (heartbeat не пишется в Postgres) → снимок берётся из реестра подключений агентов (gRPC-стримы) через Valkey, а не из `club_computers.last_seen_at`. Таблицу партиционировать по месяцам.
- [ ] **`monitoring:certificates`** — `hourly()`, без защиты от наложения. `app/Console/Commands/CheckCertificates.php` → `app/Services/Monitoring/Certificates.php::refresh()`: TLS-соединение к `host:443` каждого хоста (`MONITORING_SSL_HOSTS` или хосты `APP_URL`/`SUPERADMIN_DOMAIN`/`WIKI_DOMAIN`), дата окончания сертификата → кэш `monitoring.certificates` навсегда (страница суперадминки сама наружу не ходит). Алерта по сроку нет — только отображение.
  - Go: тикер 1 ч, singleton, `crypto/tls.Dial` с `InsecureSkipVerify` только для чтения `NotAfter`; результат — в Valkey. Добавить алерт «< 14 дней» (Q-J4).
- [ ] **`monitoring:prune`** — `daily()`. `app/Console/Commands/PruneMonitoringData.php`: удаляет `server_metrics`, `request_metrics`, `club_load_snapshots` старше 30 дней (`monitoring.retention_days`), `club_request_metrics` старше 2 дней. Одним `DELETE` без батчей.
  - Go: при партиционировании по месяцам/дням — `DROP`/`DETACH PARTITION` (singleton, ежедневно); для `club_request_metrics` — дневные партиции или Valkey вместо таблицы (данные нужны только «за последние минуты»).
- [ ] **`queue:work --stop-when-empty --max-time=50 --sleep=1`** — `everyMinute()->withoutOverlapping(5)`. Обрабатывает очередь `default` (только `DeliverWebhook`). Задержка доставки вебхука до ~1 мин + время в очереди.
  - Go: постоянный воркер доставки вебхуков (п. 50.2), не singleton — масштабируется по инстансам через `SKIP LOCKED`.
- [ ] **`sanctum:prune-expired --hours=24`** — `daily()`. Удаляет из `personal_access_tokens` токены, истёкшие > 24 ч назад (токены агента/мобилок/QR-входа на 30 дней: `Api/AuthController.php` ~114, ~179, `AgentRegisterController.php` ~64, `QrLogins.php` ~66).
  - Go: Sanctum не переносится. Refresh-токены — в Postgres (`auth_refresh_tokens`, с `expires_at`) или Valkey с TTL. Если Postgres — ежедневная чистка singleton; если Valkey — не нужна.
- [ ] **`club-computers:expire-sessions`** — `everyMinute()`, **без** `withoutOverlapping`. `app/Console/Commands/ExpireComputerSessions.php` → `ClubComputerSessions::expireAll()` (`app/Services/ClubComputerSessions.php` ~189–195): один `UPDATE club_computers SET current_session_id=NULL, current_session_user_id=NULL, session_guest=0, session_ends_at=NULL, updated_at=now WHERE session_ends_at <= now()` по всем клубам. Без расчёта возврата (время израсходовано полностью) и без записи в журнал действий. Нужна, чтобы сессия на **офлайн-ПК** закончилась вовремя.
  - Точность: до 1 мин (cron) + длительность предыдущих задач минуты.
  - Go: singleton-тикер **каждые 5–10 с** (дешёвый `UPDATE ... WHERE session_ends_at <= now() RETURNING club_id, id` по индексу `session_ends_at`) + публикация `computer.session.expired` в NATS → push в админку (WebSocket) и агенту (стрим). Альтернатива — таймер в памяти на каждую сессию у инстанса-владельца клуба; отклонена: теряется при рестарте. Q-J1: нужна ли запись в журнал/ledger при естественном окончании.
- [ ] **`integrations:prune`** — `hourly()->withoutOverlapping()`. `app/Console/Commands/PruneIntegrationData.php`: батчами по 1 000 id удаляет `api_request_logs` старше 90 дн., `api_idempotency_keys` старше 24 ч, `webhook_deliveries` в статусах `delivered|failed|skipped` старше 30 дн.; при `CACHE_STORE=database` — до 50 000 протухших строк `cache`.
  - Go: singleton, ежечасно, те же сроки и батчи (`DELETE ... WHERE id IN (SELECT id ... LIMIT 1000)`); `api_request_logs` — партиции по месяцам и `DROP PARTITION`; чистка `cache` не нужна (Valkey с TTL).
- [ ] **`session-purchases:prune`** (замыкание) — `daily()`. Удаляет `session_purchases` старше 8 дней, кроме принадлежащих текущим сессиям (`current_session_id` любого ПК), и `time_packages` с `expires_at < now()` (сохранённые минуты тарифа с ограничением использования сгорают).
  - Важно для денег: удаление истёкших пакетов = клиент теряет минуты. В Go — singleton, ежедневно, **с записью в ledger/аудит** списания истёкших пакетов (Q-J2), иначе сверка балансов минут невозможна.
- [ ] **`agent-qr-logins:prune`** (замыкание) — `daily()`. Удаляет `agent_qr_logins`, истёкшие более суток назад (QR живёт 5 мин).
  - Go: QR-коды входа — в Valkey с TTL 5 мин; задача не нужна. Если останутся в Postgres — ежедневная чистка singleton.
- [ ] **`subscriptions:expire`** — `everyFiveMinutes()->withoutOverlapping()`. `app/Console/Commands/ExpireSubscriptions.php`: для `club_subscriptions` со `status='active'` и `current_period_ends_at <= now()` → `status='expired'` в транзакции + `PlatformLogger::record('subscription.expired', ...)`. Доступ закрывается и без неё (`ClubSubscription::isActive()` сравнивает дату), задача только выравнивает статус. Триалы (`trialing`) не трогает.
  - Побочный эффект: `ClubSubscription::saved` сбрасывает кэш API-ключей клуба (`AppServiceProvider` ~113).
  - Go: singleton, 5 мин (или 1 мин), тот же запрос + событие `club.subscription.changed` в NATS (сброс кэшей, отключение агентов/API). Q-J5: выравнивать ли так же `trialing → expired`.

## 50.2. Очередь: `App\Jobs\DeliverWebhook`

- [ ] **Постановка**: `WebhookDispatcher::dispatch($event, $data)` (`app/Services/Integrations/WebhookDispatcher.php` ~24–51) создаёт строку `webhook_deliveries` (`status=pending`, `next_attempt_at=now`) **внутри транзакции бизнес-операции** и ставит `DeliverWebhook::dispatch($id)->afterCommit()`. Повтор вручную — `IntegrationController::retryDelivery()` (~188–200).
- [ ] **Выполнение** (`app/Jobs/DeliverWebhook.php`): пропуск, если доставка уже финальна; `skipped`, если endpoint удалён/выключен; SSRF-проверка `WebhookUrlGuard::check()` с «пришпиливанием» IP (`CURLOPT_RESOLVE`); POST с таймаутом 5 с (connect 3 с), без редиректов; подпись `X-Zafiro-Signature: v1=hex(HMAC-SHA256(secret, "{timestamp}.{body}"))`, заголовки `X-Zafiro-Event`, `X-Zafiro-Delivery` (uuid события), `X-Zafiro-Timestamp`, `User-Agent: Zafiro-Webhooks/1`.
- [ ] **Ретраи**: `$tries = 10` (страховка), фактически — `release($delay)` по списку `webhook_retry_backoff_seconds = [10, 60, 300, 1800, 7200]` → до 6 попыток за ~2,6 ч; затем `failed`, `endpoint.failure_count++`; при 20 подряд неудачных доставках endpoint выключается (`is_active=false`) и сбрасывается кэш слушателей. Успех обнуляет `failure_count`.
- [ ] Из-за модели «воркер раз в минуту на 50 с» задержка `release(10)` фактически 10–60 с.
- [ ] Go: воркер `webhooks.Deliverer` (N горутин на инстанс): `SELECT ... FROM webhook_deliveries WHERE status='pending' AND next_attempt_at <= now() ORDER BY next_attempt_at LIMIT 50 FOR UPDATE SKIP LOCKED` → отправка вне транзакции с «арендой» (`locked_until`) → обновление статуса. Пробуждение по NATS-сообщению `webhooks.enqueued` (публикуется после commit) + опрос раз в 5 с как страховка. Семантика (подпись, бэкофф, отключение после 20 ошибок, SSRF-guard с пиннингом IP через `net.Dialer.Control`) — один в один. Индекс `(status, next_attempt_at)` уже есть.

## 50.3. Работа «на хвосте» HTTP-запроса

- [ ] **`RecordRequestMetrics::terminate()`** (`app/Http/Middleware/RecordRequestMetrics.php`, глобальный middleware, `bootstrap/app.php`): после ответа дописывает строку `канал\tclub\tms\tstatus` в файл минуты (`RequestRecorder::record()`) и вызывает `SchedulerWatchdog::check()`. Отключается `MONITORING_RECORD_REQUESTS=false`.
  - Go: middleware chi с in-memory счётчиками (см. `monitoring:collect`), без файлов. Сторож — метрика последнего успешного запуска задач + алерт из singleton-воркера на **другом** инстансе (или внешний Prometheus Alertmanager).
- [ ] **`ApiKey last_used_at`** — обновляется не чаще раза в минуту на ключ (`AuthenticateApiKey::touch()`, флаг в кэше). Go: батч-флаш раз в минуту из памяти.
- [ ] **`LogApiRequest`** — синхронная вставка в `api_request_logs` на каждый запрос `/api/v1`. Go: буферизованная пачечная запись (канал + `COPY`/multi-insert раз в 1 с), ошибки записи не ломают запрос (как сейчас).

## 50.4. Хуки моделей и «ленивые» истечения (без задач)

- [ ] **`AppServiceProvider::invalidateApiKeyCache()`** (~101–124): `ClubMembership::saved/deleted` (смена роли/права интеграций) → сброс кэша ключей пользователя; `ClubSubscription::saved/deleted` и `Club::saved` (смена `name|slug|locale|timezone|settings`) → сброс кэша всех ключей клуба; `WebhookEndpoint::saved/deleted` → сброс кэша слушателей вебхуков клуба. Go: явные вызовы инвалидации в сервисном слое + событие NATS `cache.invalidate.*`, чтобы сбросились кэши всех инстансов.
- [ ] **`Club::booted()`** (`app/Models/Club.php` ~25–45): генерация ключа установки при создании, запрет его изменения. Go: в сервисе создания клуба + CHECK/триггер.
- [ ] **`BelongsToClub`** (`app/Models/Concerns/BelongsToClub.php`): глобальный scope `club` и проставление `club_id` при создании. Go: sqlc-запросы с `club_id` + RLS.
- [ ] **Ленивые истечения** (проверяются при чтении, отдельной задачи нет): визит сотрудника на ПК (`club_computers.staff_until`), брони (`computer_reservations` по времени), коды восстановления пароля и переводов (`password_recovery_codes`, `transfer_codes` — старше суток удаляются только при следующем запросе того же пользователя, `PasswordRecovery::sendCode()` ~47, `FriendTransferCodes::send()`), алерты (`Alerts` в кэше навсегда). В Go коды — Valkey с TTL; для `staff_until` и броней — по-прежнему проверка при чтении + событие по таймеру для push-уведомления (Q-J6).

## 50.5. Ручные команды artisan

- [ ] **`translations:sync`** — `SyncTranslations` → `TranslationCatalog::sync()`: добавляет в `translations` недостающие строки из `database/translations/{code}.php` для всех языков, не трогая отредактированные; при вставке — `flush()` (новая версия пакетов). Запускается при каждой выкладке. Go: команда `zafiro translations sync` + автоматически при старте (под advisory lock), источник — embed-файлы.
- [ ] **`agent:translations`** — `ExportAgentTranslations`: пишет `WindowsAgent/Frontend/i18n/{ru,kk,en}.json` из группы `agent` (встроенные в агента тексты). Go: генератор в `task gen` (или `go generate`), выход — в репозиторий агента.
- [ ] **`superadmin:grant {phone} {--revoke}`** — `GrantSuperadmin`: выдать/снять `users.is_superadmin` по телефону (`+7` + 10 цифр). Go: `zafiro admin grant-superadmin --phone ... [--revoke]` + запись в платформенный журнал (сейчас только `Log::warning`).
- [ ] **`inspire`** — демонстрационная команда Laravel. Не переносить.

## 50.6. Сводка

| # | Задача | Частота | Защита | Go: тип | Singleton |
|---|---|---|---|---|---|
| 1 | monitoring:collect | 1 мин | withoutOverlapping(5) | тикер (+ in-memory счётчики на каждом инстансе) | снимок — да |
| 2 | monitoring:clubs | 5 мин | withoutOverlapping(5) | тикер | да |
| 3 | monitoring:certificates | 1 ч | — | тикер | да |
| 4 | monitoring:prune | 1 д | — | тикер / DROP PARTITION | да |
| 5 | queue:work (вебхуки) | 1 мин, ≤50 с | withoutOverlapping(5) | постоянный воркер, SKIP LOCKED + NATS wakeup | нет |
| 6 | sanctum:prune-expired | 1 д | — | тикер или TTL Valkey | да |
| 7 | club-computers:expire-sessions | 1 мин | — | тикер 5–10 с + NATS-событие | да |
| 8 | integrations:prune | 1 ч | withoutOverlapping | тикер | да |
| 9 | session-purchases:prune | 1 д | — | тикер + ledger | да |
| 10 | agent-qr-logins:prune | 1 д | — | TTL Valkey | — |
| 11 | subscriptions:expire | 5 мин | withoutOverlapping | тикер + NATS-событие | да |
| 12 | DeliverWebhook (job) | по событию | tries=10, бэкофф 5 шагов | см. №5 | нет |
| 13 | RecordRequestMetrics/SchedulerWatchdog | каждый запрос | — | middleware + метрики | нет |
| 14 | translations:sync | вручную/выкладка | — | CLI + при старте | lock |
| 15 | agent:translations | вручную | — | генератор | — |
| 16 | superadmin:grant | вручную | — | CLI | — |

Итого: **11 записей расписания**, **1 job-класс очереди**, **3 рабочие ручные команды** (+ `inspire`), **2 механизма на хвосте запроса**, **3 группы хуков моделей**, ленивые истечения — 5 видов.
