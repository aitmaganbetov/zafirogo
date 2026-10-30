# 30. База данных: инвентаризация схемы Zafiro (MySQL → Postgres)

Источник: все 86 миграций `../Zafiro/database/migrations` (с `0001_01_01_000000_create_users_table.php`
по `2026_11_03_100000_add_staff_message_to_club_computers_table.php`) и 38 моделей `../Zafiro/app/Models`.
Ниже — **итоговая** схема после всех миграций. Postgres-зеркало: `db/migrations/0001_init.sql`;
отличия и новое — `docs/schema-changes.md`; вопросы — `docs/open-questions/database.md`.

Ссылки на миграции даются сокращённо по префиксу имени файла (например `040140` =
`2026_10_13_040140_finalize_club_tenancy.php`).

## Сводка

| Показатель | Значение |
|---|---|
| Миграций | 86 (из них с изменением данных — 12, см. «Миграции данных») |
| Таблиц в MySQL всего | 51 (42 предметные + 8 Laravel-инфраструктуры + служебная `migrations`) |
| Перенесено в `0001_init.sql` | **42** |
| Не переносится | **9** (`sessions`, `password_reset_tokens`, `cache`, `cache_locks`, `jobs`, `job_batches`, `failed_jobs`, `personal_access_tokens`, `migrations`) |
| Моделей Eloquent | 38 (`Concerns/` не считаем) |
| Модели с `BelongsToClub` | 24 (+ тенантные без трейта: `club_subscriptions`, `club_zone_tariff`, `news` с NULL, `club_request_metrics`, `club_load_snapshots`) |
| Таблицы без модели | `club_zone_tariff` (pivot), `api_idempotency_keys`, `server_metrics`, `request_metrics`, `club_request_metrics`, `club_load_snapshots` |
| Модели без своей таблицы | нет |

### Как сейчас хранятся деньги (важно)

- **Основная часть сумм — `decimal(12,2)` / `decimal(14,2)` в ТЕНГЕ** (не в тиынах), cast `decimal:2`:
  `products.price`, `tariffs.price`, `club_zones.price_per_hour`, `club_services.price`, `shop_orders.amount`,
  `member_balances.balance`, `member_balances.bonus_balance`, `club_memberships.total_spent`,
  `saas_plans.price_monthly` — `decimal(12,2)`; `cash_shifts.opening_balance`, `cash_shifts.closing_balance`,
  `cash_transactions.amount` — `decimal(14,2)`.
- **В тиынах (целое) уже хранятся**: `session_purchases.main_cents`, `session_purchases.bonus_cents`,
  `transfer_codes.amount_cents` (`unsignedBigInteger`, cast `integer`).
- Код переводит между ними строковыми функциями `AgentTariffPurchaseService::amountToCents()` /
  `formatCents()` (`app/Services/AgentTariffPurchaseService.php`, ~стр. 385–395) — точно, без float;
  но в `CashRegisterService` встречаются `(float)` и `/ 100` (`app/Services/CashRegisterService.php`,
  ~стр. 126, 133, 169–187) — см. open-questions.
- **Суммы внутри JSON** (`cash_transactions.receipt_data`, `shop_orders.items`, `activity_logs.details`,
  `webhook_deliveries.payload`, `clubs.settings` → уровни лояльности/бонусов) хранятся в тенге
  (строка `"123.45"` или число). В 0001 содержимое JSON не меняется.
- Не деньги, хотя `decimal`: `club_memberships.discount` `decimal(5,2)` — проценты;
  `server_metrics.load_*` `decimal(6,2)` — load average.
- Валюта: `tariffs.currency` (`'KZT'` по умолчанию), настройка клуба `finances.currency` ∈ `KZT, RUB, $`
  (`app/Http/Requests/SaveSettingsRequest.php`, стр. 31). «Тиын» = 1/100 основной единицы валюты клуба.

**Решение в 0001:** денежные `decimal` → `bigint` в тиынах (значение × 100 при переносе, точно),
имена полей сохранены, у каждого поля `COMMENT`. Это смена единицы измерения — зафиксировано
в `docs/schema-changes.md` и вынесено вопросом владельцу (`docs/open-questions/database.md`, В-1).

### Тенантность

Трейт `app/Models/Concerns/BelongsToClub.php`: global scope `club` (`where club_id = CurrentClub::id()`,
без клуба — `1 = 0`), при `creating` подставляет `club_id` или бросает `LogicException`, связь `club()`.
Трейт `Concerns/UsesClubTimezone.php`: время хранится в UTC (`config/app.php` `'timezone' => 'UTC'`),
читается в часовом поясе текущего клуба.

Межтенантная целостность в MySQL держится **составными FK** `(club_id, X_id) → parent(club_id, id)`
(`042959_add_composite_tenant_foreign_keys`, `computer_reservations`), для этого у родителей есть
`UNIQUE (club_id, id)`. В Postgres сохранено 1:1.

### Особенности MySQL, которые нужно учесть

- [ ] Кодировка `utf8mb4`, сопоставление `utf8mb4_unicode_ci` (`config/database.php`, ~стр. 56–57) —
  **регистронезависимые** UNIQUE и сравнения (`users.phone`, `clubs.slug`, `(club_id, sku)`,
  `(club_id, name)` у тарифов/категорий, `languages.code`). Postgres по умолчанию регистрозависим → open-questions В-7.
- [ ] MySQL создаёт индекс под каждый FK автоматически; в Postgres эти индексы созданы явно
  (имена `<table>_<col>_foreign` / имя составного FK).
- [ ] `unsigned` целые: в Postgres нет — тип на ступень шире + `CHECK (col >= 0)`.
- [ ] `timestamp` и `datetime` в MySQL хранят UTC (приложение пишет UTC). Перенос: читать при
  `SET time_zone = '+00:00'`, писать в `timestamptz` как UTC.
