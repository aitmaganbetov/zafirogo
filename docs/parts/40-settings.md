# 40. Настройки: клуба, платформы, конфигурация

> Раздел инвентаризации Этапа 0. Источник правды — `../Zafiro` (Laravel). Все ссылки на файлы даны от корня старого репозитория.
> Открытые вопросы раздела: `docs/open-questions/settings-jobs-integrations.md`.

## 40.0. Где и как хранятся настройки

- [ ] **Настройки клуба — JSON-колонка `clubs.settings`** (`database/migrations/2026_10_13_040135_create_clubs_table.php`, `$table->json('settings')->nullable()`), cast `array` в `app/Models/Club.php` (`casts()`, ~строка 22). Отдельной таблицы `settings`/`club_settings` **нет**.
  - Структура: три секции `club`, `finances`, `guests` → `{ "club": {...}, "finances": {...}, "guests": {...} }`.
  - В БД хранится только то, что клуб хоть раз сохранил; значения по умолчанию в БД **не пишутся**: они подставляются при чтении.
- [ ] **Чтение** — `app/Services/CurrentClub.php` (`settings()`, ~строка 71): возвращает `club->settings` как есть, без дефолтов.
  Особый случай: если `settings === null` и `slug === 'legacy'`, то настройки читаются из файла `storage/app/settings.json` (наследие первой версии; см. открытый вопрос Q-S1).
  Каждый сервис сам задаёт дефолт через `data_get(..., default)` / `?? default` — дефолты продублированы в нескольких местах (контроллер + сервис + blade). В Go нужен **один** типизированный `ClubSettings` с дефолтами и нормализацией.
- [ ] **Чтение для экрана/API** — `app/Http/Controllers/SettingsController.php` (`loadSettings()`, ~315–329): `array_replace_recursive(defaultSettings(), CurrentClub::settings())`, затем все «минутные» поля секции `club` (кроме `pc_action_after_session_end`, `agent_shop_enabled`, `agent_auto_update`) нормализуются через `minutes()` (~416–426): число → int или float; **строка** вида `"30 сек"` / `"5,5 мин"` → первое число из строки (запятая → точка), если в строке есть «сек» — делится на 60. Это поддержка старых строковых значений; при переносе данных MySQL → Postgres надо прогнать ту же нормализацию (Q-S2).
- [ ] **Дефолты** — `SettingsController::defaultSettings()` (~331–376). Это единственное полное описание схемы.
- [ ] **Запись** — `SettingsController::save()` (~208–313), маршрут `POST /settings/save` (`settings.save`, `routes/web.php` ~213), валидация `app/Http/Requests/SaveSettingsRequest.php`.
  - Сохраняется **одна секция** за запрос (`section` ∈ `club|finances|guests`); меняются только поля, присутствующие в запросе (`$request->has($field)`).
  - Приведение типов: булевы — `$request->boolean()` (форма шлёт hidden `0` + checkbox `1`); «минутные» — `number((float))` (int, если целое); `loyalty_levels`/`topup_bonus_levels` — `PercentLevels::normalize`; `bonus_max_percent`, `auto_billing_pause_minutes` — `(int)`; остальное (`currency`, `pc_action_after_session_end`) — как пришло.
  - Пустые строки таблицы уровней отбрасываются до валидации (`SaveSettingsRequest::prepareForValidation()`, ~79–87).
  - Весь JSON `settings` перезаписывается целиком (`$club->settings = $settings; $club->save()`), без блокировки строки: параллельное сохранение двух секций может потерять изменения (в Go — `UPDATE ... SET settings = jsonb_set(...)` или `SELECT ... FOR UPDATE`).
  - Аудит: при изменениях — `ActivityLogger::record('settings.updated', ...)` с диффом `было → стало` по каждому полю (уровни форматируются как `порог: процент%` через `;`). Событие показывается в уведомлениях (`app/Services/ClubNotifications.php`, иконка `fa-sliders`).
  - Побочный эффект: `Club::saved` в `app/Providers/AppServiceProvider.php` (`invalidateApiKeyCache()`, ~119–123) сбрасывает кэш метаданных API-ключей клуба при изменении `settings` (метаданные ключа содержат копию `settings`).
