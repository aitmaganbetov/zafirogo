# Бэклог: решено, но ещё не сделано

Источник — решения 30.10.2026 ([DECISIONS.md](DECISIONS.md), строки «Решение (30.10.2026)» в
[OPEN_QUESTIONS.md](OPEN_QUESTIONS.md) и `open-questions/*.md`). Формат: ID вопроса — что сделать.
Сюда не входит то, что реализуется параллельно 30.10.2026: Q-002, CQ-3, Q-010, Q-011, Q-012, Q-016, SV-2, SV-9, RQ-6,
Q-028, L-1, Q-031, Q-037, Q-052, RQ-3, подпись обновлений агента Ed25519.

## Этап 3 — экраны

- Q-015, CSQ-3, RQ-7 — касса: печать Z-отчёта перед закрытием смены (`afterprint` → nonce); nonce перенести в Valkey, колонку `z_report_nonce_hash` удалить.
- Q-019 — «НДС» в кассе и предпросмотр гостевой сессии — общий TS-модуль в тиынах с тестом против серверной формулы; итог — с сервера.
- Q-024 — экран настроек клуба: `session_idle_timeout` и `session_auto_terminate_when_pc_unavailable` хранятся и показываются как в старом.
- Q-033 — вход и регистрация в панели: нормализация телефона по `PhoneNumber::normalize()` (ведущая 8 принимается).
- Q-062, Q-063 — WebSocket шлёт готовые изменения данных; «живые» бейджи «Чат» и «Заказы»; `location.reload()` не переносить.
- Q-064 — шрифты и иконки из npm (`@fontsource/inter`, `@fortawesome/fontawesome-free@6.4.0`, Chart.js), CSP `font-src 'self'`, без CDN.
- Q-065 — импорт переводов: плейсхолдеры Laravel → i18next при импорте; редактор суперадминки показывает формат Laravel.
- Q-066 — поле «Поиск...» в верхней панели — как декорация.
- Q-067, PD-3 — форма прихода/расхода кассы поверх REST; `club-map.align` и `warehouse.products.update` не переносить.
- Q-069, stage2-auth §8 — роутинг SPA по хосту; проверка «Здесь входят только суперадмины Zafiro.» на `SUPERADMIN_DOMAIN`, домен wiki.
- Q-070 — касса и склад: поиск клиента/товара с подгрузкой вместо выпадающих списков.
- RQ-1, CSQ-8 — общий валидатор форм: все ошибки полей сразу, как FormRequest.
- RQ-11 — карта клуба: `classes`/подсветка, `session_tariff` («Поминутно: :count мин.»), `agent_outdated`, `staff_kind`.
- RQ-12 — история баланса клиента: заголовки `GuestHistory` («Возврат пополнения на кассе» и т.п.) по кодам операций.
- W-3, EV-7, CQ-5, SV-10, RQ-8 — журнал действий и описания кассы: хранить ключ + параметры (`details._t`), переводить при показе.
- stage2-auth §5 — ключ перевода для 429 «Слишком много попыток».
- EV-11 — переводы kk/en для «Слишком много подключений.».
- stage2-auth «Сверка с permissions.ts» — SPA берёт `me.permissions` из `/api/auth/me` вместо своего расчёта.
- GG-4, PD-12 — суперадминка переводов: версия растёт при любой правке (включая удаление), после правки — команда `TranslationsChanged`.
- GG-10 — страница `/pc-login/{code}` → `ApproveQrLogin`; узкая RLS-политика поиска открытого QR по коду.
- GG-11 — маршруты картинок: `/club-logos/{club}/{file}`, `/club-signin-images/{club}/{file}`, `/news-images/{news}/{file}`.
- Коды регистрации (DECISIONS «Агент и безопасность») — эндпоинты и экран панели: выдача кодов, список ПК, «Перерегистрировать», «Отключить ПК» (по роли).

## Этап 4 — интеграции (v1, вебхуки, Telegram, мобилки, PWA)

