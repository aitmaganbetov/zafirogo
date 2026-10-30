# 60. Интеграции

> Раздел инвентаризации Этапа 0. Ссылки — на старый репозиторий `../Zafiro`.
> Открытые вопросы — `docs/open-questions/settings-jobs-integrations.md`.
> Секреты указаны **только именами** (где хранятся), значения не приводятся.

## 60.1. Telegram

В проекте **три** разных механизма с Telegram; бота для клиентов/клубов нет, входящих вебхуков от Telegram нет (сервер только ходит наружу).

- [ ] **Telegram Gateway — коды подтверждения** (`app/Services/TelegramGateway.php`).
  - API: `POST https://gatewayapi.telegram.org/{method}` с `Authorization: Bearer <token>`, таймаут 10 с. Методы: `checkVerificationStatus` (проверка токена при сохранении: `REQUEST_ID_INVALID` = токен валиден, `ACCESS_TOKEN*` = нет), `sendVerificationMessage {phone_number (E.164), code, ttl}` — Gateway возвращает деньги, если не доставлено за `ttl`.
  - Секрет: токен Gateway — `platform_settings['telegram_gateway'].token`, зашифрован `Crypt` (ключ `APP_KEY`), читается мимо кэша, в логи не пишется (логируется только имя метода и класс исключения; телефон тоже не логируется). `bootstrap/app.php` — `dontFlash(['token','bot_token'])`.
  - Настройка: `/superadmin/telegram` (`Superadmin/TelegramController::update()/destroy()`), журнал `superadmin.telegram_gateway_set|_cleared`. Счётчик отправленных за 30 дней — по `platform_logs.event = 'user.password_code_sent'`.
  - Потребители:
    - [ ] **Восстановление пароля** (`app/Services/PasswordRecovery.php`): 6-значный код (`random_int(100000, 999999)`), живёт `CODE_MINUTES = 5`, повтор не раньше `RESEND_SECONDS = 60`, не более `CODES_PER_HOUR = 5` в час на аккаунт, `MAX_ATTEMPTS = 5` неверных вводов на код; хранится bcrypt-хэш в `password_recovery_codes`; неизвестный/заблокированный номер получает тот же ответ (нет перебора аккаунтов). Успех: новый пароль, удаление всех Sanctum-токенов и web-сессий пользователя, удаление кодов, журнал `user.password_recovered`. Маршруты: web `GET/POST /forgot-password`, `GET/POST /reset-password` (`routes/web.php` ~159–163); агент `POST /api/password/code`, `POST /api/password/reset` (`routes/api.php` ~45–51); лимитер `password-recovery`: 10/мин на IP+телефон, 30/мин на IP (`AppServiceProvider` ~72–75).
    - [ ] **Подтверждение перевода другу из мобильного приложения** (`app/Services/FriendTransferCodes.php`): код привязан к другу и сумме, одноразовый; те же константы 5 мин / 60 с / 5 в час / 5 попыток; таблица `transfer_codes`. Маршруты `POST /api/guest/friends/{friendship}/transfer/code|confirm` (`routes/api.php` ~139–141, throttle 5/мин и 10/мин). Мобилки на этом этапе не переносим — Q-I3.
  - Go: клиент `internal/integrations/telegram/gateway` (net/http, таймаут 10 с, без логирования тела/токена), токен в `platform_settings` с шифрованием AES-GCM ключом из env. Коды — в Postgres (как сейчас) или Valkey с TTL; хэш кода — достаточно HMAC-SHA256 с секретом сервера (коды короткоживущие) или Argon2id. **Переносится как есть** (логика лимитов — буквально).
- [ ] **Бот оповещений о сбоях** (`app/Services/Monitoring/TelegramAlerts.php`).
  - API: `https://api.telegram.org/bot<token>/{getMe|getUpdates|sendMessage}`, таймаут 10 с. Токен — часть URL, поэтому исключения логируются только классом.
  - Секрет: `platform_settings['telegram_alerts'] = {token (Crypt), chat_id, locale}`.
  - Настройка: `/superadmin/telegram` (`updateAlerts`, `testAlerts` с `throttle:5,1`, `destroyAlerts`); без `chat_id` берётся последний чат из `getUpdates` (нужно написать боту `/start`); тестовое сообщение при подключении.
  - Отправка: только через `app/Services/Monitoring/Alerts.php::update()` — сообщение один раз при начале проблемы (`⚠️`) и один раз при окончании (`✅`); состояние «проблема открыта» — ключ кэша `monitoring.alert.<key>` навсегда. Язык — того суперадмина, что подключил бота. Ключи алертов: `scheduler`, `disk`, `errors`, `queue`, `club-offline.<club_id>`.
  - Go: тот же клиент Bot API; состояние алертов — в Valkey (`SET NX`), чтобы при нескольких инстансах сообщение ушло один раз. **Переносится как есть**; при наличии Prometheus/Alertmanager можно позже заменить (Q-I4).