- [ ] **Права**: просмотр — `settings.view`, сохранение — `settings.update` (обе — `ClubPermissions::MANAGERS`, `app/Support/ClubPermissions.php` ~42; маппинг маршрутов — `app/Http/Middleware/RequireClubArea.php` ~63–64). PIN агента и время обслуживания — право `workers.update` (`RequireClubArea.php` ~67).
- [ ] **Экраны** (`resources/views/settings/`): `club.blade.php` (`/settings/club`), `finances.blade.php` (`/settings/finances`, подшаблон `percent-levels.blade.php` для таблиц уровней), `guests.blade.php` (`/settings/guests`), `languages.blade.php` (`/settings/languages`), `api.blade.php` (`/settings/api` — справочник URL настроек), `integrations.blade.php` (`/settings/integrations`, см. 60-integrations), `permissions*.blade.php` (права сотрудников — не настройки клуба в смысле JSON, описываются в разделе прав). Общий layout — `settings/layout.blade.php`, якоря — `anchors.blade.php`.
- [ ] **API чтения отдельного ключа**: `GET /api/settings/{section}/{key}` (`routes/api.php` ~164–170; `auth:sanctum`, `current-club`, `subscription`, `throttle:60,1`) → `SettingsController::showSetting()` (~193–206): `{ip, setting, name, value}`. Доступно **любой** роли клуба (замечание в `docs/integration-api.md`, раздел 1). Решение о судьбе — Q-S3.

## 40.1. Секция `club` (`clubs.settings.club`)

Экран: `/settings/club` (`resources/views/settings/club.blade.php`), блоки «Сессии и ПК», «Агент на ПК», «Сотрудники на смене».

- [ ] **`club.session_idle_timeout`** — число минут (int|float, шаг 0.01 в форме).
  - Дефолт: `15` (`SettingsController::defaultSettings()`; дубль в `club.blade.php` ~31).
  - Валидация: `required_if:section,club|numeric|min:0|max:10080` (`SaveSettingsRequest` ~24).
  - Где используется: **нигде в логике** (grep по `app/`, `WindowsAgent/` — только контроллер, request, view и тесты `tests/Feature/SettingsTest.php`, `AgentShopTest.php`). Хранится и показывается, но не влияет на поведение. → Q-S4.
- [ ] **`club.pc_action_after_session_end`** — enum-строка `none|restart|shutdown`.
  - Дефолт: `'none'`.
  - Валидация: `sometimes|in:none,restart,shutdown` (~25).
  - Используется: `app/Http/Controllers/ClubComputerController.php` (`afterSessionAction()`, ~511–521) → поле `club.after_session.action` в ответе heartbeat агенту (неизвестное значение → `none`).
- [ ] **`club.pc_restart_after_session_end`** — число минут (int|float), задержка перед действием из предыдущего пункта (название историческое: относится к любому действию).
  - Дефолт: `0.5` (30 секунд).
  - Валидация: `required_if:section,club|numeric|min:0|max:10080` (~26).
  - Используется: `ClubComputerController::afterSessionAction()` → `delay_seconds = (int) round(max(0, minutes) * 60)`; нечисловое → `30`.
- [ ] **`club.session_auto_terminate_when_pc_unavailable`** — число минут.
  - Дефолт: `5`.
  - Валидация: `required_if:section,club|numeric|min:0|max:10080` (~27).
  - Используется: **нигде**. Сессии на офлайн-ПК завершаются только по `session_ends_at` командой `club-computers:expire-sessions` (см. 50-jobs). → Q-S4.
- [ ] **`club.booking_interval`** — число минут, «зазор» до и после каждой брони.
  - Дефолт: `30` (дубль: `ComputerReservations::DEFAULT_GAP_MINUTES = 30`, `app/Services/ComputerReservations.php` ~19).
  - Валидация: `required_if:section,club|numeric|min:0|max:10080` (~28).
  - Используется: `ComputerReservations::gapMinutes()` (~23–28): `max(0, (int) round((float) value))`, нечисловое → 30. Применяется в `create()` (поиск конфликтов `[starts-gap, ends+gap]`, проверка оплаченной сессии) и в выборках «ПК зарезервирован» (~97, ~109, ~121); показывается на экране броней (`ComputerReservationController.php` ~44).
- [ ] **`club.max_cashiers_on_shift`** — int, лимит одновременно вошедших в панель кассиров; `0` — без лимита.
  - Дефолт: `0`.
  - Валидация: `sometimes|integer|min:0|max:100` (~29).
  - Используется: `app/Services/StaffShifts.php` (`limit()`, `hasPlace()`; ключи в `StaffShifts::SETTINGS`) — при входе в панель `app/Http/Controllers/AuthController.php` (~110); на экране сотрудников `WorkerController.php` (~36–42). «На смене» = есть живая web-сессия в таблице `sessions` за `SESSION_LIFETIME` минут (работает только при `SESSION_DRIVER=database`, иначе лимит не действует). В Go сессии будут в Valkey — механизм подсчёта «на смене» нужно переопределить (Q-S5).