- Q-056 — `/api/v1` 1:1 по `openapi-v1.yaml`; дополнить спецификацию (`webhooks:`, права, пагинация `/api/products`) до генерации; проверить в проде `api_keys.last_used_at`.
- Q-057, EV-1 — вебхуки: outbox → доставки, прежние подпись и `event_id`, повторы `[10, 60, 300, 1800, 7200]` с, отключение после 20 ошибок.
- Q-053 — хэш-цепочка аудита по клубу и экспорт аудита.
- Q-058 — совместимый слой старого REST для `mobileemployee`/`mobileclient` и «адрес сервера по клубу» в приложениях.
- Q-046 — мобильный API: вход без клуба выбирает членство по `id`.
- Q-060 — Telegram-бот алертов платформы; состояние алертов в Valkey; журнал ошибок — кольцевой буфер slog.
- GG-5, L-2, DECISIONS «Telegram» — восстановление пароля и коды перевода через Telegram (`RequestPasswordResetCode`, `ResetPassword`, `RequestTransferCode`); лимиты 60 с / 5 в час на аккаунт.
- GG-10 — подтверждение QR-входа из мобильного приложения.
- Q-068 — PWA: статический `manifest.webmanifest`, service worker с путями `/assets/`, статический `offline.html`, регистрация; баннер установки.

## Перенос данных (tools/migrate-from-mysql)

- Q-003 — JSON (`receipt_data`, `shop_orders.items`, уровни лояльности/бонуса) → формат v2: тиыны, базисные пункты, `"v": 2`.
- Q-004, PQ-8 — отрицательные балансы — входящим остатком с пометкой в отчёте; `CHECK (balance >= 0)` — после проверки данных.
- Q-005 — пересчёт балансов из истории старой БД и отчёт расхождений по клубу.
- Q-007 — `tariff_sales.sold` = число `activity_logs` `tariff.purchased` по тарифу.
- Q-022 — клуб `legacy`: содержимое `storage/app/settings.json` → `clubs.settings` (нет файла — дефолты).
- Q-023 — нормализация настроек («30 сек» → число, черновые ключи) с отчётом о неизвестных ключах.
- Q-025 — проверка дублей без учёта регистра (`users.phone`, `clubs.slug`, `products(club_id, sku)` и др.) перед переносом.
- Q-026 — распределение значений всех полей с CHECK; неизвестные — в отчёт.
- Q-027 — старые не-сотруднические роли `club_memberships.role` → `client`.
- Q-029 — старые id сохраняются; `setval` выше максимального перенесённого id.
- Q-030 — проверка отсутствия NULL в `created_at`.
- Q-050, EV-2 — секреты `APP_KEY` (вебхуки, токены Telegram) — расшифровка старым ключом и шифрование новым; ключ установки клуба не переносить.
- Q-051 — bcrypt-хэши паролей и PIN — как есть; коды восстановления и перевода не переносить.
- Q-077 — инструмент живёт в `server/cmd/migrate-from-mysql`; ссылки на `tools/migrate-from-mysql` в `CLAUDE.md` поправить (решает владелец).
- Q-083 — `server_metrics`, `request_metrics` не переносить; `club_request_metrics`, `club_load_snapshots` — по желанию.
- BQ-3 — уровни лояльности/бонуса — список без равных порогов, прочее — в отчёт.
- CSQ-2 — `cash_transactions.income_kind` старых приходов по `activity_logs` (`top_up`, `guest_session`).
- CQ-9 — `product_id` в старые чеки, где имя товара однозначно.
- SV-3 — `session_purchases.zone_id`/`source` частей текущих сессий из `activity_logs`.
- Q-058 — порядок перевода: клубы с активными мобилками — последними.
- L-7, EV-10, PD-8 — партиции `api_request_logs`, `webhook_deliveries` (с отдельной таблицей ключей дедупликации), `platform_logs`, метрик, `club_load_snapshots`, `chat_messages` — до первого перевода клуба; инструмент пишет сразу в партиции.

## Эксплуатация