- [ ] Laravel `timestamps()` — оба поля `NULL`-абельные и без DEFAULT; так же в 0001.
- [ ] Миграция `042959` пропускается на SQLite (тесты Pest) — значит тесты не проверяли составные FK.

---

## Предметные таблицы (42) — переносятся

### Пользователи, клубы, подписки

- [ ] **`users`** — модель `User` (`HasApiTokens`, `Notifiable`, `UsesClubTimezone`), BelongsToClub: **нет** (глобальная).
  - Поля: `id` bigint uns PK; `name` varchar(255) NN; `phone` varchar(255) NN **UNIQUE**; `email` varchar(255) NULL;
    `email_verified_at` timestamp NULL; `password` varchar(255) NN (bcrypt, cast `hashed`); `remember_token` varchar(100) NULL;
    `created_at`/`updated_at` timestamp NULL; `is_superadmin` bool NN DEFAULT false (`2026_10_15_000000`);
    `blocked_at` timestamp NULL (`2026_10_15_100000`).
  - История: `role`, `discount`, `balance`, `bonus_balance` добавлены в `2026_10_09_000000` и удалены в `040140`
    (перенесены в `club_memberships`/`member_balances`); `telegram_*` добавлены в `2026_10_16_120000`, удалены в `2026_10_18_120000`.
  - Индексы: `users_phone_unique`.
  - Casts: `email_verified_at` datetime, `password` hashed, `is_superadmin` boolean, `blocked_at` datetime.
  - Связи: `memberships()` hasMany ClubMembership; `currentMembership()` hasOne (по `CurrentClub`); `tokens()` (Sanctum).
  - Hidden: `password`, `remember_token`.

- [ ] **`clubs`** — модель `Club` (`UsesClubTimezone`), BelongsToClub: нет (это и есть тенант).
  - Поля: `id`; `name` varchar NN; `slug` varchar NN **UNIQUE**; `settings` json NULL (cast array — все настройки клуба,
    см. MIGRATION.md раздел настроек); timestamps; `installation_key_hash` char(64) NULL UNIQUE; `installation_key_prefix`
    char(12) NULL INDEX (`043934`); `installation_key` text NULL — **зашифрован** `Crypt` (cast `encrypted`, `044406`);
    `locale` varchar(16) NULL (`181908`); `timezone` varchar(64) NN DEFAULT `'Asia/Almaty'` (`060000`);
    `agent_exit_pin_hash` varchar NULL (bcrypt, `SettingsController` ~стр. 70); `agent_exit_pin_changed_at` timestamp NULL (`180000`);
    `agent_staff_minutes` smallint uns NN DEFAULT 15 (`220000`); `logo_path` varchar NULL (`210000`);
    `signin_image_path` varchar NULL (`2026_10_19_120000`).
  - Индексы: `clubs_slug_unique`, `clubs_installation_key_hash_unique`, `clubs_installation_key_prefix_index`.
  - Логика модели: при `creating` генерирует ключ установки (Str::random(64), sha256, префикс 12); при `updating`
    запрещает менять ключ (`LogicException`) — `app/Models/Club.php`, `booted()`.
  - Связи: `memberships()` hasMany; `subscription()` hasOne ClubSubscription.
  - Hidden: `installation_key*`, `agent_exit_pin_hash`, `logo_path`, `signin_image_path`.

- [ ] **`saas_plans`** — модель `SaasPlan`, BelongsToClub: нет.
  - Поля: `id`; `slug` varchar(80) UNIQUE; `name` varchar(120); `price_monthly` decimal(12,2) NULL (**тенге**);
    `trial_days` smallint uns DEFAULT 0; `features` json NULL; `is_active` bool DEFAULT true INDEX; timestamps.
  - Данные: `042815` создаёт план `trial` (14 дней) при отсутствии.
  - Casts: `price_monthly` decimal:2, `trial_days` integer, `features` array, `is_active` boolean. Связь: `subscriptions()` hasMany.

- [ ] **`club_subscriptions`** — модель `ClubSubscription`, BelongsToClub: **нет** (есть `club_id`, но scope не применяется).
  - Поля: `id`; `club_id` FK clubs **CASCADE**, **UNIQUE** (одна подписка на клуб); `saas_plan_id` FK saas_plans **RESTRICT**;
    `status` varchar(30) INDEX; `started_at` timestamp NN; `trial_ends_at`, `current_period_ends_at`, `canceled_at` timestamp NULL;
    `provider` varchar(40) NULL; `provider_reference` varchar NULL; timestamps.
  - Enum `status`: `trialing` (`AuthController.php` ~182), `active`, `expired` (`Console/Commands/ExpireSubscriptions.php` ~29),
    `suspended` (`Superadmin/ClubController.php` ~198). → CHECK.
  - Логика: `isActive()`, `isPastPaidPeriod()`, scope `active` (`app/Models/ClubSubscription.php`).
  - Связи: `plan()`, `club()` belongsTo.