- [ ] **Старый бот Zafiro для клиентов** — удалён. Остался заглушечный `GET /api/guest/telegram` → `{available:false, linked:false}` (`app/Http/Controllers/Api/GuestTelegramController.php`) для агентов < 0.0.57; ключ `platform_settings.telegram_bot` удаляется миграцией `2026_10_18_120000_telegram_gateway_password_recovery.php`. **Не переносить** (API агента проектируется заново).

## 60.2. Вебхуки (исходящие)

- [ ] **Модель данных**: `webhook_endpoints(club_id, created_by, url ≤500, secret text (encrypted cast), events json, is_active, failure_count, last_success_at, last_failure_at)`, `webhook_deliveries(club_id, webhook_endpoint_id, event_id uuid unique, event, payload json, status pending|delivered|failed|skipped, attempts, last_status_code, last_error ≤255, next_attempt_at, delivered_at)` — `database/migrations/2026_10_13_190912_create_integration_api_tables.php` (~66–96). Индексы: `(club_id, is_active)`, `(webhook_endpoint_id, created_at)`, `(status, next_attempt_at)`.
- [ ] **Подписки/управление**: `/settings/integrations` (`app/Http/Controllers/IntegrationController.php`, middleware `subscription` + `can:manage-integrations` = владелец или член с `club_memberships.can_manage_integrations`). Создание: `url` `required|string|max:500|url:https,http` + `WebhookUrlGuard`, `events` `required|array|min:1|in:<config>`; лимит `MAX_WEBHOOKS = 20` на клуб; секрет `whsec_` + 40 символов показывается **один раз**. Также: изменение (URL/события/вкл-выкл; включение обнуляет `failure_count`), перевыпуск секрета, удаление, повтор доставки `failed|skipped`. Последние 20 доставок на экране. Аудит: `webhook.created|updated|secret_rotated|deleted|retried`.
- [ ] **События** (`config/integrations.php` → `webhook_events`) и места генерации (`WebhookDispatcher::dispatch()` внутри транзакции):
  - `product.created` — `app/Actions/CreateProductAction.php` ~52;
  - `product.updated` — `app/Actions/UpdateProductAction.php` ~37;
  - `stock.changed` — `CreateProductAction.php` ~49, `AdjustStockAction.php` ~56, `CashRegisterService.php` ~62, ~226, `AgentShop.php` ~80, ~137;
  - `sale.completed` — `CashRegisterService.php` ~81;
  - `computer.reservation_changed` — `ClubComputerController.php` ~166, `Api/V1/ComputerController.php` ~60;
  - `computer.controlled` — `ClubComputerController.php` ~196, `Api/V1/ComputerController.php` ~100.
  - Конверт: `{id: uuid, event, created_at ISO8601, club_id, data}`; правило: в `data` нет секретов и персональных данных (`CLAUDE.md`, раздел API).
- [ ] **Отправка, подпись, ретраи** — `app/Jobs/DeliverWebhook.php`, подробно в `50-jobs.md` п. 50.2. Подпись `v1=hex(HMAC-SHA256(secret, "{timestamp}.{body}"))`; допуск по времени для получателя — `webhook_signature_tolerance_seconds = 300` (только документирован, проверяет получатель). Бэкофф `[10, 60, 300, 1800, 7200]` с, отключение после 20 неудач подряд.
- [ ] **SSRF-защита** — `app/Services/Integrations/WebhookUrlGuard.php`: только http(s), без user:pass, HTTPS обязателен (`WEBHOOK_REQUIRE_HTTPS`), порты 80/443/8080/8443, все A/AAAA — публичные (`WEBHOOK_ALLOW_PRIVATE_HOSTS=false`), запрос пришпиливается к проверенному IP; редиректы запрещены.
- [ ] **Кэш слушателей**: `webhooks.listeners.<club_id>` на 600 с, сброс при изменении endpoint'ов (хук в `AppServiceProvider`).
- [ ] **Секреты**: `webhook_endpoints.secret` — зашифрован `APP_KEY` (Laravel `encrypted` cast); нужен в открытом виде для HMAC, поэтому в Go — симметричное шифрование (AES-GCM, ключ из env, версионирование ключа), а не хэш. При переносе — расшифровать старым `APP_KEY` в `tools/migrate-from-mysql` (Q-S8).
- [ ] Go: **переносится как есть** (формат подписи и заголовков — тот же, чтобы интеграторам ничего не менять), отправка — воркер с `SKIP LOCKED` + NATS-пробуждение. Решение о сохранении функции — Q-I1.