- Q-071, A2 §8 — HTTP за прокси (HSTS на прокси), gRPC — отдельный порт с mTLS (L4 passthrough), реальный IP — PROXY protocol v2 от доверенного балансировщика.
- Q-072 — Dockerfile (distroless/static, один бинарник) и CD; Dependabot для docker.
- Q-055 — `http.MaxBytesReader` 1 MiB для JSON; rate limit на Valkey по маршрутам Laravel (`docs/MIGRATION.md`).
- Q-079 — открыть `vuln.go.dev` в прокси сред агентов; обновить `golangci-lint` в образе.
- Q-080, PD-4 — cooldown Dependabot 14 дней; `grpc` v1.85.0 → последняя v1.84.x.
- Q-082 — Postgres 18.x во всех окружениях.
- Q-083 — страница «Сервер»: `server_metrics` с `instance_id` и `/metrics` (Prometheus).
- Q-085 — файлы (логотипы, картинки входа, новости) — S3-совместимое хранилище (MinIO), имена сохранить.
- Q-032 — после переходного периода удалить поля команд из `club_computers` отдельной миграцией.
- L-6, SV-1 — чистка ключей идемпотентности (`ledger_idempotency`, `billing_requests`) старше 13 месяцев ролью владельца.
- L-8, PD-9 — роль-владелец миграций в проде (CREATEROLE, не суперпользователь), data-миграции под `SET ROLE zafiro_platform`; строка в `docs/dev-setup.md`.
- A2 §8 «Очистка» — воркер: коды регистрации старше 30 дней, сертификаты, истёкшие более 90 дней назад.
- A2 §8 «Windows/SChannel» — проверка агента на Windows 10 и 11 (CNG, TLS 1.2 с ECDSA-сертификатом клиента).
- AS-6 — Valkey: primary + реплика, без cluster mode.
- EV-4 — липкие сессии по пользователю на балансировщике (лимит WebSocket-соединений на инстанс).
- CSQ-7 — снимок остатка кассы при закрытии смены, сумма проводок только после снимка.

## Доделки сервера и агента (до первого перевода клуба)

Не относятся к экранам и интеграциям, но решены и не сделаны.

- Q-021, SV-11 — тикер истечения сессий раз в 5–10 с (сейчас раз в минуту).
- Q-047, GG-12 — лимиты `GetTranslations` 30/мин на ПК и `GetProfile` 60/мин на токен.
- Q-054, stage2-auth §2 — опция `ReadOnly` транзакции в режиме просмотра суперадмином.
- CSQ-1 — вид ledger-операции `cash_shift_opening`.
- CSQ-6 — ссылка `reverses_transaction_id` у `pos_refund` (запрос/индекс по `cash_transaction_id`).
- CSQ-9, PD-6 — `cash_transactions.description` → `text`.
- CQ-7 — сортировка «Продано товаров» по тиынам, затем по имени.
- PQ-4 — CHECK `price BETWEEN 0 AND 999999999999` для `tariffs.price`, `club_zones.price_per_hour`.
- SV-7 — поправить комментарий предела в `bonus.AddSpending`.
- A2 §4, AS-7 — `ERROR_CODE_CERTIFICATE_REQUIRED` и `ERROR_CODE_STREAM_REPLACED` в конец enum при следующем изменении proto.
- A2 §6 — код перерегистрации перезаписывает MAC и device_id карточки, если MAC свободен.
- GG-6 — `GetSeatMap` (карта пересадки гостем) по присутствию.
- GG-8, PD-13 — журнал неудачных входов `auth.api_login_failed` во все клубы аккаунта.
- W-1 — `ResultHandler`, `PresenceListener`, `presence.Sweeper.OnOffline`.
- W-2 — `SessionID` в `billingsvc.ExpiredSession`.
- W-4, PD-15 — запросы `platform_logs` (`superadmin.login`, `superadmin.club_viewed`).
- RQ-4, PD-16 — единый формат `computer.state` в `docs/realtime.md`.
- RQ-5 — вид `guest_session` в каталоге `cash.transaction` (`docs/realtime.md`).
- AS-1 — `docs/agent-protocol.md` §2: subject на инстанс вместо `agent.cmd.<club>.<computer>`.
- AS-11 — недостающие поля `ClubConfig` в `Welcome` вместе с модулями настроек и переводов.
- MQ-2 — `docs/business-rules.md` §0: формулировка `round()` по PHP 8.4 (без «предокругления»).
- S1-SES-03 — дописать в BR-035 разницу удалённого тарифа при пересадке и запуске пакета.
- Q-043 — таблица «старый код → `ERROR_CODE_*` + gRPC-статус» в `docs/agent-protocol.md`.