- [ ] **`club_memberships`** — модель `ClubMembership`, BelongsToClub: **да**.
  - Поля: `id`; `club_id` FK clubs CASCADE; `user_id` FK users CASCADE; `role` varchar(255) NN DEFAULT `'Пользователь'`;
    `permissions` json NULL (`2026_10_30`, формат `{"cash.view": true, …}` после `2026_10_31`); `discount` decimal(5,2) DEFAULT 0 (%);
    `total_spent` decimal(12,2) DEFAULT 0 (**тенге**, `2026_10_15_000020`); timestamps; `notifications_seen_at` timestamp NULL (`173104`);
    `can_manage_integrations` bool DEFAULT false (`190912`).
  - Индексы: `UNIQUE (club_id, user_id)` (на него ссылаются составные FK 6 таблиц); FK-индекс `user_id`.
  - Enum `role`: staff — `owner, admin, manager, cashier, operator`; клиент — `client` (`app/Support/ClubRoles.php`);
    **плюс старые не-staff роли** (DEFAULT `'Пользователь'`, бэкфилл `040139` из `users.role`) → CHECK **не ставим** (open-questions В-9).
  - Casts: `discount`, `total_spent` decimal:2; `notifications_seen_at` datetime; `can_manage_integrations` boolean; `permissions` array.
  - Связи: `user()` belongsTo; `balance()` hasOne MemberBalance по `user_id` (+ scope клуба).

- [ ] **`member_balances`** — модель `MemberBalance`, BelongsToClub: **да**.
  - Поля: `id`; `club_id` ubigint NN; `user_id` ubigint NN; `balance` decimal(12,2) DEFAULT 0 (**тенге**);
    `bonus_balance` decimal(12,2) DEFAULT 0 (**тенге**); timestamps.
  - Индексы: `UNIQUE (club_id, user_id)`. FK `member_balances_membership_fk (club_id, user_id) → club_memberships(club_id, user_id)` **CASCADE**.
  - Нет FK `club_id → clubs` напрямую (каскад идёт через membership). Нет CHECK на неотрицательность.
  - Casts: `balance`, `bonus_balance` decimal:2. Связь: `user()`.
  - Сейчас баланс меняется `update`/`decrement` поля (`CashRegisterService`, `AgentShop` ~стр. 70) — в новом стеке только через ledger.

- [ ] **`club_invitations`** — модель `ClubInvitation`, BelongsToClub: **да**.
  - Поля: `id`; `club_id` FK CASCADE; `email` varchar NULL; `role` varchar(40) NN; `token_hash` varchar(64) UNIQUE;
    `invited_by` FK users NULL **SET NULL**; `expires_at` timestamp NN; `accepted_at` timestamp NULL; timestamps.
  - Индексы: `(club_id, accepted_at, expires_at)`, unique `token_hash`.
  - Enum `role`: `admin, manager, cashier, operator` (`ClubInvitationController.php` стр. 27) → CHECK.
  - Casts: `expires_at`, `accepted_at` datetime.

### Склад и касса

- [ ] **`product_categories`** — модель `ProductCategory`, BelongsToClub: **да**.
  - Поля: `id`; `name` varchar NN; timestamps; `club_id` FK clubs CASCADE.
  - Индексы: `UNIQUE (club_id, name)` (было `UNIQUE(name)` до `040140`); `UNIQUE (club_id, id)`; `club_id`.
  - Связь: `products()` hasMany.

- [ ] **`products`** — модель `Product` (`HasFactory`, `SoftDeletes`), BelongsToClub: **да**.
  - Поля: `id`; `sku` varchar NN; `name` varchar NN; `category` varchar NN INDEX (старое текстовое поле категории);
    `product_category_id` ubigint NULL; `price` decimal(12,2) (**тенге**); `quantity` int uns DEFAULT 0; timestamps;
    `club_id` FK CASCADE; `deleted_at` timestamp NULL (`2026_11_01`).
  - Индексы: `UNIQUE (club_id, sku)`, `UNIQUE (club_id, id)`, `club_id`, `(club_id, name)`, `category`.
  - FK: `products_club_category_fk (club_id, product_category_id) → product_categories(club_id, id)` **RESTRICT**
    (раньше nullOnDelete — заменено в `042959`).
  - Casts: `price` decimal:2, `quantity` integer. Связи: `stockMovements()` hasMany, `productCategory()` belongsTo.
  - `quantity` unsigned — уход в минус в MySQL strict = ошибка БД; в Postgres сохранено `CHECK (quantity >= 0)`.

- [ ] **`stock_movements`** — модель `StockMovement`, BelongsToClub: **да**.
  - Поля: `id`; `product_id` NN; `user_id` NULL; `type` varchar NN INDEX; `quantity` int uns; `balance_after` int uns;
    `note` varchar NULL; timestamps; `club_id` FK CASCADE.
  - Индексы: `type`, `(product_id, created_at)`, `club_id`, `(club_id, created_at)`.
  - FK: `stock_movements_club_product_fk (club_id, product_id) → products(club_id, id)` CASCADE;
    `stock_movements_club_user_fk (club_id, user_id) → club_memberships(club_id, user_id)` (NO ACTION — удаление
    участника с движениями запрещено).
  - Enum `type`: `receipt`, `write_off` (`StockMovement::TYPE_*`) → CHECK.
  - Casts: `quantity`, `balance_after` integer. Связь: `product()` belongsTo.

- [ ] **`cash_shifts`** — модель `CashShift`, BelongsToClub: **да**.
  - Поля: `id`; `opened_by`, `closed_by` NULL; `status` varchar INDEX; `open_marker` bool NULL;
    `opening_balance` decimal(14,2) (**тенге**); `closing_balance` decimal(14,2) NULL; `opened_at` timestamp NN;
    `closed_at` NULL; timestamps; `club_id` FK CASCADE.
  - Индексы: `UNIQUE (club_id, open_marker)` — **одна открытая смена на клуб** (`true` у открытой, `NULL` у закрытой);
    `UNIQUE (club_id, id)`; `club_id`; `status`.
  - FK: `cash_shifts_opened_by_membership_fk`, `cash_shifts_closed_by_membership_fk` → `club_memberships(club_id, user_id)`.
  - Enum `status`: `open`, `closed` (`CashShift::STATUS_*`) → CHECK.
  - Casts: балансы decimal:2, `opened_at`/`closed_at` datetime. Связь: `transactions()` hasMany.