- [ ] **`club.max_operators_on_shift`** — то же для роли `operator`. Дефолт `0`, валидация `sometimes|integer|min:0|max:100` (~30).
- [ ] **`club.agent_idle_logout_minutes`** — число минут: клиент, вошедший на ПК без оплаченной сессии, разлогинивается через N минут; `0` — никогда.
  - Дефолт: `GuestFeatures::DEFAULT_IDLE_LOGOUT_MINUTES = 5` (`app/Services/GuestFeatures.php`).
  - Валидация: `sometimes|numeric|min:0|max:1440` (~52); в форме `step=1`.
  - Используется: `GuestFeatures::idleLogoutMinutes()` — `max(0, (int) ceil((float) value))`; отдаётся агенту в heartbeat `club.idle_logout_minutes` (`ClubComputerController.php` ~469). Таймер — на стороне агента.
- [ ] **`club.agent_shop_enabled`** — bool, магазин в агенте.
  - Дефолт: `true`.
  - Валидация: `nullable|boolean` (~53).
  - Используется: `GuestFeatures::shopEnabled()`; `Api/GuestShopController.php` (~17, при выключенном — отказ); heartbeat `club.guest_features.shop` (`ClubComputerController.php` ~467).
- [ ] **`club.agent_auto_update`** — bool, служба агента сама ставит новую версию.
  - Дефолт: `true`.
  - Валидация: `nullable|boolean` (~54).
  - Используется: `app/Services/AgentUpdate.php` (`clubAllowsAutoUpdate()`, ~112–115) → `offerFor()` (см. 60-integrations, «Обновления агента»).

### Колонки `clubs`, которые по смыслу — настройки клуба (редактируются на `/settings/club` и `/settings/languages`)

- [ ] **`clubs.timezone`** — string(64), IANA-зона. Дефолт колонки `'Asia/Almaty'` (`2026_10_14_060000_add_timezone_to_clubs_table.php`). Валидация `required|string|timezone:all` (`SettingsController::timezone()`, ~38–55). Аудит `club.timezone_changed`. Список зон: сначала Казахстан, затем все по смещению (`timezoneOptions()`, ~109–124). Используется везде для отображения времени (`CurrentClub::timezone()`), в снимках загрузки (`SnapshotClubLoad`).
- [ ] **`clubs.locale`** — string(16) nullable, язык интерфейса клуба. Дефолт при `null` — `LocaleResolver::DEFAULT_CLUB_LOCALE = 'ru'` (`app/Services/LocaleResolver.php` ~17, `forClub()` ~27–32); если язык выключен — fallback. Валидация `required|in:<включённые коды>` (`app/Http/Controllers/LanguageController.php`, `select()`), маршрут `POST /settings/language`. Аудит `club.language_changed`.
- [ ] **`clubs.agent_exit_pin_hash` / `agent_exit_pin_changed_at`** — PIN сотрудника для выхода из агента, хранится хэшем (`Hash::make`, bcrypt). Валидация `nullable|string|regex:/^\d{4,8}$/|confirmed`; пустое значение удаляет PIN (`SettingsController::agentPin()`, ~58–82, `POST /settings/agent-pin`, право `workers.update`). Аудит `club.agent_pin_changed`. В Go — Argon2id; старые bcrypt-хэши переносить и перехэшировать при первой успешной проверке (Q-S6).
- [ ] **`clubs.agent_staff_minutes`** — unsigned smallint, сколько минут сотрудник может работать на ПК до возврата агента. Дефолт колонки `15` (`2026_10_14_220000_add_agent_staff_visits.php`), константа `ClubComputerSessions::DEFAULT_STAFF_MINUTES = 15`. Валидация `required|integer|min:1|max:240` (`SettingsController::agentStaffTime()`, ~85–102, `POST /settings/agent-staff-time`). Используется `ClubComputerSessions::startStaffVisit()` (~236–242).
- [ ] **`clubs.logo_path`** — логотип: `required|file|mimes:png,jpg,jpeg|max:1024 КБ|dimensions:min 32×32, max 4096×4096` (`app/Http/Controllers/ClubLogoController.php` ~38–39), диск `local`, публичный URL с именем файла (`Club::logoUrl()`). Отдаётся агенту в heartbeat.
- [ ] **`clubs.signin_image_path`** — картинка экрана входа агента: `mimes:png,jpg,jpeg|max:3072 КБ|dimensions:min 640×360, max 4096×4096` (`ClubSignInImageController.php` ~38–41). URL — `Club::signInImageUrl()`.
- [ ] **`clubs.installation_key` (+ `_hash`, `_prefix`)** — ключ установки агента; показывается на `/settings/club` (блок «Ключ установки клуба»), только чтение; создаётся в `Club::booted()` (`Str::random(64)`, хранится `encrypted` + sha256 + префикс 12 символов), изменение запрещено (`LogicException`). Описывается подробнее в разделе агента; в настройках — только отображение.

## 40.2. Секция `finances` (`clubs.settings.finances`)