## 60.3. Интеграционный API v1 и API-ключи

- [ ] **Маршруты** (`routes/api_v1.php`, префикс `/api/v1`, подключение `routes/api.php` ~179–183; цепочка middleware `api.log → api.size → api.key → api.limit`, приоритет до model binding — `bootstrap/app.php` ~53–57; ошибки в своём формате — `app/Support/ApiError.php`, `bootstrap/app.php` ~69). Контракт — `resources/api/openapi-v1.yaml`, Swagger UI — `GET /docs/api` (`routes/web.php` ~127–128). Подробное описание — `docs/integration-api.md` старого репо.
  - `GET /me` (any); `GET /products`, `/products/{id}`, `/product-categories`, `/stock-movements` (`products:read`); `POST /products`, `PATCH /products/{id}` (`products:write` + idempotent optional); `POST /stock-movements` (`stock:write` + idempotent **required**); `GET /sales`, `/sales/{id}` (`sales:read`); `GET /zones`, `/computers`, `/computers/{id}` (`computers:read`); `PUT /computers/{id}/reservation` (`bookings:write` + idempotent); `POST /computers/control` (`computers:control` + idempotent required); `GET /tariffs` (`tariffs:read`); `GET /members` (`members:read`). Итого 17 маршрутов. Контроллеры — `app/Http/Controllers/Api/V1/*`, сериализация — `Transformers.php`.
- [ ] **Ключи** (`app/Services/Integrations/ApiKeyManager.php`, таблица `api_keys`):
  - Формат `zfk_<8 [a-z0-9]>_<40 [A-Za-z0-9]>`; хранится только `sha256(полный ключ)` (`key_hash`) и уникальный `prefix` (`zfk_xxxxxxxx`); секрет показывается один раз.
  - Поля: `name`, `scopes json`, `allowed_ips json` (IP/CIDR, `IpUtils::checkIp`), `rate_limit_per_minute` (1…`max_rate_limit_per_minute`), `expires_at` (> сегодня), `last_used_at/ip`, `revoked_at/by`, `rotated_from_id`.
  - Проверка `verify()`: regexp → кэш метаданных по префиксу (300 с; «нет такого» — 60 с) → `hash_equals` → отозван / истёк / клуб удалён / **владелец ключа потерял право интеграций** / подписка неактивна (402).
  - Ротация: новый ключ с теми же параметрами в том же клубе + отзыв старого в одной транзакции. Лимит `MAX_ACTIVE_KEYS = 50` на клуб. Аудит `api_key.created|rotated|revoked`, `integrations.permission_changed`.
  - Клуб берётся **только** из ключа; `X-Club-Id`/`club_id` в запросе, отличный от клуба ключа, → 403 `tenant_override_forbidden` (`AuthenticateApiKey::overridesTenant()`). Маршрут без `api.scope:*` недоступен (`declaresScope()`, default deny).
  - Токен принимается из `Authorization: Bearer` или `X-Api-Key`, длина ≤ 128.