- [ ] **`cash_transactions`** — модель `CashTransaction`, BelongsToClub: **да**.
  - Поля: `id`; `cash_shift_id` NN; `refund_of_id` NULL **UNIQUE** FK → `cash_transactions.id` **SET NULL** (`2026_10_24`,
    один возврат на приход); `user_id` NULL; `type` varchar INDEX; `amount` decimal(14,2) (**тенге**); `description` varchar NN;
    `receipt_data` json NULL; timestamps; `club_id` FK CASCADE.
  - Индексы: `type`, `club_id`, `(club_id, created_at)`, unique `refund_of_id`.
  - FK: `cash_transactions_club_shift_fk (club_id, cash_shift_id) → cash_shifts(club_id, id)` CASCADE;
    `cash_transactions_club_user_fk (club_id, user_id) → club_memberships`.
  - Enum `type`: `income`, `expense` → CHECK. Маркер `description = 'Продажа POS'` (`CashTransaction::POS_SALE`).
  - JSON `receipt_data`: `{items: [{product_id?, name, quantity, price, …}]}` для продажи POS или
    `{top_up: {user_id, balance_type: balance|bonus_balance, bonus: <тенге float>}}` (`CashRegisterService.php` ~стр. 69, 126).
  - Casts: `amount` decimal:2, `receipt_data` array. Связи: `shift()` belongsTo, `refund()` hasOne self; `refundKind()`.

- [ ] **`club_services`** — модель `ClubService`, BelongsToClub: **да** (`2026_11_02`).
  - Поля: `id`; `club_id` FK CASCADE; `name` varchar(150); `price` decimal(12,2) (**тенге**); `unit` varchar(30) NULL;
    `is_active` bool DEFAULT true; timestamps. Индекс `(club_id, is_active, name)`.
  - Casts: `price` decimal:2, `is_active` boolean.

### Зоны, ПК, тарифы

- [ ] **`club_zones`** — модель `ClubZone`, BelongsToClub: **да**.
  - Поля: `id`; `name` varchar NN; `description` varchar NULL; `capacity` int uns NULL; `price_per_hour` decimal(12,2) NULL
    (**тенге**, `2026_10_15_170000`; NULL — нет поминутной продажи); `icon` varchar(40) DEFAULT `desktop`; `theme` varchar(20)
    DEFAULT `neutral`; `status` varchar(20) DEFAULT `available` INDEX; `is_featured` bool DEFAULT false; `sort_order` int uns
    DEFAULT 0 INDEX; `position_x`/`position_y` int uns DEFAULT 20; `width` DEFAULT 320; `height` DEFAULT 180 (`104240`);
    `rotation` tinyint uns DEFAULT 0 (`123811`); timestamps; `club_id` FK CASCADE.
  - Индексы: `status`, `sort_order`, `club_id`, `UNIQUE (club_id, id)`.
  - Enum (`app/Http/Requests/StoreClubZoneRequest.php`, стр. 27–29; `UpdateClubZoneLayoutRequest.php` стр. 28):
    `icon` ∈ `crown, desktop, gamepad, mug-hot, couch, dice, door-open, bong, smoking`;
    `theme` ∈ `green, neutral, yellow, purple, blue`; `status` ∈ `available, busy, service`; `rotation` ∈ `0, 90` → CHECK (кроме rotation: только ≥ 0).
  - Casts: целые, `price_per_hour` decimal:2, `is_featured` boolean. Связи: `computers()` hasMany (order by number);
    `tariffs()` belongsToMany через `club_zone_tariff` c `wherePivot('club_id', current)`.

- [ ] **`club_computers`** — модель `ClubComputer`, BelongsToClub: **да**. Самая «горячая» таблица (heartbeat).
  - Поля: `id`; `club_zone_id` NN; `number` int uns NN; `status` varchar(20) DEFAULT `available` INDEX;
    `power_command` varchar(16) NULL, `power_command_at`, `wake_requested_at` timestamp NULL (`2026_10_21`);
    `is_reserved` bool DEFAULT false (`125823`); `position_x`/`position_y` int uns DEFAULT 5; timestamps;
    `last_seen_at` timestamp NULL INDEX (`2026_10_09_000002`); `club_id` FK CASCADE;
    `device_name`, `display_name`, `device_id` varchar(255) NULL; `agent_state` varchar(20) NULL; `agent_version` varchar(50) NULL;
    `agent_update_version` varchar(20) NULL; `agent_update_offers` tinyint uns DEFAULT 0; `agent_update_offered_at` NULL (`2026_10_20`);
    `current_session_id` varchar(255) NULL (UUID-строка); `current_session_user_id` FK users NULL **SET NULL**;
    `session_guest` bool DEFAULT false; `session_ends_at` NULL; `staff_user_id` FK users NULL SET NULL; `staff_kind` varchar(16) NULL;
    `staff_until` NULL (`220000`); `mac_address` varchar(17) NULL; `local_ip`, `remote_ip` varchar(45); `cpu_model`, `gpu_model`,
    `os_version` varchar; `cpu_cores` smallint uns; `ram_total_mb` int uns; `hardware_reported_at` (`072421`);
    `agent_token_hash` char(64) NULL UNIQUE; `agent_token_issued_at` NULL (`150000`); `staff_message` varchar(300) NULL;
    `staff_message_at` NULL (`2026_11_03`).
  - Индексы: `UNIQUE (club_zone_id, number)`; `UNIQUE (club_id, id)`; `UNIQUE (club_id, device_id)` (глобальный unique
    `device_id` снят в `075615`); `UNIQUE (club_id, mac_address)` (`2026_10_17_100000`, MAC нормализуется `Support/MacAddress`);
    `UNIQUE agent_token_hash`; `(club_id, device_name)`; `(club_id, wake_requested_at)`; `status`; `last_seen_at`; `club_id`.
  - FK: `club_computers_club_zone_fk (club_id, club_zone_id) → club_zones(club_id, id)` CASCADE; users SET NULL ×2.
  - Enum: `status` ∈ `available, online, offline, restarting` (`ClubComputerSessions.php` ~95, `ClubComputerController.php` ~178,
    `Api/V1/ComputerController.php` ~80); `agent_state` ∈ `idle, in_session, locked, shutting_down` (`ClubComputer::AGENT_STATES`);
    `power_command` ∈ `restart, shutdown, service` (`ComputerPower::COMMANDS`); `staff_kind` ∈ `pin, panel, signin`
    (`AgentExitPinController` ~48, `ClubComputerController` ~191, ~500) → CHECK. Вычисляемый статус `reserved`/`online`/`offline`
    — в `currentStatus()`, в БД не пишется.
  - Константы: `HEARTBEAT_TIMEOUT_SECONDS = 90`.
  - Casts: целые, booleans, 10 полей datetime. Hidden: `agent_token_hash`.
  - Связи: `zone()`, `staffUser()`, `sessionUser()` belongsTo; `reservations()` hasMany.
  - Производительность: `last_seen_at`, `remote_ip`, `status='online'` пишутся на **каждый heartbeat**
    (`ClubComputerSessions.php` ~стр. 95) — в новом стеке в Valkey.