Экран: `/settings/finances` (`resources/views/settings/finances.blade.php`), блоки «Валюта», «Оплата времени», «Накопительная скидка», «Бонусы за пополнение».
Формулы расчётов — в `docs/business-rules.md` (другой раздел); здесь — только схема ключей.

- [ ] **`finances.currency`** — enum-строка `KZT|RUB|$`.
  - Дефолт: `'KZT'` (продублирован в ~10 местах через `data_get(..., 'finances.currency', 'KZT')`).
  - Валидация: `required_if:section,finances|in:KZT,RUB,$` (~31).
  - Используется только как **подпись** (на расчёты не влияет): `TariffController.php` (~37 — копируется в `tariffs.currency` при создании тарифа, ~108), `DashboardController.php` (~64), `AgentTariffPurchaseService.php` (~121), `Api/AuthController.php` (~232, ~258), `Api/GuestMinutesController.php` (~34), `Api/Staff/CashController.php` (~29), `Api/Staff/MeController.php` (~36, ~76), `Http/Resources/UserResource.php` (~25), `finances.blade.php` (подписи «Потратил от, :currency»).
- [ ] **`finances.loyalty_enabled`** — bool, накопительная скидка включена.
  - Дефолт: `false`. Валидация: `nullable|boolean` (~32).
  - Используется: `app/Services/LoyaltyProgram.php` (`enabled()`, `levels()` — при выключенной уровни = `[]`).
- [ ] **`finances.loyalty_levels`** — JSON-массив `[{ "spent": number, "percent": number }]`.
  - `spent` — порог накопленных трат **в тенге** (не тиынах!), число с ≤ 2 знаками после запятой, хранится int, если целое, иначе float; `percent` — 0.01…100, ≤ 2 знака.
  - Нормализация `PercentLevels::normalize($levels, 'spent')` (`app/Support/PercentLevels.php` ~12–26): отбросить строки без числовых полей, округлить до 0.01, отсортировать по порогу.
  - Дефолт: `[]`.
  - Валидация (~33–36): `nullable|array|max:20` (`LoyaltyProgram::MAX_LEVELS = 20`); элемент `array:spent,percent`; `spent`: `required|numeric|min:0.01|max:999999999|distinct`; `percent`: `required|numeric|min:0.01|max:100`. Сообщения — `SaveSettingsRequest::messages()`.
  - Используется: `LoyaltyProgram::percentFor()` → `PercentLevels::percentAt()` (сравнение в центах: `cents >= round(spent*100)`, берётся max процент достигнутых уровней); `discountFor()` = `max(membership.discount, percentFor)`; применяется в `AgentTariffPurchaseService.php` (~149, ~366) через `discounted()` (~371–376: базисные пункты `round(percent*100)`, `intdiv(price*bp + 5000, 10000)`); показ в приложении сотрудника `Api/Staff/CustomerController.php` (~35).
  - История: миграция `2026_10_18_100000_backfill_loyalty_spending.php` переводила черновой формат `loyalty_rules [{threshold, discount}]`, `loyalty_type`, `tax_rate` в текущий и удаляла эти ключи. В данных MySQL их уже быть не должно, но инструмент переноса должен это проверить (Q-S2).
- [ ] **`finances.topup_bonus_enabled`** — bool. Дефолт `false`. Валидация `nullable|boolean` (~43). Используется `app/Services/TopUpBonus.php` (`enabled()`).
- [ ] **`finances.topup_bonus_levels`** — JSON-массив `[{ "amount": number, "percent": number }]`, `amount` — порог суммы пополнения **в тенге** (≤ 2 знака), `percent` 0.01…100.
  - Нормализация: `PercentLevels::normalize($levels, 'amount')`.
  - Дефолт: `[]`.
  - Валидация (~44–47): `nullable|array|max:20` (`TopUpBonus::MAX_LEVELS = 20`); `array:amount,percent`; `amount`: `required|numeric|min:0.01|max:999999999|distinct`; `percent`: `required|numeric|min:0.01|max:100`.
  - Используется: `TopUpBonus::for(float $amount)` (~29–35): `cents = round(amount*100)`, бонус = `intdiv(cents * round(percent*100) + 5000, 10000)` (округление половины вверх до тиына). Вызовы: `CashRegisterController::topUpBalance()` (~73–76), `Api/Staff/CashController::topUp()` (~57–60), `CashRegisterService` (конструктор).