- [ ] **Scopes** — 9 прав из `config/integrations.php` (`computers:control` и `members:read` помечены опасными); проверка `RequireApiScope` (403 `insufficient_scope` + `required_scopes`).
- [ ] **Лимиты**: неудачная аутентификация — 20/мин на IP (только `invalid_api_key`), ключ — `rate_limit_per_minute` или 120 (≤ 1000), клуб — 600/мин; фиксированное окно в минуту (`Cache::increment`), заголовки `X-RateLimit-Limit/Remaining`, `Retry-After` (`app/Http/Middleware/Api/ApiRateLimit.php`). Размер тела ≤ 256 КБ (`LimitApiRequestSize`), пачка ≤ 50 элементов (`max_bulk_items`).
- [ ] **Идемпотентность** (`app/Http/Middleware/Api/ApiIdempotency.php`, таблица `api_idempotency_keys`, уникальность `(api_key_id, idempotency_key)`): заголовок `Idempotency-Key` `[A-Za-z0-9_:-]{1,100}`; хэш `sha256(METHOD path\nbody)`; другой запрос с тем же ключом → 422 `idempotency_key_reused`; параллельный → 409 `idempotency_in_progress`; повтор → сохранённый ответ + `Idempotent-Replayed: true`; ответ 5xx или исключение — запись удаляется (можно повторить); `required` без заголовка → 428. Хранение 24 ч.
- [ ] **Журнал запросов** — `api_request_logs` (метод, endpoint ≤191, статус, длительность, IP, префикс ключа; 90 дней), показ последних 30 на `/settings/integrations`.
- [ ] **Связь с настройками клуба**: в кэш метаданных кладётся копия `clubs` (`id, name, slug, locale, timezone, settings`), запрос выполняется от «виртуального» клуба без запроса в БД, часовой пояс — UTC.
- [ ] Go: схема ключей переносится как есть (sha256 от высокоэнтропийного ключа достаточно; Argon2 не нужен), кэш метаданных — Valkey + локальный LRU с инвалидацией по NATS; rate limit — Valkey (`INCR` + `EXPIRE`, то же окно); идемпотентность — Postgres (как сейчас) или Valkey. OpenAPI v1 — в `oapi-codegen` отдельным спецом. Нужно ли API v1 в прежнем виде — Q-I1.

## 60.4. Обновления агента

- [ ] **Источник версии**: `platform_settings['agent_release']` (суперадмин, `Superadmin/AgentReleaseController.php`) или фолбэк env `AGENT_LATEST_VERSION|AGENT_RELEASED_AT|AGENT_DOWNLOAD_URL` (`config/club.php`). Подробно — `40-settings.md` п. 40.5.
- [ ] **Откуда файлы**: GitHub Releases старого репо, шаблон `AgentUpdate::INSTALLER_URL = https://github.com/aitmaganbetov/Zafiro/releases/download/agent-v{version}/ZafiroAgentSetup-{version}.exe`; рядом `.sha256` (`"<hex>  <имя>"`), публикуется CI `.github/workflows/windows-agent.yml` (тег `agent-vX.Y.Z`, ~115–183). Сервер при объявлении скачивает `.sha256` (таймаут 15 с) и сохраняет хэш; без хэша автообновление не включается.
- [ ] **Подписи**: только SHA-256 файла, переданный сервером в heartbeat. **Цифровой подписи (Authenticode/Ed25519) нет** — доверие держится на TLS до сервера и GitHub. Требование ТЗ: обновления подписаны Ed25519 → новое (Q-I2).
- [ ] **Каналы**: один «канал» — последняя объявленная версия на всю платформу; поклубного/поэтапного (canary) выката нет; клуб может только выключить автообновление (`settings.club.agent_auto_update`).
- [ ] **Доставка предложения**: `AgentUpdate::offerFor()` (`app/Services/AgentUpdate.php` ~124–149) в ответе heartbeat (`agent_update: {version, url, sha256}`, `ClubComputerController.php` ~479) — только если: релиз с `auto_update=true`, URL `https://`, sha256 валиден, клуб разрешил, версия агента ниже (`version_compare`, нечитаемая версия = старая), на ПК нет оплаченной сессии и `agent_state` не `idle|in_session`; не чаще `MAX_OFFERS = 3` раз на версию с интервалом `OFFER_INTERVAL_MINUTES = 120` (учёт в `club_computers.agent_update_version|offers|offered_at` — для бездисковых ПК).
- [ ] **Агент** (`WindowsAgent/ZafiroAgent/Service/AgentUpdater.cs`): принимает URL только с `github.com`, `objects.githubusercontent.com`, `release-assets.githubusercontent.com`; ≤ 300 МБ; сверяет SHA-256 открытого на чтение файла; тихая установка Inno Setup `/VERYSILENT /SUPPRESSMSGBOXES /NORESTART /SP- /MERGETASKS="!protect"`; одна попытка на версию в час; старые установщики удаляет.
- [ ] **Уведомление в панели**: `AgentUpdate::status()` — сколько ПК клуба со старой версией (выборка всех `agent_version` клуба в PHP — в Go агрегат в SQL), ссылка на скачивание только `https://`.
- [ ] Go: новый механизм — манифест релиза `{version, channel, url, sha256, size, ed25519_signature}`, подпись ключом из CI (публичный ключ вшит в агент), доставка предложения по gRPC-стриму, хранение файлов — GitHub Releases нового репо или собственное хранилище (Q-I2). Логику «только свободный ПК», «3 предложения/120 мин», флаг клуба — **перенести как есть**.