- [ ] **`tariffs`** — модель `Tariff` (`SoftDeletes`), BelongsToClub: **да**.
  - Поля: `id`; `name` varchar(100) NN; `description` text NULL; `duration_minutes` int uns INDEX; `price` decimal(12,2)
    (**тенге**); `is_active` bool DEFAULT true INDEX; timestamps; `payment_method` varchar(32) DEFAULT `main_balance_only`;
    `currency` varchar(10) DEFAULT `KZT`; `discount_enabled` bool DEFAULT false; `user_roles` json NULL;
    `usage_limit_enabled` bool DEFAULT false; `usage_limit_minutes` int uns NULL; `quantity_limit_enabled` bool DEFAULT false;
    `quantity_limit` int uns NULL; `validity_periods` json NULL; `sale_periods` json NULL (`2026_10_26`); `club_id` FK CASCADE;
    `deleted_at` NULL (`2026_10_29`).
  - Удалены: `color`, `club_name` (`2026_10_28`); `sale_starts_at`/`sale_ends_at` (добавлены `2026_10_25`, удалены `2026_10_26`).
  - Индексы: `UNIQUE (club_id, name)` (учитывает и удалённые тарифы!), `UNIQUE (club_id, id)`, `club_id`, `duration_minutes`, `is_active`.
  - Enum `payment_method` ∈ `main_balance_only, main_or_bonus` (`StoreTariffRequest.php` стр. 28) → CHECK.
  - JSON `validity_periods`/`sale_periods`: `[{start_day: mon..sun, start_time: "HH:MM", end_day, end_time}]`
    (`StoreTariffRequest.php` стр. 37–45, `Support/WeeklyPeriods`; исправление данных `2026_10_27`). `user_roles`: список ролей.
  - Casts: целые, `price` decimal:2, booleans, 3 array. Связи: `zones()` belongsToMany с pivot `club_id`. Методы `onSale()`, `inValidityPeriod()`.

- [ ] **`club_zone_tariff`** — pivot, модели нет; тенантный (есть `club_id`).
  - Поля: `club_zone_id`, `tariff_id`, `club_id` NN. PK `(club_zone_id, tariff_id)`; индекс `club_id`.
  - FK: `(club_id, club_zone_id) → club_zones` CASCADE; `(club_id, tariff_id) → tariffs` CASCADE; `club_id → clubs` CASCADE.

### Сессии гостей, пакеты, брони, QR-вход

- [ ] **`session_purchases`** — модель `SessionPurchase`, BelongsToClub: **да** (без UsesClubTimezone).
  - Поля: `id`; `club_id` FK CASCADE; `session_id` varchar(36) (UUID сессии ПК); `user_id` FK users CASCADE; `kind` varchar(10);
    `tariff_id` FK tariffs NULL SET NULL; `name` varchar; `minutes` int uns; `main_cents`, `bonus_cents` ubigint DEFAULT 0
    (**тиыны**); `expires_at` datetime NULL; timestamps.
  - Индексы: `(club_id, session_id)`, `created_at`.
  - Enum `kind` ∈ `tariff, minutes` (`SessionPurchase::KIND_*`; запуск пакета пишется как `tariff`,
    `AgentTariffPurchaseService.php` ~191) → CHECK.
  - Это «состав» платной сессии для возврата (`SessionSettlement.php`).
  - **Не история:** строки старше 8 дней (кроме идущих сессий) удаляются ежедневно, истёкшие `time_packages` — тоже
    (`routes/console.php`, `session-purchases:prune`). Долговременной истории денег за время в БД нет — только `activity_logs`.

- [ ] **`time_packages`** — модель `TimePackage`, BelongsToClub: **да**.
  - Поля: `id`; `club_id` FK CASCADE; `user_id` FK CASCADE; `tariff_id` FK NULL SET NULL; `name`; `minutes_left` int uns;
    `expires_at` datetime NN; timestamps. Индекс `(club_id, user_id, expires_at)`.
  - Связь: `tariff()` belongsTo **withTrashed**.