- [ ] **`finances.discount_scope`** — enum-строка `tariffs|hourly|all` (`PaymentRules::DISCOUNT_SCOPES`, `app/Services/PaymentRules.php` ~13).
  - Дефолт: `'tariffs'` (и при неизвестном значении — `discountScope()`, ~17–22).
  - Валидация: `sometimes|in:tariffs,hourly,all` (~37).
  - Используется: `discountOnTariffs()` (`!== 'hourly'`) → `AgentTariffPurchaseService.php` ~365 (только тарифы с `discount_enabled`); `discountOnHourly()` (`!== 'tariffs'`) → `AgentTariffPurchaseService.php` ~145 (поминутная оплата).
- [ ] **`finances.bonus_max_percent`** — int 0…100, какой % цены можно оплатить бонусами; `0` — бонусы не платят за время.
  - Дефолт: `100`.
  - Валидация: `sometimes|integer|min:0|max:100` (~38); при чтении зажимается `max(0, min(100, (int)))`, нечисловое → 100 (`PaymentRules::bonusMaxPercent()`, ~35–40).
  - Используется: `bonusLimitCents(price) = intdiv(price * percent, 100)` (~84–87) → `AgentTariffPurchaseService.php` ~241 (кроме тарифов `main_balance_only`); `bonusPercentOffered()` (~76–81: если `< 100` и смешанная оплата выключена — агенту отдаётся 0) → `AgentTariffPurchaseService.php` ~353–354, `Api/GuestMinutesController.php` ~32; поле `bonus_max_percent` есть в модели агента `WindowsAgent/ZafiroAgent/Models/Tariff.cs` ~30.
- [ ] **`finances.allow_mixed_payment`** — bool, бонусы за часть цены + остаток с основного баланса. Дефолт `false`. Валидация `nullable|boolean` (~39). Используется `PaymentRules::mixedPaymentAllowed()` → `AgentTariffPurchaseService.php` ~242, ~355; `GuestMinutesController.php` ~33.
- [ ] **`finances.allow_whole_balance`** — bool, агент предлагает купить минуты на весь основной баланс. Дефолт `false`. Валидация `nullable|boolean` (~40). Используется `PaymentRules::wholeBalanceAllowed()` → `GuestMinutesController.php` ~38.
- [ ] **`finances.auto_billing`** — bool, поминутная оплата начинается сама после входа (после паузы) и после окончания тарифа. Дефолт `false`. Валидация `nullable|boolean` (~41). Используется `PaymentRules::autoBillingEnabled()` → `GuestMinutesController.php` ~40 (флаг для агента; покупку минут инициирует агент).
- [ ] **`finances.auto_billing_pause_minutes`** — int 0…60, пауза после входа до начала поминутной оплаты.
  - Дефолт: `5`.
  - Валидация: `sometimes|integer|min:0|max:60` (~42); при чтении зажимается 0…60, нечисловое → 5 (`PaymentRules::autoBillingPauseMinutes()`, ~53–58).
  - Используется: `GuestMinutesController.php` ~41.

## 40.3. Секция `guests` (`clubs.settings.guests`)

Экран: `/settings/guests` (`resources/views/settings/guests.blade.php`). Чтение — `app/Services/GuestFeatures.php` (`enabled()` через `FILTER_VALIDATE_BOOLEAN`, отсутствующее значение → `false`, кроме `guest_mode_enabled`). Все пять ключей — bool, валидация `nullable|boolean` (`SaveSettingsRequest` ~48–51, ~55). Четыре ключа отдаются агенту в heartbeat `club.guest_features` (`GuestFeatures::all()`, `ClubComputerController.php` ~467).

- [ ] **`guests.show_qr_code_on_login`** (фича `qr_login`) — показывать QR-код на экране входа агента. Дефолт `false`. Используется в heartbeat (агент решает показывать QR).
- [ ] **`guests.allow_guest_self_transfer`** (фича `self_transfer`) — самостоятельная пересадка клиента. Дефолт `false`. `GuestFeatures::ensure('self_transfer')` → `Api/GuestSeatController.php` ~20, ~28 (403 «Эта возможность отключена в клубе.»).
- [ ] **`guests.allow_adding_friends`** (фича `friends`) — друзья. Дефолт `false`. `ensure('friends')` → `Api/GuestFriendController.php` ~30, ~48.
- [ ] **`guests.allow_balance_transfer_to_friends`** (фича `balance_transfer`) — перевод баланса друзьям. Дефолт `false`. `ensure('balance_transfer')` → `GuestFriendController.php` ~63, ~84, ~98.
- [ ] **`guests.guest_mode_enabled`** — продажа времени на кассе гостю без аккаунта. Дефолт **`true`**. Используется `GuestFeatures::guestModeEnabled()` → `GuestSessionController.php` ~18, `Api/Staff/ComputerController.php` ~59, `Api/Staff/MeController.php` ~28 (влияет на набор прав в `StaffPermissions::for()`).

## 40.4. Сводка ключей `clubs.settings`

