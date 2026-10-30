# Проверка согласованности документов Этапа 0

Рецензент-сводчик, Этап 0. Проверены: `docs/parts/10…60-*.md`, `docs/business-rules.md` (BR), `docs/golden-scenarios.md` (GS),
`docs/schema-changes.md`, `docs/agent-protocol.md`, `docs/open-questions/*.md` (7 файлов), `CLAUDE.md`,
`db/migrations/0001_init.sql`, `proto/agent/v1/*.proto`. Спорные места сверены со старым кодом `/home/user/Zafiro`
(только чтение). Сводный список вопросов владельцу — [`docs/OPEN_QUESTIONS.md`](OPEN_QUESTIONS.md).

Итог: **найдено 15 расхождений**, **исправлено 6** (7 правок в 4 файлах), **9 вынесено в вопросы** (Q-003, Q-015, Q-021,
Q-031, Q-043, Q-047, Q-050, Q-065, Q-086) или в действия для оркестратора.

## 1. Что проверено

### 1.1. Единицы денег

- [x] BR §2 (стр. 17–19, таблица 150–157), GS «Единицы» (стр. 123, 2685), `30-database.md` (стр. 26–45),
  `schema-changes.md` 3.1, `0001_init.sql` (шапка стр. 8–9 и 12 комментариев `'Тиыны (в MySQL decimal…)'`),
  `proto` (`*_tiyn`, `common.proto` стр. 7), `CLAUDE.md` «Деньги» — **согласованы**: в MySQL тенге `decimal(12,2)/(14,2)`
  (12 полей), уже в тиынах `session_purchases.main_cents/bonus_cents`, `transfer_codes.amount_cents`; в Go/Postgres/proto —
  `int64`/`bigint` тиыны, перенос `× 100`. Число полей (12) совпадает в BR, schema-changes и 0001.
- [x] Термины «центы» (BR, по коду PHP `cents`) и «тиыны» (GS, 0001, proto) — синонимы, BR стр. 17 это оговаривает.
- [ ] **Не согласовано:** суммы внутри JSON. 0001 (`cash_transactions.receipt_data`, `activity_logs.details` — «тенге»),
  BR-052 (стр. ~928: «в `receipt_data` сохранить числа как сейчас (float в JSON)»), `40-settings.md` (уровни
  `spent`/`amount` — тенге, int|float) против `open-questions/database.md` В-2 и Q-S7 (рекомендуют перевести в тиыны с
  версией формата) и правила `CLAUDE.md` «никогда float». → **Q-003**.
- [x] Пересчёт 8 контрольных чисел GS вручную (GS стр. 161, 352, 395, 404, 2693, 2734, 2743, 2785) по формулам
  `TopUpBonus::for()`, `discounted()`, `minutesPriceInCents()`, `SessionSettlement::settle()`, `PaymentRules::bonusLimitCents()` —
  совпадают.

### 1.2. Числа и лимиты

- [x] Таблица именованных лимитов `20-api.md` 1.1 — сверена построчно с `app/Providers/AppServiceProvider.php` `boot()`
  (стр. 47–97): все 13 лимитеров совпадают (включая `perMinutes(10, 5)` = 5 за 10 мин у `agent-register`).
- [x] Лимиты `throttle:N,1` в `10-screens.md` (стр. 47–50), BR (BR-030 `10,1`, BR-091 `10,1`, BR-092 `5,1`/`10,1`),
  комментарии `proto/agent/v1/*.proto` у каждого RPC — сверены с `routes/api.php`, `routes/web.php`, `routes/api_staff.php`:
  совпадают, кроме двух новых лимитов в proto (см. 2, п. 5 и вопрос Q-047).
- [x] Ограничения API v1 (`20-api.md` 1.2: 20 неудачных попыток/мин на IP, 120/1000 на ключ, 600 на клуб, 256 КБ, 24 ч
  идемпотентности) — сверены с `config/integrations.php`; GS-299 использует тестовое значение 5 (`IntegrationApiAuthTest.php`
  стр. 182) — не противоречие.