- [ ] **`computer_reservations`** — модель `ComputerReservation`, BelongsToClub: **да**.
  - Поля: `id`; `club_id` FK CASCADE; `club_computer_id` ubigint NN; `user_id` FK NULL SET NULL; `customer_name` varchar(120) NULL;
    `customer_phone` varchar(20) NULL; `note` varchar(500) NULL; `starts_at`, `ends_at` datetime NN; `arrived_at`, `cancelled_at`
    datetime NULL; `created_by` FK users NULL SET NULL; timestamps.
  - Индексы: `(club_id, starts_at)`, `(club_computer_id, starts_at)`.
  - FK: `computer_reservations_club_computer_fk (club_id, club_computer_id) → club_computers(club_id, id)` CASCADE.
  - Состояние вычисляется: `pending/active/finished/arrived/cancelled` (`state()`); scopes `pending`, `overlapping`.

- [ ] **`agent_qr_logins`** — модель `AgentQrLogin` (`HasUuids`), BelongsToClub: **да**.
  - Поля: `id` **uuid** PK; `club_id` FK CASCADE; `club_computer_id` FK club_computers CASCADE (простой FK);
    `code_hash` varchar(64) UNIQUE; `approved_user_id` FK users NULL **CASCADE**; `approved_at`, `consumed_at` datetime NULL;
    `expires_at` datetime NN INDEX; timestamps. `LIFETIME_MINUTES = 5`.

### Гости: друзья, переводы, чат, заказы, восстановление пароля

- [ ] **`friendships`** — модель `Friendship`, BelongsToClub: **да**.
  - Поля: `id`; `club_id` FK CASCADE; `requester_id`, `addressee_id` FK users CASCADE; `accepted_at` datetime NULL; timestamps.
  - Индексы: `UNIQUE (club_id, requester_id, addressee_id)`, `(club_id, addressee_id)`. Scope `involving`.

- [ ] **`transfer_codes`** — модель `TransferCode`, BelongsToClub: **нет** (нет `club_id`; клуб — через `friendship_id`).
  - Поля: `id`; `user_id` FK CASCADE; `friendship_id` FK CASCADE; `amount_cents` ubigint (**тиыны**); `code_hash` varchar (bcrypt);
    `attempts` tinyint uns DEFAULT 0; `expires_at` datetime NN; timestamps. Индекс `(user_id, created_at)`.

- [ ] **`chat_messages`** — модель `ChatMessage`, BelongsToClub: **да**.
  - Поля: `id`; `club_id` FK CASCADE; `customer_id` FK users CASCADE; `author_id` FK users NULL SET NULL; `from_staff` bool NN;
    `body` text; `club_computer_id` FK NULL SET NULL; `read_at` datetime NULL; timestamps.
  - Индексы: `(club_id, customer_id, id)`, `(club_id, from_staff, read_at)`. `MAX_LENGTH = 1000`, `ANSWERING_ROLES = cashier, operator`.

- [ ] **`shop_orders`** — модель `ShopOrder`, BelongsToClub: **да**.
  - Поля: `id`; `club_id` FK CASCADE; `user_id` FK CASCADE; `club_computer_id` FK NULL SET NULL; `computer_name` varchar NULL;
    `items` json NN (`[{product_id, name, quantity, price: "0.00", total: "0.00"}]`, `AgentShop.php` ~стр. 55–62);
    `amount` decimal(12,2) (**тенге**); `status` varchar(20) DEFAULT `new`; `handled_by` FK users NULL SET NULL; `handled_at` datetime NULL; timestamps.
  - Индексы: `(club_id, status, id)`, `(club_id, id)`. Enum `status` ∈ `new, done, cancelled` → CHECK.

- [ ] **`password_recovery_codes`** — модель `PasswordRecoveryCode`, BelongsToClub: нет.
  - Поля: `id`; `user_id` FK CASCADE; `code_hash` varchar (bcrypt); `attempts` tinyint uns DEFAULT 0; `expires_at` datetime NN; timestamps.
    Индекс `(user_id, created_at)`.

### Новости, задачи

- [ ] **`news`** — модель `News`, BelongsToClub: **нет намеренно** (`club_id NULL` = новость платформы; scopes `platform()`, `ofClub()`, `published()`).
  - Поля: `id`; `club_id` FK NULL CASCADE; `title` varchar(150); `body` text; `image_path` varchar NULL; `is_published` bool DEFAULT true;
    `created_by` FK NULL SET NULL; timestamps. Индекс `(club_id, is_published, created_at)`. `PAGE = 20`.

- [ ] **`todo_tasks`** — модель `TodoTask`, BelongsToClub: **да**.
  - Поля: `id`; `club_id` FK CASCADE; `title` varchar(200); `due_on` **date** (день клуба, cast не задан — строка Y-m-d);
    `created_by`, `done_by` FK NULL SET NULL; `done_at` datetime NULL; timestamps. Индекс `(club_id, due_on)`.

### Журналы

- [ ] **`activity_logs`** — модель `ActivityLog`, BelongsToClub: **да**. Журнал действий клуба (растёт).
  - Поля: `id`; `event` varchar(60) INDEX; `description` text; `user_id` **NN** (`171650`); `subject_type` varchar(100) NULL
    (класс модели); `subject_id` ubigint NULL; `details` json NULL; `ip_address` varchar(45) **NN** (`171650`); `user_agent` text NULL;
    timestamps; `club_id` FK CASCADE.
  - Индексы: `event`, `(created_at, event)`, `(subject_type, subject_id)`, `club_id`, `(club_id, created_at)`, `(club_id, event, created_at)`.
  - FK: `activity_logs_club_user_fk (club_id, user_id) → club_memberships` (NO ACTION).
  - `event`: ~100 значений в `ActivityLog::EVENT_LABELS` — CHECK **не ставим** (открытый список).
  - Используется как источник данных: бэкфилл лояльности `2026_10_18_100000` читает `tariff.purchased`, `time.purchased`,
    `shop.purchased`, `shop.order_cancelled`; `GuestHistory` строит историю гостя по журналу.