| Ключ | Тип | Дефолт | Диапазон |
|---|---|---|---|
| club.session_idle_timeout | минуты (num) | 15 | 0…10080 (не используется) |
| club.pc_action_after_session_end | enum | none | none/restart/shutdown |
| club.pc_restart_after_session_end | минуты (num) | 0.5 | 0…10080 |
| club.session_auto_terminate_when_pc_unavailable | минуты (num) | 5 | 0…10080 (не используется) |
| club.booking_interval | минуты (num) | 30 | 0…10080 |
| club.max_cashiers_on_shift | int | 0 | 0…100 |
| club.max_operators_on_shift | int | 0 | 0…100 |
| club.agent_idle_logout_minutes | минуты (num) | 5 | 0…1440 |
| club.agent_shop_enabled | bool | true | |
| club.agent_auto_update | bool | true | |
| finances.currency | enum | KZT | KZT/RUB/$ |
| finances.loyalty_enabled | bool | false | |
| finances.loyalty_levels | JSON `[{spent,percent}]` | [] | ≤20, spent 0.01…999999999 ₸, percent 0.01…100 |
| finances.topup_bonus_enabled | bool | false | |
| finances.topup_bonus_levels | JSON `[{amount,percent}]` | [] | ≤20, amount 0.01…999999999 ₸, percent 0.01…100 |
| finances.discount_scope | enum | tariffs | tariffs/hourly/all |
| finances.bonus_max_percent | int % | 100 | 0…100 |
| finances.allow_mixed_payment | bool | false | |
| finances.allow_whole_balance | bool | false | |
| finances.auto_billing | bool | false | |
| finances.auto_billing_pause_minutes | int мин | 5 | 0…60 |
| guests.show_qr_code_on_login | bool | false | |
| guests.allow_guest_self_transfer | bool | false | |
| guests.allow_adding_friends | bool | false | |
| guests.allow_balance_transfer_to_friends | bool | false | |
| guests.guest_mode_enabled | bool | true | |

Итого: **26 ключей** в `clubs.settings` (10 `club` + 11 `finances` + 5 `guests`) и **7 колонок `clubs`** с ролью настроек.

Предложение для Go:
- [ ] Типизированная структура `ClubSettings` (Go) + zod-схема (web) из одного источника (OpenAPI-схема), хранение в `clubs.settings jsonb`. Дефолты применяются в одном месте при чтении. Нормализацию «старых» строковых минут — один раз в `tools/migrate-from-mysql`.
- [ ] Пороги уровней (`spent`, `amount`) при переносе можно конвертировать в тиыны (int64), чтобы не было float в денежных порогах; сравнение `cents >= round(x*100)` эквивалентно. Решение — Q-S7.
- [ ] Кэш настроек клуба — в памяти процесса/Valkey с инвалидацией через событие NATS `club.settings.changed` (сейчас кэша нет: настройки читаются из уже загруженной модели клуба в каждом запросе).
- [ ] Изменения настроек — в аудит-журнал (как `settings.updated` с диффом) и в push агентам (сейчас агенты узнают изменения только со следующим heartbeat).

## 40.5. Платформенные настройки (суперадминка): таблица `platform_settings`

Таблица `platform_settings(key varchar(64) PK, value json null, updated_by FK users, timestamps)` (`database/migrations/2026_10_15_120000_create_platform_settings_table.php`), модель `app/Models/PlatformSetting.php`: `read()` кэширует навсегда (`platform-setting:<key>`), `write()` сбрасывает кэш; `null` удаляет строку.

- [ ] **`agent_release`** (`PlatformSetting::AGENT_RELEASE`) — объявленная версия агента: `{latest_version: "X.Y.Z", released_at: date, download_url: https|null, auto_update: bool, installer_url: string|null, installer_sha256: hex64|null}`.
  - Пишется: `app/Http/Controllers/Superadmin/AgentReleaseController.php` (`update()`: `latest_version` `required|regex:/^\d{1,4}\.\d{1,4}\.\d{1,6}$/`, `released_at` `required|date`, `download_url` `nullable|url:https|max:500`, `auto_update` `nullable|boolean`; при `auto_update` URL установщика строится по шаблону `AgentUpdate::INSTALLER_URL`, sha256 скачивается из `<url>.sha256`, без него — ошибка). `destroy()` — снять объявление. Журнал `PlatformLogger`: `superadmin.agent_release_set` / `_cleared`.
  - Фолбэк, если строки нет: `config('club.agent')` (env `AGENT_LATEST_VERSION`, `AGENT_RELEASED_AT`, `AGENT_DOWNLOAD_URL`), `AgentUpdate::release()` (~79–82). В фолбэке нет `auto_update`, т. е. автообновление из env невозможно.
  - Читается: `AgentUpdate` (`status()`, `autoInstaller()`, `offerFor()`), `Superadmin/DashboardController.php` (~103). Экран: дашборд суперадминки (`resources/views/superadmin/dashboard.blade.php`).