- [x] Сроки кодов: QR-вход 5 мин (`AgentQrLogin::LIFETIME_MINUTES`), коды перевода и восстановления 5 мин / 60 с / 5 в час /
  5 попыток (`FriendTransferCodes`, `PasswordRecovery`), токен Sanctum 30 дней (`QrLogins::TOKEN_DAYS`), команда питания 120 с,
  онлайн 90 с, запись `last_seen_at` раз в 20 с (`ClubComputerSessions::SEEN_WRITE_SECONDS`) — одинаковы в BR, `20-api.md`,
  `10-screens.md`, `50-jobs.md`, proto.
- [x] Новые сроки (код регистрации ПК 15 мин, сертификат 30 дней, токен гостя ≤ 12 ч, keepalive 30 с, `Status` 60 с,
  TTL присутствия 90 с) — одинаковы в `agent-protocol.md` и `enrollment.proto`/`agent.proto`.
- [x] Лимит перевода `Friends::MAX_TRANSFER_CENTS = 100 000 000` — одинаков в BR-091, GS (стр. 2861), `common.proto` стр. 144.
- [x] Частота heartbeat (30 с, служба 60 с, экран входа 15 с) — `20-api.md` раздел 7 и `agent-protocol.md` согласованы
  с `config/club.php` и `HeartbeatService.cs`.
- [ ] **Не согласовано:** частота воркера истечения сессий в Go — BR-033 «раз в минуту», `50-jobs.md` «каждые 5–10 с»,
  `agent-protocol.md` «как в Laravel, каждую минуту». → **Q-021**.

### 1.3. Таблицы, поля, enum

- [x] Скриптом извлечены все ссылки вида `таблица.поле` из BR, GS, `50-jobs.md`, `agent-protocol.md`, `schema-changes.md`,
  `40-settings.md` и сверены с колонками `CREATE TABLE` в `0001_init.sql`: **62 различные пары таблица.поле существуют**
  (в т.ч. все денежные: `member_balances.balance/bonus_balance`, `club_memberships.total_spent/discount`, `tariffs.price`,
  `club_zones.price_per_hour`, `products.price`, `club_services.price`, `shop_orders.amount`, `cash_shifts.opening_balance/
  closing_balance`, `cash_transactions.amount/refund_of_id/type`, `session_purchases.main_cents/bonus_cents/kind`,
  `transfer_codes.amount_cents/attempts`, `saas_plans.price_monthly`, `time_packages.minutes_left`). 24 «несовпадения» —
  имена маршрутов и прав (`tariffs.delete`, `news.view`, `users.store`…), ключ кэша `api_keys.meta.*` и `translations."group"` —
  ложные срабатывания.
- [x] Все 17 CHECK-ограничений enum в 0001 (`club_computers.status/agent_state/power_command/staff_kind`,
  `session_purchases.kind`, `shop_orders.status`, `cash_shifts.status`, `cash_transactions.type`, `stock_movements.type`,
  `tariffs.payment_method`, `club_subscriptions.status`, `club_zones.icon/theme/status`, `club_invitations.role`,
  `webhook_deliveries.status`, `request_metrics.channel`) сверены с BR и кодом (`ClubComputer::AGENT_STATES`,
  `SessionPurchase::KIND_*`, BR-075, BR-130) — совпадают.
- [x] Опечатки в именах таблиц — найдены и исправлены (2, п. 3).

### 1.4. Права и роли

- [x] Матрица BR-140 сверена с `app/Support/ClubPermissions.php` `SECTIONS` (стр. 23–44, `STAFF`/`DESK`/`MANAGERS`) —
  совпадает по всем 28 строкам.
- [x] Права маршрутов `/api/staff/*` в `20-api.md` (стр. 120–145) сверены с `routes/api_staff.php` — совпадают
  (кроме формулировки про чат, исправлено — 2, п. 2).
- [x] `10-screens.md` «Общие правила доступа» и BR-144 (`RequireClubArea::ROUTES`) — согласованы; `club-role:owner,admin`
  для прав сотрудников, `can:manage-integrations` — одинаково в BR и `10-screens.md`.