## 60.5. Переводы (локализация)

- [ ] **Источник**: `database/translations/{ru,kk,en}.php` (по 2 274 строки; ≈ 1 924 ключа группы `*` и ≈ 324 ключа группы `agent`; ключ — русский текст). Русский — исходный язык (`TranslationCatalog::SOURCE_LOCALE = 'ru'`), фолбэк — `en`.
- [ ] **Хранение**: `languages(code, name, is_enabled, is_system, sort_order)`, `translations(language_id, group, key text, key_hash char(40), value)`, уникальность `(language_id, group, key_hash)` (`2026_10_13_181908_create_languages_and_translations_tables.php`); `Translation::hashKey(group, key)`.
- [ ] **`app/Services/TranslationCatalog.php`**: кэш `translations.state {version, enabled, groups}` + пакеты `translations.v{N}.pack.{code}` (все группы языка, слитые поверх английского фолбэка, TTL 7 дней, построение под `Cache::lock`); любое изменение → `flush()` увеличивает версию (агент перечитывает тексты по `translations_version` в heartbeat). `sync()` — см. `50-jobs.md` (`translations:sync`). `sourceKeys()` — для подсчёта прогресса перевода.
- [ ] **`app/Translation/DatabaseTranslationLoader.php`**: строки из БД поверх файловых переводов Laravel (сообщения валидации фреймворка).
- [ ] **`app/Services/TranslationTransfer.php`**: экспорт/импорт CSV (`;`, колонки `id(=key_hash); group; ru; en; <code>`), до 10 000 строк, пустые ячейки не удаляют перевод при импорте, строки с несовпадающими плейсхолдерами `:name` отклоняются с отчётом, защита от CSV-формул (`= + - @` → апостроф). Маршруты суперадминки: `GET /languages/export`, `POST /languages/import` (`file` `mimes:csv,txt|max:2048`, `throttle:20,1`), `GET/PUT /languages/{language}/translations` (до 200 значений за запрос, ≤ 5000 символов) — `app/Http/Controllers/Superadmin/LanguageController.php`, экраны `resources/views/superadmin/languages.blade.php`, `translations.blade.php`.
- [ ] **Агент**: `GET /api/agent/translations/{locale}` (`Api/AgentTranslationController.php`; `throttle:agent-translations` 600/мин на IP; `Cache-Control: public, max-age=300`) → `{locale, version, lines}` группы `agent`; встроенные тексты агента — `agent:translations` → `WindowsAgent/Frontend/i18n/*.json`.
- [ ] Go: те же таблицы; embed-файлы источника (конвертировать PHP-массивы в JSON/YAML один раз, генератор в `tools/`); кэш пакетов в памяти процесса с версией в Valkey + NATS-событие `translations.changed`; фронт React — i18next с загрузкой пакета по версии; агент — через gRPC `GetTranslations` + push версии. Плюрализация: ru — 3 формы, kk — 1, en — 2 (`trans_choice` с `|`) — сохранить формат строк, i18next настроить на тот же синтаксис или конвертировать (Q-I5). **Переносится как есть.**

## 60.6. Мониторинг (`app/Services/Monitoring/*`)

- [ ] `RequestRecorder` — счётчики запросов в файлы по минутам (см. 50-jobs).
- [ ] `ServerProbe` — `/proc/loadavg|meminfo|cpuinfo|uptime`, диск, размер БД и 8 крупнейших таблиц (MySQL `information_schema`), очередь (`jobs`, `failed_jobs`). В Go: `pg_database_size`, `pg_total_relation_size`; очередь — `webhook_deliveries`.
- [ ] `Certificates` — сроки TLS-сертификатов (ежечасно).
- [ ] `ClubAttention` — «клубы, требующие внимания» на дашборде суперадминки: онлайн ПК упал вдвое за 15 мин (было ≥ 3), ≥ 3 неудачных вебхуков за сутки, подписка/триал заканчивается в ближайшие 3 дня.
- [ ] `ErrorLog` — последние 10 ошибок из хвоста `storage/logs/laravel.log` (256 КБ). В Go: slog JSON → кольцевой буфер последних ошибок в памяти/Valkey или ссылка на внешний сбор логов (Q-I4).
- [ ] `Alerts`, `TelegramAlerts`, `SchedulerWatchdog` — см. 60.1 и 50-jobs.
- [ ] Экран — дашборд суперадминки «Сервер» (`Superadmin/DashboardController.php`, `resources/views/superadmin/dashboard.blade.php`). Секретов нет (кроме токена бота).