- [ ] **`platform_logs`** — модель `PlatformLog`, BelongsToClub: нет (действия суперадмина).
  - Поля: `id`; `user_id` FK NULL SET NULL; `club_id` FK NULL SET NULL; `event` varchar(64); `description` varchar(500);
    `subject_type`/`subject_id` (nullableMorphs, индекс); `details` json; `ip_address` varchar(45) NN; `user_agent` varchar(500) NULL; timestamps.
  - Индексы: `created_at`, `(club_id, created_at)`, `(subject_type, subject_id)`. `EVENT_LABELS` — 28 событий.

- [ ] **`platform_settings`** — модель `PlatformSetting` (PK строка `key`), BelongsToClub: нет.
  - Поля: `key` varchar(64) PK; `value` json NULL; `updated_by` FK users NULL SET NULL; timestamps.
  - Известные ключи: `agent_release` (`PlatformSetting::AGENT_RELEASE`), `telegram_gateway` (`{token: <Crypt>}`,
    `Services/TelegramGateway.php` ~55), `telegram_alerts` (`{token: <Crypt>, chat_id, locale}`, `Services/Monitoring/TelegramAlerts.php` ~75);
    `telegram_bot` удалён (`2026_10_18_120000`). Значения кэшируются навсегда (`Cache::rememberForever`).

### Локализация

- [ ] **`languages`** — модель `Language`, BelongsToClub: нет. Поля: `id`; `code` varchar(16) UNIQUE; `name` varchar(64);
  `is_enabled` bool DEFAULT true; `is_system` bool DEFAULT false; `sort_order` smallint uns DEFAULT 0; timestamps.
  Сид в миграции `181908`: `ru`, `kk`, `en` (system) + `translations:sync`. Связь `translations()` hasMany.
- [ ] **`translations`** — модель `Translation`, BelongsToClub: нет. Поля: `id`; `language_id` FK CASCADE; `group` varchar(64)
  DEFAULT `'*'`; `key` text; `key_hash` char(40) (`sha1(group|key)`); `value` text; timestamps.
  Индекс `UNIQUE (language_id, group, key_hash)`. В Postgres `group` — зарезервированное слово, в SQL экранируется `"group"`.

### Интеграционный API и вебхуки (`2026_10_13_190912_create_integration_api_tables`)

- [ ] **`api_keys`** — модель `ApiKey`, BelongsToClub: **да**. Поля: `id`; `club_id` FK CASCADE; `created_by` FK users (NO ACTION);
  `name` varchar(100); `prefix` varchar(16) UNIQUE; `key_hash` char(64) (Hidden); `scopes` json NN; `allowed_ips` json NULL;
  `rate_limit_per_minute` smallint uns NULL; `expires_at`, `last_used_at` NULL; `last_used_ip` varchar(45); `revoked_at` NULL;
  `revoked_by` FK users NULL (NO ACTION); `rotated_from_id` FK api_keys NULL SET NULL; timestamps. Индекс `(club_id, revoked_at)`.
- [ ] **`api_request_logs`** — модель `ApiRequestLog` (`UPDATED_AT = null`), BelongsToClub: нет. Поля: `id`; `club_id`, `api_key_id`
  ubigint NULL **без FK**; `key_prefix` varchar(16); `method` varchar(8); `endpoint` varchar(191); `status` smallint uns; `duration_ms` int uns;
  `ip` varchar(45); `created_at` timestamp NN DEFAULT CURRENT_TIMESTAMP. Индексы `(club_id, created_at)`, `(api_key_id, created_at)`, `created_at`.
- [ ] **`api_idempotency_keys`** — модели нет (middleware `app/Http/Middleware/Api/ApiIdempotency.php`). Поля: `id`; `api_key_id` FK CASCADE;
  `idempotency_key` varchar(100); `request_hash` char(64); `status` smallint uns NULL; `response` longtext NULL; `created_at` DEFAULT CURRENT_TIMESTAMP.
  Индексы `UNIQUE (api_key_id, idempotency_key)`, `created_at`. Тенант — через ключ API.
- [ ] **`webhook_endpoints`** — модель `WebhookEndpoint`, BelongsToClub: **да**. Поля: `id`; `club_id` FK CASCADE; `created_by` FK users;
  `url` varchar(500); `secret` text (**зашифрован** cast `encrypted`, Hidden); `events` json; `is_active` bool DEFAULT true;
  `failure_count` int uns DEFAULT 0; `last_success_at`, `last_failure_at` NULL; timestamps. Индекс `(club_id, is_active)`.
- [ ] **`webhook_deliveries`** — модель `WebhookDelivery`, BelongsToClub: **да**. Поля: `id`; `club_id` FK CASCADE;
  `webhook_endpoint_id` FK CASCADE; `event_id` uuid UNIQUE; `event` varchar(64); `payload` json; `status` varchar(16) DEFAULT `pending`;
  `attempts` smallint uns DEFAULT 0; `last_status_code` smallint uns NULL; `last_error` varchar(255) NULL; `next_attempt_at`, `delivered_at` NULL; timestamps.
  Индексы `(webhook_endpoint_id, created_at)`, `(status, next_attempt_at)`, `(club_id, status, created_at)`.
  Enum `status` ∈ `pending, delivered, failed, skipped` (`app/Jobs/DeliverWebhook.php` ~41–69) → CHECK. Таблица — очередь доставки
  (outbox): в новом стеке остаётся как outbox, транспорт — NATS JetStream.