- [x] `ClubRoles::STAFF/SHIFT`, `ClubComputerSessions::STAFF_ROLES`, `StaffPermissions::REPORTS` — одинаково в BR-141/142,
  `20-api.md`, GS (ОВ-9).

### 1.5. Фоновые задачи

- [x] Расписание `50-jobs.md` сверено с `routes/console.php`: 11 задач, частоты и `withoutOverlapping` совпадают;
  `subscriptions:expire` (5 мин, только `active`) — одинаково в `50-jobs.md` и BR (стр. ~1528); триалы — Q-J5 и BR согласованы.
- [x] Истечение сессий без расчёта и без журнала — одинаково в BR-033 (§15 п. 17), GS ОВ-5, `50-jobs.md` — **но** не в
  `20-api.md` (исправлено — 2, п. 1).
- [ ] **Не согласовано:** механизм «один исполнитель на кластер» — `CLAUDE.md` «лидер или JetStream-консьюмер»,
  `50-jobs.md` — Postgres advisory lock. → **Q-086**.

### 1.6. Покрытие proto

- [x] Все 48 маршрутов `routes/api.php` (без `/api/staff/*` и `/api/v1/*`) есть в таблице `agent-protocol.md` §10: 41 — с новым
  RPC, остальные — в списке «не переносятся» с причиной.
- [x] Все RPC из таблицы существуют в `proto/agent/v1/*.proto` (36 `rpc`, 6 сервисов); все поля команд/сообщений из таблицы
  (`reboot`, `shutdown`, `enter_service_mode`, `show_message`, `wake_on_lan`, `update_available`, `start_session`, `end_session`,
  `config_changed`, `translations_changed`, `balance_changed`, `guest_counters_changed`, `chat_message_received`,
  `qr_login_approved`, `Hello.boot_id/capabilities`, `Welcome.translations_version`) объявлены; подпись обновления —
  `signature_ed25519` (`agent.proto` стр. 697).
- [ ] **Не согласовано:** коды ошибок — GS ОВ-7 предлагает сохранить старые строки (`wrong_computer`, `qr_expired`) в
  `ErrorInfo.reason`, а `common.proto` (стр. 22–24) кладёт туда имена enum `ERROR_CODE_*`. → **Q-043**.

### 1.7. Открытые вопросы между собой

- [ ] `database.md` В-5 и `settings-jobs-integrations.md` Q-S8 дают **противоположные** рекомендации по `installation_key` и
  токенам Telegram. → **Q-050**.
- [ ] `business-rules.md` OQ-BR-08 (флаг печати Z в БД `cash_shifts.z_printed_at`) и `screens.md` №11 (nonce/флаг в Valkey
  с TTL) — разные решения. → **Q-015**.
- [ ] `screens.md` №5 (конвертировать `:name` → `{{name}}`) и `settings-jobs-integrations.md` Q-I5 (сохранить формат Laravel +
  свой форматтер) — противоположные рекомендации. → **Q-065**.
- [x] Остальные дубликаты (единица денег, >2 знаков, `legacy`, bcrypt, v1/вебхуки, мобилки, PWA, CDN, TS) согласованы по сути —
  объединены в `OPEN_QUESTIONS.md`.

## 2. Найденные расхождения и исправления