- [ ] **`telegram_gateway`** (`TelegramGateway::SETTING`) — `{token: <Crypt::encryptString>}`. Пишется `Superadmin/TelegramController::update()` (`token` `required|string|max:255|regex:/^[A-Za-z0-9_\-:.]{20,255}$/`, проверка токена запросом `checkVerificationStatus`), удаляется `destroy()`. Читается **мимо кэша** (`TelegramGateway::token()`). Экран `/superadmin/telegram` (`resources/views/superadmin/telegram.blade.php`).
- [ ] **`telegram_alerts`** (`TelegramAlerts::SETTING`) — `{token: <encrypted>, chat_id: string, locale: string}`. Пишется `TelegramController::updateAlerts()` (`bot_token` `regex:/^\d{5,15}:[A-Za-z0-9_\-]{30,100}$/`, `chat_id` `nullable|regex:/^(-?\d{1,20}|@[A-Za-z0-9_]{5,32})$/`; без chat_id берётся последний чат из `getUpdates`; отправляется тестовое сообщение), удаляется `destroyAlerts()`. Читается мимо кэша.
- [ ] **`telegram_bot`** — устаревший ключ (бот Zafiro), удаляется миграцией `2026_10_18_120000_telegram_gateway_password_recovery.php` (~29). Не переносить.
- [ ] **Секреты**: значения `token` зашифрованы `APP_KEY` (Laravel Crypt, AES-256-CBC + HMAC). При переносе в Go нужно расшифровать старым ключом и перешифровать новым механизмом (Q-S8); проще — попросить суперадмина ввести токены заново.
- [ ] **Языки** (платформенно): таблица `languages(code, name, is_enabled, is_system, sort_order)` — включённые языки влияют на выбор языка клубами и агентом (`Superadmin/LanguageController.php`: `code` `regex:/^[a-z]{2,3}(-[a-z0-9]{2,8})?$/`; нельзя выключить язык, выбранный каким-либо клубом, и язык по умолчанию `ru`). Подробно — в 60-integrations («Переводы»).
- [ ] **Тарифные планы SaaS** (`saas_plans`, `/superadmin/plans`) — данные, а не настройки; описываются в разделе подписок.

## 40.6. Конфигурация `config/*.php` (поведение, не секреты)

- [ ] **`config/club.php`**: `computer_heartbeat_interval_seconds` (env `COMPUTER_HEARTBEAT_INTERVAL_SECONDS`, 30; отдаётся агенту как `recommended_interval_seconds = max(5, value)`, `ClubComputerController.php` ~438); `agent.latest_version|released_at|download_url` (env `AGENT_*`, фолбэк для `agent_release`).
- [ ] **`config/integrations.php`** — всё про API v1 и вебхуки (подробно в 60-integrations):
  `scopes` (9 прав: `products:read`, `products:write`, `stock:write`, `sales:read`, `computers:read`, `bookings:write`, `computers:control`*, `tariffs:read`, `members:read`* — * «опасные»), `webhook_events` (6 событий), `key_prefix = 'zfk_'`, `key_cache_ttl_seconds = 300`, `default_rate_limit_per_minute` (env `API_KEY_RATE_LIMIT`, 120), `max_rate_limit_per_minute` (env `API_KEY_RATE_LIMIT_MAX`, 1000), `club_rate_limit_per_minute` (env `API_CLUB_RATE_LIMIT`, 600), `failed_auth_per_minute_per_ip = 20`, `max_request_kb = 256`, `max_bulk_items = 50`, `request_log_retention_days = 90`, `idempotency_retention_hours = 24`, `webhook_retry_backoff_seconds = [10, 60, 300, 1800, 7200]`, `webhook_timeout_seconds = 5`, `webhook_signature_tolerance_seconds = 300`, `webhook_disable_after_failures = 20`, `webhook_delivery_retention_days = 30`, `webhook_require_https` (env `WEBHOOK_REQUIRE_HTTPS`, true), `webhook_allow_private_hosts` (env `WEBHOOK_ALLOW_PRIVATE_HOSTS`, false).