### Мониторинг (`2026_10_19_150000_create_monitoring_tables`, `2026_10_19_170000`) — моделей нет

- [ ] **`server_metrics`** — раз в минуту: `minute` int uns UNIQUE (Unix/60); `load_1/5/15` decimal(6,2); `cpu_count`; `memory_*_mb`;
  `disk_*_mb`; `uptime_seconds`; `database_mb`; `queue_pending`, `queue_failed` DEFAULT 0; `queue_oldest_seconds`. Без timestamps.
  Поля `queue_*` описывают очередь Laravel (`jobs`/`failed_jobs`) — в новом стеке смысл меняется (JetStream).
- [ ] **`request_metrics`** — `minute`, `channel` varchar(16), `requests`, `errors`, `duration_ms`; `UNIQUE (minute, channel)`.
  Enum `channel` ∈ `panel, agent, api, guest` (`Services/Monitoring/RequestRecorder::CHANNELS`) → CHECK.
- [ ] **`club_request_metrics`** — `club_id` FK CASCADE, `minute`, `agent_requests`, `api_requests`, `panel_requests`, `guest_requests`,
  `errors`, `duration_ms` (все DEFAULT 0); `UNIQUE (club_id, minute)`, индекс `minute`.
- [ ] **`club_load_snapshots`** — каждые 5 минут: `club_id` FK CASCADE, `captured_at` timestamp NN, `weekday` (1 = пн, пояс клуба),
  `hour`, `computers`, `online`, `busy`; индексы `(club_id, captured_at)`, `captured_at`.

---

## Laravel-инфраструктура — НЕ переносится (9)

- [ ] `sessions` (`0001_01_01_000000`) — веб-сессии админки → **Valkey** (сессии/refresh-токены с TTL) либо stateless access-JWT + refresh.
- [ ] `password_reset_tokens` (`0001_01_01_000000`) — стандартный сброс по e-mail; в коде восстановление идёт через
  `password_recovery_codes` + Telegram Gateway (`Services/PasswordRecovery.php`) → не нужна.
- [ ] `cache`, `cache_locks` (`0001_01_01_000001`) — кэш и блокировки (`Cache::rememberForever` для `PlatformSetting` и др.) → **Valkey**
  (кэш, rate limit, распределённые блокировки `SET NX PX`).
- [ ] `jobs`, `job_batches`, `failed_jobs` (`0001_01_01_000002`) — очередь (сейчас `DeliverWebhook` через `schedule:run`) →
  **NATS JetStream** (стримы + consumer'ы, DLQ вместо `failed_jobs`) и Go-воркеры вместо cron.
- [ ] `personal_access_tokens` (`2026_10_09_000001`, Sanctum; токены гостей/мобилок с abilities `club:<id>`,
  `QrLogins.php` ~66, `Api/AuthController.php` ~114, `AgentRegisterController.php` ~64) → **свои токены**:
  короткий access + ротируемый refresh (таблица refresh-токенов — новая миграция Этапа 1); агент ПК — mTLS-сертификат.
  Старые токены не переносим: гости перелогинятся, агенты переустанавливаются (решение №3 ТЗ).
- [ ] `migrations` (служебная Laravel) → `goose_db_version` (создаёт goose).

---

## Очистка (retention) в старом коде — `routes/console.php`

- [ ] `monitoring:prune` (daily) — метрики мониторинга; `integrations:prune` (hourly) — `api_request_logs`, `api_idempotency_keys`,
  `webhook_deliveries` (сроки — в командах `app/Console/Commands`); `sanctum:prune-expired --hours=24` (daily);
  `session-purchases:prune` (daily, см. выше); `agent-qr-logins:prune` (daily, старше суток).
- [ ] Очередь — драйвер `database` (`config/queue.php` стр. 16), воркер `queue:work --stop-when-empty --max-time=50` раз в минуту.

## Миграции данных (не DDL) — что повторить или учесть при переносе

- [ ] `040139_backfill_club_data` — клуб `legacy`, перенос ролей/балансов из `users` (уже исторично; в ETL не нужен).
- [ ] `042815_backfill_legacy_club_subscription` — план `trial` 14 дней и подписка `legacy` (данные придут переносом).
- [ ] `044406_add_persistent_installation_key` — генерация ключей установки (зашифрованы APP_KEY → open-questions В-5).
- [ ] `065745` — `device_name = PC-%02d`.
- [ ] `181908` — сид языков `ru/kk/en` + `translations:sync` (для чистой установки нового стека нужен сид → schema-changes).
- [ ] `2026_10_14_200000_revoke_duplicate_agent_tokens` — чистка Sanctum (не нужен).
- [ ] `2026_10_17_100000` — нормализация MAC (`Support/MacAddress::normalize`), дубли → NULL.
- [ ] `2026_10_18_100000_backfill_loyalty_spending` — пересчёт `total_spent` по журналу (в тиынах через `amountToCents`)
  и миграция `settings.finances` (`loyalty_rules` → `loyalty_levels`, удаление `loyalty_type`, `tax_rate`).
- [ ] `2026_10_18_120000` — удаление `platform_settings.telegram_bot`.
- [ ] `2026_10_27_100000` — исправление недельных периодов тарифов, заканчивающихся в понедельник.
- [ ] `2026_10_30_100000` / `2026_10_31_100000` — права: `settings.club.allow_cashier_refunds` → `permissions`, затем
  раздел → `раздел.действие`.

## Проверка

- [ ] `goose up → down → up` на `postgres:18.6` — проходит (отчёт субагента в, Этап 0).
- [ ] `sqlc generate` и `sqlc vet` (правило `tenant-filter`) — проходят; `go build ./internal/store/...` — ок.