| № | Файл | Было → стало | Проверено по коду | Статус |
|---|---|---|---|---|
| 1 | `docs/parts/20-api.md` стр. 219 | «`expireIfDue` (завершение сессии с расчётом)» → «истёкшая сессия только очищается — поля сессии в NULL, без расчёта возврата» | `app/Services/ClubComputerSessions.php` `expireIfDue()` стр. 177–186 — только `forceFill(SESSION_FIELDS)`, `SessionSettlement` не вызывается | исправлено |
| 2 | `docs/parts/20-api.md` стр. 142 | «отвечать могут cashier/operator (`ChatMessage::ANSWERING_ROLES`, проверка в контроллере)» → «отвечает сотрудник с правом `chat.create` (по умолчанию cashier/operator, владелец — всегда; проверка `ChatController::answers()`…)» | `app/Http/Controllers/ChatController.php` `answers()` стр. 121–124 → `ClubPermissions::allows(…, 'chat.create')`; `ANSWERING_ROLES` нигде не проверяется | исправлено |
| 2a | `docs/business-rules.md` BR-141 | добавлено уточнение: `ANSWERING_ROLES` — константа, фактическая проверка — право `chat.create` | то же | исправлено |
| 3 | `docs/parts/20-api.md` стр. 84 | `members_balances` → `member_balances` | `0001_init.sql`, `app/Models/MemberBalance.php` | исправлено |
| 4 | `proto/agent/v1/common.proto` стр. 194 | `(members_balances)` → `(member_balances)` (комментарий) | то же | исправлено |
| 5 | `proto/agent/v1/guest.proto` стр. 69 | «Лимит: 60/мин на токен.» → добавлено «(новый: у `GET /api/me` в Laravel throttle не было…)» — шапка файла утверждает, что лимиты повторяют старые | `routes/api.php` стр. 33–35 (`/me` без `throttle`) | исправлено; решение — Q-047 |
| 6 | `docs/business-rules.md` BR-150 | «имя уникально в клубе среди не удалённых» → + «но индекс БД `UNIQUE (club_id, name)` учитывает и удалённые → ошибка БД (500)» | `app/Http/Requests/StoreTariffRequest.php` стр. 23 (`whereNull('deleted_at')`) против `database/migrations/2026_10_13_040140_finalize_club_tenancy.php` стр. 33–35 (`unique(['club_id','name'])`) и `0001_init.sql` стр. 444 | исправлено; решение — Q-031 (В-13 описывал только индекс) |
| 7 | суммы в JSON | BR-052 (оставить float) vs В-2/Q-S7 (тиыны) | `CashRegisterService.php` стр. 69, 126 | вопрос Q-003 |
| 8 | воркер истечения сессий | 1 мин (BR-033, agent-protocol) vs 5–10 с (`50-jobs.md`) | `routes/console.php` стр. 34 (`everyMinute`) | вопрос Q-021 |
| 9 | singleton задач | `CLAUDE.md` vs `50-jobs.md` | — (новая архитектура) | вопрос Q-086 |
| 10 | коды ошибок агента | GS ОВ-7 vs `common.proto` | — | вопрос Q-043 |
| 11 | секреты APP_KEY | В-5 vs Q-S8 | `app/Models/Club.php` (cast `encrypted`), `TelegramGateway.php` ~55 | вопрос Q-050 |
| 12 | флаг печати Z | OQ-BR-08 vs screens №11 | `CashRegisterController.php` стр. 137, 150, 183–186 (флаг и nonce в сессии Laravel) | вопрос Q-015 |
| 13 | формат плейсхолдеров | screens №5 vs Q-I5 | — | вопрос Q-065 |
| 14 | лимит `GetTranslations` | proto 30/мин на ПК vs Laravel 600/мин на IP | `routes/api.php` стр. 80–83, `AppServiceProvider.php` | осознанное изменение, подтверждение — Q-047 |
| 15 | `docs/MIGRATION.md` | содержит копию `20-api.md` с ошибками п. 1–3 (стр. 475, 533, 610) | — | **действие оркестратору**: пересобрать `MIGRATION.md` из исправленных `docs/parts/*.md` (файл не мой, не правил) |

## 3. Что осталось

- [ ] Пересобрать `docs/MIGRATION.md` из `docs/parts/` (п. 15).
- [ ] Числа GAP-сценариев (GS «Пробелы покрытия») проверены выборочно (8 из 36) — полная проверка прогоном Pest в копии
  старого проекта, Q-006.
- [ ] `buf lint`/`buf generate` после правки комментариев proto не запускались (изменены только комментарии).
- [ ] `CLAUDE.md` ссылается на `docs/OPEN_QUESTIONS.md` — файл создан; исходные `docs/open-questions/*.md` оставлены
  (решение об удалении — за оркестратором).
- [ ] Не проверялось: соответствие каждого экрана `10-screens.md` маршрутам `routes/web.php` (вне объёма), текст `dev-setup.md`.