- [ ] **`config/monitoring.php`**: `record_requests` (env `MONITORING_RECORD_REQUESTS`, true), `requests_path` (env `MONITORING_REQUESTS_PATH`, `storage/framework/metrics`), `retention_days = 30`, `backup_path` (env `BACKUP_PATH`), `certificate_hosts` (env `MONITORING_SSL_HOSTS`, CSV; пусто → хосты `APP_URL`, `SUPERADMIN_DOMAIN`, `WIKI_DOMAIN`), пороги алертов: `scheduler_silent_minutes = 5`, `disk_percent = 85`, `error_rate_percent = 5`, `error_rate_min_errors = 10`, `queue_pending = 100`, `queue_oldest_minutes = 10`.
- [ ] **`config/app.php`**: `superadmin_domain` (env `SUPERADMIN_DOMAIN`; пусто — суперадминка на `/superadmin`, `app/Support/SuperadminHost.php`, `RestrictSuperadminHost`), `wiki_domain` (env `WIKI_DOMAIN`; `RestrictWikiHost`), `timezone = 'UTC'` (хранение в UTC), `locale` (env `APP_LOCALE`).
- [ ] **`config/session.php`**: `SESSION_DRIVER=database` и `SESSION_LIFETIME=120` влияют на бизнес-логику (лимит сотрудников на смене, `StaffShifts::onShift()`; сброс сессий при восстановлении пароля, `PasswordRecovery::reset()`).
- [ ] **`config/queue.php`**: `QUEUE_CONNECTION=database`, `DB_QUEUE_RETRY_AFTER=90` — очередь вебхуков (заменяется, см. 50-jobs).
- [ ] **`config/cache.php`**: `CACHE_STORE=database` — общий кэш (ключи API, лимиты, состояния алертов, пакеты переводов); `integrations:prune` чистит протухшие строки `cache` только при этом драйвере. В Go → Valkey.
- [ ] **`config/services.php`, `config/mail.php`**: postmark/resend/ses/slack и SMTP — **шаблонные, в коде не используются** (`Mail::`, `Notification::` не встречаются в `app/`). `MAIL_MAILER=log`. Не переносить (Q-I6).
- [ ] **Жёстко заданные константы с влиянием на поведение** (кандидаты в конфиг Go): `AgentUpdate::MAX_OFFERS = 3`, `OFFER_INTERVAL_MINUTES = 120`, `INSTALLER_URL`; `PasswordRecovery`/`FriendTransferCodes`: `CODE_MINUTES = 5`, `RESEND_SECONDS = 60`, `CODES_PER_HOUR = 5`, `MAX_ATTEMPTS = 5`; `IntegrationController::MAX_ACTIVE_KEYS = 50`, `MAX_WEBHOOKS = 20`; `ClubComputer::HEARTBEAT_TIMEOUT_SECONDS = 90`; `SnapshotClubLoad::OFFLINE_ALERT_MIN_ONLINE = 3`; `ClubAttention`: `OFFLINE_MIN_ONLINE = 3`, `WEBHOOK_FAILURES = 3`, `SUBSCRIPTION_DAYS = 3`; лимиты частоты `RateLimiter::for(...)` в `AppServiceProvider::boot()` (~47–97).

## 40.7. Переменные `.env.example` / `env()` (только имена)

- [ ] Влияют на поведение приложения: `APP_URL`, `APP_ENV`, `APP_DEBUG`, `APP_LOCALE`, `APP_FALLBACK_LOCALE`, `SUPERADMIN_DOMAIN`, `WIKI_DOMAIN`, `COMPUTER_HEARTBEAT_INTERVAL_SECONDS`, `AGENT_LATEST_VERSION`, `AGENT_RELEASED_AT`, `AGENT_DOWNLOAD_URL`, `API_KEY_RATE_LIMIT`, `API_KEY_RATE_LIMIT_MAX`, `API_CLUB_RATE_LIMIT`, `WEBHOOK_REQUIRE_HTTPS`, `WEBHOOK_ALLOW_PRIVATE_HOSTS`, `MONITORING_RECORD_REQUESTS`, `MONITORING_REQUESTS_PATH`, `MONITORING_SSL_HOSTS`, `BACKUP_PATH`, `SESSION_DRIVER`, `SESSION_LIFETIME`, `QUEUE_CONNECTION`, `CACHE_STORE`, `BCRYPT_ROUNDS`, `LOG_*`.
- [ ] Секреты/инфраструктура (в Go заменяются своими): `APP_KEY`, `APP_PREVIOUS_KEYS` (шифрование `clubs.installation_key`, `webhook_endpoints.secret`, Telegram-токенов), `DB_*`, `REDIS_*`, `MAIL_*`, `AWS_*`, `POSTMARK_*`, `RESEND_API_KEY`, `SLACK_*`, `MEMCACHED_*`, `SQS_*`, `DYNAMODB_*`, `BEANSTALKD_*`.
- [ ] Предлагаемые переменные Go (koanf, префикс `ZAFIRO_`): эквиваленты первой группы + `ZAFIRO_ENCRYPTION_KEY` (AES-GCM для токенов/секретов вебхуков), `ZAFIRO_LEGACY_APP_KEY` (только для `tools/migrate-from-mysql`, расшифровка старых значений).