## 60.7. Новости

- [ ] `app/Services/NewsManager.php`: новости платформы (`club_id = null`, суперадминка `/platform-news`, `Superadmin/NewsController.php`) и клуба (`/news`, `NewsController.php`). Валидация: `title ≤150`, `body ≤5000`, `is_published`, картинка `png|jpg|jpeg ≤2 МБ, 64×64…4096×4096`, файл `news/<club|platform>/<random40>.<ext>` на диске `local`; публичная выдача `GET /news-images/{news}/{file}` (`routes/web.php` ~141–143).
- [ ] Агент: `GET /api/guest/news` (`Api/GuestNewsController.php`, throttle 30/мин) → `{platform: [...], club: [...]}` (по `GuestNewsController::LIMIT = 10` последних опубликованных).
- [ ] Go: перенос как есть; файлы — локальный диск/объектное хранилище (S3-совместимое) — Q-I7. Внешних сервисов нет.

## 60.8. Почта, SMS

- [ ] **Почта не используется**: `MAIL_MAILER=log`, в `app/` нет `Mail::`, `Notification::`, `Mailable`. Конфиги `config/mail.php`, `config/services.php` (postmark, resend, ses, slack) — шаблон Laravel. Приглашения сотрудников (`/invitations/{token}`) передаются ссылкой вручную.
- [ ] **SMS нет**: все коды — через Telegram Gateway.
- [ ] Go: не переносить; заложить интерфейс `Notifier` на будущее (Q-I6).

## 60.9. Прочее, касающееся интеграций

- [ ] **`GET /api/settings/{section}/{key}`** и экран `/settings/api` — справочник URL для чтения настроек и товаров мобильным приложением (`SettingsController::api()`, `showSetting()`). Q-S3.
- [ ] **`GET /api/products`** — товары для мобильного приложения (любая роль клуба, без пагинации; `docs/integration-api.md` раздел 1). Относится к API мобилок — отложено.
- [ ] **Swagger UI** `/docs/api` (`resources/views/api-docs.blade.php`) — `swagger-ui-dist@5.17.14` с jsDelivr с SRI-хэшами; в Go — отдать ту же версию (лучше вшить через `embed.FS`, без CDN).

## 60.10. Сводка

| # | Интеграция | Внешняя сторона | Секреты (имя/место) | Перенос |
|---|---|---|---|---|
| 1 | Telegram Gateway: восстановление пароля | gatewayapi.telegram.org | `platform_settings.telegram_gateway.token` (Crypt/APP_KEY) | как есть |
| 2 | Telegram Gateway: код перевода другу | то же | то же | вместе с мобилками (Q-I3) |
| 3 | Telegram-бот алертов | api.telegram.org | `platform_settings.telegram_alerts.token` | как есть |
| 4 | Заглушка `/api/guest/telegram` | — | — | не переносить |
| 5 | Исходящие вебхуки | URL клиента | `webhook_endpoints.secret` (encrypted/APP_KEY) | как есть (Q-I1) |
| 6 | API v1 + ключи + scopes + идемпотентность + лимиты + журнал | внешние системы клуба | `api_keys.key_hash` (sha256) | решение Q-I1 |
| 7 | Обновления агента | GitHub Releases | — (sha256), подписи нет | переделать с Ed25519 (Q-I2) |
| 8 | Переводы (каталог, CSV, sync, агент) | — | — | как есть |
| 9 | Мониторинг сервера/клубов | TLS-хосты | — | частично заменить метриками |
| 10 | Новости | — | — | как есть |
| 11 | Почта/SMS | — | `MAIL_*` не используются | не переносить |
| 12 | `/api/settings/*` для мобилок | — | — | Q-S3 |

Итого: **12 интеграционных узлов** (из них внешних сервисов — 3: Telegram Gateway, Telegram Bot API, GitHub Releases; плюс исходящие вебхуки к произвольным URL).
