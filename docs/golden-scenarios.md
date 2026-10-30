# Эталонные сценарии (golden scenarios) Zafiro → zafirogo

Этап 0, задача 3. Источник — все Pest-тесты старого проекта: `../Zafiro/tests/Feature/*.php` (72 файла), `../Zafiro/tests/Unit/*.php` (2 файла), общие помощники `tests/Pest.php`, `tests/TestCase.php`, `tests/IntegrationApiHelpers.php`, фабрики `database/factories/UserFactory.php`, `ProductFactory.php`, сидер `database/seeders/ProductSeeder.php` (используется одним тестом склада). Каждый сценарий — будущий Go-тест (`testing` + testcontainers с реальным Postgres) с теми же входными данными и теми же ожидаемыми числами. Расчёты денег в Go должны давать эти суммы **до тиына**.

## Сводка

- Файлов тестов: **74** (72 в `tests/Feature` + 2 в `tests/Unit`).
- Тестов (`test(...)`/`it(...)`): **453** (Feature — 451, Unit — 2).
- Сценариев из тестов (наборы данных раскрыты): **528** — P1: **115**, P2: **235**, P3: **178**.
- Дополнительных сценариев «нет в старых тестах» (раздел «Пробелы покрытия»): **36** — P1: 30, P2: 6.

### По пакетам Go

| Пакет | Сценариев | P1 | P2 | P3 | + пробелы |
|---|---:|---:|---:|---:|---:|
| `integration` | 93 | 2 | 74 | 17 | 0 |
| `club` | 89 | 8 | 43 | 38 | 0 |
| `agent` | 61 | 6 | 34 | 21 | 0 |
| `billing` | 59 | 49 | 4 | 6 | 23 |
| `i18n` | 52 | 0 | 11 | 41 | 0 |
| `auth` | 42 | 5 | 34 | 3 | 0 |
| `cash` | 28 | 21 | 3 | 4 | 13 |
| `saas` | 22 | 11 | 10 | 1 | 0 |
| `stock` | 20 | 12 | 1 | 7 | 0 |
| `notify` | 15 | 0 | 7 | 8 | 0 |
| `activity` | 15 | 0 | 10 | 5 | 0 |
| `monitoring` | 11 | 0 | 1 | 10 | 0 |
| `guest` | 10 | 1 | 2 | 7 | 0 |
| `web` | 10 | 0 | 1 | 9 | 0 |
| `—` | 1 | 0 | 0 | 1 | 0 |
| **Итого** | **528** | **115** | **235** | **178** | **36** |

### Файл теста → сценарии → приоритет

Приоритет файла — по старшему (наименьшему номеру) приоритету его сценариев; в скобках — разбивка.

| Файл теста | Тестов | Сценариев | Приоритет | P1 / P2 / P3 | ID |
|---|---:|---:|---|---|---|
| `TopUpBonusTest.php` | 3 | 3 | P1 | 3 / 0 / 0 | GS-001…GS-003 |
| `CashRefundTest.php` | 3 | 3 | P1 | 2 / 1 / 0 | GS-004…GS-006 |
| `CashRegisterTest.php` | 8 | 8 | P1 | 6 / 1 / 1 | GS-007…GS-014 |
| `ShiftReportTest.php` | 3 | 3 | P1 | 1 / 0 / 2 | GS-015…GS-017 |
| `OwnerTopUpTest.php` | 5 | 7 | P1 | 4 / 3 / 0 | GS-018…GS-024 |
| `SubscriptionTest.php` | 2 | 2 | P1 | 2 / 0 / 0 | GS-025…GS-026 |
| `LoyaltyTest.php` | 7 | 11 | P1 | 11 / 0 / 0 | GS-027…GS-037 |
| `PerMinuteTest.php` | 4 | 8 | P1 | 8 / 0 / 0 | GS-038…GS-045 |
| `SessionTimeTest.php` | 11 | 11 | P1 | 9 / 0 / 2 | GS-046…GS-056 |
| `TariffTest.php` | 4 | 4 | P1 | 3 / 0 / 1 | GS-057…GS-060 |
| `TariffEditTest.php` | 11 | 11 | P1 | 6 / 1 / 4 | GS-061…GS-071 |
| `AgentShopTest.php` | 10 | 10 | P1 | 5 / 2 / 3 | GS-072…GS-081 |
| `ComputerSessionTest.php` | 7 | 9 | P1 | 6 / 3 / 0 | GS-082…GS-090 |
| `GuestSessionTest.php` | 7 | 7 | P1 | 3 / 1 / 3 | GS-091…GS-097 |
| `GuestFeaturesTest.php` | 14 | 14 | P1 | 3 / 6 / 5 | GS-098…GS-111 |
| `ComputerReservationTest.php` | 13 | 13 | P1 | 2 / 4 / 7 | GS-112…GS-124 |
| `WarehouseTest.php` | 7 | 7 | P1 | 3 / 0 / 4 | GS-125…GS-131 |
| `AfterSessionActionTest.php` | 1 | 1 | P3 | 0 / 0 / 1 | GS-132…GS-132 |
| `ClubZoneTest.php` | 8 | 8 | P3 | 0 / 0 / 8 | GS-133…GS-140 |
| `ClubServiceTest.php` | 2 | 2 | P1 | 2 / 0 / 0 | GS-141…GS-142 |
| `ProductDeleteTest.php` | 2 | 2 | P2 | 0 / 1 / 1 | GS-143…GS-144 |
| `ProductApiImageTest.php` | 2 | 2 | P3 | 0 / 0 / 2 | GS-145…GS-146 |
| `ClubComputerDeleteTest.php` | 2 | 2 | P2 | 0 / 1 / 1 | GS-147…GS-148 |
| `ClubComputerListTest.php` | 2 | 2 | P2 | 0 / 2 / 0 | GS-149…GS-150 |
| `ComputerHeartbeatTest.php` | 6 | 6 | P2 | 0 / 2 / 4 | GS-151…GS-156 |
| `ComputerMacIdentityTest.php` | 7 | 7 | P2 | 0 / 6 / 1 | GS-157…GS-163 |
| `ComputerPowerTest.php` | 10 | 10 | P1 | 1 / 1 / 8 | GS-164…GS-173 |
| `ComputerAgentApiTest.php` | 10 | 10 | P1 | 1 / 5 / 4 | GS-174…GS-183 |
| `AgentExitPinTest.php` | 4 | 4 | P2 | 0 / 4 / 0 | GS-184…GS-187 |
| `AgentRegisterTest.php` | 3 | 3 | P1 | 1 / 2 / 0 | GS-188…GS-190 |
| `AgentSecurityTest.php` | 12 | 12 | P1 | 1 / 11 / 0 | GS-191…GS-202 |
| `DisklessAgentTest.php` | 8 | 8 | P2 | 0 / 7 / 1 | GS-203…GS-210 |
| `AgentUpdateNoticeTest.php` | 7 | 7 | P2 | 0 / 2 / 5 | GS-211…GS-217 |
| `AgentLocalizationTest.php` | 5 | 5 | P3 | 0 / 0 / 5 | GS-218…GS-222 |
| `ClubInstallationKeyTest.php` | 3 | 3 | P2 | 0 / 3 / 0 | GS-223…GS-225 |
| `ApiAuthTest.php` | 10 | 10 | P1 | 5 / 5 / 0 | GS-226…GS-235 |
| `PasswordRecoveryTest.php` | 7 | 7 | P2 | 0 / 6 / 1 | GS-236…GS-242 |
| `RateLimitTest.php` | 3 | 3 | P2 | 0 / 3 / 0 | GS-243…GS-245 |
| `SaasTenancyTest.php` | 3 | 3 | P1 | 1 / 2 / 0 | GS-246…GS-248 |
| `PermissionsTest.php` | 6 | 6 | P2 | 0 / 6 / 0 | GS-249…GS-254 |
| `UsersTest.php` | 11 | 15 | P1 | 6 / 7 / 2 | GS-255…GS-269 |
| `WorkersTest.php` | 4 | 4 | P2 | 0 / 4 / 0 | GS-270…GS-273 |
| `StaffApiTest.php` | 8 | 8 | P1 | 5 / 2 / 1 | GS-274…GS-281 |
| `IntegrationApiAuthTest.php` | 26 | 31 | P1 | 3 / 24 / 4 | GS-282…GS-312 |
| `IntegrationApiResourcesTest.php` | 12 | 38 | P1 | 1 / 28 / 9 | GS-313…GS-350 |
| `IntegrationSettingsTest.php` | 8 | 13 | P2 | 0 / 12 / 1 | GS-351…GS-363 |
| `WebhookTest.php` | 7 | 12 | P2 | 0 / 10 / 2 | GS-364…GS-375 |
| `SuperadminCabinetTest.php` | 12 | 12 | P1 | 4 / 7 / 1 | GS-376…GS-387 |
| `SuperadminTest.php` | 2 | 2 | P2 | 0 / 2 / 0 | GS-388…GS-389 |
| `ServerMonitoringTest.php` | 11 | 11 | P2 | 0 / 1 / 10 | GS-390…GS-400 |
| `ActivityLogTest.php` | 6 | 6 | P2 | 0 / 4 / 2 | GS-401…GS-406 |
| `ActivityLogCoverageTest.php` | 9 | 9 | P1 | 1 / 6 / 2 | GS-407…GS-415 |
| `AdminLayoutTest.php` | 2 | 2 | P3 | 0 / 0 / 2 | GS-416…GS-417 |
| `LandingPageTest.php` | 2 | 2 | P3 | 0 / 0 / 2 | GS-418…GS-419 |
| `DashboardTest.php` | 1 | 1 | P1 | 1 / 0 / 0 | GS-420…GS-420 |
| `TodoTest.php` | 2 | 2 | P3 | 0 / 0 / 2 | GS-421…GS-422 |
| `ChatTest.php` | 2 | 2 | P2 | 0 / 2 / 0 | GS-423…GS-424 |
| `NewsTest.php` | 4 | 4 | P2 | 0 / 2 / 2 | GS-425…GS-428 |
| `NotificationTest.php` | 6 | 6 | P2 | 0 / 2 / 4 | GS-429…GS-434 |
| `ClubInvitationTest.php` | 3 | 3 | P2 | 0 / 3 / 0 | GS-435…GS-437 |
| `ClubLogoTest.php` | 5 | 10 | P2 | 0 / 7 / 3 | GS-438…GS-447 |
| `ClubSignInImageTest.php` | 2 | 2 | P2 | 0 / 1 / 1 | GS-448…GS-449 |
| `ClubTimezoneTest.php` | 7 | 7 | P1 | 2 / 2 / 3 | GS-450…GS-456 |
| `GuestAccountTest.php` | 3 | 3 | P2 | 0 / 3 / 0 | GS-457…GS-459 |
| `GuestProfileTest.php` | 2 | 2 | P2 | 0 / 1 / 1 | GS-460…GS-461 |
| `SettingsTest.php` | 10 | 10 | P3 | 0 / 0 / 10 | GS-462…GS-471 |
| `LanguageSettingsTest.php` | 7 | 10 | P2 | 0 / 4 / 6 | GS-472…GS-481 |
| `LocalizationTest.php` | 9 | 13 | P3 | 0 / 0 / 13 | GS-482…GS-494 |
| `TranslationCacheTest.php` | 10 | 12 | P2 | 0 / 2 / 10 | GS-495…GS-506 |
| `TranslationTransferTest.php` | 8 | 12 | P2 | 0 / 5 / 7 | GS-507…GS-518 |
| `MobileAppTest.php` | 4 | 4 | P1 | 3 / 1 / 0 | GS-519…GS-522 |
| `PwaTest.php` | 4 | 4 | P2 | 0 / 1 / 3 | GS-523…GS-526 |
| `Unit/AgentWindowsAccountsTest.php` | 1 | 1 | P2 | 0 / 1 / 0 | GS-527…GS-527 |
| `Unit/ExampleTest.php` | 1 | 1 | P3 | 0 / 0 / 1 | GS-528…GS-528 |


## Как читать сценарии

Формат заголовка: `GS-NNN · приоритет · пакет Go · имя будущего Go-теста`. Ниже — файл и дословное имя Pest-теста, затем «Дано / Действие / Ожидается». Наборы данных (`->with([...])`) раскрыты: один набор — один сценарий, имя Go-подтеста через `/` (табличный тест, `t.Run`).

**Приоритеты:** P1 — денежный (основной и бонусный баланс, бонусы за пополнение, скидки и лояльность, касса и смены, сессии и возвраты, пополнения и списания, переводы друзьям, продажи и склад как часть продажи, подписки SaaS); P2 — права, тенантность, безопасность (аутентификация, токены, лимиты, изоляция клубов, SSRF и т. п.); P3 — прочее (интерфейс, справочники, мониторинг, переводы интерфейса).

**Пакеты Go** (каталог `server/internal/<пакет>`): `billing` — балансы, тарифы, поминутная оплата, правила оплаты, сессии и расчёт возвратов, лояльность, переводы; `cash` — касса, смены, продажи POS, пополнения, возвраты, X/Z-отчёты, гостевые сессии; `stock` — склад, товары, заказы из агента; `auth` — вход, токены, восстановление пароля, QR-вход, лимиты входа; `club` — настройки клуба, зоны и ПК (админка), брони, сотрудники и права, клиенты; `agent` — протокол агента (heartbeat, регистрация, enroll, токены ПК, PIN, питание); `guest` — гостевое API (история, друзья, профиль, новости); `integration` — внешний API v1, ключи, идемпотентность, вебхуки; `saas` — подписки, тарифы платформы, кабинет суперадмина; `notify` — уведомления, чат, новости; `activity` — журнал действий; `monitoring` — метрики и алерты; `i18n` — языки и переводы; `web` — оболочка интерфейса и PWA (в Go это в основном проверки фронтенда — Vitest/Playwright).

**Единицы.** В старом коде суммы хранятся в тенге как `decimal(12,2)` (`member_balances.balance`, `bonus_balance`; `cash_transactions.amount` — `decimal(14,2)`), ответы API отдают строки вида `"3600.00"`. В сценариях суммы записаны так же, как в тесте (тенге); в Go это `int64` в тиынах: `3600.00 ₸ = 360000 тиын`. Где тест проверяет формулу в тиынах (`minutesPriceInCents`), это указано явно. Часть старого кода считает во `float` (`CashRegisterService`, `ShiftReport`, пополнение без кассы) — в Go это заменяется целочисленной арифметикой с тем же результатом.

**Общий фон всех тестов** (если в сценарии не сказано иное):
- База пересоздаётся на каждый тест (`RefreshDatabase`, SQLite в памяти, `phpunit.xml`). Миграции создают клуб `legacy` (`database/migrations/2026_10_13_040139_backfill_club_data.php`) с активной подпиской на план `trial` (`2026_10_13_042815_backfill_legacy_club_subscription.php`), часовой пояс клуба по умолчанию `Asia/Almaty` (`2026_10_14_060000_add_timezone_to_clubs_table.php`). `tests/TestCase.php` делает `legacy` текущим клубом.
- `User::factory()->create()` создаёт пользователя с паролем `password`, телефоном `+7XXXXXXXXXX` и сразу членство в текущем клубе с ролью **`owner`**, скидкой 0 и балансом **0.00 / 0.00** (`database/factories/UserFactory.php`, `configure()`). Поэтому «первый созданный пользователь — владелец», а остальным тест меняет роль.
- `allowMixedPayment()` (`tests/Pest.php`) включает `settings.finances.allow_mixed_payment = true`. По умолчанию смешанная оплата выключена, `bonus_max_percent = 100`, `discount_scope = tariffs` (`app/Services/PaymentRules.php`).
- Время: `Carbon::setTestNow()`, `travelTo()`, `travel()`, `freezeTime()` указаны в «Дано»; если не указаны — текущее время.
- Заголовки агента: Bearer-токен пользователя со способностью `club:{id}` и/или `X-Agent-Token` ПК (`zag_` + 48 символов, `ClubComputerSessions::issueAgentToken()`).
- HTTP-статусы: «ошибка `поле`» для веб-форм — redirect с ошибкой валидации в сессии; для JSON — **422** с ошибкой поля.

**Что переносится иначе.** API агента и гостя проектируется заново (gRPC для агента, решение владельца №3), поэтому в сценариях агента важны **правила** (кто, что, с какими числами), а не форма маршрутов `/api/...`. Проверки разметки Blade (классы, `data-*`) переходят в Vitest/Playwright-тесты React; в Go остаются данные, которые эти страницы показывают. Сценарии миграций старой БД (`backfill_loyalty_spending`, `fix_tariff_periods_ending_on_monday`) относятся к `tools/migrate-from-mysql`.

Открытые вопросы по этому документу: [`docs/open-questions/golden-scenarios.md`](open-questions/golden-scenarios.md).

---

# Сценарии по файлам тестов

Порядок: сначала денежные файлы (касса, биллинг, магазин, сессии, гостевые функции), затем остальные.


## Файл: TopUpBonusTest.php

Общая подготовка (`bonusTopUpSetup()`, строки 16–27): настройки `finances` клуба legacy дополняются переданными; кассир (владелец по фабрике) и клиент «Клиент бонус» с балансами основной 100.00 ₸ / бонусный 25.00 ₸; открыта смена (`opening_balance` 0). Пополнение — `POST /cash-register/balance-top-ups` (`cash-register.balance-top-ups.store`), `payment_method=cash`.

#### GS-001 · P1 · `billing` · `TestTopUpBonusLevels_SaveNormalizeValidate`
**Тест:** `TopUpBonusTest.php` — «the manager sets up top-up bonus levels on the finance page»
- **Дано:** владелец клуба; уровни в форме: `[{10000, 10}, {"", ""}, {2000, 5}]`, `topup_bonus_enabled=1`, валюта KZT.
- **Действие:** `POST /settings/save` (`section=finances`); затем `GET /settings/finances`; затем повторный save с `[{amount:1000, percent:150}]`.
- **Ожидается:** без ошибок; `settings.finances.topup_bonus_enabled = true`; `topup_bonus_levels = [{amount:2000, percent:5}, {amount:10000, percent:10}]` (пустая строка отброшена, сортировка по сумме, числа целые — `PercentLevels::normalize()`); страница содержит `name="topup_bonus_levels[1][amount]" value="10000"` и якорь `#topup-bonus`. Процент 150 → ошибка валидации `topup_bonus_levels.0.percent`.

#### GS-002 · P1 · `cash` · `TestTopUpBonus_LevelReachedAddsBonusCashTakesMoneyOnly`
**Тест:** `TopUpBonusTest.php` — «a top-up of the main balance adds the bonus of the level it reaches; cash takes only the money»
- **Дано:** бонус включён, уровни `{2000 ₸ → 5%}`, `{10000 ₸ → 10%}`; клиент 100.00 / 25.00.
- **Действие:** три пополнения основного баланса наличными: 1000; 2500; 10000.55 ₸.
- **Ожидается:**
  - 1000 ₸ (ниже первого уровня): flash `Баланс пользователя пополнен.`; бонус остаётся 25.00.
  - 2500 ₸ → 5% = 125.00: flash `Баланс пользователя пополнен. Бонус за пополнение: 125.00.`; основной 3600.00 (100+1000+2500), бонусный 150.00; `activity_logs` событие `user.balance_topped_up` с `details ⊇ {amount: 2500, bonus_amount: 125, bonus_percent: 5}`.
  - 10000.55 ₸ → 10% = 1000.055 → **1000.06** (округление half-up, формула `intdiv(cents × round(percent×100) + 5000, 10000)` = `intdiv(1000055×1000+5000, 10000)` = 100006 тиын, `app/Services/TopUpBonus.php` `for()`): бонусный **1150.06**; основной 13600.55; сумма `cash_transactions.amount` = **13500.55** (бонус в кассу не попадает).
  - История клиента (`GuestHistory::page`) первая запись: `{kind: top_up, amount: "10000.55", bonus_amount: "1000.06"}`.

#### GS-003 · P1 · `cash` · `TestTopUpBonus_DisabledOrBonusBalanceNoBonus`
**Тест:** `TopUpBonusTest.php` — «no bonus when it is switched off or the bonus balance is topped up»
- **Дано:** `topup_bonus_enabled=false`, уровень `{100 ₸ → 50%}`; клиент 100.00 / 25.00.
- **Действие:** пополнение основного на 1000; затем бонус включается, пополнение **бонусного** баланса на 1000 (`balance_type=bonus_balance`).
- **Ожидается:** после первого — бонусный 25.00, flash без бонуса; после второго — бонусный **1025.00** (бонус за пополнение начисляется только на основной баланс; `CashRegisterService::topUpBalance()` строка ~114).

## Файл: CashRefundTest.php

#### GS-004 · P1 · `cash` · `TestRefund_SaleReturnsMoneyAndStock`
**Тест:** `CashRefundTest.php` — «a refund of a sale pays the money out of the shift and puts the goods back to the stock»
- **Дано:** владелец; смена открыта `CashRegisterService::open(1000)`; товар «Coca Cola» цена 700, остаток 10; продажа `sale([{cola, 3}])` = 2100.00, остаток 7.
- **Действие:** `GET /cash-register/operations`; `POST /cash-register/receipts/{sale}/refund`; повторный refund; `GET` чека; `GET /cash-register/reports/x`.
- **Ожидается:** операции: «№{id}», «Coca Cola × 3», ссылка на возврат. Возврат: flash `Возврат проведён: 2 100,00 KZT выданы из кассы.`; запись `cash_transactions {refund_of_id: sale.id, type: expense, amount: 2100.00}`; остаток колы снова **10**; одно `stock_movements` типа `receipt` с `quantity 3`; баланс смены `totals()['balance']` = **1000.0** (1000 + 2100 − 2100); событие `cash.refund`. Повторный возврат → ошибка `refund` («уже возвращена»). Чек показывает «Возвращено». X-отчёт: «За смену товары не продавались.», строка «Возвраты (расход) · 1 · − 2 100,00 KZT».

#### GS-005 · P1 · `cash` · `TestRefund_TopUpTakesAmountAndBonusBack`
**Тест:** `CashRefundTest.php` — «a refund of a top-up takes the amount and its bonus back from the customer balance»
- **Дано:** бонус за пополнение `{1000 ₸ → 10%}`; смена открыта с 0; `topUpBalance(customer, 2000, 'balance', 'cash')` → бонусный **200.00**.
- **Действие:** клиент «потратил»: основной выставлен 1500 → `POST refund`; затем основной выставлен 2500 → `POST refund`.
- **Ожидается:** при 1500 < 2000 → ошибка `refund` (клиент уже потратил часть); при 2500 → успех, основной **500.00**, бонусный **0.00** (снимается `min(200, бонусный)`); история клиента: `{title: "Возврат пополнения на кассе", amount: "-2000.00", bonus_amount: "-200.00"}`.

#### GS-006 · P2 · `cash` · `TestRefund_CashierNeedsRightOtherIncomeNotRefundable`
**Тест:** `CashRefundTest.php` — «cashiers refund only with the right from the owner; other income is not refunded»
- **Дано:** смена 5000; товар цена 100, остаток 5; продажа 1 шт. (100.00); прочий приход `transact(income, 300, "Разное")`; кассир (роль `cashier`, без права `cash.delete`).
- **Действие:** кассир открывает операции и пытается вернуть продажу; владелец даёт право `permissions = {cash.delete: true}`; кассир возвращает «прочий приход» и продажу.
- **Ожидается:** без права — текст «Нет права на возвраты. Его даёт владелец клуба в «Настройки → Права».», ссылки на возврат нет, `POST refund` → **403**. С правом — ссылка на возврат продажи есть, на прочий приход нет; возврат прочего прихода → ошибка `refund`; возврат продажи успешен, `cash_transactions.user_id` возврата = id кассира.

## Файл: CashRegisterTest.php

#### GS-007 · P1 · `cash` · `TestShift_OpenOnlyOne`
**Тест:** `CashRegisterTest.php` — «cash shift can be opened and only one can remain open»
- **Дано:** владелец, смен нет.
- **Действие:** `POST /cash-register/shifts {opening_balance: 10000}`; повтор с 5000.
- **Ожидается:** первый — redirect, открытых смен 1; второй — ошибка `opening_balance` («Смена уже открыта»), открытых по-прежнему 1.

#### GS-008 · P1 · `cash` · `TestShift_XReportBalance`
**Тест:** `CashRegisterTest.php` — «cash operations are included in X report»
- **Дано:** открытая смена `opening_balance` 10000.
- **Действие:** `POST /cash-register/transactions {income, 3000, "Продажа"}`, `{expense, 1000, "Расход"}`; `GET /cash-register/reports/x`.
- **Ожидается:** 2 записи `cash_transactions`; X-отчёт показывает остаток **12 000,00 KZT** (10000 + 3000 − 1000).

#### GS-009 · P1 · `cash` · `TestShift_CloseRequiresZReportPrinted`
**Тест:** `CashRegisterTest.php` — «Z report must be printed before the shift can be closed»
- **Дано:** открытая смена 5000; приход 2000.
- **Действие:** `POST /cash-register/shifts/{id}/close` без отметки печати; `GET /z-report`; повтор close с сессией `z_report_printed.{id} = true`.
- **Ожидается:** первый close → redirect на Z-отчёт с ошибкой `shift`, статус `open`; Z-отчёт 200 «Z-отчёт»; второй close → redirect на Z-отчёт, статус `closed`, `closing_balance` = **7000.00**.

#### GS-010 · P1 · `cash` · `TestPOSSale_WritesOffStockAndRecordsIncome`
**Тест:** `CashRegisterTest.php` — «POS sale writes off products and records cash income»
- **Дано:** открытая смена 0; товар цена 750, остаток 5.
- **Действие:** `POST /cash-register/sales {items: [{product_id, quantity: 2}]}`; `GET` чека.
- **Ожидается:** redirect на чек, flash `status`; чек содержит название товара и «Распечатать чек»; остаток **3**; `cash_transactions.amount` = **1500.00**; `receipt_data.items[0].quantity = 2`; `stock_movements.balance_after = 3`.

#### GS-011 · P3 · `cash` · `TestPOSPage_ShowsProductsAndTopUpPicker`
**Тест:** `CashRegisterTest.php` — «POS page displays products when shift is open»
- **Дано:** открытая смена; товар «POS Test Product», остаток 2.
- **Действие:** `GET /cash-register`.
- **Ожидается:** 200; есть «POS Test Product», «Оплатить», `data-pos-view="sale"`, `data-pos-view="balance"`, «Пополнение баланса», `role="combobox"`, «Поиск по имени или телефону»; нет «Стол 1» и `pos-tables`.

#### GS-012 · P1 · `cash` · `TestTopUp_MainBalanceByCard`
**Тест:** `CashRegisterTest.php` — «cashier can top up a users main or bonus balance from the POS»
- **Дано:** клиент «Клиент POS» 100.00 / 25.00; смена 500; бонус за пополнение не настроен.
- **Действие:** `POST /cash-register/balance-top-ups {amount: 750, balance_type: balance, payment_method: card}`.
- **Ожидается:** redirect, flash `Баланс пользователя пополнен.`; основной **850.00**, бонусный 25.00; `cash_transactions {type: income, amount: 750.00, description ~ "Клиент POS"}`; события `user.balance_topped_up` (subject = клиент) и `cash.income` (subject = транзакция).

#### GS-013 · P1 · `cash` · `TestTopUp_Validation`
**Тест:** `CashRegisterTest.php` — «balance top-up validates user, amount, account, and payment method»
- **Дано:** открытая смена.
- **Действие:** `POST balance-top-ups {user_id: 999999, amount: 0, balance_type: unknown, payment_method: unknown}`.
- **Ожидается:** ошибки `user_id`, `amount`, `balance_type`, `payment_method`; `cash_transactions` пусто.

#### GS-014 · P2 · `cash` · `TestTopUpPicker_OnlyCurrentClubMembers`
**Тест:** `CashRegisterTest.php` — «balance top-up picker lists only current club members with their balances»
- **Дано:** кассир «Касса», клиент «Алия» 1500 / 40 в текущем клубе; другой клуб `other-club`, в нём «Посторонний» (9999) и «Алия» (7777).
- **Действие:** `GET /cash-register`.
- **Ожидается:** `users` = `["Алия", "Касса"]` (сортировка по имени); текст «Основной 1 500 ₸ · Бонусный 40 ₸»; нет «Посторонний» и «7 777».

## Файл: ShiftReportTest.php

#### GS-015 · P1 · `cash` · `TestShiftReport_IncomeByKindGoodsStaff`
**Тест:** `ShiftReportTest.php` — «the X and Z report show what the shift consists of: income by kind, goods sold and staff»
- **Дано:** кассир «Айгерим»; смена `open(1000)`; «Coca Cola» 700 ₸ ×20 шт., «Чипсы» 500 ₸ ×20 шт.; ПК №1 в зоне VIP 600 ₸/час.
- **Действие:** продажа {кола 3, чипсы 1} = 2600; продажа {кола 2} = 1400; `topUpBalance(customer, 5000, balance, card)`; гостевая сессия `GuestSessions::sell([pc], null, 60, 'cash')` = 600; расход 300 «Вода для кулера». Затем `GET` X-отчёта и Z-отчёта.
- **Ожидается (оба отчёта):** «Приход по видам»: «Продажа товаров · 2 · 4 000,00 KZT», «Пополнение балансов · 1 · 5 000,00 KZT», «Гостевые сессии · 1 · 600,00 KZT»; «Продано товаров»: Coca Cola 5 (остаток **15**), Чипсы 1 (остаток **19**), «Всего 6»; «Сотрудники»: Айгерим · **5** операций · приход **9 600,00 KZT** · расход **300,00 KZT** (`app/Services/ShiftReport.php` `for()`).

#### GS-016 · P3 · `cash` · `TestShiftReport_EmptyShift`
**Тест:** `ShiftReportTest.php` — «an empty shift says no goods were sold»
- **Дано:** открытая пустая смена.
- **Действие:** `GET` Z-отчёта.
- **Ожидается:** «За смену товары не продавались.»

#### GS-017 · P3 · `cash` · `TestShiftReport_CashierCloserOperator`
**Тест:** `ShiftReportTest.php` — «the report names the cashier of the shift, who closed it and the operator who prints it»
- **Дано:** владелец «Азат», кассир «Айгерим», смена открыта кассиром.
- **Действие:** владелец `GET` X-отчёт; `close(shift, owner)`; `GET` Z-отчёт.
- **Ожидается:** X: по порядку «Номер смены», `#{id}`, «Кассир», «Айгерим», «Оператор», «Азат»; нет «Закрыл смену». Z: «Кассир Айгерим», «Закрыл смену Азат», «Оператор Азат».

## Файл: OwnerTopUpTest.php

Подготовка (`topUpCustomer()`): первый пользователь — владелец; клиент «Арман», роль `client`, 100.00 / 10.00. Маршрут — `PATCH /users/{membership}` (`users.update`).

#### GS-018 · P1 · `billing` · `TestOwnerTopUp_WithoutCashRegister`
**Тест:** `OwnerTopUpTest.php` — «the owner tops up a customer balance without the cash register, while editing the customer»
- **Дано:** см. подготовку.
- **Действие:** `PATCH {name: Арман, top_up_amount: "500", top_up_balance: balance}`; `PATCH {top_up_amount: "40.5", top_up_balance: bonus_balance}`; `PATCH {name: "Арман Сейтов"}` без суммы.
- **Ожидается:** страница пользователей содержит `name="top_up_amount"`; flash `Данные клиента сохранены. Баланс пополнен на 500.00 без кассы.`; основной **600.00**, бонусный **50.50**; `cash_transactions` пусто; 2 события `user.balance_topped_up` от владельца; история: `[1] = {kind: top_up, amount: "500.00", note: "без кассы (владелец)"}`, `[0] = {kind: top_up, bonus_amount: "40.50"}`; сохранение без суммы — основной остаётся 600.00. Бонус за пополнение здесь не начисляется (не настроен).

#### GS-019 · P2 · `billing` · `TestOwnerTopUp_DeniedForStaff/admin`
**Тест:** `OwnerTopUpTest.php` — «other staff cannot top up without the cash register» (набор `admin`)
- **Дано:** сотрудник с ролью `admin`.
- **Действие:** `GET /users`; `PATCH {top_up_amount: "500", top_up_balance: balance}`.
- **Ожидается:** поля `top_up_amount` в форме нет; ошибка `top_up_amount`; основной остаётся **100.00**.

#### GS-020 · P2 · `billing` · `TestOwnerTopUp_DeniedForStaff/manager`
**Тест:** `OwnerTopUpTest.php` — «other staff cannot top up without the cash register» (набор `manager`)
- **Дано / Действие / Ожидается:** как предыдущий, роль `manager`; основной **100.00**.

#### GS-021 · P2 · `billing` · `TestOwnerTopUp_DeniedForStaff/cashier`
**Тест:** `OwnerTopUpTest.php` — «other staff cannot top up without the cash register» (набор `cashier`)
- **Дано / Действие / Ожидается:** как предыдущий, роль `cashier`; основной **100.00**.

#### GS-022 · P1 · `billing` · `TestOwnerTopUp_WorkerOfOtherClub_AccountLocked`
**Тест:** `OwnerTopUpTest.php` — «a worker of another club plays here: this club changes the discount and balance, not the account»
- **Дано:** клиент «Арман» дополнительно — `admin` в клубе `worker-elsewhere`.
- **Действие:** владелец `GET /users`; `PATCH {discount: "7", top_up_amount: "300", top_up_balance: balance}`; `PATCH {name: "Чужое имя"}`; `POST /users/{membership}/password`.
- **Ожидается:** в списке `data-account-locked`, нет ссылки на смену пароля; первый PATCH без ошибок; смена имени → ошибка `name`; пароль → **403**; скидка **7.00**, основной **400.00** (100 + 300), имя по-прежнему «Арман».

#### GS-023 · P1 · `billing` · `TestOwnerTopUp_SuperadminCustomer`
**Тест:** `OwnerTopUpTest.php` — «a superadmin playing in a club gets a discount and a top-up like any customer, the account stays locked»
- **Дано:** клиент помечен `is_superadmin = true`.
- **Действие:** `PATCH {discount: "5", top_up_amount: "50", top_up_balance: balance}`; `POST users.password`.
- **Ожидается:** `data-account-locked`; без ошибок; пароль → **403**; скидка **5.00**; основной **150.00**.

#### GS-024 · P1 · `billing` · `TestOwnerWriteOff_MainAndBonus`
**Тест:** `OwnerTopUpTest.php` — «the owner writes off the main or the bonus balance without the cash register»
- **Дано:** клиент 100.00 / 10.00.
- **Действие:** `PATCH {balance_action: write_off, top_up_amount: "30", top_up_balance: balance}`; то же `10` с `bonus_balance`; то же `500` с `balance`; менеджер — `write_off 5`.
- **Ожидается:** flash `Данные клиента сохранены. С баланса списано 30.00 без кассы.`; списание 500 → ошибка `top_up_amount` (не ниже нуля); итог основной **70.00**, бонусный **0.00**; `cash_transactions` пусто; 2 события `user.balance_written_off`; история `[0] = {title: "Списание с баланса администрацией клуба", bonus_amount: "-10.00"}`; менеджер → ошибки `top_up_amount` и `balance_action`.

## Файл: SubscriptionTest.php

#### GS-025 · P1 · `saas` · `TestRegister_NewClubGetsTrial`
**Тест:** `SubscriptionTest.php` — «new clubs start with the configured trial plan»
- **Дано:** гость.
- **Действие:** `POST /register {name: Owner, club_name: "Trial Club", phone: "7001234567", password: secret123 ×2}`.
- **Ожидается:** redirect `/dashboard`; пользователь с телефоном **`+77001234567`** (нормализация); подписка клуба `status = trialing`, план `trial`, `trial_ends_at` в будущем; `GET /dashboard` 200.

#### GS-026 · P1 · `saas` · `TestSubscriptionExpired_RedirectsToBillingAndBlocksApiLogin`
**Тест:** `SubscriptionTest.php` — «expired clubs are sent to billing and cannot issue api tokens»
- **Дано:** клуб `expired-club`; пользователь `+77001234567` / `password`; план trial (14 дней); подписка `trialing`, `started_at = now−15 дн.`, `trial_ends_at = now−1 мин`.
- **Действие:** `GET /dashboard` с сессией `current_club_id`; `GET /billing`; `POST /api/auth/login {phone, password, club_id}`.
- **Ожидается:** redirect на `billing.show`; billing 200 с «Expired Club»; API-вход → **402**.

## Файл: LoyaltyTest.php

Подготовка (`loyaltyCustomer()`, строки 28–44): владелец; клиент (роль `client`) 5000.00 / 0.00; зона «Hall» 600 ₸/час, ПК №3; тариф «Час» 60 мин, 1000 ₸, `main_or_bonus`, `discount_enabled = true`; заголовки агента: Bearer-токен клиента со способностью `club:{id}` + `X-Agent-Token` ПК. `discount_scope` по умолчанию `tariffs` (`PaymentRules::discountScope()`).

#### GS-027 · P1 · `billing` · `TestLoyaltyLevels_SaveNormalizeKeepClear`
**Тест:** `LoyaltyTest.php` — «the manager sets up cumulative discount levels on the finance page»
- **Дано:** владелец; уровни `[{spent:"50000", percent:"10"}, {"",""}, {spent:"10000", percent:"5.5"}]`, `loyalty_enabled=1`.
- **Действие:** `POST /settings/save` (finances, KZT); `GET settings.finances`, `GET settings.api`; save только `currency=RUB`; save `currency=RUB, loyalty_levels=""`.
- **Ожидается:** `loyalty_enabled = true`, `loyalty_levels = [{spent:10000, percent:5.5}, {spent:50000, percent:10}]`; журнал `settings.updated` → `details.loyalty_levels = "— → 10000: 5.5%; 50000: 10%"`; страница с `name="loyalty_levels[1][spent]" value="50000"` и «Накопительная скидка»; `settings.api` 200; сохранение без поля уровней — уровней по-прежнему 2; пустая строка — уровни `[]`.

#### GS-028 · P1 · `billing` · `TestLoyaltyLevels_Validation/percent_over_100`
**Тест:** `LoyaltyTest.php` — «discount levels are validated» (набор `percent over 100`)
- **Дано:** `loyalty_levels = [{spent:1000, percent:101}]`.
- **Действие:** `POST /settings/save` (finances, KZT).
- **Ожидается:** ошибка `loyalty_levels.0.percent`.

#### GS-029 · P1 · `billing` · `TestLoyaltyLevels_Validation/no_percent`
**Тест:** `LoyaltyTest.php` — «discount levels are validated» (набор `no percent`)
- **Дано:** `[{spent:1000, percent:""}]`. **Действие:** то же. **Ожидается:** ошибка `loyalty_levels.0.percent`.

#### GS-030 · P1 · `billing` · `TestLoyaltyLevels_Validation/zero_spent`
**Тест:** `LoyaltyTest.php` — «discount levels are validated» (набор `zero spent`)
- **Дано:** `[{spent:0, percent:5}]`. **Действие:** то же. **Ожидается:** ошибка `loyalty_levels.0.spent`.

#### GS-031 · P1 · `billing` · `TestLoyaltyLevels_Validation/same_spent_twice`
**Тест:** `LoyaltyTest.php` — «discount levels are validated» (набор `same spent twice`)
- **Дано:** `[{spent:1000, percent:5}, {spent:1000, percent:7}]`. **Действие:** то же. **Ожидается:** ошибка `loyalty_levels.0.spent`.

#### GS-032 · P1 · `billing` · `TestLoyaltyLevels_Validation/too_many`
**Тест:** `LoyaltyTest.php` — «discount levels are validated» (набор `too many`)
- **Дано:** 21 уровень `{spent:1, percent:1}` (максимум `LoyaltyProgram::MAX_LEVELS = 20`). **Действие:** то же. **Ожидается:** ошибка `loyalty_levels`.

#### GS-033 · P1 · `billing` · `TestTariffPrice_PersonalDiscountWhenLoyaltyOff`
**Тест:** `LoyaltyTest.php` — «without the cumulative discount tariff prices are the same as before»
- **Дано:** личная скидка 12.5%, `total_spent = 100000`; `loyalty_enabled = false`, уровень `{1000 → 50%}`.
- **Действие:** `GET /api/tariffs` (агент).
- **Ожидается:** `data.0.price = "875.00"`. Формула (`AgentTariffPurchaseService::discounted()`): `100000 − intdiv(100000 × 1250 + 5000, 10000)` = 100000 − 12500 = **87500 тиын**.

#### GS-034 · P1 · `billing` · `TestLoyalty_MaxOfPersonalAndCumulative`
**Тест:** `LoyaltyTest.php` — «a customer who has spent enough gets the level discount, the larger of personal and cumulative»
- **Дано:** второй тариф «Без скидки» 30 мин, 1000 ₸, `discount_enabled = false`; личная скидка 5%; лояльность включена `{1500 → 10%}`, `{3000 → 20%}`; клиент 5000.00 / 0.
- **Действие и ожидания по шагам:**
  1. `GET /api/tariffs`: `data.1` («Час») = **950.00** (личные 5%), `data.0` («Без скидки», короче) = 1000.00.
  2. `POST /api/tariffs/{Час}/purchase {computer_id}` → `new_balance = "4050.00"`; `total_spent = 950.00`.
  3. `POST /api/guest/minutes/purchase {minutes: 60}` (600 ₸/час, скидка на поминутку не действует при scope `tariffs`) → `total_spent = 1550.00` (уровень 10%), основной 3450.00.
  4. `GET /api/tariffs`: «Час» = **900.00**, «Без скидки» = 1000.00.
  5. Покупка «Час» → `new_balance = "2550.00"`; `total_spent = 2450.00`.
  6. Владелец `GET /users`: «10.00%», «2 450.00», «накопительная».

#### GS-035 · P1 · `billing` · `TestLoyalty_BonusPaymentNotSpending`
**Тест:** `LoyaltyTest.php` — «bonus payments do not count as spending»
- **Дано:** клиент 400.00 / 1000.00; смешанная оплата включена (`allowMixedPayment()`), `bonus_max_percent` по умолчанию 100.
- **Действие:** покупка «Час» 1000 ₸ без выбора баланса (старый агент).
- **Ожидается:** 200; `total_spent = 400.00`. По коду (`sell()`, ветка `$mixed`): бонусами `min(100000, 100000 − 40000)` = 600.00, основным 400.00 → балансы 0.00 / 400.00.

#### GS-036 · P1 · `stock` · `TestAgentShop_OrderCountsAsSpendingCancelTakesBack`
**Тест:** `LoyaltyTest.php` — «agent shop orders count as spending and a cancelled order is taken back»
- **Дано:** товар «Cola» (SKU COLA) 350 ₸, остаток 10.
- **Действие:** `POST /api/guest/shop/purchase {computer_id, items: [{id: cola, quantity: 2}]}`; владелец `POST shop-orders/{order}/cancel`.
- **Ожидается:** `total_spent = 700.00` после заказа; после отмены `total_spent = 0.00`. По коду (`AgentShop::purchase()/cancel()`): основной 5000 → 4300 → снова 5000; остаток колы 10 → 8 → 10.

#### GS-037 · P1 · `billing` · `TestMigration_BackfillLoyaltySpending`
**Тест:** `LoyaltyTest.php` — «the migration counts past spending from the activity log and converts the draft settings»
- **Дано:** журнал клиента: `tariff.purchased {amount 1000, main 700, bonus 300}`, `time.purchased {main 250.50}`, `shop.purchased {amount 500}`, `shop.order_cancelled {amount 500}` (от владельца, subject = клиент); владельца: `tariff.purchased {main 40}`; `total_spent` клиента = 99999; настройки `finances = {currency KZT, tax_rate "0", loyalty_type cumulative, loyalty_enabled true, loyalty_rules [{threshold 20000, discount 7}]}`.
- **Действие:** миграция `2026_10_18_100000_backfill_loyalty_spending.php` `up()`.
- **Ожидается:** клиент `total_spent = 950.50` (700 + 250.50 + 500 − 500); владелец `40.00`; `finances = {currency: KZT, loyalty_enabled: true, loyalty_levels: [{spent: 20000, percent: 7}]}` (`tax_rate`, `loyalty_type`, `loyalty_rules` удалены). Для Go — сценарий инструмента переноса данных (`tools/migrate-from-mysql`).

## Файл: PerMinuteTest.php

#### GS-038 · P1 · `club` · `TestZoneHourlyPrice_SetAndShown`
**Тест:** `PerMinuteTest.php` — «the hourly price of a zone is set in the zone form and shown in tariffs»
- **Дано:** зона VIP с ПК №1.
- **Действие:** `PUT /club-map/{zone}` c `price_per_hour = "900"`; `GET /tariffs`; `GET /club-map`; повтор с `price_per_hour = ""`.
- **Ожидается:** `price_per_hour = 900.00`; тарифы: «Поминутная оплата по зонам», «900,00 KZT», «15,00 KZT» (900/60); карта: `data-price-per-hour="900.00"`; пустое значение → `NULL`.

#### GS-039 · P1 · `billing` · `TestMinutesPurchase_MainFirstThenBonus`
**Тест:** `PerMinuteTest.php` — «a customer buys minutes at the hourly price of the zone, main balance first»
- **Дано:** `Carbon::setTestNow('2026-10-15 10:00:00 UTC')`; клиент 100.00 / 50.00; смешанная оплата; зона «Hall» 500 ₸/час, ПК №1; агент-токен ПК.
- **Действие:** `GET /api/guest/minutes?computer_id`; `POST /api/guest/minutes/purchase {minutes: 17}` без `X-Agent-Token`; то же с токеном; затем `{minutes: 60}`; `GET /api/guest/history`.
- **Ожидается:** `available = true`, `zone = "Hall"`, `price_per_hour = "500.00"`; без токена ПК → **403**; с токеном → `minutes_added 17`, `new_balance "0.00"`, `new_bonus "8.33"`. Цена `intdiv(50000 × 17 + 30, 60)` = **14167 тиын = 141.67**: основной 100.00 + бонус 41.67. ПК: `current_session_user_id` = клиент, `session_ends_at = 10:17:00`; журнал `time.purchased ⊇ {amount "141.67", main_balance_amount "100.00", bonus_balance_amount "41.67", duration_minutes 17}`. 60 минут (500.00) → ошибка валидации `balance`. История `data.0 = {kind purchase, title "Поминутная оплата: 17 мин.", amount "-100.00", bonus_amount "-41.67"}`.

#### GS-040 · P1 · `billing` · `TestMinutesPurchase_Rejections`
**Тест:** `PerMinuteTest.php` — «minutes are not sold in a zone without an hourly price, out of range or on someone else's session»
- **Дано:** клиент основной 10000; ПК №1 в зоне «Plain» без цены; ПК №2 в «Hall» 600 ₸/час, на нём сессия другого пользователя до now+1 ч.
- **Действие / Ожидается:** `GET minutes` для Plain → `available = false`; покупка 30 мин на Plain → ошибка `minutes`; 0 мин → `minutes`; **721** мин → `minutes` (максимум 720); 30 мин на чужой сессии → `computer_id`; основной остаётся **10000.00**.

#### GS-041 · P1 · `billing` · `TestMinutesPriceInCents/600x1`
**Тест:** `PerMinuteTest.php` — «the price of minutes is the hourly price divided by 60, rounded to the cent» (набор `['600.00', 1, 1000]`)
- **Дано:** час 600.00, 1 мин. **Действие:** `AgentTariffPurchaseService::minutesPriceInCents()` = `intdiv(cents × minutes + 30, 60)`. **Ожидается:** **1000** тиын.

#### GS-042 · P1 · `billing` · `TestMinutesPriceInCents/600x90`
**Тест:** то же (набор `['600.00', 90, 90000]`). **Дано:** 600.00, 90 мин. **Ожидается:** **90000**.

#### GS-043 · P1 · `billing` · `TestMinutesPriceInCents/500x17`
**Тест:** то же (набор `['500.00', 17, 14167]`). **Дано:** 500.00, 17 мин. **Ожидается:** **14167** (850030 / 60 = 14167.17 → 14167).

#### GS-044 · P1 · `billing` · `TestMinutesPriceInCents/1x1`
**Тест:** то же (набор `['1.00', 1, 2]`). **Дано:** 1.00, 1 мин. **Ожидается:** **2** (130 / 60 = 2.17 → 2; без «+30» было бы 1).

#### GS-045 · P1 · `billing` · `TestMinutesPriceInCents/0_50x1`
**Тест:** то же (набор `['0.50', 1, 1]`). **Дано:** 0.50, 1 мин. **Ожидается:** **1** (80 / 60 → 1).

## Файл: SessionTimeTest.php

Подготовка: `freezeTime()` перед каждым тестом (время стоит, двигается только `travel`); `timeCustomer(main, bonus)` — владелец, клиент роль `client` с балансами, зона «Hall» 600 ₸/час, ПК №4, заголовки агента; `timeTariff()` — «Пакет» 120 мин, 1000 ₸, `main_or_bonus`, скидка разрешена. Завершение сессии — `POST /api/club-computers/{pc}/session/end {session_id}`.

#### GS-046 · P1 · `billing` · `TestSettlement_UnusedMinutesBackToSourceBalances`
**Тест:** `SessionTimeTest.php` — «unused minutes go back to the balances they were paid from when the customer ends the session»
- **Дано:** 1000.00 / 1000.00; `bonus_max_percent = 50`, смешанная оплата.
- **Действие:** покупка 60 мин c `balance = bonus` (600 ₸: бонусами 50% = 300, основным 300); через 30 мин завершение сессии; `GET /api/guest/history`.
- **Ожидается:** после покупки **700.00 / 700.00**; ответ завершения `{refunded "150.00", refunded_bonus "150.00", kept_minutes 0}` (`intdiv(30000 × 1800, 3600)` = 15000 по каждому балансу — `SessionSettlement::settle()`); балансы **850.00 / 850.00**; `current_session_id = NULL`; `session_purchases` пусто; журнал `time.refunded ⊇ {main_balance_amount "150.00", bonus_balance_amount "150.00"}`; история `data.0 = {kind refund, amount "150.00"}`.

#### GS-047 · P1 · `billing` · `TestSettlement_UsageLimitedTariffKeptAsPackage`
**Тест:** `SessionTimeTest.php` — «a tariff with a usage limit keeps its unused time; it is started later without paying»
- **Дано:** 1000.00 / 0; тариф «Пакет» 120 мин, 1000 ₸, `usage_limit_minutes = 43200` (30 дней).
- **Действие:** покупка `balance=main` (минут = `min(120, 43200)` = 120); через 30 мин завершение; `GET /api/guest/packages`; `POST /api/guest/packages/{p}/start`; через 60 мин завершение.
- **Ожидается:** `minutes_added 120`; завершение `{refunded "0.00", kept_minutes 90}`; `time_packages`: одна запись `minutes_left 90`, `expires_at` = дата покупки + 30 дней; список пакетов `data.0 = {minutes 90, name "Пакет"}`; старт → `minutes_added 90`, балансы **0.00 / 0.00**, пакет удалён, конец сессии ≈ now + 90 мин (±1); второе завершение → `kept_minutes 30`, `expires_at` новой записи — та же дата.

#### GS-048 · P1 · `billing` · `TestSettlement_TariffNotRefundedMinutesRefundedLastFirst`
**Тест:** `SessionTimeTest.php` — «a tariff without a usage limit is not refunded; minutes bought on top are, the last part first»
- **Дано:** 2000.00; тариф 60 мин, 500 ₸, без лимита использования.
- **Действие:** покупка тарифа; покупка 30 мин (300 ₸); через 15 мин завершение.
- **Ожидается:** после покупок основной **1200.00**; осталось 75 мин: последняя часть (30 мин поминутки) возвращается целиком, 45 мин тарифа сгорают → `{refunded "300.00", kept_minutes 0}`; основной **1500.00**; пакетов нет.

#### GS-049 · P1 · `billing` · `TestPaymentChoice_BonusUpToClubPercent`
**Тест:** `SessionTimeTest.php` — «the customer picks the balance: main, or bonuses up to the club percent; some tariffs take no bonuses»
- **Дано:** 2000.00 / 500.00; `bonus_max_percent 30`, смешанная оплата; тарифы «Пакет» (1000) и «Только деньги» (`main_balance_only`).
- **Действие / Ожидается:**
  - `GET /api/tariffs`: `data.0 = {bonus_allowed true, bonus_max_percent 30, mixed_payment true}`, `data.1.bonus_allowed = false`; `GET minutes`: `bonus_max_percent 30`, `mixed_payment true`.
  - Покупка «Пакет» `balance=bonus`: бонусами `min(500, intdiv(100000×30,100)=300)` = 300 → `{new_balance "1300.00", new_bonus "200.00"}`.
  - Покупка «Пакет» `balance=main` → `{new_balance "300.00", new_bonus "200.00"}`.
  - «Только деньги» `balance=bonus` → **422**, ошибка `balance`.
  - «Пакет» `balance=cash` → **422**, ошибка `balance`.
  - `bonus_max_percent = 0`: 10 мин `balance=bonus` → **422** `balance`.

#### GS-050 · P1 · `billing` · `TestPaymentChoice_NoMixed_OneBalance`
**Тест:** `SessionTimeTest.php` — «without mixed payment a purchase is paid from one balance: the main one or bonuses for the whole price»
- **Дано:** 2000.00 / 500.00; смешанная оплата выключена; тариф 1000.
- **Действие / Ожидается:**
  - `bonus_max_percent 30`: тарифы `{bonus_allowed false, bonus_max_percent 0, mixed_payment false}`; minutes `bonus_max_percent 0`; покупка бонусами → **422** `balance`.
  - `bonus_max_percent 100`: `bonus_allowed true, bonus_max_percent 100`; покупка бонусами (500 < 1000) → **422**, `message = "Бонусов не хватает на всю цену. Оплатите с основного баланса."`.
  - Бонусный = 1500: покупка бонусами → `{new_balance "2000.00", new_bonus "500.00"}`.
  - Без выбора баланса (старый агент) → основной: `{new_balance "1000.00", new_bonus "500.00"}`; итог **1000.00 / 500.00**.

#### GS-051 · P1 · `billing` · `TestDiscountScope_TariffsHourlyAll`
**Тест:** `SessionTimeTest.php` — «the club chooses where the discount works: tariffs, hourly time or both»
- **Дано:** личная скидка 10%; 1000.00; тариф 1000; зона 600 ₸/час.
- **Действие / Ожидается** (`[цена тарифа, цена часа]`): по умолчанию (`tariffs`) **["900.00", "600.00"]**; `hourly` → **["1000.00", "540.00"]**; `all` → **["900.00", "540.00"]**; покупка 30 мин при 540 ₸/час (`intdiv(54000×30+30, 60)` = 27000 → 270.00) → `new_balance "730.00"`.

#### GS-052 · P1 · `club` · `TestFinanceSettings_DiscountScopeBonusPercentMixed`
**Тест:** `SessionTimeTest.php` — «the finance page saves where the discount works and the bonus percent»
- **Действие:** `POST settings.save {finances, KZT, discount_scope: hourly, bonus_max_percent: 40, allow_mixed_payment: "1"}`; затем `{discount_scope: never, bonus_max_percent: 140}`.
- **Ожидается:** `finances ⊇ {discount_scope "hourly", bonus_max_percent 40, allow_mixed_payment true}`; страница: `name="allow_mixed_payment" value="1" class="settings-switch" checked`, `value="hourly" selected`, `href="#payment-rules"`; второй запрос → ошибки `discount_scope`, `bonus_max_percent`.

#### GS-053 · P1 · `billing` · `TestWholeBalance_SellsMinutesBalancePaysFor`
**Тест:** `SessionTimeTest.php` — «with "play on the whole balance" the agent is told so, and the minutes the balance pays for are sold»
- **Дано:** 2000.00; зона 600 ₸/час.
- **Действие:** `GET minutes` (`whole_balance false`); включить `allow_whole_balance`; `GET minutes`; купить 200 мин `balance=main`; через 60 мин завершить.
- **Ожидается:** `whole_balance true`; покупка: `new_balance "0.00"`, `minutes_added 200` (200 × 10 ₸ = 2000.00); завершение → `refunded "1400.00"` (140 мин: `intdiv(200000 × 8400, 12000)` = 140000); основной **1400.00**.

#### GS-054 · P3 · `club` · `TestFinanceSettings_WholeBalanceSaved`
**Тест:** `SessionTimeTest.php` — «the finance page saves "play on the whole balance"»
- **Действие:** save `{discount_scope tariffs, bonus_max_percent 100, allow_whole_balance "1"}`.
- **Ожидается:** `allow_whole_balance = true`; чекбокс `checked` на странице.

#### GS-055 · P1 · `billing` · `TestAutoBilling_AgentLearnsSettingAndPause`
**Тест:** `SessionTimeTest.php` — «per-minute billing by itself: the agent learns the setting and the pause after sign-in»
- **Действие:** `GET /api/guest/minutes` до и после `{auto_billing true, auto_billing_pause_minutes 3}`.
- **Ожидается:** по умолчанию `auto_billing false`, `auto_billing_pause_minutes 5`; после — `true`, `3` (`PaymentRules::autoBillingPauseMinutes()`, диапазон 0–60).

#### GS-056 · P3 · `club` · `TestFinanceSettings_AutoBillingPause`
**Тест:** `SessionTimeTest.php` — «the finance page saves per-minute billing after sign-in and its pause»
- **Действие:** save `{auto_billing "1", auto_billing_pause_minutes 7}`; save с `auto_billing_pause_minutes 90`.
- **Ожидается:** `{auto_billing true, auto_billing_pause_minutes 7}`; поле `type="number" min="0" max="60" step="1" value="7"`; 90 → ошибка `auto_billing_pause_minutes`.

## Файл: TariffTest.php

#### GS-057 · P1 · `billing` · `TestTariffCreate_AllFields`
**Тест:** `TariffTest.php` — «authenticated users can create and view tariffs»
- **Дано:** зона «Зал» с ПК, «Пустая зона» без ПК; `Storage::fake()`, файл `settings.json` с `finances.currency = RUB`.
- **Действие:** `GET /tariffs`; `POST /tariffs {name Ночной, description, duration 8 hours, price 3500, main_or_bonus, discount 1, zone_ids [Зал], usage_limit 2 hours, quantity_limit 5, validity_periods [mon 09:00–fri 18:00, sat 10:00–sun 20:00], is_active 1}`.
- **Ожидается:** страница: кнопка `tariff-add-button`, диалог `tariff-dialog`, «Зоны и тарифы», логотип, нет поля `currency`, «Зал» есть, «Пустая зона» нет; после POST redirect + flash `Тариф создан.`; запись: `duration_minutes 480`, `price 3500.00`, `currency RUB` (из файла настроек), активен, `usage_limit_minutes 120`, `quantity_limit 5`, периоды сохранены, зоны `[Зал]`; список: «Ночной», «480 мин.», «3 500,00 RUB».

#### GS-058 · P3 · `billing` · `TestTariffCreate_Validation`
**Тест:** `TariffTest.php` — «tariff fields are validated»
- **Дано:** все поля неверные (пустое имя, 0 длительность, неизвестная единица, цена −1, метод `invalid`, зона без ПК, лимиты 0, периоды `[]`, `is_active invalid`).
- **Ожидается:** ошибки `name, duration_value, duration_unit, price, payment_method, zone_ids.0, usage_limit_value, usage_limit_unit, quantity_limit, validity_periods, is_active`; тарифов 0.

#### GS-059 · P1 · `billing` · `TestAgentTariffPurchase_DiscountUsageLimitQuantityLimit`
**Тест:** `TariffTest.php` — «agent can list and purchase an eligible tariff for its active computer session»
- **Дано:** пользователь со скидкой 10%; смешанная оплата; зона «Игровой зал», ПК №7; тариф «Ночной» 120 мин, 100 ₸, скидка, `usage_limit_minutes 90`, `quantity_limit 1`; баланс 80.00 / 20.00; время заморожено на начало секунды.
- **Действие:** `GET /api/tariffs` без токена; с токеном; `POST /api/tariffs/{t}/purchase {computer_id}`; снова список.
- **Ожидается:** без токена **401**; список: `id` строкой, `price "90.00"` (100 − 10%), `minutes 90` (`min(120, 90)`), `currency KZT`; покупка: `success true`, `new_balance "0.00"`, `new_bonus "10.00"` (основной 80 + бонус 10), `minutes_added 90`; ПК: `current_session_id` = `session_id` ответа, пользователь, `session_ends_at = now + 90 мин`; повторный список пуст (лимит количества 1 исчерпан).

#### GS-060 · P1 · `billing` · `TestAgentTariffs_FilteredByPCZone`
**Тест:** `TariffTest.php` — «the agent lists only the tariffs of its PC zone; tariffs without zones are everywhere»
- **Дано:** зоны «Зал» (ПК 1) и «VIP» (ПК 2); тарифы «Зальный» [Зал], «Випный» [VIP], «Везде» [].
- **Ожидается:** `?computer_id=hallPc` → [Везде, Зальный]; `vipPc` → [Везде, Випный]; без ПК → все три; после `vipPc.current_session_user_id = user` без ПК → [Везде, Випный].

## Файл: TariffEditTest.php

Подготовка: `editableTariff()` — «Вечер», 180 мин, 1500 ₸, скидка, `usage_limit 43200 мин`, без лимита количества, период пт 18:00 – вс 23:59. `tariffForm()` — `main_balance_only`, «Вечер+», 4 часа, 1800, без скидки, без лимита использования, лимит количества 50, пн 00:00 – вс 23:59, неактивен.

#### GS-061 · P3 · `billing` · `TestTariffEdit_FormPrefilled`
**Тест:** `TariffEditTest.php` — «the edit form opens filled in with the tariff»
- **Ожидается:** в списке ссылка на `tariffs.edit`; форма: «Изменить тариф», `action` на `tariffs.update`, `_method=PATCH`, `value="Вечер"`, `duration_value = 3` (180 мин → 3 часа), лимит «30» `days` selected, `18:00`.

#### GS-062 · P1 · `billing` · `TestTariffUpdate_UniqueNameAndChangedFieldsLogged`
**Тест:** `TariffEditTest.php` — «the manager changes a tariff; the name stays unique in the club»
- **Дано:** «Вечер» и «Ночь»; зона VIP с ПК.
- **Действие:** PATCH «Вечер» с именем «Ночь»; PATCH c именем «Вечер» и `zone_ids [VIP]`.
- **Ожидается:** первый → ошибка `name`; второй → redirect без ошибок; `duration_minutes 240`, `price 1800.00`, `main_balance_only`, `usage_limit_minutes NULL`, `quantity_limit 50`, неактивен, зоны [VIP]; журнал `tariff.updated` `details.changed = ["price", "duration_minutes", "is_active", "zones"]`.

#### GS-063 · P1 · `billing` · `TestTariffSalePeriods_AgentCashRegisterPurchase`
**Тест:** `TariffEditTest.php` — «a tariff is on sale only on its sale days and times: the agent, the cash register and purchases follow it»
- **Дано:** `travelTo('2026-10-28 12:00')` (среда) в часовом поясе клуба; созданы «Ночь» (`sale_periods` сб 22:00 – вс 06:00), «Будни» (пн 09:00 – пт 18:00), «Всегда» (без периодов продажи), все активны.
- **Действие / Ожидается:** `sale_periods` «Ночи» сохранены как есть; форма: `name="sale_periods[0][start_time]" type="time" value="22:00"`, «Сейчас не продаётся: вне времени продажи»; тариф с `xyz`/`25:00` → ошибки `sale_periods.0.start_day`, `sale_periods.0.start_time`; агент видит [Будни, Всегда]; карта клуба не предлагает `tariff:{Ночь}`; продажа гостевой сессии с «Ночь» (`POST club-computers.guest-session`, cash) → **422** `tariff_id`; `travelTo('2026-10-31 23:00')` (суббота) → агент видит [Всегда, Ночь].

#### GS-064 · P3 · `billing` · `TestTariffList_FilterByZone`
**Тест:** `TariffEditTest.php` — «the tariff list is filtered by zone: its own tariffs and those of every zone»
- **Ожидается:** без фильтра видны «Зальный», «Випный», «Общий»; `?zone=VIP` → `<option value="{vip}" selected>VIP</option>`, «Випный», «Общий», нет «Зальный».

#### GS-065 · P3 · `billing` · `TestMigration_FixPeriodsEndingOnMonday`
**Тест:** `TariffEditTest.php` — «period rows saved until Monday by mistake end on their own day again»
- **Дано:** «День» с периодами: пн 10–пн 17; чт 10–пн 17; вс 22–пн 06; пт 23–пн 06. «Неделя»: пн 00:00–вс 23:59.
- **Действие:** миграция `2026_10_27_100000_fix_tariff_periods_ending_on_monday.php`.
- **Ожидается:** `end_day` «Дня» = [mon, thu, mon, sat]; «Неделя» — `sun`. (Сценарий для переноса данных.)

#### GS-066 · P1 · `cash` · `TestGuestSession_TariffOutsideValidityRejected`
**Тест:** `TariffEditTest.php` — «the cash register does not start a tariff outside its validity period»
- **Дано:** `travelTo('2026-10-28 23:00')` (среда); тариф «День» действует ср 10:00–17:00; открытая смена.
- **Ожидается:** на карте нет `tariff:{День}`; `POST club-computers.guest-session {tariff_id День, cash}` → **422** `tariff_id`.

#### GS-067 · P3 · `billing` · `TestTariffForm_NoColorNoClubName`
**Тест:** `TariffEditTest.php` — «the tariff form has no color and no club name»
- **Ожидается:** нет `name="color"` и `name="club_name"`.

#### GS-068 · P1 · `billing` · `TestTariffList_ReasonsNotOffered`
**Тест:** `TariffEditTest.php` — «the tariff list says why a tariff is not offered now: sold out or outside its validity period»
- **Дано:** `travelTo('2026-10-31 16:00')` (суббота); тариф «3+2 main», `quantity_limit 2`, период пн 00:00–вт 23:59; две записи журнала `tariff.purchased`.
- **Ожидается:** «Продано 2 из 2 — лимит количества исчерпан, тариф не продаётся», «Сейчас не продаётся: вне периода действия». (Лимит количества считается по числу записей `tariff.purchased` в журнале — `AgentTariffPurchaseService::purchaseCount()`.)

#### GS-069 · P1 · `billing` · `TestTariffDelete_SoftDeleteKeepsHistoryAndPasses`
**Тест:** `TariffEditTest.php` — «the owner deletes a tariff: off sale everywhere, history and passes keep it»
- **Дано:** «Ночь»; клиент; журнал `tariff.purchased {amount 1500.00}`; абонемент `time_packages {minutes_left 60, expires_at now+1 д.}`.
- **Действие:** `GET /tariffs`; `DELETE /tariffs/{t}`; список агента; повторное создание «Ночь».
- **Ожидается:** «Удалить тариф «Ночь»?»; redirect + flash `Тариф «Ночь» удалён.`; тариф мягко удалён (`withTrashed` находит); абонемент по-прежнему ссылается на «Ночь»; событие `tariff.deleted`; история клиента `data[0].title = "Покупка тарифа «Ночь»"`; агенту — 0 тарифов; имя снова свободно.

#### GS-070 · P2 · `billing` · `TestTariffDelete_OwnerOnly`
**Тест:** `TariffEditTest.php` — «only the owner deletes tariffs»
- **Дано:** менеджер.
- **Ожидается:** нет «Удалить тариф»; `DELETE` → **403**; тариф не удалён.

#### GS-071 · P1 · `billing` · `TestTariffPeriods_ClubLocalTimeNotUTC`
**Тест:** `TariffEditTest.php` — «the agent keeps to the club local days and hours of a tariff, not UTC»
- **Дано:** часовой пояс клуба `Asia/Almaty` (в тесте указан UTC+5); тариф «Дневной», продажа сб 08:00–15:00, действие сб 08:00–18:00; основной владельца 10000; ПК №5.
- **Действие:** `travelTo('2026-10-24 19:00 Asia/Almaty')` → список и покупка; `travelTo('2026-10-24 12:00 Asia/Almaty')` → список.
- **Ожидается:** в 19:00 — список пуст, покупка **422**; в 12:00 — `data.0.name = "Дневной"`.

## Файл: AgentShopTest.php

Подготовка (`shopSetup()`): владелец; клиент «Арман» (роль `client`) 1000.00 / 300.00; зона «Hall», ПК №5 с агент-токеном; «Cola» (COLA) 350 ₸, 10 шт.; «Чипсы» (CHIPS) 250.50 ₸, 1 шт. Заголовки (`shopRequest()`): Bearer клиента + `X-Agent-Token` ПК.

#### GS-072 · P1 · `stock` · `TestAgentShop_PaysFromMainBalance`
**Тест:** `AgentShopTest.php` — «a customer pays for goods in the agent from the main balance and the staff get the order»
- **Действие:** `POST /api/guest/shop/purchase {computer_id, items: [{cola, 2}, {chips, 1}]}`; `GET /api/guest/history?filter=purchases`.
- **Ожидается:** `amount "950.50"` (2×350 + 250.50), `balance "49.50"`; балансы **49.50 / 300.00** (бонусы товары не оплачивают); колы **8**, чипсов **0**; 2 `stock_movements` с `note = "Заказ из агента"`; сообщений чата 0; `shop_orders`: одна запись `{user_id, club_computer_id, computer_name = displayName(), status "new", amount "950.50"}`; журнал `shop.purchased.details.amount = "950.50"`; история `data.0 = {kind shop, amount "-950.50", note "Cola × 2, Чипсы × 1"}`.

#### GS-073 · P1 · `stock` · `TestAgentShop_RejectsStockBalanceUnknownWrongPC`
**Тест:** `AgentShopTest.php` — «goods are not sold beyond the stock or the main balance, and nothing is charged then»
- **Действие / Ожидается:** чипсы ×2 (остаток 1) → ошибка `items`; кола ×3 = 1050 > 1000 основного (бонусы 300 не учитываются) → ошибка `balance`; товар 999999 → `items`; `X-Agent-Token = zag_wrong` → **403** `code = wrong_computer`. Итог: основной **1000.00**, колы 10, заказов 0.

#### GS-074 · P3 · `stock` · `TestAgentShop_DisabledByClub`
**Тест:** `AgentShopTest.php` — «the club can turn the shop in the agent off»
- **Дано:** `settings.club.agent_shop_enabled = false`.
- **Ожидается:** `POST /api/agent/heartbeat` → `club.guest_features.shop = false`; покупка → **403** `code = shop_disabled`.

#### GS-075 · P1 · `billing` · `TestFriendTransfer_RequiresPassword`
**Тест:** `AgentShopTest.php` — «a transfer to a friend needs the account password»
- **Дано:** `guests.allow_balance_transfer_to_friends = true`; Арман (+77011112233) основной 100; Дана (+77029998877); дружба принята.
- **Действие:** `POST /api/guest/friends/{friendship}/transfer {amount "10"}` без пароля; с `password=wrong`; с `password=password`.
- **Ожидается:** первые два → ошибка `password`, основной 100.00; третий → 200, `balance "90.00"`.

#### GS-076 · P3 · `guest` · `TestGuestHistory_Filters`
**Тест:** `AgentShopTest.php` — «the customer history filters purchases, top-ups, transfers and visits»
- **Дано:** журнал клиента: `user.balance_topped_up {amount 500}`, `auth.api_login {device PC-1}`, `time.purchased {amount 100.00, 30 мин}`.
- **Ожидается:** без фильтра `kind` = [purchase, login, top_up]; `purchases` → [purchase]; `top_ups` → [top_up]; `visits` → [login]; `transfers` → []; `filter=all-of-it` → ошибка `filter`.

#### GS-077 · P1 · `agent` · `TestHeartbeat_FreshBootResetsGuestSessionOnly`
**Тест:** `AgentShopTest.php` — «a restart of the PC resets a guest session, a later restart of the agent does not»
- **Дано:** ПК №3, гостевая сессия `guest-1` (`session_guest true`, до now+1 ч).
- **Действие:** heartbeat `{}`; heartbeat `{fresh_boot: true}`; затем платная сессия клиента `paid-1` и heartbeat `{fresh_boot: true}`.
- **Ожидается:** первый → `session.guest = true`; второй → `session = null`, у ПК `session_guest false`, `session_ends_at NULL`, одно событие `computer.guest_session_reset`; платная сессия не трогается → `session.id = "paid-1"`.

#### GS-078 · P2 · `cash` · `TestGuestMode_DisabledRejectsCashGuestSession`
**Тест:** `AgentShopTest.php` — «guest mode can be turned off: no "start tariff" on the map and the cash register refuses it»
- **Ожидается:** карта содержит `guest-session-open`; после `guests.guest_mode_enabled = false` — нет; `POST club-computers.guest-session {minutes 30, cash}` → **403**.

#### GS-079 · P3 · `club` · `TestClubSettings_ShopIdleLogoutGuestModeToAgent`
**Тест:** `AgentShopTest.php` — «the club sets the shop, the sign-out without a session and guest mode, and the agent gets them»
- **Действие:** save `club {session_idle_timeout 15, pc_restart_after_session_end 0.5, session_auto_terminate_when_pc_unavailable 5, booking_interval 30, agent_idle_logout_minutes 10, agent_shop_enabled "0"}`; save `guests {guest_mode_enabled "0"}`.
- **Ожидается:** страница с текстами «Если нет активной сессии, через сколько отлогинить пользователя (в минутах)», «Включить магазин в агенте»; `club.agent_idle_logout_minutes = 10`, `agent_shop_enabled false`, `guests.guest_mode_enabled false`; heartbeat → `club.idle_logout_minutes 10`, `club.guest_features.shop false`.

#### GS-080 · P1 · `stock` · `TestShopOrders_HandOverOrCancelWithRefund`
**Тест:** `AgentShopTest.php` — «the front desk sees new orders, hands them over or cancels them with a refund»
- **Дано:** два заказа по 1 колe (350) → основной 300.00, колы 8; кассир «Кассир».
- **Действие:** `GET /shop-orders`; `GET /shop-orders/state`; `POST shop-orders/{first}/done`; `cancel` первого; `cancel` второго; история клиента; `GET /shop-orders?status=all`.
- **Ожидается:** «Заказ №{first}», «Cola»; state `{new: 2, latest: second.id}`; первый `status done`, `handled_by` = кассир; отмена обработанного → ошибка `order`; второй `cancelled`, основной **650.00** (300 + 350), колы **9**; история `data.0 = {kind refund, amount "350.00"}`; в списке «Кассир».

#### GS-081 · P2 · `auth` · `TestAgentLogin_OnePCAtATimeForCustomers`
**Тест:** `AgentShopTest.php` — «a customer signed in on one PC cannot sign in on another until signing out there»
- **Дано:** клиент +77015550011 / `password`.
- **Действие:** `POST /api/agent/user/login {phone, password, club_id, device_name PC-01}`, токен `last_used_at = now`; вход на PC-02; повтор на PC-01; `last_used_at = now−5 мин` и вход на PC-02; оператор (+77015550022, роль `operator`) с активным токеном PC-03 входит на PC-04.
- **Ожидается:** PC-01 → 200 + `access_token`; PC-02 → **409**, `code signed_in_elsewhere`, `message "Вы уже вошли на другом ПК (PC-01). Выйдите там, чтобы войти здесь."`; повтор PC-01 → 200; после 5 мин простоя PC-02 → 200; оператор → 200 (сотрудников ограничение не касается).

## Файл: ComputerSessionTest.php

Подготовка: `paidComputer($owner, $minutes)` — зона «Hall», ПК №1 `PC-01`, сессия `paid-session` пользователя до now + N мин. Bearer-токен со способностью `club:{id}`.

#### GS-082 · P1 · `agent` · `TestHeartbeat_CannotMovePaidSessionEnd`
**Тест:** `ComputerSessionTest.php` — «the agent cannot move the end of a paid session»
- **Действие:** `POST /api/club-computers/{pc}/heartbeat {state in_session, session_id paid-session, session_ends_at "2099-01-01T00:00:00Z"}`.
- **Ожидается:** 200, `session.ends_at` = исходное (now+60 мин); в БД без изменений.

#### GS-083 · P1 · `agent` · `TestHeartbeat_StateKeepsPaidSession/idle`
**Тест:** `ComputerSessionTest.php` — «idle, locked or restarting agents do not erase a paid session» (набор `idle`)
- **Действие:** heartbeat `{state: idle}`.
- **Ожидается:** `session.id = paid-session`, `session.user_id` = клиент; `current_session_id` сохранён; `agent_state = idle`.

#### GS-084 · P1 · `agent` · `TestHeartbeat_StateKeepsPaidSession/locked`
**Тест:** то же (набор `locked`). **Ожидается:** как выше, `agent_state = locked`.

#### GS-085 · P1 · `agent` · `TestHeartbeat_StateKeepsPaidSession/shutting_down`
**Тест:** то же (набор `shutting_down`). **Ожидается:** как выше, `agent_state = shutting_down`.

#### GS-086 · P2 · `agent` · `TestPaidSession_OtherMemberCannotTakeOverOrEnd`
**Тест:** `ComputerSessionTest.php` — «another club member cannot take over or wipe a paid session»
- **Дано:** сессия владельца; токен кассира.
- **Ожидается:** heartbeat `{in_session, session_id: stolen}` → **422**; `session/end {paid-session}` → **403**; `current_session_user_id` = владелец.

#### GS-087 · P1 · `billing` · `TestSessionExpiry_HeartbeatAndScheduler`
**Тест:** `ComputerSessionTest.php` — «a paid session ends when its time is over: on heartbeat and by the scheduler»
- **Дано:** ПК1 сессия до now+30 мин; ПК2 сессия `s2` до now+10 мин; `Carbon::setTestNow(now + 31 мин)`.
- **Действие:** heartbeat ПК1 `{in_session, paid-session}`; команда `club-computers:expire-sessions`.
- **Ожидается:** `session null`, `session_expired true`, у ПК1 `current_session_id NULL`; после команды у ПК2 `current_session_id NULL`. (В Go — воркер истечения сессий.)

#### GS-088 · P1 · `billing` · `TestSessionEnd_ByStaffLogged`
**Тест:** `ComputerSessionTest.php` — «the owner or staff end a session early and it is logged»
- **Дано:** пользователь с ролью `cashier`, его сессия.
- **Ожидается:** `session/end {session_id wrong}` → **422**; с верным → 200, `session null`; `current_session_id NULL`; событие `computer.session_ended` от этого пользователя.

#### GS-089 · P2 · `agent` · `TestResolve_DeviceTokenOnlyForStaff`
**Тест:** `ComputerSessionTest.php` — «staff get a device token for the PC, customers do not»
- **Дано:** владелец; второй пользователь с ролью `cashier` (в тесте назван «customer»); ПК №3 `PC-03`.
- **Действие:** `POST /api/club-computers/resolve {device_name PC-03}` токеном кассира, затем дважды токеном владельца; heartbeat агента с первым и вторым `X-Agent-Token`.
- **Ожидается:** кассиру — без `agent_token`; владельцу — `agent_token` вида `^zag_[A-Za-z0-9]{48}$`; в БД `agent_token_hash = sha256(второй)`, поле скрыто из `toArray()`; heartbeat с первым (перевыпущенным) → **401**; со вторым → 200, `computer_id`, `session null`, `agent_state idle`.

#### GS-090 · P2 · `agent` · `TestAgentToken_OwnPCAndActiveSubscription`
**Тест:** `ComputerSessionTest.php` — «a device token works only for its own PC and needs an active subscription»
- **Ожидается:** heartbeat без токена → **401** `code invalid_agent_token`; с токеном → 200 `computer_id`; подписка клуба `canceled` → **402**.

## Файл: GuestSessionTest.php

Подготовка: `guestCashier()` — пользователь с ролью `cashier`; `openShift()` — открытая смена с 0. Продажа гостевой сессии — `POST /club-computers/guest-session` (`club-computers.guest-session`), сервис `GuestSessions::sell()`.

#### GS-091 · P1 · `cash` · `TestGuestSession_TariffOnPCsPaidIntoShift`
**Тест:** `GuestSessionTest.php` — «a cashier starts a tariff on PCs for a guest without an account, paid into the open shift»
- **Дано:** `Carbon::setTestNow('2026-10-16 10:00:00 UTC')`; зона «Hall», ПК №1 и №2; тариф «Час» 60 мин, 700 ₸.
- **Действие:** `GET /club-map`; продажа `{computer_ids [1], tariff_id, cash}` без смены; открыть смену; `{computer_ids [1, 2], tariff, card}`; ещё раз `{[1], tariff, cash}`.
- **Ожидается:** карта: «Подключить тариф»; без смены → ошибка `payment_method`; со сменой → 200, `amount "1400.00"` (700 × 2 ПК); оба ПК: `session_guest true`, `current_session_user_id NULL`, `hasPaidSession()`, конец = 11:00; `cash_transactions`: одна `{income, 1400.00}`; повторная продажа на ПК1 продлевает до **12:00**.

#### GS-092 · P1 · `cash` · `TestGuestSession_MinutesByZoneAndRejections`
**Тест:** `GuestSessionTest.php` — «minutes are priced by the zone; busy PCs, bookings and zones without a price are refused»
- **Дано:** 10:00 UTC; смена; VIP 600 ₸/час: ПК1 свободен, ПК3 занят клиентом до 11:00, ПК4 забронирован 10:20–12:00; «Plain» без цены — ПК2.
- **Ожидается:** 30 мин на ПК1, `qr` → `amount "300.00"`; ПК2 → ошибка `minutes`; ПК3 → `computer_ids`; ПК4 (бронь начинается раньше окончания 30 мин) → `computer_ids`; `cash_transactions` = 1.

#### GS-093 · P2 · `agent` · `TestGuestSession_HeartbeatAndOperatorForbidden`
**Тест:** `GuestSessionTest.php` — «the agent sees the guest session in the heartbeat; a customer cannot buy on it; operators cannot sell it»
- **Дано:** смена; VIP 600 ₸/час; продана гостевая сессия 30 мин наличными.
- **Ожидается:** heartbeat → `session.guest true`, `session.user_id null`; heartbeat `{state idle}` гостевое время не стирает; оператор (`operator`) продаёт → **403**.

#### GS-094 · P1 · `cash` · `TestGuestSession_TariffZonesOnMapAndServer`
**Тест:** `GuestSessionTest.php` — «the start tariff dialog knows the zones of each tariff, so it shows the tariffs of the selected PCs»
- **Дано:** «Зальный» (700, зона Hall), «Везде» (500, без зон); единственный ПК — в VIP.
- **Ожидается:** карта: `data-tariff-zones="{hall}"><input type="radio" name="choice" value="tariff:{id}"` и `data-tariff-zones=""><input type="radio"`; продажа «Зальный» на ПК VIP → **422** `tariff_id`.

#### GS-095 · P3 · `club` · `TestClubMap_SessionUserAndPurchases`
**Тест:** `GuestSessionTest.php` — «the club map shows who sits at a PC and what they bought»
- **Дано:** «Арман» (+77010000001) на ПК1 (`session-1`), гостевая на ПК2; `session_purchases`: тариф «Ночной» 300 мин (100000 тиын), поминутка «Hall» 30 мин (30000 тиын).
- **Ожидается:** `data-session-user="Арман · +77010000001" data-session-tariff="Ночной, Поминутно: 30 мин."`; `data-session-user="Гость без аккаунта" data-session-tariff="Оплачено на кассе"`; `id="computer-info-session-user"`.

#### GS-096 · P3 · `club` · `TestClubMap_PCGlowClasses`
**Тест:** `GuestSessionTest.php` — «a PC on the club map glows by who uses it»
- **Ожидается:** классы `cm-computer`: ПК1 свободен → `online pc-free`; ПК2 сессия с аккаунтом → `online pc-account`; ПК3 гостевая → `online pc-guest`; ПК4 `staff_until` +1 ч (`pin`) → `online pc-staff`; ПК5 `last_seen_at` −1 ч → `offline`; текст «Гостевая сессия».

#### GS-097 · P3 · `club` · `TestClubMapState_GoesOfflineAfterHeartbeatTimeout`
**Тест:** `GuestSessionTest.php` — «the open club map learns that a PC went off»
- **Ожидается:** карта `online pc-free` и URL `club-map.state`; `GET /club-map/state` → `computers.{id}.classes = "online pc-free"`; через `HEARTBEAT_TIMEOUT_SECONDS + 5` = 95 с → `classes "offline"`, `connection "offline"`.

## Файл: GuestFeaturesTest.php

Подготовка: `guestSettings()` перезаписывает `settings` клуба `{guests: …}` и ставит `timezone = UTC`; `allGuestFeatures()` включает QR-вход, самопересадку, друзей и переводы; `guestApi()` — Bearer клиента; `guestComputer(n)` — ПК в зоне «Hall», онлайн (`last_seen_at = now`).

#### GS-098 · P3 · `agent` · `TestHeartbeat_GuestFeatures`
**Тест:** `GuestFeaturesTest.php` — «the agent heartbeat reports the guest features of the club»
- **Дано:** только `allow_adding_friends = true`.
- **Ожидается:** `club.guest_features = {qr_login false, self_transfer false, friends true, balance_transfer false, shop true}`; `club.idle_logout_minutes = 5` (умолчание).

#### GS-099 · P2 · `guest` · `TestFriends_AddAcceptRemoveMaskedPhones`
**Тест:** `GuestFeaturesTest.php` — «customers add friends by phone, the other one accepts, and phones stay masked»
- **Дано:** Арман +77011112233, Дана +77029998877.
- **Действие / Ожидается:** Арман `POST /api/guest/friends {phone "8 702 999 88 77"}` → **201**, `outgoing.0 = {name Дана, phone "+7 702 ••• •• 77"}`; повтор → ошибка `phone`; свой номер → `phone`; несуществующий +77000000000 → `phone`; Арман принимает свою заявку → ошибка `friendship`; Дана видит `incoming.0.name = Арман`; Дана принимает → `friends.0.name = Арман`; у Армана `friends.0.name = Дана`, `incoming` пуст, `features.friends = true`; удаление → `friends` пуст; `friendships` = 0; событие `friend.accepted`.

#### GS-100 · P3 · `guest` · `TestFriends_MutualRequestAccepts`
**Тест:** `GuestFeaturesTest.php` — «a request to someone who already asked you makes you friends»
- **Ожидается:** заявка Армана → 201; встречная заявка Даны → 201, `friends` = 1; `accepted_at` не NULL.

#### GS-101 · P2 · `guest` · `TestFriends_OtherClubNotFoundAndSwitchOff`
**Тест:** `GuestFeaturesTest.php` — «friends of another club are not found and the switch off closes adding friends»
- **Дано:** членство «чужого» в клубе удалено.
- **Ожидается:** при `allow_adding_friends = false` → **403**; при `true` → ошибка `phone`.

#### GS-102 · P1 · `billing` · `TestFriendTransfer_MainBalanceOnlyWithinBalance`
**Тест:** `GuestFeaturesTest.php` — «friends transfer money from the main balance only, within the balance»
- **Дано:** Арман 100.00 / 50.00; Дана 10.00; дружба принята; незавершённая заявка Армана к «незнакомцу».
- **Действие / Ожидается:** `100.01` → ошибка `amount`; `0` → `amount`; `-5` → `amount`; по незавершённой дружбе → `friendship`; от незнакомца по чужой дружбе → `friendship`; `"40,50"` (запятая) → 200, `balance "59.50"`; Арман **59.50 / 50.00**, Дана **50.50**; журнал `balance.transferred.details = {amount "40.50", to_user_id Дана, friendship_id}`; после выключения `allow_balance_transfer_to_friends` → **403**.

#### GS-103 · P1 · `billing` · `TestSeatTransfer_MovesRestOfSession`
**Тест:** `GuestFeaturesTest.php` — «a customer moves the rest of the session to a free PC where the tariff is valid»
- **Дано:** `setTestNow('2026-10-15 10:00:00 UTC')`; зоны Hall, VIP; ПК: №1 (from, сессия `sess-1` клиента до 11:30), №2 свободен, №3 занят другим до 11:00, №4 офлайн, №5 в VIP, №6 `is_reserved`, №7 бронь 11:00–12:00; тариф «Hall» 120 мин, 100 ₸ только для Hall; журнал `tariff.purchased {session_id sess-1}`.
- **Действие:** `GET /api/guest/seats?computer_id=1`; `POST /api/guest/seat-transfer` без `X-Agent-Token`; на №3; на №5; от чужого клиента; на №2.
- **Ожидается:** `current_computer_id = 1`, 2 зоны, первая «Hall»; `available`: №1 false, №2 **true**, №3, №4, №6, №7, №5 — false; №2 `{zone_id Hall, number 2, x 5, y 5}`; без токена ПК → **403**; №3 и №5 → ошибка `target_computer_id`; чужой клиент → `computer_id`; №2 → 200, `computer.id = 2`, `session.id = sess-1`; у №1 сессии нет; у №2 `sess-1`, тот же клиент, конец **11:30** (остаток 90 мин сохранён, денег не списывается); событие `session.transferred`.

#### GS-104 · P3 · `guest` · `TestSeatTransfer_DisabledByClub`
**Тест:** `GuestFeaturesTest.php` — «seat transfer is closed when the club has it switched off»
- **Ожидается:** `GET seats` и `POST seat-transfer` → **403**.

#### GS-105 · P2 · `auth` · `TestQRLogin_WebApprovalTokenOnce`
**Тест:** `GuestFeaturesTest.php` — «a PC shows a QR code, the customer confirms on the phone and the PC gets the token once»
- **Действие / Ожидается:** `POST /api/agent/qr-login` (токен ПК №7) → **201**, `url = route('pc-login.show', code)`, в БД `code_hash = sha256(code)`; `poll {id, device_name PC-07}` → `pending`; гость `GET pc-login.show` → redirect на вход; клиент «Арман» → 200 с именем; `POST pc-login.approve` → redirect `pc-login.done`; повторно → redirect на `pc-login.show`; poll → `approved`, `user.name Арман`, `access_token` работает для `GET /api/me` (`user_id`); следующий poll → `expired`; событие `auth.qr_login`.

#### GS-106 · P2 · `auth` · `TestQRLogin_ExpiryOwnPCMembersOnly`
**Тест:** `GuestFeaturesTest.php` — «QR codes expire, belong to their PC and are only for members of the club»
- **Ожидается:** poll чужим ПК → `expired`; подтверждение не-членом клуба → ошибка `code`; через 6 мин `GET pc-login.show` → «QR-код устарел».

#### GS-107 · P2 · `auth` · `TestQRLogin_MobileAppApprove`
**Тест:** `GuestFeaturesTest.php` — «the mobile app scans the PC QR code and confirms the sign-in with its token»
- **Ожидается:** `GET /api/guest/qr-login/{code}` без токена → **401**; с токеном → ровно `{computer: displayName, club_name}`; `POST .../approve` → 200 `computer`; повтор → **410** `code qr_expired`; poll → `approved`, `user.name Арман`.

#### GS-108 · P2 · `auth` · `TestQRLogin_MobileOtherClubOrExpired`
**Тест:** `GuestFeaturesTest.php` — «the mobile app does not confirm expired codes or codes of another club»
- **Дано:** клиент — также владелец клуба `qr-other-club` (активная подписка), токен приложения со способностью этого клуба.
- **Ожидается:** approve кода ПК клуба legacy → **403** `code other_club`, `approved_user_id NULL`; через 6 мин `GET` → **410** `qr_expired`.

#### GS-109 · P3 · `auth` · `TestQRLogin_DisabledByClub`
**Тест:** `GuestFeaturesTest.php` — «the QR code is not issued when the club has it switched off»
- **Ожидается:** `POST /api/agent/qr-login` → **403** `code qr_disabled`.

#### GS-110 · P1 · `guest` · `TestGuestHistory_AllKindsWithAmounts`
**Тест:** `GuestFeaturesTest.php` — «the agent shows the customer their own log: purchases, top-ups, transfers, seat changes and sign-ins»
- **Дано:** журнал Армана: пополнение основного 1000 (наличные), бонусного 200 (карта), `tariff.purchased {500.00: main 400.00, bonus 100.00}`, `session.transferred {PC-01 → PC-02}`; пополнение Даны 999; балансы Армана и Даны по 500; дружба; Дана переводит Арману 50.
- **Ожидается:** история Армана (`next_before null`), `[kind, title, amount, bonus_amount]`:
  1. `transfer_in`, «Перевод от друга Дана», `"50.00"`, null;
  2. `seat`, «Пересадка на ПК PC-02», null, null;
  3. `purchase`, «Покупка тарифа «Час»», `"-400.00"`, `"-100.00"`;
  4. `top_up`, «Пополнение на кассе», null, `"200.00"`;
  5. `top_up`, «Пополнение на кассе», `"1000.00"`, null.
  
  История Даны: 2 записи, `data.0 = {kind transfer_out, title "Перевод другу Арман", amount "-50.00"}`.

#### GS-111 · P3 · `guest` · `TestGuestHistory_Pagination`
**Тест:** `GuestFeaturesTest.php` — «the customer log comes in pages»
- **Дано:** 35 записей `auth.api_login` (PC-1…PC-35).
- **Ожидается:** первая страница 30 записей, `data.0.note = PC-35`; `?before={next_before}` → 5 записей, `next_before null`, `data.4.note = PC-1`.

## Файл: ComputerReservationTest.php

Подготовка: `reservationClub()` — часовой пояс клуба `UTC` (если не указан другой); `reservationComputer(n)` — ПК в зоне «Hall»; `reservationForm()` — `{club_computer_id, starts_at (локальное время клуба), duration_minutes (60), user_id = текущий сотрудник}`; `setBookingInterval(m)` — `settings.club.booking_interval`. Почти везде `Carbon::setTestNow('2026-10-15 06:00:00 UTC')`.

#### GS-112 · P3 · `club` · `TestMenu_LinksToBookings`
**Тест:** `ComputerReservationTest.php` — «the workspace menu links to the bookings page»
- **Ожидается:** `GET /dashboard` содержит ссылку `reservations.index` и «Брони».

#### GS-113 · P3 · `club` · `TestReservation_ClubTimezone`
**Тест:** `ComputerReservationTest.php` — «staff book a PC for a time in the club time zone and see it on that day»
- **Дано:** 06:00 UTC; пояс клуба `Asia/Almaty` (UTC+5); клиент «Арман» +77011112233.
- **Действие:** `POST /reservations {starts_at "2026-10-15T18:00", 120 мин, user_id Арман, note "ДР"}`.
- **Ожидается:** redirect `reservations.index?date=2026-10-15`; в БД `starts_at "2026-10-15 13:00:00"`, `ends_at "2026-10-15 15:00:00"` (UTC), `user_id`, `customer_name Арман`, `customer_phone +77011112233`, `created_by` = владелец; страница дня: «18:00 – 20:00», «Арман · +77011112233», «Ожидается»; 16.10 — «На этот день броней нет.»; журнал — «Бронирование ПК».

#### GS-114 · P3 · `club` · `TestReservation_IntervalBetweenBookings`
**Тест:** `ComputerReservationTest.php` — «bookings of a PC keep the club interval between them; other PCs are free»
- **Дано:** интервал 30 мин; ПК1, ПК2; бронь ПК1 12:00–13:00.
- **Ожидается:** ПК1 13:20 → ошибка `starts_at`; ПК1 10:40 (до 11:40 + 30 = 12:10) → `starts_at`; ПК1 13:30 → ок; ПК2 12:00 → ок; броней **3**.

#### GS-115 · P2 · `club` · `TestReservation_PastNoUserForeignPC`
**Тест:** `ComputerReservationTest.php` — «a booking cannot be in the past, without a club user or for a PC of another club»
- **Ожидается:** 05:00 (прошлое) → `starts_at`; `user_id null` → `user_id`; пользователь без членства → `user_id`; ПК другого клуба → `club_computer_id`; броней (без скоупа клуба) **0**.

#### GS-116 · P1 · `club` · `TestReservation_PaidSessionRunsIntoBooking`
**Тест:** `ComputerReservationTest.php` — «a PC with a paid session running into the booking cannot be booked»
- **Дано:** интервал 15; платная сессия до 08:00.
- **Ожидается:** бронь с 08:10 → `starts_at`; с 08:15 → ок.

#### GS-117 · P2 · `club` · `TestReservation_CustomerPickerClubOnly`
**Тест:** `ComputerReservationTest.php` — «the customer picker finds club users by name or phone, not users of other clubs»
- **Ожидается:** `GET /reservations/customers?q=Арман` → 1 запись `{name "Арман Сейтов", phone "+77011112233"}` («Арман Чужой» без членства не найден); `q=8 702 999` → «Дана»; `q=А` (1 символ) → 0.

#### GS-118 · P3 · `club` · `TestReservation_CancelOrArriveOnce`
**Тест:** `ComputerReservationTest.php` — «bookings can be cancelled or closed by the arrival once»
- **Ожидается:** брони 08:00 и 12:00; отмена первой ок; «пришёл» по отменённой → ошибка `reservation`; «пришёл» по второй ок; `state()` = `cancelled` и `arrived`.

#### GS-119 · P2 · `club` · `TestReservation_ForeignClubNotFound`
**Тест:** `ComputerReservationTest.php` — «bookings of another club are not found»
- **Ожидается:** `POST /reservations/{чужая}/cancel` → **404**; `cancelled_at` NULL.

#### GS-120 · P2 · `club` · `TestReservation_ClientsForbidden`
**Тест:** `ComputerReservationTest.php` — «customers cannot open the bookings page»
- **Ожидается:** роль `client` → `GET /reservations` **403**.

#### GS-121 · P1 · `billing` · `TestAgentPurchase_BlockedByOthersBooking`
**Тест:** `ComputerReservationTest.php` — «the agent does not sell time running into a booking of someone else, the booked customer may play»
- **Дано:** интервал 10; `booked` (+77012223344) и `stranger` по 1000.00; тариф «Час» 60 мин, 100 ₸; бронь 07:30–09:30 для `booked`.
- **Действие / Ожидается:** 06:00 `stranger` покупает (до 07:00 + 10 = 07:10 < 07:30) → 200 (основной 900.00); сессия сброшена; 06:25 `stranger` → **422** `computer_id` (07:25 + 10 мин заходит на бронь); `booked` → 200; у брони `arrived_at` проставлен (`ComputerReservations::markArrivedBy()`).

#### GS-122 · P3 · `club` · `TestClubMap_ReservedDuringBooking`
**Тест:** `ComputerReservationTest.php` — «the club map shows a PC as reserved during its booking»
- **Ожидается:** интервал 0; бронь 06:30; в 06:00 — `cm-computer offline`; в 06:45 — `cm-computer reserved`.

#### GS-123 · P3 · `club` · `TestClubMap_EveryBookedPC`
**Тест:** `ComputerReservationTest.php` — «the club map shows every booked PC, also after a PC without a booking»
- **Ожидается:** брони ПК1 и ПК2 на 06:15; по ПК1 «пришёл»; карта: `class="cm-computer offline" data-id="{1}"`, `class="cm-computer reserved" data-id="{2}"`.

#### GS-124 · P3 · `club` · `TestReservation_StoreManyAllOrNone`
**Тест:** `ComputerReservationTest.php` — «the club map books the selected PCs for a customer and a period, all or none»
- **Дано:** `setTestNow('2026-10-16 06:00 UTC')`, интервал 0; клиент «Арман».
- **Ожидается:** карта содержит `id="booking-modal"`; `POST /reservations/many {computer_ids [1, 2], user_id, starts_at "2026-10-16T12:00", 120}` → **201**, `message "Забронировано ПК: 2, для Арман на 16.10 12:00 – 14:00."`, броней 2; `[3, 1]` на 13:00 → **422** `starts_at`, у ПК3 брони нет; без `user_id` → **422** `user_id`.

## Файл: WarehouseTest.php

#### GS-125 · P3 · `stock` · `TestWarehouse_CreateCategoryAndProduct`
**Тест:** `WarehouseTest.php` — «category and product can be created in warehouse»
- **Действие:** `POST warehouse.categories.store {name Десерты}`; `POST warehouse.products.store {sku DST-001, name Чизкейк, product_category_id, price 1800, quantity 7}`.
- **Ожидается:** обе — redirect + flash; товар `sku DST-001`, `category "Десерты"`, `product_category_id`, `quantity 7`; `stock_movements`: `receipt`, `quantity 7`, `balance_after 7`.

#### GS-126 · P3 · `stock` · `TestWarehouse_PageWithSeeder`
**Тест:** `WarehouseTest.php` — «authenticated user can open warehouse page»
- **Дано:** `ProductSeeder` (10 товаров).
- **Ожидается:** `GET /warehouse`: «Склад», «Товаров на складе: 10», `images/product-placeholder.svg`, «Coca-Cola 0,5 л», «Кабель HDMI 2 м».

#### GS-127 · P3 · `stock` · `TestWarehouse_Pagination`
**Тест:** `WarehouseTest.php` — «warehouse stock table is paginated while the movement form lists every product»
- **Дано:** 60 товаров «Товар 01»…«Товар 60».
- **Ожидается:** «Товаров на складе: 60»; `stock` — 50 на странице, `total 60`; `products` — 60; `?page=2` — первый «Товар 51».

#### GS-128 · P1 · `stock` · `TestStock_Receipt`
**Тест:** `WarehouseTest.php` — «product can be received into stock»
- **Действие:** `POST /warehouse/stock-movements {product_id, receipt, 7, note Поставка}` (остаток 10).
- **Ожидается:** остаток **17**; движение `receipt`, 7, `balance_after 17`, `note Поставка`.

#### GS-129 · P1 · `stock` · `TestStock_WriteOff`
**Тест:** `WarehouseTest.php` — «product can be written off from stock»
- **Действие:** списание 4 из 10. **Ожидается:** остаток **6**; движение `write_off`, `balance_after 6`.

#### GS-130 · P1 · `stock` · `TestStock_WriteOffBeyondStockRejected`
**Тест:** `WarehouseTest.php` — «product cannot be written off beyond available stock»
- **Действие:** списание 4 при остатке 3. **Ожидается:** redirect `/warehouse`, ошибка `quantity`; остаток 3; движений 0.

#### GS-131 · P3 · `stock` · `TestProductsAPI_Paginated`
**Тест:** `WarehouseTest.php` — «mobile API returns paginated products with image and stock data»
- **Дано:** товар MOB-001 «Мобильный товар», 1500, 5 шт.
- **Ожидается:** `GET /api/products` → `data.0 = {sku MOB-001, name, price 1500 (число), currency KZT, quantity 5, in_stock true, image_url "{app.url}/images/product-placeholder.svg"}`, `meta.total 1`.

## Файл: AfterSessionActionTest.php

#### GS-132 · P3 · `agent` · `TestHeartbeat_AfterSessionAction`
**Тест:** `AfterSessionActionTest.php` — «the agent heartbeat reports what to do with the PC after a session ends»
- **Ожидается:** по умолчанию `club.after_session = {action none, delay_seconds 30}`; при `club.pc_action_after_session_end = restart`, `pc_restart_after_session_end = 1.5` (минуты) → `{action restart, delay_seconds 90}`.

## Файл: ClubZoneTest.php

#### GS-133 · P3 · `club` · `TestClubMap_ZoneLifecycleLayoutControl`
**Тест:** `ClubZoneTest.php` — «authenticated user can manage club map zones»
- **Дано:** владелец; зона `{name "Турнирный зал", description, capacity 12, icon desktop, theme blue, status available, is_featured true, sort_order 15}`.
- **Действие / Ожидается:**
  - `POST club-map.store` → redirect + flash; ПК в зоне 0; дважды `POST club-computers.store` → flash `Компьютер добавлен в зону.`, ПК **2**.
  - `GET /club-map`: `id="reserve-selected"`, «Турнирный зал», `class="cm-status available">Свободно`, `cm-zone-info`, «12 мест», `cm-computer offline`, `cm-computer-edit`, `computer-info-panel`, `computer-session-info`, `data-has-session="false"`.
  - `PUT` с `name "Главный зал", status busy` → имя/статус изменены, ПК 2.
  - `PATCH club-map.layout {240, 160, 480, 260}` → `{saved: true}`, координаты сохранены.
  - Соседняя зона (813, 507, 300×180); перенос первой на (820, 520, 300×180) → **409** `{"message": "Карточки залов не могут пересекаться."}`, `position_x` остаётся 240.
  - `POST club-map.align` → первая (240, 160), соседняя **(820, 500)** (выравнивание по сетке).
  - ПК1 `currentStatus() = offline`; `PATCH club-computers.layout {125, 75}` → сохранено; второй ПК в ту же ячейку → **409** «Эта ячейка уже занята другим компьютером.».
  - `POST api.club-computers.heartbeat` ПК1 → `{status: online}`.
  - `POST club-computers.control {[1,2], power_off}` → `{updated 2, status offline}`; оба `offline`.
  - `control {reserve}` → `{reserved true, updated 2}`, `is_reserved`, статус `reserved`; heartbeat → `{status reserved}`; повторно `reserve` → `{reserved false, updated 2}`, статус `online`.
  - `DELETE club-map.destroy` → зона удалена, соседняя осталась.

#### GS-134 · P3 · `club` · `TestComputerEdit_NumberZoneCapacityNameUnique`
**Тест:** `ClubZoneTest.php` — «computer details can be edited from the club map»
- **Дано:** «Исходная зона» с ПК №1 (`Agent PC 01`, 5,5); «Новая зона» capacity 1 с ПК №2 (5,5).
- **Ожидается:** номер 2 (занят) → ошибка `number`; номер 3 в полную зону → `club_zone_id`; после capacity 2 → flash `Данные компьютера обновлены.`, `number 3`, `display_name "Главный игровой ПК"`, `displayName()` = он же (и после смены `device_name` агентом), зона новая, позиция **(52, 5)** (следующая свободная ячейка); имя «Занятое имя» уже есть → страница с `id="computer-form-errors"`, «Это имя уже используется в клубе.», скрипт закрытия `zone-modal`.

#### GS-135 · P3 · `club` · `TestZone_Validation`
**Тест:** `ClubZoneTest.php` — «club zone input is validated»
- **Ожидается:** пустое имя, `icon script-tag`, `theme unknown`, `status unknown`, `sort_order -1` → ошибки `name, icon, theme, status, sort_order`; зон 0.

#### GS-136 · P3 · `club` · `TestZone_SaveKeepsEditMode`
**Тест:** `ClubZoneTest.php` — «saving a zone keeps the map editor enabled»
- **Ожидается:** при ошибке `name` с `edit_mode 1` и после успешного сохранения (`session edit_mode = true`) карта: `class="cm-page edit-mode"`, `id="editor-toggle" checked`.

#### GS-137 · P3 · `club` · `TestZone_CouchSofaShapeAndRotation`
**Тест:** `ClubZoneTest.php` — «couch zones render a sofa shape»
- **Ожидается:** `icon couch` → `class="cm-zone green available sofa"`, `cm-sofa-art`, `cm-sofa-seat`, без `<h3>`, статуса, `cm-zone-info`, `cm-capacity`; layout `{80×60, rotation 90}` → сохранено, класс `... sofa rotated-90`.

#### GS-138 · P3 · `club` · `TestZone_IconWithoutComputers`
**Тест:** `ClubZoneTest.php` — «non-sofa zones without computers still render their icon»
- **Ожидается:** `fas fa-gamepad`, `cm-zone-info`, `<h3>Игровая зона</h3>`; нет `cm-status available` и `cm-capacity`.

#### GS-139 · P3 · `club` · `TestComputerCreate_RespectsCapacity`
**Тест:** `ClubZoneTest.php` — «manual computer creation respects zone capacity»
- **Ожидается:** capacity 1: первый ПК — «Компьютер добавлен в зону.», второй — «Достигнута вместимость зоны.»; ПК 1.

#### GS-140 · P3 · `billing` · `TestZone_AttachTariffs`
**Тест:** `ClubZoneTest.php` — «tariffs can be attached to and changed on a zone»
- **Дано:** «Часовой» 60 мин 1000; «Дневной» 480 мин 5000.
- **Ожидается:** создание зоны с `tariff_ids [Часовой]` → привязан он; `PUT` с `[Дневной]` → только он; карта `data-tariff-ids="{Дневной}"`.

## Файл: ClubServiceTest.php

#### GS-141 · P1 · `cash` · `TestServices_SoldWithGoodsWithoutStock`
**Тест:** `ClubServiceTest.php` — «a manager keeps the services; a cashier sells them with goods in one receipt, without stock»
- **Дано:** владелец создаёт услуги «PS5 — 1 час» 1500 ₸ (ед. «час») и «Старое» 10 ₸, затем «Старое» `is_active 0`; кассир; смена 0; «Cola» 500 ₸, 10 шт.
- **Действие:** кассир `GET /cash-register`; `POST /cash-register/sales {[{service_id PS5, 2}, {product_id cola, 1}]}`; X-отчёт; продажа неактивной услуги; кассир `POST services.store`.
- **Ожидается:** касса: `data-key="s{ps}"` есть, `s{old}` нет; продажа **3500.00** (2×1500 + 500); в чеке «PS5 — 1 час», «Cola»; колы **9**; X-отчёт: «Продано товаров … PS5 — 1 час · 2 · —» (у услуги нет остатка); неактивная → ошибка `items.0.service_id`; кассир создаёт услугу → **403**.

#### GS-142 · P1 · `cash` · `TestRefund_SaleWithServiceReturnsGoodsOnly`
**Тест:** `ClubServiceTest.php` — «refunding a sale with a service returns the money and the goods only»
- **Дано:** «Распечатка» 50 ₸/стр.; «Cola» 500 ₸, 10 шт.; смена 1000; продажа {распечатка 10, кола 2} = 1500.
- **Ожидается:** возврат без ошибок; колы снова **10**; сумма возврата **1500.00**.

## Файл: ProductDeleteTest.php

#### GS-143 · P3 · `stock` · `TestProductDelete_SoftDeleteSkuFreed`
**Тест:** `ProductDeleteTest.php` — «the owner deletes a product: off the cash register and the warehouse, history kept, SKU free again»
- **Дано:** «Coca Cola», SKU `COLA-05`, 5 шт.; движение `receipt`.
- **Ожидается:** склад: «Удаление товара», ссылка удаления; `DELETE warehouse.products.destroy` → redirect + `Товар «Coca Cola» удалён.`; товар мягко удалён, SKU переименован в **`COLA-05~deleted-{id}`**; движения сохранены; журнал `product.deleted ⊇ {sku COLA-05}`; на кассе нет `data-name="Coca Cola"`; новый товар с `COLA-05` создаётся (1 активный).

#### GS-144 · P2 · `stock` · `TestProductDelete_ManagerNeedsRight`
**Тест:** `ProductDeleteTest.php` — «managers do not delete products unless the owner gives the right»
- **Ожидается:** менеджер не видит «Удаление товара»; `DELETE` → **403**; после `permissions {warehouse.delete: true}` → redirect, товар удалён.

## Файл: ProductApiImageTest.php

#### GS-145 · P3 · `stock` · `TestProductResource_ImageURL`
**Тест:** `ProductApiImageTest.php` — «the products API gives the PC agent the product photo as an absolute URL»
- **Ожидается:** файл есть → `{app.url}/storage/products/cola.jpg`; файла нет → `{app.url}/images/product-placeholder.svg`.

#### GS-146 · P3 · `cash` · `TestCashRegister_ProductPhoto`
**Тест:** `ProductApiImageTest.php` — «the cash register shows the product photo on the card and in the order»
- **Ожидается:** `<img src="{url}" alt="Cola" loading="lazy">` и `data-image="{url}"`.

## Файл: ClubComputerDeleteTest.php

#### GS-147 · P3 · `club` · `TestComputerDelete_RemovesBookings`
**Тест:** `ClubComputerDeleteTest.php` — «a manager deletes a PC from the club map, its bookings go with it»
- **Дано:** ПК №7 с бронью клиента (+1 ч … +2 ч).
- **Ожидается:** карта содержит `computer-delete-form`; `DELETE club-computers.destroy` → redirect + flash; ПК удалён, броней 0; журнал `computer.deleted.details.number = 7`.

#### GS-148 · P2 · `club` · `TestComputerDelete_RunningSessionAndCashierForbidden`
**Тест:** `ClubComputerDeleteTest.php` — «a PC with a running session is not deleted, and the front desk cannot delete PCs»
- **Ожидается:** кассир → **403**; при идущей сессии владелец → ошибка `computer`, ПК на месте.

## Файл: ClubComputerListTest.php

#### GS-149 · P2 · `club` · `TestComputerList_DetailsAndTenantIsolation`
**Тест:** `ClubComputerListTest.php` — «club PC list displays all computer details and hides other clubs»
- **Дано:** ПК №7 в VIP: `PC-07`, `smbios-07`, MAC `AA:BB:CC:DD:EE:FF`, IP `192.168.1.7` / `203.0.113.7`, `AMD Ryzen 7 5800X` 16 ядер, RAM 65536 МБ, `RTX 4070`, `Windows 11 Pro`, `idle`, агент `1.4.0`, сессия `session-07`, онлайн; ПК `SECRET-PC` другого клуба.
- **Ожидается:** `GET club-computers.index`: «Список ПК», все поля, «64,0 GB» (65536 МБ), «Online»; нет «SECRET-PC».

#### GS-150 · P2 · `club` · `TestComputerList_OperatorAndCashierAllowed`
**Тест:** `ClubComputerListTest.php` — «operators and cashiers can view the PC inventory»
- **Ожидается:** роли `operator` и `cashier` → 200.

## Файл: ComputerHeartbeatTest.php

#### GS-151 · P3 · `agent` · `TestHeartbeat_MarksOnlineReturnsInterval`
**Тест:** `ComputerHeartbeatTest.php` — «authenticated agent heartbeat marks computer online and returns polling interval»
- **Дано:** ПК `offline`; токен пользователя `wpf-agent` (без способностей).
- **Ожидается:** `POST api.club-computers.heartbeat` → `computer_id`, `status online`, `heartbeat_interval_seconds 30`; `currentStatus() = online`, `last_seen_at` заполнен.

#### GS-152 · P3 · `agent` · `TestComputerStatus_StaleHeartbeatOffline`
**Тест:** `ComputerHeartbeatTest.php` — «computers with stale heartbeats are reported offline»
- **Ожидается:** `status online`, `last_seen_at = now − (HEARTBEAT_TIMEOUT_SECONDS + 1)` = now − 91 с (`ClubComputer::HEARTBEAT_TIMEOUT_SECONDS = 90`) → `currentStatus() = offline`.

#### GS-153 · P2 · `agent` · `TestHeartbeat_RequiresAuth`
**Тест:** `ComputerHeartbeatTest.php` — «computer heartbeat requires authentication»
- **Ожидается:** без токена → **401**.

#### GS-154 · P3 · `agent` · `TestHeartbeat_NoWriteWhenUnchanged`
**Тест:** `ComputerHeartbeatTest.php` — «a heartbeat that changes nothing is not written while last_seen_at is fresh»
- **Дано:** время заморожено; ПК №3 с агент-токеном.
- **Ожидается (число `UPDATE club_computers` за запрос):** первый `{state idle}` → 1; через 5 с тот же → **0**, `last_seen_at` прежний; через 1 с `{state locked}` → 1, `agent_state locked`; через `SEEN_WRITE_SECONDS + 1` = 21 с (`ClubComputerSessions::SEEN_WRITE_SECONDS = 20`) тот же → 1, `last_seen_at = now`, `connectionStatus() = online`. (Требование нагрузки для 50 000 ПК.)

#### GS-155 · P2 · `agent` · `TestHeartbeat_CustomerBlock`
**Тест:** `ComputerHeartbeatTest.php` — «with the customer token the heartbeat brings their balance and what is new for them»
- **Дано:** клиент 1500 / 200; входящая заявка в друзья; чат: сообщение клиента и ответ сотрудника.
- **Ожидается:** с Bearer клиента → `customer = {user_id, balance 1500, bonus_balance 200, friend_requests 1, chat_last_staff_id = id ответа}` (балансы числами); без токена, с токеном другого клуба (`hb-other`) и с `not-a-token` → `customer = null`.

#### GS-156 · P3 · `agent` · `TestHeartbeat_AgentUpdateOffers`
**Тест:** `ComputerHeartbeatTest.php` — «the agent service of a free outdated PC is offered the update a few times»
- **Дано:** релиз агента `{latest_version 0.0.70, auto_update true, installer_url, installer_sha256 "ab"×32}`; ПК №5 `0.0.62`, `locked`.
- **Ожидается:** `{state locked}` (окно агента) → `agent_update null`; `{can_update true}` (служба) → `agent_update = {version 0.0.70, url ".../agent-v0.0.70/ZafiroAgentSetup-0.0.70.exe", sha256}`, `status` = текущий статус ПК; сразу повторно → null; далее каждые `OFFER_INTERVAL_MINUTES + 1` = 121 мин предлагается, всего `MAX_OFFERS` = 3 раза (`app/Services/AgentUpdate.php`), потом null; ПК `idle` (кто-то вошёл) → null; версия 0.0.70 → null; клуб `agent_auto_update false` → null, `true` → предложение; релиз без `auto_update` → null.

## Файл: ComputerMacIdentityTest.php

Константы: образ бездисковой загрузки `ZAFIRO-IMAGE`, UUID `4C4C4544-0042-3510-8051-B4C04F4A3132`, MAC `aa:bb:cc:00:00:01`, `AA-BB-CC-00-00-02`, `aabbcc000003`. `X-Agent-Device = MacAddress::deviceId(normalize(mac))`.

#### GS-157 · P3 · `agent` · `TestMacAddress_Normalize`
**Тест:** `ComputerMacIdentityTest.php` — «MAC addresses are normalized to AA:BB:CC:DD:EE:FF»
- **Ожидается:** `aa-bb-cc-dd-ee-0f` и `aabb.ccdd.ee0f` → `AA:BB:CC:DD:EE:0F`; `00:00:00:00:00:00`, `FF:FF:FF:FF:FF:FF`, `AA:BB:CC`, `null` → `null`.

#### GS-158 · P2 · `agent` · `TestEnroll_ThreePCsFromOneImage`
**Тест:** `ComputerMacIdentityTest.php` — «three PCs booted from one image get three cards and none of them loses its MAC or token»
- **Действие:** три `POST /api/agent/enroll {club_key, device_id UUID, device_name ZAFIRO-IMAGE, mac_address}`; после каждого — heartbeat всех уже зарегистрированных.
- **Ожидается:** каждый heartbeat → свой `computer_id`; 3 ПК, MAC `AA:BB:CC:00:00:01/02/03`, номера 1, 2, 3; повторный enroll ПК1 → тот же `computer_id`, ПК по-прежнему 3.

#### GS-159 · P2 · `agent` · `TestRegister_StaffThreePCsOneImage`
**Тест:** `ComputerMacIdentityTest.php` — «staff connecting three PCs of one image get three cards»
- **Ожидается:** для каждого MAC: `resolve {…, claim true}` → **404** `code computer_not_found`; `register` → **201** `computer_id`; `resolve` → 200 с этим id; 3 разных id, MAC нормализованы.

#### GS-160 · P2 · `agent` · `TestHeartbeat_NeverMovesMacBetweenCards`
**Тест:** `ComputerMacIdentityTest.php` — «a heartbeat never moves another MAC into a card»
- **Ожидается:** heartbeat токеном ПК1 с MAC `…:02` → 200, MAC ПК1 не меняется; токеном пустого ПК2 с MAC ПК1 → MAC ПК2 остаётся NULL; с новым `aa:bb:cc:00:00:09` → ПК2 получает `AA:BB:CC:00:00:09`.

#### GS-161 · P2 · `agent` · `TestAgentToken_DeviceMismatch`
**Тест:** `ComputerMacIdentityTest.php` — «a token copied with the image does not work on a PC with another MAC»
- **Ожидается:** токен ПК (MAC `…:01`) с `X-Agent-Device` от `…:02` → **401** `code device_mismatch`; со своим MAC → 200; со старым UUID → 200.

#### GS-162 · P2 · `agent` · `TestEnroll_TakesOverCardWithoutMac`
**Тест:** `ComputerMacIdentityTest.php` — «a card registered without a MAC is taken over by its PC instead of a new card»
- **Ожидается:** карточка №5 `device_id OLD-UUID`; enroll `{OLD-UUID, MAC …:01}` → её `computer_id`, MAC записан; enroll `{OLD-UUID, MAC …:02}` → **новая** карточка, у №5 MAC прежний.

#### GS-163 · P2 · `club` · `TestComputerEdit_MacUniqueStaffOnly`
**Тест:** `ComputerMacIdentityTest.php` — «only staff move a MAC to another card, and a MAC stays on one card»
- **Ожидается:** ПК2 с MAC ПК1 → ошибка `mac_address`; `not a mac` → `mac_address`; у ПК1 MAC очищен (`""`) → ок; затем ПК2 `aa-bb-cc-00-00-01` → ок; ПК1 MAC NULL, ПК2 `AA:BB:CC:00:00:01`.

## Файл: ComputerPowerTest.php

Подготовка: `powerPc(n, mac, online, agent = ComputerPower::AGENT_VERSION = "0.0.66")` — ПК в «Hall» с версией агента, MAC, `last_seen_at` now (онлайн) или −1 ч; `powerBeat()` — heartbeat с `{commands: true}`. Константы `app/Services/ComputerPower.php`: `COMMAND_TTL_SECONDS = 120`, `SERVICE_AGENT_VERSION = 0.0.72`, `MESSAGE_AGENT_VERSION = 0.0.73`, `MESSAGE_TTL_SECONDS = 300`; `ClubComputerSessions::DEFAULT_STAFF_MINUTES = 15`.

#### GS-164 · P3 · `agent` · `TestPower_RestartShutdownDeliveredOnce`
**Тест:** `ComputerPowerTest.php` — «restart and shut down reach the agent with its heartbeat once»
- **Ожидается:** `POST club-computers.control {restart}` → `status restarting`, `queued 1`, `message "Перезагрузка: ПК получит команду в течение минуты (1 ПК)."`; heartbeat `{state locked}` (без `commands`) → нет `power_command`; с `commands` → `restart`; повтор → `null`; `power_off` → heartbeat `{locked, commands}` → `shutdown`.

#### GS-165 · P3 · `agent` · `TestPower_CommandExpires`
**Тест:** `ComputerPowerTest.php` — «a command the PC did not take in time does not fire on the next boot»
- **Ожидается:** `power_off`, через 121 с heartbeat → `power_command null`, в БД `power_command NULL`.

#### GS-166 · P3 · `agent` · `TestPower_WakeOnLanViaRelays`
**Тест:** `ComputerPowerTest.php` — «a PC that is off is woken by the running PCs of the club»
- **Дано:** ПК1 офлайн с MAC `…:01`, ПК2 офлайн без MAC, ПК3 онлайн (ретранслятор).
- **Ожидается:** `power_on [1, 2]` → `status null`, `{woken 1, without_mac 1, relays 1}`, сообщение «Сигнал включения отправят другие ПК клуба (Wake-on-LAN): 1 ПК. Без MAC-адреса: 1.»; ПК1 остаётся `offline`; heartbeat ПК3 → `wake_macs ["AA:BB:CC:00:00:01"]`; после того как ПК1 онлайн → `[]`; после истечения 121 с → `[]`.

#### GS-167 · P3 · `agent` · `TestPower_WakeRefusedMessages`
**Тест:** `ComputerPowerTest.php` — «turning on is refused in words when nobody can send the signal»
- **Ожидается:** нет включённых ПК → «Включить некому: в клубе нет включённых ПК с агентом, который отправит сигнал Wake-on-LAN.»; ПК без MAC → «Не удалось включить: агент ещё не сообщил MAC-адрес этих ПК.»

#### GS-168 · P3 · `agent` · `TestPower_OldAgentsWarned`
**Тест:** `ComputerPowerTest.php` — «staff are told when the agents are too old to carry the commands out»
- **Дано:** ПК1 офлайн 0.0.59; ПК2 онлайн 0.0.59; ПК3 онлайн 0.0.66.
- **Ожидается:** `power_on [1]` → `{relays 1, old_relays 1}`; ПК3 уходит офлайн → «Включить некому: на включённых ПК клуба старый агент. Сигнал Wake-on-LAN отправляет агент версии 0.0.66 или новее — обновите агент хотя бы на одном ПК.»; `restart [2]` → «Агент на этих ПК устарел и не выполнит команду: обновите его до версии 0.0.66 или новее.»; `power_off [2, 3]` → «Выключение: ПК получит команду в течение минуты (1 ПК). На 1 ПК агент устарел (нужна версия 0.0.66): они команду не выполнят.»

#### GS-169 · P2 · `agent` · `TestPower_ServiceModeStaffVisit`
**Тест:** `ComputerPowerTest.php` — «the technical mode reaches the agent as Ctrl+Alt+P: a staff visit, and the service keeps the agent closed»
- **Дано:** сотрудник «Азат»; ПК1 агент 0.0.72; ПК2 агент 0.0.70.
- **Ожидается:** `control {service}` → `status null`, `queued 2`, `outdated 1`, сообщение «Техрежим: агент на ПК закроется в течение минуты, как по PIN-коду (1 ПК). На 1 ПК агент устарел (нужна версия 0.0.72): они команду не выполнят.»; ПК1 `staff_kind panel`, `staff_user_id` = Азат, `hasStaffVisit()`; heartbeat → `power_command service`, `staff_minutes 15`; повтор → `null`, без `staff_minutes`.

#### GS-170 · P1 · `billing` · `TestEndSessions_FromMapRightsRequired`
**Тест:** `ComputerPowerTest.php` — «staff end the sessions of the selected PCs from the map»
- **Дано:** ПК1 с гостевой сессией `guest-1`; ПК2 свободен.
- **Ожидается:** владелец `POST club-computers.end-sessions [1, 2]` → `ended 1`, сообщение «Сессия завершена на 1 ПК. Неиспользованное время вернулось клиенту.»; у ПК1 сессии нет; кассир без права → **403**.

#### GS-171 · P3 · `agent` · `TestStaffMessage_DeliveredOnce`
**Тест:** `ComputerPowerTest.php` — «a message from the map reaches the agent with its heartbeat once»
- **Ожидается:** `POST club-computers.notify {[1 (0.0.73), 2 (0.0.72)], "  Через 10 минут закрываемся  "}` → `sent 1`, сообщение «Уведомление появится на экране в течение минуты (1 ПК). На 1 ПК агент устарел (нужна версия 0.0.73): там уведомления не будет.»; heartbeat без `commands` → нет `staff_message`; с `commands` → «Через 10 минут закрываемся» (обрезаны пробелы); повтор → нет; событие `computer.messaged`.

#### GS-172 · P3 · `agent` · `TestStaffMessage_Expires`
**Тест:** `ComputerPowerTest.php` — «a message the PC did not take in time is not shown on the next boot»
- **Ожидается:** через 301 с — `staff_message` отсутствует и очищен в БД.

#### GS-173 · P3 · `agent` · `TestStaffMessage_Validation`
**Тест:** `ComputerPowerTest.php` — «a message needs text of at most 300 characters»
- **Ожидается:** пустое и 301 символ → ошибка `message`; `staff_message NULL`.

## Файл: ComputerAgentApiTest.php

#### GS-174 · P3 · `agent` · `TestResolve_ByStableIdAndCaseInsensitiveName`
**Тест:** `ComputerAgentApiTest.php` — «agent resolves a configured computer case-insensitively and binds its stable id»
- **Дано:** VIP, ПК №7 `PC-07`, `device_id stable-machine-id`.
- **Ожидается:** без токена → **401**; `{device_name different-name, device_id stable-machine-id}` → `{computer_id, number "7", name "PC-07", zone {id, name VIP}}`; `pc-07` → тот же; `unknown-pc` → **404** `code computer_not_found`.

#### GS-175 · P2 · `saas` · `TestResolve_RequiresActiveSubscription`
**Тест:** `ComputerAgentApiTest.php` — «computer resolution requires a token and an active club subscription»
- **Ожидается:** без токена **401**; подписка `trialing` с `trial_ends_at = now − 1 с` → **402**.

#### GS-176 · P3 · `agent` · `TestHeartbeat_EmptyCompatibleFullStoresHardware`
**Тест:** `ComputerAgentApiTest.php` — «empty heartbeat remains compatible and full heartbeat stores agent state»
- **Дано:** `setTestNow('2026-10-13 12:00:00 UTC')`; ПК №7 `PC-07`, зона «Основной зал».
- **Ожидается:** пустой heartbeat → `{computer_id, status online, recommended_interval_seconds 30, heartbeat_interval_seconds 30, number "7", zone.name}`; полный (`in_session`, `session-42`, `session_ends_at 13:30Z`, версия 1.4.0, MAC, IP, CPU 16 ядер, RAM 65536, GPU, ОС) → `hardware.*` в ответе; в БД `agent_state in_session`, `current_session_id session-42`, **`session_ends_at NULL`** (конец никогда не берётся от агента), прочие поля, `remote_ip` и `hardware_reported_at` заполнены, `last_seen_at = 12:00:00`; через 91 с `connectionStatus() = offline`; heartbeat `{idle}` — `session_ends_at` остаётся NULL.

#### GS-177 · P3 · `agent` · `TestHeartbeat_Validation`
**Тест:** `ComputerAgentApiTest.php` — «heartbeat rejects unsupported states and session ids outside a session»
- **Ожидается:** `state unknown` → **422** `state`; `{idle, session_id}` → **422** `session_id`; `{mac not-a-mac, local_ip invalid-ip}` → **422** `mac_address`, `local_ip`.

#### GS-178 · P2 · `agent` · `TestHeartbeat_ForeignClubForbidden`
**Тест:** `ComputerAgentApiTest.php` — «heartbeat forbids access to a computer in another club»
- **Ожидается:** `resolve` ПК чужого клуба → **404** `computer_not_found`; heartbeat чужого ПК → **403**.

#### GS-179 · P3 · `agent` · `TestRegister_CreatesOnceNoDuplicates`
**Тест:** `ComputerAgentApiTest.php` — «agent registration creates a computer and repeated requests do not create duplicates»
- **Ожидается:** `POST club-computers.register {PC-07, smbios-pc-07, aa:bb:cc:dd:ee:ff}` → **201** `{name PC-07, number "1", zone.name "Не назначена"}`, в БД MAC `AA:BB:CC:DD:EE:FF`; повтор с другим именем и `{device_name pc-07}` → тот же id; ПК 1, зона «Не назначена» одна.

#### GS-180 · P2 · `agent` · `TestRegister_RequiresAuthAndRights`
**Тест:** `ComputerAgentApiTest.php` — «registration requires auth and computer management rights»
- **Ожидается:** без токена **401**; кассир → **403** «Недостаточно прав для регистрации компьютера в клубе.»; ПК 0.

#### GS-181 · P2 · `agent` · `TestRegister_ZoneValidation`
**Тест:** `ComputerAgentApiTest.php` — «registration validates club zone and zone capacity»
- **Ожидается:** зона capacity 0 → **422** `zone_id`; зона другого клуба → **422** `zone_id`; ПК 0.

#### GS-182 · P1 · `saas` · `TestRegister_SubscriptionAndPlanLimit`
**Тест:** `ComputerAgentApiTest.php` — «registration respects inactive subscriptions and plan computer limits»
- **Ожидается:** истёкший trial → **402**; `active`, план `features.max_computers = 0` → **422** `device_name`; ПК 0.

#### GS-183 · P2 · `agent` · `TestRegister_SameDeviceIdInTwoClubs`
**Тест:** `ComputerAgentApiTest.php` — «the same stable device id can be registered independently in separate clubs»
- **Ожидается:** `shared-smbios-id` в клубе legacy и во втором клубе (активная подписка) → оба **201**, разные id; в `club_computers` 2 записи с этим `device_id`.

## Файл: AgentExitPinTest.php

#### GS-184 · P2 · `club` · `TestExitPin_HashedAndLogged`
**Тест:** `AgentExitPinTest.php` — «an owner sets the agent PIN; only its hash is stored and the change is logged»
- **Ожидается:** до — «PIN не задан: закрыть агент на ПК сейчас нельзя.»; `POST settings.agent-pin {pin 4821, pin_confirmation 4821}` → redirect; `agent_exit_pin_hash` ≠ «4821», `password_verify` истинно; одно событие `club.agent_pin_changed`; в журнале нет «4821»; поле скрыто из `toArray()`; страница «PIN задан {d.m.Y H:i}».

#### GS-185 · P2 · `club` · `TestExitPin_ValidationOwnerOnly`
**Тест:** `AgentExitPinTest.php` — «the PIN must be 4 to 8 digits and confirmed; managers cannot change it»
- **Ожидается:** `12`, несовпадение `1234/9999`, `abcd` → ошибка `pin`; менеджер → **403**; хэш NULL.

#### GS-186 · P2 · `agent` · `TestExitPin_AgentCheck`
**Тест:** `AgentExitPinTest.php` — «the agent checks the PIN with its device token»
- **Ожидается:** PIN не задан → **409** `pin_not_configured`; хэш bcrypt(4821): `0000` → **422** `{valid false, code invalid_pin}`; `4821` → 200 `valid true`; без токена ПК → **401**.

#### GS-187 · P2 · `agent` · `TestExitPin_RateLimitedPerPC`
**Тест:** `AgentExitPinTest.php` — «guessing the PIN is limited per PC»
- **Ожидается:** 5 неверных (1001…1005) → 422 каждый; 6-я попытка, даже верная → **429**.

## Файл: AgentRegisterTest.php

#### GS-188 · P2 · `auth` · `TestAgentRegister_NewCustomer`
**Тест:** `AgentRegisterTest.php` — «a customer registers on a club PC and is signed in as a customer of that club»
- **Ожидается:** `POST /api/agent/register {Арман, "8 701 222 33 44", secret-1, PC-01}` + `X-Agent-Token` → **201** `{club_id, user.name Арман}`; пользователь `+77012223344`, пароль bcrypt; членство роль `client` в этом клубе; событие `user.registered`; `access_token` работает для `/api/me`.

#### GS-189 · P2 · `auth` · `TestAgentRegister_Rejections`
**Тест:** `AgentRegisterTest.php` — «registration refuses a taken phone, a short password and requests without the PC token»
- **Ожидается:** уже клиент этого клуба → **422** «Вы уже клиент этого клуба. Войдите со своим паролем.»; пароль `123` → **422** `password`; без `X-Agent-Token` → **401**.

#### GS-190 · P1 · `auth` · `TestAgentRegister_JoinFromOtherClubSeparateBalance`
**Тест:** `AgentRegisterTest.php` — «a customer of another club joins this club with the password of the account»
- **Дано:** «Арман» +77012223344 / secret-1 — клиент клуба `join-other-club` с балансом 500.
- **Ожидается:** вход в клуб ПК → **403** `not_club_member`; регистрация с неверным паролем → **422** `password`, членств 1; с верным (`87012223344`, имя «Чужое имя») → **201**, `user.name` остаётся «Арман»; пользователь один; членства в обоих клубах; баланс в новом клубе **0.0**, в старом **500.0** (балансы по клубам раздельны); событие `user.joined_club` в новом клубе; способности токена `["club:{id}"]`.

## Файл: AgentSecurityTest.php

Подготовка: `securityPc(n)` — ПК `PC-{n}` в «Hall»; `securityMember(role, phone)` — пользователь с паролем `secret-pass` и ролью. В тесте «customer» — это пользователь с ролью `cashier` (не входит в `ClubComputerSessions::STAFF_ROLES = owner, admin, manager, operator`).

#### GS-191 · P2 · `auth` · `TestAgentLogin_RevokesPreviousPCToken`
**Тест:** `AgentSecurityTest.php` — «signing in on a PC again revokes that PC's previous token, phones keep theirs»
- **Ожидается:** два входа `POST /api/agent/user/login` с `device_name PC-07` → первый токен удалён, второй жив; два входа `POST /api/auth/login` с `ios-app` → оба токена живы.

#### GS-192 · P2 · `agent` · `TestHeartbeat_CustomerCannotRenameOrFakeHardware`
**Тест:** `AgentSecurityTest.php` — «a customer token reports the agent state but cannot rename the PC or fake its hardware»
- **Ожидается:** токен «клиента» `{locked, device_name HACKED, cpu_model Fake}` → 200; `agent_state locked`, `device_name PC-1`, `cpu_model NULL`; токен `admin` `{device_name PC-01, cpu_model Ryzen}` → сохраняется.

#### GS-193 · P1 · `billing` · `TestPurchase_DeviceTokenRequiredOnBoundPC`
**Тест:** `AgentSecurityTest.php` — «a PC with a device token sells time only to the agent on that PC»
- **Дано:** ПК1 с агент-токеном, ПК2 без; клиент основной 1000; тариф «Hour» 60 мин, 100 ₸.
- **Ожидается:** покупка на ПК1 без `X-Agent-Token` → **403** `wrong_computer`; с поддельным `zag_xxx…` → **403**; с верным → 200 `success true` (основной 900.00 — по коду); на ПК2 без токена → 200 (основной 800.00 — по коду).

#### GS-194 · P2 · `agent` · `TestExitPin_LoggedUnderSignedInUser`
**Тест:** `AgentSecurityTest.php` — «exits and wrong PINs are logged under the user signed in on the PC»
- **Ожидается:** PIN 7319 → 422; 4821 → 200; события `[agent.exit_pin_failed, agent.staff_exit]`, `user_id` = вошедший пользователь, `ip_address 127.0.0.1`; ни «4821», ни «7319» в журнале нет.

#### GS-195 · P2 · `agent` · `TestExitPin_RateLimitedPerClub`
**Тест:** `AgentSecurityTest.php` — «PIN guessing is limited per club, not only per PC»
- **Дано:** 21 ПК, по 5 неверных попыток с каждого.
- **Ожидается:** после **100** неудачных попыток по клубу следующая → **429**.

#### GS-196 · P2 · `agent` · `TestAgentToken_Rotate`
**Тест:** `AgentSecurityTest.php` — «the agent service swaps its device token for a new one and the old one stops working»
- **Ожидается:** `POST /api/agent/token/rotate` → новый `zag_[A-Za-z0-9]{48}` ≠ старого; старый в heartbeat → **401**; новый → 200; повторный rotate старым → **401**.

#### GS-197 · P2 · `agent` · `TestHeartbeat_UserTokenRejectedOnBoundPC`
**Тест:** `AgentSecurityTest.php` — «a PC with a device token does not accept heartbeats signed with a user token»
- **Ожидается:** heartbeat ПК с агент-токеном, подписанный пользовательским токеном → **403** `use_device_token`; `agent_state` и `current_session_id` NULL.

#### GS-198 · P2 · `agent` · `TestHeartbeat_ServerTime`
**Тест:** `AgentSecurityTest.php` — «the heartbeat reports the server clock so the agent does not trust the PC clock»
- **Дано:** `travelTo(сегодня 12:30:15)`.
- **Ожидается:** `server_time = now (UTC)` в формате `Y-m-d\TH:i:s.vP` (например `2026-10-30T12:30:15.000+00:00`).

#### GS-199 · P2 · `agent` · `TestExitPin_StaffVisitForClubMinutes`
**Тест:** `AgentSecurityTest.php` — «closing the agent with the PIN marks the PC as in staff hands for the club time»
- **Дано:** PIN 4821, `agent_staff_minutes = 25`.
- **Ожидается:** верный PIN → `staff_minutes 25`; `hasStaffVisit()`, `staff_kind pin`, `staff_user_id NULL`, `staff_until ≈ now + 25 мин`; heartbeat `shutting_down` метку сохраняет; `locked` — снимает.

#### GS-200 · P2 · `agent` · `TestResolve_StaffSigninLogged`
**Тест:** `AgentSecurityTest.php` — «a staff sign-in on a PC is logged and shown as staff work, a customer resolve is not»
- **Ожидается:** `resolve` токеном «клиента» (`cashier`) → метки нет; токеном `operator` → `staff_kind signin`, `staff_user_id` = оператор; событие `agent.staff_signin` от него.

#### GS-201 · P2 · `club` · `TestAgentStaffTime_OwnerOnlyRange`
**Тест:** `AgentSecurityTest.php` — «owners set the staff time on a PC, other roles cannot»
- **Ожидается:** `POST settings.agent-staff-time {0}` и `{241}` → ошибка `minutes`; `{40}` → `agent_staff_minutes 40`; менеджер → **403**.

#### GS-202 · P2 · `agent` · `TestResolve_ClaimIssuesNewTokenOnly`
**Тест:** `AgentSecurityTest.php` — «a staff member signing in on a connected PC gets no new device token»
- **Ожидается:** `resolve {claim false}` оператором → без `agent_token`, хэш прежний, события `agent.staff_signin` нет, старый токен работает; `claim true` и без флага → `agent_token` есть.

## Файл: DisklessAgentTest.php

`DISKLESS_UUID = 4C4C4544-0042-3510-8051-B4C04F4A3132`; ключ установки клуба — `clubs.installation_key` (64 символа).

#### GS-203 · P2 · `agent` · `TestEnroll_RegisteredPCByUUID`
**Тест:** `DisklessAgentTest.php` — «a registered PC gets its own token by the club key and its hardware UUID»
- **Ожидается:** ПК №7 с UUID и старым токеном; `POST /api/agent/enroll {club_key, device_id (lowercase UUID), PC-07}` → `{computer_id, number "7"}`, новый `zag_…` относится к этому ПК, старый недействителен; heartbeat с `X-Agent-Device = UUID` → 200.

#### GS-204 · P2 · `agent` · `TestEnroll_UnknownPCAddedOnce`
**Тест:** `DisklessAgentTest.php` — «an unknown PC with the club key is added to the club and gets a token»
- **Ожидается:** `OTHER-UUID` → новая карточка в зоне «Не назначена», токен её; повторный enroll — карточка одна.

#### GS-205 · P2 · `saas` · `TestEnroll_WrongKeyOrInactiveSubscription`
**Тест:** `DisklessAgentTest.php` — «a wrong key or an inactive subscription get no token»
- **Ожидается:** ключ `x`×64 → **401** `invalid_club_key`; без `device_id` → **422**; подписка `canceled` → **402**.

#### GS-206 · P2 · `agent` · `TestEnroll_PCOfOtherClubNewCard`
**Тест:** `DisklessAgentTest.php` — «a PC of another club joins this club as a new card and keeps the other card»
- **Ожидается:** UUID уже есть в клубе `other-club`; enroll в legacy → 200; карточек с UUID **2**, карточка другого клуба на месте.

#### GS-207 · P2 · `agent` · `TestAgentToken_CloneMismatch`
**Тест:** `DisklessAgentTest.php` — «a token copied with a boot image does not work on another PC but stays valid on its own»
- **Ожидается:** `X-Agent-Device CLONE-UUID` → **401** `device_mismatch`; свой UUID в нижнем регистре → 200; без заголовка → 200.

#### GS-208 · P2 · `agent` · `TestEnroll_RateLimitedPerPC`
**Тест:** `DisklessAgentTest.php` — «enrollment attempts are rate limited per PC»
- **Ожидается:** 5 раз `UNKNOWN` → 200; 6-й → **429**; `ANOTHER` → 200.

#### GS-209 · P2 · `agent` · `TestEnroll_SameUUIDOtherMacNewCard`
**Тест:** `DisklessAgentTest.php` — «a second PC with the same UUID but another MAC gets its own card and leaves the first one»
- **Ожидается:** UUID `03000200-0400-0500-0006-000700080009`, MAC `AA:BB:CC:00:11:22` у ПК1; enroll с MAC `…:99:99:99` → новая карточка с этим MAC; с `aa-bb-cc-00-11-22` → ПК1; MAC ПК1 прежний.

#### GS-210 · P3 · `agent` · `TestEnroll_PlaceholderMacUUID`
**Тест:** `DisklessAgentTest.php` — «a PC identified by its MAC (placeholder UUID) enrolls like any other»
- **Ожидается:** ПК №3 `device_id MAC-AABBCC001122`; enroll с тем же `device_id` и MAC → его `computer_id`.

## Файл: AgentUpdateNoticeTest.php

Подготовка: `releaseAgent(version, url)` — `config('club.agent')` с `latest_version`, `released_at 2026-10-15 09:00:00`, `download_url` (по умолчанию `https://zafiro.kz/downloads/ZafiroAgentSetup-1.2.0.exe`); уведомления — `GET notifications.index`.

#### GS-211 · P3 · `notify` · `TestAgentUpdateNotice_Owners`
**Тест:** `AgentUpdateNoticeTest.php` — «owners are told about a new agent version while some PCs run an older one»
- **Дано:** ПК: 1.1.9, 1.2.0, без версии; релиз 1.2.0.
- **Ожидается:** уведомление `id agent-update-1.2.0`, заголовок «Новая версия агента 1.2.0», текст содержит «1» и «2», `url` = список ПК, `unread true`.

#### GS-212 · P3 · `notify` · `TestAgentUpdateNotice_NoneWhenUpToDate`
**Тест:** `AgentUpdateNoticeTest.php` — «there is no notice when every PC is up to date or no release is configured»
- **Ожидается:** ПК 1.2.0 и 1.3.0 — нет уведомления (без релиза и с релизом 1.2.0); `latest_version = "latest"` + ПК 0.9.0 — нет уведомлений `agent-update*`.

#### GS-213 · P3 · `notify` · `TestAgentUpdateNotice_NumericCompare`
**Тест:** `AgentUpdateNoticeTest.php` — «versions are compared as numbers and unreadable ones count as old»
- **Ожидается:** ПК 1.10.0 при релизе 1.9.0 → нет; добавлен ПК `dev-build` → есть `agent-update-1.9.0`.

#### GS-214 · P2 · `notify` · `TestAgentUpdateNotice_NotForCashiers`
**Тест:** `AgentUpdateNoticeTest.php` — «cashiers do not get the agent notice»
- **Ожидается:** кассир не получает `agent-update-1.2.0`.

#### GS-215 · P3 · `club` · `TestComputerList_DisklessUpdateHint`
**Тест:** `AgentUpdateNoticeTest.php` — «the PC list explains the diskless update and marks old agents»
- **Ожидается:** «Бездисковая система — обновить один раз в образе:», ссылка на установщик, «устарела».

#### GS-216 · P2 · `club` · `TestComputerList_OnlyHttpsDownloadLink`
**Тест:** `AgentUpdateNoticeTest.php` — «only an https download link is shown»
- **Ожидается:** при `javascript:alert(1)` — заголовок «Новая версия агента 1.2.0» есть, ссылки и «Скачать установщик» нет.

#### GS-217 · P3 · `club` · `TestClubMap_AgentVersionOutdated`
**Тест:** `AgentUpdateNoticeTest.php` — «the PC panel on the club map shows the agent version and whether it is too old»
- **Ожидается:** `data-agent-version="1.1.9" data-agent-outdated="true"`, `data-agent-version="1.2.0" data-agent-outdated="false"`, `id="computer-info-agent-version"`.

## Файл: AgentLocalizationTest.php

#### GS-218 · P3 · `i18n` · `TestAgentTexts_AllTranslatedSamePlaceholders`
**Тест:** `AgentLocalizationTest.php` — «every agent text is in the catalog and translated to kazakh and english with the same placeholders»
- **Ожидается:** ключей из кода агента (`L.T("…")`, `data-i18n*`, кириллица в `main.js`) больше **150**; все есть в группе `agent` (ru); в kk и en нет пропусков, плейсхолдеры `:name` совпадают, переводы непустые. (В Go — проверка каталога i18n агента.)

#### GS-219 · P3 · `i18n` · `TestAgentTexts_ShippedJSONMatchesCatalog`
**Тест:** `AgentLocalizationTest.php` — «the texts shipped with the agent match the catalog (php artisan agent:translations)»
- **Ожидается:** `WindowsAgent/Frontend/i18n/{code}.json` = экспорт каталога для каждого языка.

#### GS-220 · P3 · `i18n` · `TestAgentTranslationsAPI_VersionBumps`
**Тест:** `AgentLocalizationTest.php` — «the agent gets its texts with the superadmin's edits and a version that changes with them»
- **Ожидается:** `GET /api/agent/translations/kk` → `locale kk`, `lines["Магазин"] = "Дүкен"`; после правки на «Клуб дүкені» → новое значение, `version` +1; `xx` → **404**; выключенный `en` → **404**.

#### GS-221 · P3 · `i18n` · `TestHeartbeat_ClubLocaleLanguagesVersion`
**Тест:** `AgentLocalizationTest.php` — «the heartbeat tells the agent the club language, the enabled languages and the translations version»
- **Ожидается:** `club.locale kk`, `club.translations_version` = версия каталога, языки содержат ru/kk/en, у kk имя «Қазақша».

#### GS-222 · P3 · `i18n` · `TestAgentMessages_LocaleHeaderFallback`
**Тест:** `AgentLocalizationTest.php` — «server messages to the agent follow the language picked on the PC, else the club language»
- **Ожидается:** язык клуба en; неверный PIN с `X-Agent-Locale kk` → «PIN-код қате.»; без заголовка → «Wrong PIN.»; `zz` → «Wrong PIN.»

## Файл: ClubInstallationKeyTest.php

#### GS-223 · P2 · `club` · `TestInstallationKey_PermanentEncryptedHashed`
**Тест:** `ClubInstallationKeyTest.php` — «each club receives a permanent installation key stored encrypted and hashed»
- **Ожидается:** ключ виден на `settings.club`, не виден на `settings.api`; длина **64**; `installation_key_hash = sha256(key)`, `installation_key_prefix` = первые 12 символов; попытка заменить ключ → `LogicException`; `POST /api/clubs/resolve {club_key}` → `{club_id, club_name}`; неверный → **401**.

#### GS-224 · P2 · `auth` · `TestInstallationKey_BindsLoginToClub`
**Тест:** `ClubInstallationKeyTest.php` — «installation key binds api login to its club for a multi-club user»
- **Ожидается:** пользователь в двух клубах; `POST /api/auth/login {phone, password, club_key}` → `{club_id, club_name "Installation Club"}`; с `club_key` и чужим `club_id` → **403**.

#### GS-225 · P2 · `club` · `TestInstallationKey_NoRotateRevokeRoutes`
**Тест:** `ClubInstallationKeyTest.php` — «installation key change and revoke routes do not exist»
- **Ожидается:** менеджер не видит ключ на `settings.api`; маршрутов `settings.api-key.rotate` и `settings.api-key.revoke` нет.

## Файл: ApiAuthTest.php

#### GS-226 · P2 · `auth` · `TestMobileLogin_MeLogout`
**Тест:** `ApiAuthTest.php` — «mobile users can login, inspect their profile, and revoke their token»
- **Дано:** «Мобильный пользователь» +79991234567 / `secret-password`, роль «Гость», скидка 5, баланс 1200 / 30.
- **Ожидается:** `POST /api/auth/login {phone, password, device_name ios-app}` → `{token_type Bearer, user.phone, user.role "Гость", user.balance 1200}`; `GET /api/auth/me` → `user.name`; `POST /api/auth/logout` → токен удалён из `personal_access_tokens`; повторный `/me` → **401**.

#### GS-227 · P2 · `auth` · `TestMobileLogin_InvalidCredentials`
**Тест:** `ApiAuthTest.php` — «mobile login rejects invalid credentials»
- **Ожидается:** неверный пароль → **401**.

#### GS-228 · P2 · `auth` · `TestMobileMe_RequiresToken`
**Тест:** `ApiAuthTest.php` — «mobile profile requires a bearer token»
- **Ожидается:** `GET /api/auth/me` без токена → **401**.

#### GS-229 · P1 · `auth` · `TestApiMe_TokenClubBalanceOnly`
**Тест:** `ApiAuthTest.php` — «api me returns only the authenticated users data for the token club»
- **Дано:** клуб A (валюта KZT): «Иван» +77001234567 баланс 150.5 / 20; клуб B (`api-me-other-club`): тот же пользователь `manager`, баланс 9999 / 888; токен со способностью `club:A`.
- **Ожидается:** без токена **401**; с токеном — ровно `{user_id, name "Иван", phone "+77001234567", club_id A, balance 150.5, bonus_balance 20.0, currency "KZT"}`, нет `token` и `password`; с заголовком `X-Club-Id: B` → **403**.

#### GS-230 · P1 · `saas` · `TestApiMe_InactiveSubscription402`
**Тест:** `ApiAuthTest.php` — «api me returns payment required when the token clubs subscription is inactive»
- **Ожидается:** trial истёк 1 с назад → `GET /api/me` **402**.

#### GS-231 · P1 · `auth` · `TestAgentLogin_ReturnsClubBalance`
**Тест:** `ApiAuthTest.php` — «agent user login returns access token and user balance for the selected club»
- **Дано:** клуб `agent-club` (KZT, подписка active); пользователь +79990000001, роль `cashier`, баланс 250.50 / 30.25.
- **Ожидается:** `POST /api/agent/user/login {phone, password, club_id}` → 200: `token_type Bearer`, `user = {id, name, phone, club_id, balance 250.5, bonus_balance 30.25, currency KZT, role cashier}`, есть `access_token`, `expires_at`.

#### GS-232 · P2 · `auth` · `TestAgentLogin_InvalidCredentials`
**Тест:** `ApiAuthTest.php` — «agent user login rejects invalid credentials»
- **Ожидается:** неверный пароль → **401**.

#### GS-233 · P2 · `auth` · `TestAgentLogin_RequiresMembership`
**Тест:** `ApiAuthTest.php` — «agent user login requires membership for the requested club»
- **Ожидается:** пользователь — член только другого клуба → **403**.

#### GS-234 · P1 · `saas` · `TestAgentLogin_InactiveSubscription`
**Тест:** `ApiAuthTest.php` — «agent user login requires active subscription for the selected club»
- **Ожидается:** trial истёк → **402**, `message "Подписка клуба не активна."`.

#### GS-235 · P1 · `auth` · `TestApiMe_BalanceFromToken`
**Тест:** `ApiAuthTest.php` — «get api me returns the authenticated users balance and profile from the bearer token»
- **Дано:** клуб `token-club` (KZT, active); +79990000005 `operator`, баланс 321.25 / 12.5.
- **Ожидается:** `GET /api/me` → `{user_id, name "Токен Пользователь", phone, club_id, balance 321.25, bonus_balance 12.5, currency KZT}`.

## Файл: PasswordRecoveryTest.php

Подготовка: `withGateway()` — токен Telegram Gateway `AAGexampleGatewayToken_1234567890` (зашифрован в `platform_settings`); `fakeGateway()` перехватывает `gatewayapi.telegram.org/*`; пользователь +77001112233 / `old-secret` с токеном `PC`.

#### GS-236 · P2 · `auth` · `TestPasswordRecovery_AgentFlowSignsOutEverywhere`
**Тест:** `PasswordRecoveryTest.php` — «in the agent a code goes to Telegram and sets a new password; the account is signed out everywhere»
- **Ожидается:** `POST api.auth.password.code {phone "8 700 111-22-33"}` → `code_minutes 5`; отправлен 1 код из 6 цифр запросом `/sendVerificationMessage` с `Authorization: Bearer <token>`, `phone_number +77001112233`, `ttl 300`; хранится только хэш; сброс с несовпадающим подтверждением → **422** `password`; верный → 200: новый пароль, токенов 0, веб-сессий 0, кодов 0, журнал платформы содержит `user.password_code_sent`, `user.password_recovered`; повтор кода → **422** `code`.

#### GS-237 · P2 · `auth` · `TestPasswordRecovery_WebFlow`
**Тест:** `PasswordRecoveryTest.php` — «on the site: phone, then code and new password, then sign-in»
- **Ожидается:** страница входа со ссылкой «забыли пароль»; `GET auth.password.reset` без шага 1 → redirect на forgot; `POST auth.password.send {700 111-22-33}` → redirect reset, страница с `+77001112233`; код `000000` → ошибка `code`; верный → redirect на вход; вход с новым паролем успешен.

#### GS-238 · P2 · `auth` · `TestPasswordRecovery_UnknownNumberSameAnswer`
**Тест:** `PasswordRecoveryTest.php` — «an unknown number gets the same answer and costs nothing»
- **Ожидается:** неизвестный номер → 200; кодов не отправлено, записей нет.

#### GS-239 · P2 · `auth` · `TestPasswordRecovery_Limits`
**Тест:** `PasswordRecoveryTest.php` — «codes are limited: one a minute, five an hour, five tries each, five minutes long»
- **Ожидается:** второй запрос сразу → **422** `phone`; 5 неверных попыток «сжигают» код — верный после них → **422** `code`; через 61 с новый код; через 6 мин он истёк → 422; ещё 3 кода с интервалом 61 с — ок; 6-й в течение часа → **422** `phone`; отправлено ровно **5** кодов; пароль прежний.

#### GS-240 · P2 · `auth` · `TestPasswordRecovery_NoGatewayOrRejectedOrBlocked`
**Тест:** `PasswordRecoveryTest.php` — «without Gateway, or when Telegram does not take the number, the customer is told; blocked accounts get nothing»
- **Ожидается:** без Gateway → **422** `phone`, страница «Восстановление пароля через Telegram пока не подключено. Обратитесь к администратору клуба.»; Gateway отвечает `PHONE_NUMBER_INVALID` → **422** `phone`, кодов нет; заблокированный (`blocked_at`) +77005554433 → 200, отправок 0.

#### GS-241 · P2 · `saas` · `TestSuperadmin_TelegramGatewayToken`
**Тест:** `PasswordRecoveryTest.php` — «the superadmin connects Gateway: the token is checked and stored encrypted»
- **Ожидается:** ответ `ACCESS_TOKEN_INVALID` → ошибка `token`, не настроен; ответ `REQUEST_ID_INVALID` (токен валиден) → сохранён, «Telegram Gateway подключён», в БД нет открытого токена, `token()` расшифровывает; `DELETE` → не настроен.

#### GS-242 · P3 · `guest` · `TestGuestTelegram_NotAvailable`
**Тест:** `PasswordRecoveryTest.php` — «old agents are told Telegram in the profile is not available»
- **Ожидается:** `GET api.guest.telegram` → `{available false, linked false}`.

## Файл: RateLimitTest.php

#### GS-243 · P2 · `auth` · `TestRateLimit_QRPollingSeparateFromLogin`
**Тест:** `RateLimitTest.php` — «the QR code polling of a PC does not use up the sign-in limit of the club IP»
- **Ожидается:** 25 poll подряд → 200; сразу после вход с неверным паролем → **401** (не 429); `clubs.resolve` с неверным ключом → **401**.

#### GS-244 · P2 · `auth` · `TestRateLimit_LoginPerPhone`
**Тест:** `RateLimitTest.php` — «sign-in is limited per phone, so one customer cannot block the other PCs of the club»
- **Ожидается:** 5 неверных входов +77011112233 → 401; 6-й → **429**; другой телефон → **401**.

#### GS-245 · P2 · `auth` · `TestRateLimit_FriendsPollingDoesNotBlockPurchase`
**Тест:** `RateLimitTest.php` — «a signed-in customer polling friends can still buy a tariff»
- **Ожидается:** 15 × `GET friends` → 200; покупка тарифа (ПК 999) → статус ≠ 429.

## Файл: SaasTenancyTest.php

#### GS-246 · P1 · `saas` · `TestRegister_OwnerAndBalanceOutsideUsers`
**Тест:** `SaasTenancyTest.php` — «registration creates a club owner and keeps balances outside users»
- **Ожидается:** регистрация «Новый клуб» с `7001234567` → redirect `/dashboard`; членство роль `owner`, баланс **0.00**; в `users` нет колонок `balance`, `bonus_balance`, `role`.

#### GS-247 · P2 · `club` · `TestTenancy_DataAndMembershipIsolated`
**Тест:** `SaasTenancyTest.php` — «tenant data and membership access are isolated by club»
- **Ожидается:** одинаковый SKU `SHARED-001` в клубах A и B; пользователь B видит «Товар клуба B», не видит «A»; пользователь A с `current_club_id = B` → **403**; в контексте B товаров 1.

#### GS-248 · P2 · `auth` · `TestTenancy_TokenCannotSwitchClubByHeader`
**Тест:** `SaasTenancyTest.php` — «club-bound api tokens cannot switch clubs with a header»
- **Ожидается:** токен `club:B` + `X-Club-Id: A` → `GET /api/products` **403**.

## Файл: PermissionsTest.php

#### GS-249 · P2 · `club` · `TestPermissions_RoleDefaults`
**Тест:** `PermissionsTest.php` — «by default every role keeps the rights it had»
- **Ожидается:** owner `tariffs.delete` ✓; admin `workers.update` ✓, `tariffs.delete` ✗; manager `workers.view` ✗, `warehouse.create` ✓, `chat.create` ✗; cashier `cash.create` ✓, `cash.delete` ✗, `customers.update` ✓; operator `cash.view` ✗, `chat.create` ✓.

#### GS-250 · P2 · `club` · `TestPermissions_EditCashierRights`
**Тест:** `PermissionsTest.php` — «pick an employee, then tick rights: a cashier may create products but not see the cash register»
- **Ожидается:** список с «Айгерим» и ссылкой; форма «Права: Айгерим», столбцы «Просмотр, Создание, Изменение, Удаление», есть `rights[warehouse][create]`, нет `rights[stock][delete]`; `PUT` с `warehouse.view/create = 1`, `cash.view = 0` → flash «Права сохранены.»; `permissions = {cash.view: false, warehouse.view: true, warehouse.create: true}` (хранятся только отличия от роли); журнал `permissions.updated.details = {"Касса: Просмотр": "нет", "Склад: каталог: Просмотр": "да", "Склад: каталог: Создание": "да"}`; кассир: склад 200, касса **403**, создание категории ок, движение склада **403**; сброс → `permissions NULL`.

#### GS-251 · P2 · `club` · `TestPermissions_AdminEditsCashierOnly`
**Тест:** `PermissionsTest.php` — «an admin sets the rights of a cashier, not their own and not of other admins»
- **Ожидается:** админ видит ссылку на кассира, не на другого админа; свои права → **403**; `PUT` другому админу → **403**.

#### GS-252 · P2 · `club` · `TestPermissions_ManagerForbidden`
**Тест:** `PermissionsTest.php` — «managers and the front desk do not open the rights page»
- **Ожидается:** менеджер → **403**.

#### GS-253 · P2 · `club` · `TestPermissions_HiddenWithoutRight`
**Тест:** `PermissionsTest.php` — «without a right the page and its buttons are not there»
- **Ожидается:** кассир с `customers.create = false`, `reservations.view = false`: нет `open-user-dialog`; `POST users.store` → **403**; брони → **403**; на карте нет `<span>Брони</span>`.

#### GS-254 · P2 · `club` · `TestPermissions_EveryGuardedRouteHasRight`
**Тест:** `PermissionsTest.php` — «every route of the guarded groups has a right, and every right exists»
- **Ожидается:** у каждого маршрута с middleware `club-area` есть право (`RequireClubArea::rightFor`), каждое право из `RequireClubArea::ROUTES` существует в `ClubPermissions`. (В Go — тест полноты таблицы «маршрут → право».)

## Файл: UsersTest.php

#### GS-255 · P1 · `club` · `TestUsersList_CustomersWithBalances`
**Тест:** `UsersTest.php` — «the user list shows the club customers with their balances, not the staff»
- **Ожидается:** «Иван Петров», «+79991234567», «12.50%», «1 250.50», «75.00».

#### GS-256 · P2 · `auth` · `TestUsersList_GuestRedirect`
**Тест:** `UsersTest.php` — «guest users are redirected away from the user list»
- **Ожидается:** redirect на `auth.show-login`.

#### GS-257 · P2 · `club` · `TestCashier_CashYesSettingsNo`
**Тест:** `UsersTest.php` — «cashiers can open the cash register but cannot change club settings»
- **Ожидается:** касса 200; `settings.club` **403**.

#### GS-258 · P1 · `club` · `TestUsersCreate_CustomerWithBalances`
**Тест:** `UsersTest.php` — «the user list creates customers only»
- **Ожидается:** страница: «Добавить клиента», `user-create-dialog`, `>+7</span>`, нет `<option value="cashier"`; `POST users.store {Айжан Садыкова, 7001234567, email, guest-secret ×2, discount 10.5, balance 2500, bonus_balance 150}` → flash «Пользователь добавлен.»; телефон `+77001234567`, роль `client`, скидка **10.50**, балансы **2500.00 / 150.00**, пароль хэширован; событие `user.created` от админа. (Начальный баланс без кассы — см. открытые вопросы.)

#### GS-259 · P3 · `club` · `TestUsersCreate_Validation`
**Тест:** `UsersTest.php` — «user creation validates unique phone and password confirmation»
- **Ожидается:** занятый `7001234567` и несовпадающее подтверждение → ошибки `phone`, `password`.

#### GS-260 · P2 · `auth` · `TestUsersCreate_GuestRedirect`
**Тест:** `UsersTest.php` — «guest users cannot create accounts through the admin form»
- **Ожидается:** redirect на вход.

#### GS-261 · P3 · `auth` · `TestLogout_NonStaff`
**Тест:** `UsersTest.php` — «non-staff members can still log out»
- **Ожидается:** роль «Пользователь» → `POST auth.logout` redirect `/`.

#### GS-262 · P1 · `club` · `TestUsersCreate_FrontDeskNoDiscountNoBalance/cashier`
**Тест:** `UsersTest.php` — «cashiers and operators register customers without a discount or balance» (набор `cashier`)
- **Ожидается:** «Добавить клиента», нет `new-user-discount` и `new-user-balance`; с `balance 5000` → **403**, пользователь не создан; без баланса → redirect, роль `client`, скидка **0.00**, баланс **0.00**.

#### GS-263 · P1 · `club` · `TestUsersCreate_FrontDeskNoDiscountNoBalance/operator`
**Тест:** то же (набор `operator`). **Ожидается:** как для кассира.

#### GS-264 · P2 · `club` · `TestFrontDesk_LandingAndHiddenMenu/cashier`
**Тест:** `UsersTest.php` — «cashiers and operators do not see the admin menu and land on their own page» (набор `['cashier', 'cash-register.index']`)
- **Ожидается:** `/dashboard` → redirect на кассу; на ней нет ссылок `settings`, `tariffs.index`, `news.index`, `billing.show`, `activity-logs.index`, `dashboard`; есть `chat.index`; `billing.show` → **403**.

#### GS-265 · P2 · `club` · `TestFrontDesk_LandingAndHiddenMenu/operator`
**Тест:** то же (набор `['operator', 'club-computers.index']`). **Ожидается:** redirect на список ПК; остальное как выше.

#### GS-266 · P2 · `club` · `TestFrontDesk_MapWithoutEditorStockReadOnly/cashier`
**Тест:** `UsersTest.php` — «cashiers and operators open the club map without the editor, the PC list and the stock» (набор `cashier`)
- **Ожидается:** карта с `class="cm-editor-actions"  hidden`; список ПК 200; `stock.index` — «Coca Cola», «7»; ссылка на `stock.index`; `warehouse` **403**; `club-map.store` **403**.

#### GS-267 · P2 · `club` · `TestFrontDesk_MapWithoutEditorStockReadOnly/operator`
**Тест:** то же (набор `operator`). **Ожидается:** как выше.

#### GS-268 · P1 · `stock` · `TestFrontDesk_ReceiveGoodsNoWriteOff/cashier`
**Тест:** `UsersTest.php` — «cashiers and operators receive goods into stock; write-offs stay with managers» (набор `cashier`)
- **Дано:** «Coca Cola» 3 шт.
- **Ожидается:** «Оприходовать»; `stock.receive {quantity 0}` → ошибка; `{12, Поставка}` → остаток **15**, движение `receipt` от сотрудника; списание → **403**.

#### GS-269 · P1 · `stock` · `TestFrontDesk_ReceiveGoodsNoWriteOff/operator`
**Тест:** то же (набор `operator`). **Ожидается:** как выше, остаток **15**.

## Файл: WorkersTest.php

#### GS-270 · P2 · `club` · `TestWorkers_AddByPhoneChangeRoleRemove`
**Тест:** `WorkersTest.php` — «the owner adds a registered user as a worker by phone, changes the role and removes them»
- **Ожидается:** «Работники»; «Дана» (клиент) по `8 702 999 88 77` → `cashier`; «незнакомец» без членства +77011112233 → `operator` (членство создаётся); неизвестный номер и повтор Даны → ошибка `phone`; смена роли на `manager`; удаление → роль снова `client`, Дана в списке клиентов.

#### GS-271 · P2 · `club` · `TestWorkers_AdminLimits`
**Тест:** `WorkersTest.php` — «an admin cannot touch administrators, the owner or their own role»
- **Ожидается:** админ меняет другого админа → **403**; себя → **403**; удаление владельца → **404**; добавление с ролью `admin` → ошибка `role`; менеджер → список работников **403**.

#### GS-272 · P2 · `auth` · `TestWebLogin_CustomersOnlyForPCConfirm`
**Тест:** `WorkersTest.php` — «customers cannot open the club panel; they sign in on the site only to confirm a PC sign-in»
- **Ожидается:** клиент входит на сайт → ошибка `phone`, гость; с `url.intended = /pc-login/{a×40}` → redirect туда, аутентифицирован; `dashboard` и `billing` → **403**.

#### GS-273 · P2 · `auth` · `TestWebLogin_MaxCashiersOnShift`
**Тест:** `WorkersTest.php` — «only as many cashiers as the club allows can be signed in at once»
- **Дано:** `club.max_cashiers_on_shift = 1`; кассир 1 с живой сессией.
- **Ожидается:** вход кассира 2 → ошибка `phone`; после устаревания сессии 1 (−3 ч) → redirect `/dashboard`.

## Файл: StaffApiTest.php

Подготовка: `staffApiMember(role)` — токен «Zafiro Staff» со способностью клуба; `staffApiComputer(n)` — ПК в «Hall» (600 ₸/час), онлайн.

#### GS-274 · P2 · `auth` · `TestStaffAPI_MeRolesPermissions`
**Тест:** `StaffApiTest.php` — «only staff use the staff API; me tells the role, permissions and counters»
- **Ожидается:** клиент → `api.staff.me` и `computers.index` **403**; без токена **401**; кассир → `{role cashier, user.name Кассир, club.currency KZT, permissions {cash ✓, guest_session ✓, end_session ✗, control ✓, chat_reply ✓, stock_receive ✓, stock_manage ✗, reports ✗}, counters.orders_new 0, shift null}`; оператор → `cash ✗, end_session ✓`; оператор `cash.show` **403**; кассир `reports.today` **403**; кассир `stock.movements.store` **403**.

#### GS-275 · P3 · `auth` · `TestStaffAPI_LoginRolesPerClub`
**Тест:** `StaffApiTest.php` — «the sign-in tells the role in each club, so the app keeps the staff ones»
- **Ожидается:** `api.auth.login` → `user.role cashier`, `clubs.0.role cashier`.

#### GS-276 · P1 · `agent` · `TestStaffAPI_MapControlEndSession`
**Тест:** `StaffApiTest.php` — «the club map shows PCs with sessions, staff visits and tariffs; control and end session use the panel actions»
- **Ожидается:** `computers.0.session.user.name Ерлан`, `connection online`, `agent_version 0.0.61`; ПК2 `session null`, `connection offline` (10 мин без связи), `agent_version null`; `tariffs.0 = {id, price "800.00"}`, `guest_mode true`; `control reserve` → `reserved true`; кассир `end-session` → **403**; оператор → 200, сессии нет.

#### GS-277 · P1 · `cash` · `TestStaffAPI_CashierFullShift`
**Тест:** `StaffApiTest.php` — «a cashier works the whole shift: open, top-up with a bonus, sale, cash out, close with a Z-report»
- **Дано:** бонус за пополнение `{1000 ₸ → 10%}`; клиент «Гость»; «Cola» 350 ₸, 10 шт.
- **Действие / Ожидается:**
  1. `GET cash.show` → `shift null`, `transactions []`; пополнение без смены → **422**.
  2. `POST cash.shifts.open {5000}` → **201**.
  3. Пополнение 2000 картой → `{bonus "200.00", balance "2000.00", bonus_balance "200.00"}`.
  4. Продажа колы ×2 → **201** `{amount "700.00", items.0.total "700.00"}`.
  5. Расход 100 «Вода» → **201**; расход 99999 → **422** (в кассе меньше).
  6. `me` → `shift.balance "7600.00"`; `cash.show` → `totals {income "2700.00", expense "100.00", balance "7600.00"}`, `report.items.0.name Cola`, 3 транзакции.
  7. `POST cash.shifts.close` → `shift.closing_balance "7600.00"`, `report.staff.0.name` = кассир; `cash.show` → `shift null`.

#### GS-278 · P1 · `billing` · `TestStaffAPI_CustomersSearch`
**Тест:** `StaffApiTest.php` — «customers are searched by name or phone, with balances and the discount in force»
- **Ожидается:** `search=Ерлан` → одна запись ровно `{user_id, name "Ерлан Сейтов", phone "+77011112233", balance "500.00", bonus_balance "50.00", discount "5.00", discount_cumulative false, total_spent "0.00"}`; `8 702 999` → Айгерим; без поиска → 2 записи (сотрудники не клиенты).

#### GS-279 · P1 · `stock` · `TestStaffAPI_GoodsReceiveWriteOff`
**Тест:** `StaffApiTest.php` — «goods: everyone receives, managers also write off»
- **Ожидается:** `products?search=col` → `quantity 3`, `price "350.00"`; оператор приход 5 → `quantity 8`; оператор списание → **403**; менеджер списание 2 → `quantity 6`; списание 99 → **422**.

#### GS-280 · P1 · `stock` · `TestStaffAPI_OrdersDoneCancelOnce`
**Тест:** `StaffApiTest.php` — «orders from PCs are listed and handed over or cancelled once»
- **Дано:** клиент «Арман» баланс 0; заказы: «PC 5» 700.00 (Cola ×2, товар 999 не существует), «PC 6» 100.00.
- **Ожидается:** `orders.index` → `new_count 2`, `data.1.items.0.name Cola`, `data.1.customer.name Арман`; `done` первого → `{status done, handled_by "Оператор"}`; повтор → **422** `order`; `cancel` второго → `cancelled`, баланс **100.00**; `status=all` → 2 записи, `new_count 0`.

#### GS-281 · P2 · `notify` · `TestStaffAPI_ChatNotificationsDayReport`
**Тест:** `StaffApiTest.php` — «chat: everyone reads, cashiers and operators answer; notifications and the day report»
- **Ожидается:** менеджер видит беседу `{customer.name Гость, unread 1, last.body Помогите}`; менеджер отвечает → **403**; оператор `chat.poll` → `messages.0.body`, `unread 0`; ответ → **201** `message.from_staff true`; уведомления `{unread_count, items}`; `notifications.seen` → `unread_count 0`; `reports.today` (менеджер) → `week` 7 дней, `today.income "0.00"`, `computers {total 1, online 1}`, `low_stock.0.name Чипсы` (2 шт.), `week.6.date` = сегодня в поясе клуба.

## Файл: IntegrationApiAuthTest.php

Подготовка: `integrationClub(slug)` — клуб с активной подпиской (план `trial`) и владельцем; `issueApiKey(club, user, scopes, data)` — ключ вида `zfk_<8 символов префикса>_<40 символов секрета>` (`ApiKeyManager::create()`). Тестовые маршруты `/api/v1/test/*` (`write` требует `products:read,products:write`; `undeclared` без области; `charge` — `api.idempotent:required`, счётчик в кэше; `echo` — размер тела).

#### GS-282 · P2 · `integration` · `TestV1Auth_ValidKeyRateHeaders`
**Тест:** `IntegrationApiAuthTest.php` — «a valid key reaches its club and gets rate limit headers»
- **Ожидается:** `GET /api/v1/me` c `Authorization: Bearer <key>` → `data.club.id`, `data.key.scopes ["products:read"]`, заголовок `X-RateLimit-Limit`; с `X-Api-Key: <key>` → 200.

#### GS-283 · P2 · `integration` · `TestV1Auth_InvalidKey/no_header`
**Тест:** `IntegrationApiAuthTest.php` — «missing, malformed and wrong keys are rejected with the same error» (набор `no header`)
- **Ожидается:** без заголовка → **401** `error.code invalid_api_key`.

#### GS-284 · P2 · `integration` · `TestV1Auth_InvalidKey/not_a_key`
**Тест:** то же (набор `not a key`). **Дано:** `Bearer hello`. **Ожидается:** **401** `invalid_api_key`.

#### GS-285 · P2 · `integration` · `TestV1Auth_InvalidKey/wrong_secret`
**Тест:** то же (набор `wrong secret`). **Дано:** `Bearer <key>x`. **Ожидается:** **401** `invalid_api_key`.

#### GS-286 · P2 · `integration` · `TestV1Auth_InvalidKey/sanctum_style_token`
**Тест:** то же (набор `sanctum style token`). **Дано:** `Bearer 1|abcdefghijklmnopqrstuvwxyz`. **Ожидается:** **401** `invalid_api_key`.

#### GS-287 · P2 · `integration` · `TestV1Auth_WrongSecretValidPrefix`
**Тест:** `IntegrationApiAuthTest.php` — «a wrong secret with a valid prefix is rejected»
- **Ожидается:** последний символ ключа изменён → **401** `invalid_api_key`.

#### GS-288 · P2 · `integration` · `TestV1Auth_RevokedImmediately`
**Тест:** `IntegrationApiAuthTest.php` — «a revoked key stops working at once even when its metadata is cached»
- **Ожидается:** до отзыва 200; после `revoke` → **401** `api_key_revoked` (несмотря на кэш).

#### GS-289 · P2 · `integration` · `TestV1Auth_Expired`
**Тест:** `IntegrationApiAuthTest.php` — «an expired key is rejected»
- **Ожидается:** `expires_at = now + 1 ч`: сразу 200; через 61 мин → **401** `api_key_expired`.

#### GS-290 · P2 · `integration` · `TestV1Auth_RotateKeepsScopes`
**Тест:** `IntegrationApiAuthTest.php` — «rotation revokes the old key and keeps the permissions on the new one»
- **Ожидается:** новый ключ: `scopes ["products:read", "sales:read"]`, `rotated_from_id` = старый; секрет другой; старый → `api_key_revoked`; новый → 200.

#### GS-291 · P2 · `integration` · `TestV1Auth_OwnerLostAccess`
**Тест:** `IntegrationApiAuthTest.php` — «a key stops working when its creator loses the integrations permission»
- **Ожидается:** создатель `admin` с `can_manage_integrations`: 200; право снято → `api_key_owner_lost_access`; членство удалено → то же.

#### GS-292 · P1 · `saas` · `TestV1Auth_SubscriptionInactive`
**Тест:** `IntegrationApiAuthTest.php` — «keys of a club without an active subscription are refused»
- **Ожидается:** подписка `canceled` → **402** `subscription_inactive`.

#### GS-293 · P2 · `integration` · `TestV1Auth_IPAllowlist`
**Тест:** `IntegrationApiAuthTest.php` — «the IP allowlist accepts listed addresses and networks only»
- **Дано:** `allowed_ips = ["10.0.0.0/24", "192.168.1.5"]`.
- **Ожидается:** 10.0.0.77 → 200; 192.168.1.5 → 200; 10.0.1.1 → **403** `ip_not_allowed`.

#### GS-294 · P2 · `integration` · `TestV1Auth_TenantOverride/header`
**Тест:** `IntegrationApiAuthTest.php` — «the club cannot be chosen or spoofed through the request» (набор `header`)
- **Ожидается:** `X-Club-Id` чужого клуба (`victim-club`) → **403** `tenant_override_forbidden`; своего → 2xx.

#### GS-295 · P2 · `integration` · `TestV1Auth_TenantOverride/query`
**Тест:** то же (набор `query`). **Ожидается:** `?club_id=` чужого → **403** `tenant_override_forbidden`; своего → 2xx.

#### GS-296 · P2 · `integration` · `TestV1Auth_TenantOverride/body`
**Тест:** то же (набор `body`). **Ожидается:** `POST /api/v1/test/echo {club_id: чужой}` → **403** `tenant_override_forbidden`; свой → 2xx.

#### GS-297 · P2 · `integration` · `TestV1Auth_ScopesRequired`
**Тест:** `IntegrationApiAuthTest.php` — «scopes are required and nothing is allowed by default»
- **Ожидается:** ключ `products:read` на `/test/write` → **403** `insufficient_scope`, `required_scopes ["products:write"]`; ключ с обеими областями → 200; маршрут без объявленной области → **403** `insufficient_scope`.

#### GS-298 · P2 · `integration` · `TestV1Routes_AllDeclareScope`
**Тест:** `IntegrationApiAuthTest.php` — «every v1 route declares a scope»
- **Ожидается:** у каждого маршрута `api/v1/*` (кроме `test`) есть middleware `api.scope:*`.

#### GS-299 · P2 · `integration` · `TestV1Auth_FailedAuthIPBlock`
**Тест:** `IntegrationApiAuthTest.php` — «repeated failed authentication from one IP is blocked»
- **Дано:** `integrations.failed_auth_per_minute_per_ip = 5`.
- **Ожидается:** 5 неверных ключей → 401; затем верный ключ с того же IP → **429** `too_many_failed_attempts` + `Retry-After`; с IP 10.9.9.9 → 200.

#### GS-300 · P2 · `integration` · `TestV1Auth_RevokedKeysDoNotBlockIP`
**Тест:** `IntegrationApiAuthTest.php` — «revoked keys do not block the IP for other keys»
- **Ожидается:** лимит 3; 6 запросов отозванным ключом → `api_key_revoked` каждый раз; другой ключ → 200.

#### GS-301 · P2 · `integration` · `TestV1RateLimit_PerKey`
**Тест:** `IntegrationApiAuthTest.php` — «each key has its own rate limit»
- **Ожидается:** `rate_limit_per_minute 3`: 3 запроса 200, 4-й → **429** `rate_limited` + `Retry-After`; другой ключ → 200.

#### GS-302 · P2 · `integration` · `TestV1RateLimit_PerClub`
**Тест:** `IntegrationApiAuthTest.php` — «all keys of a club share the club rate limit, other clubs are unaffected»
- **Ожидается:** `club_rate_limit_per_minute 4`: 2+2 запроса двумя ключами — 200; 5-й → **429**; ключ другого клуба → 200.

#### GS-303 · P2 · `integration` · `TestV1_PayloadTooLarge`
**Тест:** `IntegrationApiAuthTest.php` — «oversized request bodies are refused»
- **Ожидается:** тело ~300 КБ → **413** `payload_too_large`; маленькое → 200.

#### GS-304 · P1 · `integration` · `TestV1Idempotency_ReplayAndMismatch`
**Тест:** `IntegrationApiAuthTest.php` — «idempotency keys replay the first response and reject a different body»
- **Ожидается:** без `Idempotency-Key` → **428** `idempotency_key_required`; `order-42` → **201** `charge 1`; повтор → **201** `charge 1` + `Idempotent-Replayed: true`; другое тело (`amount 999`) → **422** `idempotency_key_reused`; операция выполнена ровно **1** раз.

#### GS-305 · P2 · `integration` · `TestV1Idempotency_PerApiKey`
**Тест:** `IntegrationApiAuthTest.php` — «idempotency keys of different API keys do not collide»
- **Ожидается:** `same` от ключа 1 → `charge 1`; `same` от ключа 2 → `charge 2`.

#### GS-306 · P1 · `integration` · `TestV1Idempotency_InProgress`
**Тест:** `IntegrationApiAuthTest.php` — «an idempotency key that is still being processed is not run twice»
- **Дано:** строка `api_idempotency_keys {key busy, request_hash = sha256("POST api/v1/test/charge\n[]")}` без ответа.
- **Ожидается:** **409** `idempotency_in_progress`; счётчик не увеличен.

#### GS-307 · P2 · `integration` · `TestV1Audit_NoSecretsOrQuery`
**Тест:** `IntegrationApiAuthTest.php` — «every call is audited without secrets or query strings»
- **Ожидается:** 2 записи `api_request_logs`: первая `{club_id, api_key_id, key_prefix, method GET, endpoint "/api/v1/me", status 200}`; вторая (неверный секрет) `club_id NULL`, `key_prefix` есть, `status 401`; нет секрета и `secret-value` из query.

#### GS-308 · P2 · `integration` · `TestV1Keys_OnlyHashStored`
**Тест:** `IntegrationApiAuthTest.php` — «only a hash is stored and the cache never holds the secret»
- **Ожидается:** `key_hash = sha256(полный ключ)`; секрета (символы с 14-го) нет ни в строке БД, ни в кэше `api_keys.meta.{prefix}`; `key_hash` скрыт из `toArray()`.

#### GS-309 · P3 · `integration` · `TestV1Perf_WarmCallOneQuery`
**Тест:** `IntegrationApiAuthTest.php` — «an authenticated call with warm caches costs one query: the audit row»
- **Ожидается:** второй вызов `/me` — ровно 1 SQL-запрос, `insert into "api_request_logs"`.

#### GS-310 · P3 · `integration` · `TestV1Perf_ColdCallIndexedLookup`
**Тест:** `IntegrationApiAuthTest.php` — «a cold call verifies the key with a few indexed queries and no key list»
- **Ожидается:** 6 ключей, кэш очищен: к `api_keys` ровно 1 запрос с `"prefix" = ?`.

#### GS-311 · P3 · `integration` · `TestV1_NotFoundEnvelope`
**Тест:** `IntegrationApiAuthTest.php` — «unknown v1 endpoints answer with the JSON error envelope»
- **Ожидается:** `/api/v1/nothing-here` → **404** `error.code not_found`.

#### GS-312 · P3 · `integration` · `TestLegacyAPI_Unchanged`
**Тест:** `IntegrationApiAuthTest.php` — «existing API endpoints keep their behaviour»
- **Ожидается:** `GET /api/products` без токена → **401** без `error.code`; ключей 0.

## Файл: IntegrationApiResourcesTest.php

Подготовка: `twoClubsWithData()` — клубы `mine` и `theirs` (активные подписки), в каждом по `seedIntegrationData()`: категория «{Tag} drinks», товар SKU `SKU-SAME` 500 ₸ ×10, движение `receipt` 10, зона «{Tag} hall», ПК №1, тариф «{Tag} hour» 60 мин 1000 ₸ (привязан к зоне), открытая смена с продажей 500 и прочим приходом 100; ключ клуба `mine` со всеми областями `ALL_SCOPES`.

#### GS-313 · P2 · `integration` · `TestV1List_OnlyOwnClub/products`
**Тест:** `IntegrationApiResourcesTest.php` — «every list endpoint returns only the key's club» (набор `/api/v1/products`)
- **Ожидается:** `GET /api/v1/products` → 200, `data` непустой, в ответе нет «Theirs».

#### GS-314 · P2 · `integration` · `TestV1List_OnlyOwnClub/product_categories`
**Тест:** `IntegrationApiResourcesTest.php` — «every list endpoint returns only the key's club» (набор `/api/v1/product-categories`)
- **Ожидается:** `GET /api/v1/product-categories` → 200, `data` непустой, в ответе нет «Theirs».

#### GS-315 · P2 · `integration` · `TestV1List_OnlyOwnClub/stock_movements`
**Тест:** `IntegrationApiResourcesTest.php` — «every list endpoint returns only the key's club» (набор `/api/v1/stock-movements`)
- **Ожидается:** `GET /api/v1/stock-movements` → 200, `data` непустой, в ответе нет «Theirs».

#### GS-316 · P2 · `integration` · `TestV1List_OnlyOwnClub/sales`
**Тест:** `IntegrationApiResourcesTest.php` — «every list endpoint returns only the key's club» (набор `/api/v1/sales`)
- **Ожидается:** `GET /api/v1/sales` → 200, `data` непустой, в ответе нет «Theirs».

#### GS-317 · P2 · `integration` · `TestV1List_OnlyOwnClub/zones`
**Тест:** `IntegrationApiResourcesTest.php` — «every list endpoint returns only the key's club» (набор `/api/v1/zones`)
- **Ожидается:** `GET /api/v1/zones` → 200, `data` непустой, в ответе нет «Theirs».

#### GS-318 · P2 · `integration` · `TestV1List_OnlyOwnClub/computers`
**Тест:** `IntegrationApiResourcesTest.php` — «every list endpoint returns only the key's club» (набор `/api/v1/computers`)
- **Ожидается:** `GET /api/v1/computers` → 200, `data` непустой, в ответе нет «Theirs».

#### GS-319 · P2 · `integration` · `TestV1List_OnlyOwnClub/tariffs`
**Тест:** `IntegrationApiResourcesTest.php` — «every list endpoint returns only the key's club» (набор `/api/v1/tariffs`)
- **Ожидается:** `GET /api/v1/tariffs` → 200, `data` непустой, в ответе нет «Theirs».

#### GS-320 · P2 · `integration` · `TestV1List_OnlyOwnClub/members`
**Тест:** `IntegrationApiResourcesTest.php` — «every list endpoint returns only the key's club» (набор `/api/v1/members`)
- **Ожидается:** `GET /api/v1/members` → 200, `data` непустой, в ответе нет «Theirs».

#### GS-321 · P2 · `integration` · `TestV1List_OwnRecords`
**Тест:** `IntegrationApiResourcesTest.php` — «lists contain the club's own records»
- **Ожидается:** `products` → 1 запись, id своего товара; `sales` → 1 запись (только продажа POS, не «Cash in»), id своей продажи; `tariffs` → `data.0.zone_ids = [своя зона]`; `zones` → `data.0.computers_count 1`.

#### GS-322 · P2 · `integration` · `TestV1_ForeignRecordsByIdNotFound`
**Тест:** `IntegrationApiResourcesTest.php` — «records of another club cannot be read or changed by id»
- **Ожидается:** `GET/PATCH products/{чужой}` → **404** `not_found`; `GET sales/{чужой}`, `GET computers/{чужой}`, `PUT computers/{чужой}/reservation` → **404**; `POST computers/control` с чужим ПК → **422** `validation_failed`; `POST stock-movements` с чужим товаром → **422**; `POST products` с чужой категорией → **422** `error.fields.product_category_id`; фильтры `?product_id=` / `?zone_id=` чужие → 0 записей; чужой товар: цена 500.00, остаток 10; чужой ПК не забронирован и не выключен.

#### GS-323 · P2 · `integration` · `TestV1Scope/GET_products`
**Тест:** `IntegrationApiResourcesTest.php` — «each endpoint requires its scope» (набор `['GET', '/api/v1/products', 'products:read']`)
- **Дано:** ключ со всеми областями, кроме `products:read`. **Действие:** `GET /api/v1/products` (+ `Idempotency-Key: x`). **Ожидается:** **403** `insufficient_scope`, `required_scopes = ["products:read"]`.

#### GS-324 · P2 · `integration` · `TestV1Scope/GET_products_product`
**Тест:** `IntegrationApiResourcesTest.php` — «each endpoint requires its scope» (набор `['GET', '/api/v1/products/{product}', 'products:read']`)
- **Дано:** ключ со всеми областями, кроме `products:read`. **Действие:** `GET /api/v1/products/{product}` (+ `Idempotency-Key: x`). **Ожидается:** **403** `insufficient_scope`, `required_scopes = ["products:read"]`.

#### GS-325 · P2 · `integration` · `TestV1Scope/GET_product_categories`
**Тест:** `IntegrationApiResourcesTest.php` — «each endpoint requires its scope» (набор `['GET', '/api/v1/product-categories', 'products:read']`)
- **Дано:** ключ со всеми областями, кроме `products:read`. **Действие:** `GET /api/v1/product-categories` (+ `Idempotency-Key: x`). **Ожидается:** **403** `insufficient_scope`, `required_scopes = ["products:read"]`.

#### GS-326 · P2 · `integration` · `TestV1Scope/GET_stock_movements`
**Тест:** `IntegrationApiResourcesTest.php` — «each endpoint requires its scope» (набор `['GET', '/api/v1/stock-movements', 'products:read']`)
- **Дано:** ключ со всеми областями, кроме `products:read`. **Действие:** `GET /api/v1/stock-movements` (+ `Idempotency-Key: x`). **Ожидается:** **403** `insufficient_scope`, `required_scopes = ["products:read"]`.

#### GS-327 · P2 · `integration` · `TestV1Scope/POST_products`
**Тест:** `IntegrationApiResourcesTest.php` — «each endpoint requires its scope» (набор `['POST', '/api/v1/products', 'products:write']`)
- **Дано:** ключ со всеми областями, кроме `products:write`. **Действие:** `POST /api/v1/products` (+ `Idempotency-Key: x`). **Ожидается:** **403** `insufficient_scope`, `required_scopes = ["products:write"]`.

#### GS-328 · P2 · `integration` · `TestV1Scope/PATCH_products_product`
**Тест:** `IntegrationApiResourcesTest.php` — «each endpoint requires its scope» (набор `['PATCH', '/api/v1/products/{product}', 'products:write']`)
- **Дано:** ключ со всеми областями, кроме `products:write`. **Действие:** `PATCH /api/v1/products/{product}` (+ `Idempotency-Key: x`). **Ожидается:** **403** `insufficient_scope`, `required_scopes = ["products:write"]`.

#### GS-329 · P2 · `integration` · `TestV1Scope/POST_stock_movements`
**Тест:** `IntegrationApiResourcesTest.php` — «each endpoint requires its scope» (набор `['POST', '/api/v1/stock-movements', 'stock:write']`)
- **Дано:** ключ со всеми областями, кроме `stock:write`. **Действие:** `POST /api/v1/stock-movements` (+ `Idempotency-Key: x`). **Ожидается:** **403** `insufficient_scope`, `required_scopes = ["stock:write"]`.

#### GS-330 · P2 · `integration` · `TestV1Scope/GET_sales`
**Тест:** `IntegrationApiResourcesTest.php` — «each endpoint requires its scope» (набор `['GET', '/api/v1/sales', 'sales:read']`)
- **Дано:** ключ со всеми областями, кроме `sales:read`. **Действие:** `GET /api/v1/sales` (+ `Idempotency-Key: x`). **Ожидается:** **403** `insufficient_scope`, `required_scopes = ["sales:read"]`.

#### GS-331 · P2 · `integration` · `TestV1Scope/GET_sales_sale`
**Тест:** `IntegrationApiResourcesTest.php` — «each endpoint requires its scope» (набор `['GET', '/api/v1/sales/{sale}', 'sales:read']`)
- **Дано:** ключ со всеми областями, кроме `sales:read`. **Действие:** `GET /api/v1/sales/{sale}` (+ `Idempotency-Key: x`). **Ожидается:** **403** `insufficient_scope`, `required_scopes = ["sales:read"]`.

#### GS-332 · P2 · `integration` · `TestV1Scope/GET_zones`
**Тест:** `IntegrationApiResourcesTest.php` — «each endpoint requires its scope» (набор `['GET', '/api/v1/zones', 'computers:read']`)
- **Дано:** ключ со всеми областями, кроме `computers:read`. **Действие:** `GET /api/v1/zones` (+ `Idempotency-Key: x`). **Ожидается:** **403** `insufficient_scope`, `required_scopes = ["computers:read"]`.

#### GS-333 · P2 · `integration` · `TestV1Scope/GET_computers`
**Тест:** `IntegrationApiResourcesTest.php` — «each endpoint requires its scope» (набор `['GET', '/api/v1/computers', 'computers:read']`)
- **Дано:** ключ со всеми областями, кроме `computers:read`. **Действие:** `GET /api/v1/computers` (+ `Idempotency-Key: x`). **Ожидается:** **403** `insufficient_scope`, `required_scopes = ["computers:read"]`.

#### GS-334 · P2 · `integration` · `TestV1Scope/GET_computers_computer`
**Тест:** `IntegrationApiResourcesTest.php` — «each endpoint requires its scope» (набор `['GET', '/api/v1/computers/{computer}', 'computers:read']`)
- **Дано:** ключ со всеми областями, кроме `computers:read`. **Действие:** `GET /api/v1/computers/{computer}` (+ `Idempotency-Key: x`). **Ожидается:** **403** `insufficient_scope`, `required_scopes = ["computers:read"]`.

#### GS-335 · P2 · `integration` · `TestV1Scope/PUT_computers_computer_reservation`
**Тест:** `IntegrationApiResourcesTest.php` — «each endpoint requires its scope» (набор `['PUT', '/api/v1/computers/{computer}/reservation', 'bookings:write']`)
- **Дано:** ключ со всеми областями, кроме `bookings:write`. **Действие:** `PUT /api/v1/computers/{computer}/reservation` (+ `Idempotency-Key: x`). **Ожидается:** **403** `insufficient_scope`, `required_scopes = ["bookings:write"]`.

#### GS-336 · P2 · `integration` · `TestV1Scope/POST_computers_control`
**Тест:** `IntegrationApiResourcesTest.php` — «each endpoint requires its scope» (набор `['POST', '/api/v1/computers/control', 'computers:control']`)
- **Дано:** ключ со всеми областями, кроме `computers:control`. **Действие:** `POST /api/v1/computers/control` (+ `Idempotency-Key: x`). **Ожидается:** **403** `insufficient_scope`, `required_scopes = ["computers:control"]`.

#### GS-337 · P2 · `integration` · `TestV1Scope/GET_tariffs`
**Тест:** `IntegrationApiResourcesTest.php` — «each endpoint requires its scope» (набор `['GET', '/api/v1/tariffs', 'tariffs:read']`)
- **Дано:** ключ со всеми областями, кроме `tariffs:read`. **Действие:** `GET /api/v1/tariffs` (+ `Idempotency-Key: x`). **Ожидается:** **403** `insufficient_scope`, `required_scopes = ["tariffs:read"]`.

#### GS-338 · P2 · `integration` · `TestV1Scope/GET_members`
**Тест:** `IntegrationApiResourcesTest.php` — «each endpoint requires its scope» (набор `['GET', '/api/v1/members', 'members:read']`)
- **Дано:** ключ со всеми областями, кроме `members:read`. **Действие:** `GET /api/v1/members` (+ `Idempotency-Key: x`). **Ожидается:** **403** `insufficient_scope`, `required_scopes = ["members:read"]`.

#### GS-339 · P3 · `integration` · `TestV1Products_CreateUpdateBusinessRules`
**Тест:** `IntegrationApiResourcesTest.php` — «products are created and updated through the existing business rules»
- **Ожидается:** `POST products {API-1, Api tea, своя категория, price "250.50", quantity 4}` → **201** `{quantity 4, category "Mine drinks"}`; повтор SKU → **422** `error.fields.sku`; `PATCH {price 300, name}` → `price "300.00"`; `PATCH {quantity 999}` → **422** `quantity` (остаток меняется только движениями); товар в клубе `mine`, остаток 4; журнал: `product.created`, `product.updated`, `stock.received`, у всех `details.api_key` = первые 12 символов ключа, IP 127.0.0.1.

#### GS-340 · P3 · `integration` · `TestV1Products_SameSkuOtherClub`
**Тест:** `IntegrationApiResourcesTest.php` — «the same SKU may exist in another club»
- **Ожидается:** создание `SKU-SAME-2` → **201**; товаров `SKU-SAME` во всех клубах 2.

#### GS-341 · P1 · `stock` · `TestV1StockMovement_IdempotentAndRules`
**Тест:** `IntegrationApiResourcesTest.php` — «stock movements apply once per idempotency key and respect stock rules»
- **Дано:** свой товар, остаток 10; тело `{write_off, quantity 3}`.
- **Ожидается:** без `Idempotency-Key` → **428**; `wo-1` → **201**; повтор `wo-1` → **201**, тот же ответ; `wo-2` с `quantity 100` → **422** `quantity`; `balance_after` **7**, остаток **7** (списано один раз).

#### GS-342 · P3 · `integration` · `TestV1Computers_ReservationIdempotent`
**Тест:** `IntegrationApiResourcesTest.php` — «computers can be reserved and released idempotently»
- **Ожидается:** `PUT reservation {true}` → `{is_reserved true, status reserved}`; повтор → 200; `{false}` → `is_reserved false`; событий `computer.controlled` **2** (повтор не пишет).

#### GS-343 · P2 · `integration` · `TestV1Control_RefuseInSessionBatchLimit`
**Тест:** `IntegrationApiResourcesTest.php` — «power commands refuse computers in a customer session and limit batch size»
- **Ожидается:** без ключа идемпотентности → **428**; ПК в сессии `power_off` → **409** `{code conflict, computer_ids [id]}`, статус `online`; 51 ПК → **422** (максимум 50); после снятия сессии → `updated 1`, статус `offline`.

#### GS-344 · P2 · `integration` · `TestV1Members_MaskedPhonesNoEmail`
**Тест:** `IntegrationApiResourcesTest.php` — «members are listed with masked phones and without e-mail»
- **Ожидается:** `phone_masked` по шаблону `^\+\d \*\*\* \*\*\* \d{2} \d{2}$`; в ответе нет полного телефона и e-mail.

#### GS-345 · P3 · `integration` · `TestV1_CursorPagination`
**Тест:** `IntegrationApiResourcesTest.php` — «cursor pagination walks the whole list»
- **Ожидается:** 5 товаров, `limit=2` → 2, 2, 1, у последней страницы `next_cursor null`; `limit=500` → **422**.

#### GS-346 · P3 · `integration` · `TestV1List_NoNPlusOne/products`
**Тест:** `IntegrationApiResourcesTest.php` — «list endpoints do not run a query per record» (набор `/api/v1/products`)
- **Ожидается:** после добавления 5 пользователей, товаров, ПК и тарифов число SQL-запросов `GET /api/v1/products` не меняется.

#### GS-347 · P3 · `integration` · `TestV1List_NoNPlusOne/computers`
**Тест:** `IntegrationApiResourcesTest.php` — «list endpoints do not run a query per record» (набор `/api/v1/computers`)
- **Ожидается:** после добавления 5 пользователей, товаров, ПК и тарифов число SQL-запросов `GET /api/v1/computers` не меняется.

#### GS-348 · P3 · `integration` · `TestV1List_NoNPlusOne/tariffs`
**Тест:** `IntegrationApiResourcesTest.php` — «list endpoints do not run a query per record» (набор `/api/v1/tariffs`)
- **Ожидается:** после добавления 5 пользователей, товаров, ПК и тарифов число SQL-запросов `GET /api/v1/tariffs` не меняется.

#### GS-349 · P3 · `integration` · `TestV1List_NoNPlusOne/members`
**Тест:** `IntegrationApiResourcesTest.php` — «list endpoints do not run a query per record» (набор `/api/v1/members`)
- **Ожидается:** после добавления 5 пользователей, товаров, ПК и тарифов число SQL-запросов `GET /api/v1/members` не меняется.

#### GS-350 · P3 · `integration` · `TestV1List_NoNPlusOne/zones`
**Тест:** `IntegrationApiResourcesTest.php` — «list endpoints do not run a query per record» (набор `/api/v1/zones`)
- **Ожидается:** после добавления 5 пользователей, товаров, ПК и тарифов число SQL-запросов `GET /api/v1/zones` не меняется.

## Файл: IntegrationSettingsTest.php

Подготовка: `WebhookUrlGuard` подменён — `internal.example.com` резолвится в 192.168.0.10, остальные хосты — в 93.184.216.34; `staffMember(role, integrations)`; `legacyClubOwner()`.

#### GS-351 · P2 · `integration` · `TestIntegrationsPage_Access`
**Тест:** `IntegrationSettingsTest.php` — «the page is open to the owner and to staff with the integrations permission only»
- **Ожидается:** владелец → «API и интеграции», «Доступ к интеграциям»; `admin` без права → **403**, на `settings.club` нет ссылки; кассир с `can_manage_integrations` → 200 без блока «Доступ к интеграциям».

#### GS-352 · P2 · `integration` · `TestIntegrationsKey_ShownOnceHashStored`
**Тест:** `IntegrationSettingsTest.php` — «a new key secret is shown once and only its hash is stored»
- **Действие:** `POST settings.integrations.keys.store {name Accounting, scopes [products:read, sales:read], allowed_ips "203.0.113.10\n198.51.100.0/24", rate_limit_per_minute 60, expires_at +1 мес.}`.
- **Ожидается:** 200 с `Cache-Control: no-store, private`; в ответе ключ `zfk_[a-z0-9]{8}_[A-Za-z0-9]{40}`; `key_hash = sha256(key)`; `allowed_ips = ["203.0.113.10", "198.51.100.0/24"]`; ключа нет в сессии; на странице виден только префикс; журнал `api_key.created` (владелец, 127.0.0.1) без ключа.

#### GS-353 · P2 · `integration` · `TestIntegrationsKey_Validation/unknown_scope`
**Тест:** `IntegrationSettingsTest.php` — «key input is validated» (набор `unknown scope`)
- **Дано:** `{name K, scopes [products:read]}` + `scopes ['everything']`. **Ожидается:** ошибка `scopes.0`; ключей 0.

#### GS-354 · P2 · `integration` · `TestIntegrationsKey_Validation/no_scope`
**Тест:** `IntegrationSettingsTest.php` — «key input is validated» (набор `no scope`)
- **Дано:** `{name K, scopes [products:read]}` + `scopes []`. **Ожидается:** ошибка `scopes`; ключей 0.

#### GS-355 · P2 · `integration` · `TestIntegrationsKey_Validation/bad_ip`
**Тест:** `IntegrationSettingsTest.php` — «key input is validated» (набор `bad ip`)
- **Дано:** `{name K, scopes [products:read]}` + `allowed_ips '10.0.0.300'`. **Ожидается:** ошибка `allowed_ips`; ключей 0.

#### GS-356 · P2 · `integration` · `TestIntegrationsKey_Validation/bad_network`
**Тест:** `IntegrationSettingsTest.php` — «key input is validated» (набор `bad network`)
- **Дано:** `{name K, scopes [products:read]}` + `allowed_ips '10.0.0.0/40'`. **Ожидается:** ошибка `allowed_ips`; ключей 0.

#### GS-357 · P2 · `integration` · `TestIntegrationsKey_Validation/past_expiry`
**Тест:** `IntegrationSettingsTest.php` — «key input is validated» (набор `past expiry`)
- **Дано:** `{name K, scopes [products:read]}` + `expires_at = вчера`. **Ожидается:** ошибка `expires_at`; ключей 0.

#### GS-358 · P2 · `integration` · `TestIntegrationsKey_Validation/limit_too_high`
**Тест:** `IntegrationSettingsTest.php` — «key input is validated» (набор `limit too high`)
- **Дано:** `{name K, scopes [products:read]}` + `rate_limit_per_minute 100000`. **Ожидается:** ошибка `rate_limit_per_minute`; ключей 0.

#### GS-359 · P2 · `integration` · `TestIntegrationsKey_RotateRevokeFromPage`
**Тест:** `IntegrationSettingsTest.php` — «revoking and rotating keys from the page takes effect immediately»
- **Ожидается:** `POST keys.rotate` → новый ключ в ответе; старый → **401**; новый → 200; `DELETE keys.revoke` → redirect, новый → **401** `api_key_revoked`; событий `api_key.rotated` + `api_key.revoked` = 2.

#### GS-360 · P2 · `integration` · `TestIntegrations_ForeignKeysWebhooksNotFound`
**Тест:** `IntegrationSettingsTest.php` — «keys and webhooks of another club cannot be touched»
- **Ожидается:** revoke/rotate чужого ключа и удаление чужого вебхука → **404**; на странице нет чужого префикса и `other.example.com`; чужой ключ не отозван, вебхук на месте.

#### GS-361 · P2 · `integration` · `TestWebhooks_CreateSecretOnceSSRFGuard`
**Тест:** `IntegrationSettingsTest.php` — «webhooks are created with a one-time secret and checked against internal addresses»
- **Ожидается:** `https://internal.example.com/hook` (частный IP) → ошибка `url`; `http://…` → ошибка `url`; `https://hooks.example.com/hook` с событиями `stock.changed, sale.completed` → 200, секрет показан один раз, в БД зашифрован, на странице не виден; `PATCH {is_active 0}` → выключен; повтор неудачной доставки (`failed`, 6 попыток) → статус `skipped` (вебхук выключен); удаление → вебхуков 0; событий `webhook.created/updated/retried/deleted` = 4.

#### GS-362 · P2 · `integration` · `TestIntegrations_OwnerGrantsPermissionKeysFollow`
**Тест:** `IntegrationSettingsTest.php` — «only the owner grants the integrations permission and removing it disables the member's keys»
- **Ожидается:** ключ менеджера с правом работает; менеджер даёт право кассиру → **403**; владелец снимает право менеджера → `can_manage_integrations false`, событие `integrations.permission_changed` от владельца; ключ менеджера → **401** `api_key_owner_lost_access`.

#### GS-363 · P3 · `integration` · `TestAPIDocs_OpenAPICoversAllRoutes`
**Тест:** `IntegrationSettingsTest.php` — «the API reference is published and documents every v1 route»
- **Ожидается:** `GET api-docs` → `swagger-ui`; `api-docs.spec` → `Content-Type: application/yaml; charset=utf-8`; множество «МЕТОД путь» из `openapi-v1.yaml` совпадает с маршрутами `api/v1/*`. (В Go — сверка роутера chi со спецификацией `oapi-codegen`.)

## Файл: WebhookTest.php

Подготовка: `WebhookUrlGuard` — `internal.example.com` → 10.0.0.5, остальные → 93.184.216.34; `Http::preventStrayRequests()`; `webhookEndpoint(club, owner, events, url)` с секретом `whsec_test_secret`.

#### GS-364 · P2 · `integration` · `TestWebhook_SignedDelivery`
**Тест:** `WebhookTest.php` — «events are delivered with a verifiable signature, timestamp and delivery id»
- **Ожидается:** создание товара через API (`quantity 3`) → 2 запроса (`product.created`, `stock.changed`); `X-Zafiro-Signature = "v1=" + HMAC-SHA256(timestamp + "." + body, secret)`; `X-Zafiro-Timestamp` в пределах 60 с; `X-Zafiro-Delivery = payload.id`; `payload.club_id` = клуб; статусы доставок `delivered`.

#### GS-365 · P2 · `integration` · `TestWebhook_OnlySubscribedSameClub`
**Тест:** `WebhookTest.php` — «only subscribed endpoints of the same club are called»
- **Ожидается:** свой вебхук подписан только на `sale.completed`, чужой клуб — на `product.created`; создание товара → запросов нет, доставок 0.

#### GS-366 · P2 · `integration` · `TestWebhook_RollbackSendsNothing`
**Тест:** `WebhookTest.php` — «a rolled back operation sends nothing»
- **Ожидается:** товар с остатком 0; списание 5 → **422**; запросов нет, доставок 0 (вебхук `stock.changed` только после коммита).

#### GS-367 · P2 · `integration` · `TestWebhook_RetryBackoffThenDisable`
**Тест:** `WebhookTest.php` — «failed deliveries are retried with backoff, then marked failed and the endpoint disabled»
- **Дано:** `webhook_retry_backoff_seconds [10, 60]`, `webhook_disable_after_failures 1`; ответы 503.
- **Ожидается:** после первой попытки `pending`, `attempts 1`, `last_status_code 503`, `next_attempt_at` > now + 5 с; ещё 2 запуска `DeliverWebhook` → `failed`, `attempts 3`, вебхук выключен; новый товар → новых доставок нет (всего 1), HTTP-запросов 3.

#### GS-368 · P2 · `integration` · `TestWebhook_SSRFBlocked/loopback`
**Тест:** `WebhookTest.php` — «webhooks to private networks, plain http and odd ports are blocked» (набор `loopback`)
- **Дано:** URL `https://127.0.0.1/hook`, ретраев нет. **Ожидается:** запросов нет; доставка `{status failed, last_error "blocked: private_address"}`.

#### GS-369 · P2 · `integration` · `TestWebhook_SSRFBlocked/metadata_service`
**Тест:** `WebhookTest.php` — «webhooks to private networks, plain http and odd ports are blocked» (набор `metadata service`)
- **Дано:** URL `https://169.254.169.254/latest`, ретраев нет. **Ожидается:** запросов нет; доставка `{status failed, last_error "blocked: private_address"}`.

#### GS-370 · P2 · `integration` · `TestWebhook_SSRFBlocked/private_DNS`
**Тест:** `WebhookTest.php` — «webhooks to private networks, plain http and odd ports are blocked» (набор `private DNS`)
- **Дано:** URL `https://internal.example.com/hook`, ретраев нет. **Ожидается:** запросов нет; доставка `{status failed, last_error "blocked: private_address"}`.

#### GS-371 · P2 · `integration` · `TestWebhook_SSRFBlocked/plain_http`
**Тест:** `WebhookTest.php` — «webhooks to private networks, plain http and odd ports are blocked» (набор `plain http`)
- **Дано:** URL `http://hooks.example.com/hook`, ретраев нет. **Ожидается:** запросов нет; доставка `{status failed, last_error "blocked: https_required"}`.

#### GS-372 · P2 · `integration` · `TestWebhook_SSRFBlocked/odd_port`
**Тест:** `WebhookTest.php` — «webhooks to private networks, plain http and odd ports are blocked» (набор `odd port`)
- **Дано:** URL `https://hooks.example.com:22/hook`, ретраев нет. **Ожидается:** запросов нет; доставка `{status failed, last_error "blocked: port_not_allowed"}`.

#### GS-373 · P2 · `integration` · `TestWebhook_SSRFBlocked/credentials`
**Тест:** `WebhookTest.php` — «webhooks to private networks, plain http and odd ports are blocked» (набор `credentials`)
- **Дано:** URL `https://user:pass@hooks.example.com/hook`, ретраев нет. **Ожидается:** запросов нет; доставка `{status failed, last_error "blocked: invalid_url"}`.

#### GS-374 · P3 · `integration` · `TestWebhook_WebUIEventsPublished`
**Тест:** `WebhookTest.php` — «sales and PC commands from the web interface are published too»
- **Ожидается:** `club-computers.control` `restart` и `reserve` из веб-панели → доставки `[computer.controlled, computer.reservation_changed]`.

#### GS-375 · P3 · `integration` · `TestIntegrationsPrune`
**Тест:** `WebhookTest.php` — «pruning removes only expired integration records»
- **Дано:** логи API 91 и 1 день назад; ключи идемпотентности 25 ч и сейчас; доставки `delivered` 31 д., `pending` 31 д., `failed` 2 д.; кэш `old-bucket` (истёк) и `live-bucket`.
- **Ожидается:** команда `integrations:prune`: кэш `[live-bucket]`; логов 1; ключи `[new]`; доставки `[pending, failed]`. (В Go — воркер очистки.)

## Файл: SuperadminCabinetTest.php

Подготовка: `cabinetAdmin()` — суперадмин +77009990001 / `secret-pass` без членства в клубах; кабинет — на отдельном хосте `SUPERADMIN_DOMAIN = superadmin.localhost`.

#### GS-376 · P2 · `saas` · `TestCabinet_SuperadminOnly`
**Тест:** `SuperadminCabinetTest.php` — «only superadmins open the cabinet»
- **Ожидается:** страницы `superadmin.dashboard, clubs.index, users.index, plans.index, logs.index, languages`: гость → redirect на вход; владелец клуба → **403** (и `clubs.view` **403**); суперадмин → 200; карточка клуба с его именем; `logs.index?tab=clubs` 200.

#### GS-377 · P2 · `auth` · `TestCabinet_LoginHost`
**Тест:** `SuperadminCabinetTest.php` — «a superadmin without a club signs in to the cabinet, club pages send them there»
- **Ожидается:** вход на сайте клубов → redirect в кабинет, не аутентифицирован; вход на хосте кабинета → redirect на `superadmin.dashboard`, аутентифицирован; `/dashboard` кабинета → redirect туда же; запись `superadmin.login` в журнале платформы.

#### GS-378 · P2 · `saas` · `TestCabinet_SeparateHostAndViewing`
**Тест:** `SuperadminCabinetTest.php` — «the cabinet is on its own host: not on the club site, and no club panel there except a viewed club»
- **Ожидается:** `route('superadmin.users.index') = http://superadmin.localhost/accounts`; на сайте клубов `/superadmin` и `/superadmin/clubs` → **404**; на панели клуба суперадмина с клубом нет ссылки на кабинет; на хосте кабинета `/users` → redirect в кабинет, пока не начат просмотр клуба; после `clubs.view` → 200, на `/dashboard` «Вернуться в кабинет»; после `clubs.leave` снова redirect; гость → redirect; вход не-суперадмина на хосте кабинета → ошибка `phone`.

#### GS-379 · P1 · `saas` · `TestCabinet_ExtendSuspendChangePlan`
**Тест:** `SuperadminCabinetTest.php` — «the superadmin extends, suspends and moves a club to another plan»
- **Дано:** план `pro` 50000 ₸/мес., `travelTo(начало сегодняшнего дня)`.
- **Ожидается:** продление на 0 дней → ошибка `days`; на 30 → `status active`, `current_period_ends_at = сегодня + 30 дн.`; ещё на 10 → **сегодня + 40 дн.** (добавляется к оплаченному периоду); смена плана → `saas_plan_id = pro`; приостановка → `isActive() = false`, владелец на `/dashboard` → redirect на billing; журнал платформы: `superadmin.subscription_extended`, `subscription_plan_changed`, `subscription_suspended`.

#### GS-380 · P2 · `auth` · `TestCabinet_BlockUserEverywhere`
**Тест:** `SuperadminCabinetTest.php` — «a blocked user is signed out everywhere and cannot sign in again»
- **Ожидается:** `superadmin.users.block` → `isBlocked()`, токенов 0, сессий 0; открытая сессия на следующем запросе → redirect на вход, гость; веб-вход → ошибка `phone`; вход агента → **403** `account_blocked`; старый API-токен → **401**; `unblock` → не заблокирован.

#### GS-381 · P2 · `saas` · `TestCabinet_SelfLockoutAndBlockSuperadmin`
**Тест:** `SuperadminCabinetTest.php` — «superadmins cannot lock themselves out, and a superadmin must lose the role before being blocked»
- **Ожидается:** заблокировать себя / снять с себя роль → ошибка `user`; `grant` другому → суперадмин; блок суперадмина → ошибка `user`; `revoke` → не суперадмин; записей `superadmin.granted/revoked` = 2.

#### GS-382 · P1 · `saas` · `TestCabinet_PlansCRUD`
**Тест:** `SuperadminCabinetTest.php` — «the superadmin creates and edits platform plans»
- **Ожидается:** `slug "bad slug"` → ошибка `slug`; `{start, Старт, price_monthly 15000, trial_days 14, features "До 20 ПК\n\nАгент Windows", is_active 1}` → `features ["До 20 ПК", "Агент Windows"]`, активен; `PATCH {price_monthly 17000, trial_days 7}` без `is_active` → неактивен, `trial_days 7`, цена **17000.0**.

#### GS-383 · P2 · `saas` · `TestCabinet_ViewClubReadOnly`
**Тест:** `SuperadminCabinetTest.php` — «a superadmin views a club read-only and the platform log shows it»
- **Ожидается:** `clubs.view` → redirect на `/dashboard` с «Вернуться в кабинет»; все GET-страницы клуба без параметров (`current-club`, не `api/`) отвечают < 500; `POST tariffs.store` → **403**; `clubs.switch` → **403**; тариф «Взлом» не создан; запись `superadmin.club_viewed` (клуб, суперадмин); в журнале клуба записей от суперадмина нет; `clubs.leave` → redirect на карточку клуба, ключ просмотра снят из сессии; `/dashboard` → redirect в кабинет.

#### GS-384 · P1 · `saas` · `TestSubscription_PaidPeriodExpiresScheduler`
**Тест:** `SuperadminCabinetTest.php` — «a paid subscription ends at its paid date and the scheduler marks it expired»
- **Дано:** подписка `active`, `current_period_ends_at = now + 1 день`.
- **Ожидается:** владелец → `/dashboard` 200; через 2 дня `isActive() = false`, redirect на billing (до запуска планировщика); `subscriptions:expire` → вывод `expired: 1`, `status expired`, запись `subscription.expired` без пользователя; billing «Истекла»; повторный запуск → `expired: 0`; продление истёкшей на 30 дней считается **от сегодня**: `status active`, `current_period_ends_at = сегодня (UTC) + 30`; `/dashboard` 200. (В Go — воркер истечения подписок.)

#### GS-385 · P1 · `saas` · `TestSubscription_NoPaidDateNeverExpires`
**Тест:** `SuperadminCabinetTest.php` — «a subscription without a paid date does not expire»
- **Ожидается:** `active`, `current_period_ends_at NULL`, через 400 дней `subscriptions:expire` → `expired: 0`, подписка активна (и в скоупе `active()`).

#### GS-386 · P3 · `saas` · `TestCabinet_AgentReleaseAnnounce`
**Тест:** `SuperadminCabinetTest.php` — «the superadmin announces a new agent version and clubs with older agents are told»
- **Ожидается:** до — нет `agent-update-1.2.0`; владелец `PUT agent-release` → **403**; `latest_version v1.2` → ошибка; `download_url http://…` → ошибка; `1.2.0` с https-ссылкой GitHub → ок, на дашборде «1.2.0», у владельца уведомление, запись `superadmin.agent_release_set`, ссылка на странице ПК; `DELETE` → уведомления нет.

#### GS-387 · P2 · `saas` · `TestCabinet_AutoUpdateRequiresChecksum`
**Тест:** `SuperadminCabinetTest.php` — «auto-update is turned on only with the installer checksum from GitHub»
- **Дано:** `{installer_url}.sha256` отвечает `"0F0F…  ZafiroAgentSetup-1.3.0.exe"` (sha = `0f`×32), прочее 404.
- **Ожидается:** `1.3.0` с `auto_update 1` → `{auto_update true, installer_url, installer_sha256 = sha в нижнем регистре}`, `autoInstaller() = {version 1.3.0, url, sha256}`, sha на дашборде; `9.9.9` с `auto_update` → ошибка `auto_update`, релиз остаётся 1.3.0; `1.3.1` без флажка → `autoInstaller() = null`.

## Файл: SuperadminTest.php

#### GS-388 · P2 · `saas` · `TestSuperadminGrant_ConsoleOnly`
**Тест:** `SuperadminTest.php` — «superadmin rights are granted and revoked only from the console»
- **Ожидается:** `superadmin:grant "8 (700) 123-45-67"` → суперадмин; `--revoke` → нет; неизвестный номер → ошибка команды.

#### GS-389 · P2 · `auth` · `TestSuperadminFlag_NotMassAssignable`
**Тест:** `SuperadminTest.php` — «the superadmin flag cannot be set through registration or user forms»
- **Ожидается:** `users.store` и `auth.register` с `is_superadmin 1` → суперадминов нет.

## Файл: ServerMonitoringTest.php

Подготовка: метрики запросов пишутся в минутные файлы `requests-{minute}.log` (строки `канал\tclub_id\tмс\tстатус`); бот алертов — токен `123456789:AAFake…`, `chat_id 42`.

#### GS-390 · P3 · `monitoring` · `TestMetrics_MinuteFilesFoldedIntoDB`
**Тест:** `ServerMonitoringTest.php` — «requests are counted per channel and club in minute files, then folded into the database»
- **Ожидается:** 3 запроса (страница входа, heartbeat агента 401, `/api/v1/me` 401) → 1 файл, в БД 0; `monitoring:collect` в ту же минуту ничего не читает; через минуту → `request_metrics = {panel 1, agent 1, api 1}`, ошибок 0, файлов нет, `server_metrics` 2 строки. Файл с строками `agent 12 мс 200`, `agent 30 мс 500`, `api` клуба 999999, `panel 100 мс`, `guest 8 мс` → `club_request_metrics {agent 2, panel 1, guest 1, api 0, errors 1, duration_ms 150}`; клуб 999999 не создаётся; поздняя строка `panel 50 мс` → `panel 2, duration 200`; `request_metrics` для `agent` той минуты `{requests 2, errors 1, duration_ms 42}`.

#### GS-391 · P3 · `monitoring` · `TestDashboard_ClubLoadChart`
**Тест:** `ServerMonitoringTest.php` — «the server page shows the load of each club and compares the selected ones on a chart»
- **Дано:** 7 клубов, клуб N: агент N×100, api 10, panel 20, guest 5 запросов, у 7-го 7 ошибок, `duration_ms` N×1000; всего по серверу 3000 запросов, 56000 мс; старая запись 99999 за пределами 24 ч.
- **Ожидается:** «Нагрузка по клубам», порядок «Клуб 7, Клуб 6, Клуб 1»; у клуба 7: **735** запросов (700+10+20+5), «1%» ошибок (7/735), «12.5%» времени сервера (7000/56000); 7 чекбоксов, отмечено 5 самых тяжёлых; выбор `[1, 2, "x"]` → отмечено 2, ссылка `period=7d&clubs[0]=…`; `clubs=nope` → 200.

#### GS-392 · P3 · `monitoring` · `TestAlerts_QueueOnceStartOnceEnd`
**Тест:** `ServerMonitoringTest.php` — «an alert is sent once when a problem starts and once when it is over»
- **Ожидается:** 100 заданий в очереди, два сбора → 1 алерт «Копится очередь: 100 заданий»; очередь очищена → 2-й алерт «Очередь разобрана».

#### GS-393 · P3 · `monitoring` · `TestAlerts_5xxShare`
**Тест:** `ServerMonitoringTest.php` — «a high share of 5xx errors is reported»
- **Ожидается:** 100 запросов, 20 ошибок → алерт «Ошибки 5xx: 20%».

#### GS-394 · P3 · `monitoring` · `TestAlerts_SilentScheduler`
**Тест:** `ServerMonitoringTest.php` — «requests notice a silent scheduler, and the scheduler reports it is back»
- **Ожидается:** последняя метрика 10 мин назад; два запроса → 1 алерт «Планировщик молчит 10 мин»; `monitoring:collect` → «Планировщик снова работает».

#### GS-395 · P3 · `monitoring` · `TestClubSnapshots_HeatMapOfflineAlert`
**Тест:** `ServerMonitoringTest.php` — «club snapshots feed the heat map, and a club whose PCs all go offline is reported»
- **Ожидается:** 4 ПК онлайн, 2 заняты → снимок `{computers 4, online 4, busy 2}`; через 5 мин все офлайн → алерт «все ПК ушли офлайн»; карточка клуба: «Загрузка по часам», «занято 50%»; список: «Требуют внимания», «Возможно, у клуба пропал интернет»; ПК снова онлайн → алерт «снова в сети».

#### GS-396 · P3 · `monitoring` · `TestCabinetClubs_TableSortExpiring`
**Тест:** `ServerMonitoringTest.php` — «the clubs table shows load and activity, sorts by every column and lists subscriptions about to end»
- **Ожидается:** подписка до +2 дней; 3 ПК онлайн и заняты, 1 офлайн; 150 запросов агента за минуту → «Подписка заканчивается», «3 / 4», «75%», «10», «ещё 2 дня»; сортировка по каждому из 9 столбцов работает; `sort=password` → ошибка `sort`.

#### GS-397 · P3 · `monitoring` · `TestDashboard_MachineHistoryScheduler`
**Тест:** `ServerMonitoringTest.php` — «the server page shows the machine, history, channels and the scheduler state»
- **Ожидается:** без метрик «Планировщик ещё не запускался»; с метрикой минуту назад → «Планировщик работает», «Агенты ПК», `<polyline`; `period=7d` → «Запросов в минуту»; `period[]=x` → 200; последняя метрика 30 мин назад → «Планировщик молчит 30 минут».

#### GS-398 · P2 · `monitoring` · `TestAlertsBot_TokenEncrypted`
**Тест:** `ServerMonitoringTest.php` — «the alerts bot is connected with its token kept encrypted and out of the session»
- **Ожидается:** не-суперадмин → **403**; `not-a-token` → ошибка `bot_token`, без `_old_input.bot_token`; верный → `chat_id "-100500"` (из `getUpdates`), токен в БД зашифрован, `settings()['token']` расшифровывается; страница показывает chat_id, не токен; тестовое сообщение ок; удаление → не настроен.

#### GS-399 · P3 · `monitoring` · `TestAlertsBot_NeedsStart`
**Тест:** `ServerMonitoringTest.php` — «a bot that nobody wrote to asks for /start»
- **Ожидается:** `getUpdates` пуст → ошибка `bot_token`, не настроен.

#### GS-400 · P3 · `monitoring` · `TestMonitoringPrune`
**Тест:** `ServerMonitoringTest.php` — «old snapshots are pruned»
- **Ожидается:** `monitoring:prune`: `server_metrics` остаётся 1 (свежая), `request_metrics` 0 (31 день), `club_load_snapshots` 0 (31 день), `club_request_metrics` 0 (3 дня — хранится меньше).

## Файл: ActivityLogTest.php

#### GS-401 · P2 · `activity` · `TestActivityLog_StockCashPOSRecorded`
**Тест:** `ActivityLogTest.php` — «warehouse, cash, and POS operations are recorded in the activity log»
- **Дано:** «Кофе» 5 шт.
- **Действие:** приход 4 «Поставка утром»; открыть смену 1000; приход в кассу 500; продажа кофе ×2.
- **Ожидается:** события `stock.received`, `cash.shift_opened`, `cash.income`, `stock.written_off`, `product.purchased`; страница «Логи действий», «Поставка утром», «Покупка товаров: Кофе × 2».

#### GS-402 · P2 · `activity` · `TestActivityLog_WebLoginLogout`
**Тест:** `ActivityLogTest.php` — «successful web login and logout are recorded with the actor»
- **Ожидается:** форма входа с `>+7</span>`; вход по номеру без `+7` → redirect `dashboard`, событие `auth.login`; выход → `auth.logout`.

#### GS-403 · P3 · `activity` · `TestActivityLog_Filters`
**Тест:** `ActivityLogTest.php` — «activity log filters by event, user, date range, and search»
- **Ожидается:** фильтр `event=stock.received, user_id, from=вчера, search=Кофе` → «Оприходование: Кофе, 8 шт.»; нет «Кассовое поступление тест» и записи 10-дневной давности «Чай».

#### GS-404 · P2 · `activity` · `TestActivityLog_GuestRedirect`
**Тест:** `ActivityLogTest.php` — «activity log page is protected from guests»
- **Ожидается:** redirect на вход.

#### GS-405 · P2 · `activity` · `TestActivityLog_ActorAndIPRequired`
**Тест:** `ActivityLogTest.php` — «activity log entries always name the acting user and the client ip»
- **Ожидается:** `cash.shift_opened` с `user_id` и `ip_address 127.0.0.1`; запись без IP → `QueryException` (NOT NULL).

#### GS-406 · P3 · `activity` · `TestActivityLog_NestedDetails`
**Тест:** `ActivityLogTest.php` — «the activity log shows nested details such as the goods of an agent order»
- **Ожидается:** `shop.purchased` с `items` → строка «items: Cola × 2, Чипсы × 1».

## Файл: ActivityLogCoverageTest.php

#### GS-407 · P2 · `activity` · `TestActivityLog_InvitationNoToken`
**Тест:** `ActivityLogCoverageTest.php` — «creating and accepting a club invitation is logged without the token»
- **Ожидается:** `invitation.created` (владелец, IP, `{role cashier, email}`) без токена; принятие приглашения (`7001112233`) → redirect `dashboard`; `invitation.accepted` от нового кассира в клубе приглашения, `{role cashier, new_account true}`.

#### GS-408 · P2 · `activity` · `TestActivityLog_ClubSwitch`
**Тест:** `ActivityLogCoverageTest.php` — «switching clubs is logged in the club the user switched to»
- **Ожидается:** `club.switched` в клубе назначения, `details.from_club_id` = прежний.

#### GS-409 · P2 · `activity` · `TestActivityLog_FailedWebLogin`
**Тест:** `ActivityLogCoverageTest.php` — «failed web sign-in for an existing account is logged with the reason»
- **Ожидается:** `auth.login_failed` (пользователь, IP), `details = {reason invalid_credentials}`; пароля в записи нет.

#### GS-410 · P2 · `activity` · `TestActivityLog_FailedUnknownPhoneToSystemLog`
**Тест:** `ActivityLogCoverageTest.php` — «failed sign-in for an unknown phone goes to the system log»
- **Ожидается:** в `activity_logs` нет; в системный лог один `warning` с `{phone "+77009998877", reason invalid_credentials, ip 127.0.0.1}`.

#### GS-411 · P2 · `activity` · `TestActivityLog_FailedAPILogin`
**Тест:** `ActivityLogCoverageTest.php` — «failed API sign-ins are logged with the reason»
- **Ожидается:** неверный пароль → `{reason invalid_credentials, device PC-01}`; верный пароль с неверным `club_key` → **401**, `reason invalid_club_key`.

#### GS-412 · P2 · `activity` · `TestActivityLog_SettingsDiff`
**Тест:** `ActivityLogCoverageTest.php` — «settings changes are logged with old and new values and render in the log»
- **Ожидается:** `guests.show_qr_code_on_login = 1` → `details = {section guests, show_qr_code_on_login "нет → да"}`; сохранение финансов без изменений не пишет запись (всего 1); на странице «show_qr_code_on_login: нет → да».

#### GS-413 · P3 · `activity` · `TestActivityLog_ZoneCRUD`
**Тест:** `ActivityLogCoverageTest.php` — «zone create, update and delete are logged»
- **Ожидается:** `zone.created` (subject = зона); `zone.updated ⊇ {name "VIP → VIP Premium", capacity "4 → 6"}`; удаление зоны с 1 ПК → `zone.deleted.details = {computers_removed 1}`.

#### GS-414 · P3 · `activity` · `TestActivityLog_ComputersCreateControlRegister`
**Тест:** `ActivityLogCoverageTest.php` — «adding, registering and controlling PCs is logged»
- **Ожидается:** `computer.created`; `computer.controlled = {action restart, computers "PC-01", updated 1}`; `reserve`; регистрация агентом `{DESKTOP-NEW, machine-42}` → `computer.registered`; повтор не пишет второй раз.

#### GS-415 · P1 · `cash` · `TestZReportPrinted_NonceLogged`
**Тест:** `ActivityLogCoverageTest.php` — «confirmed Z-report printing is logged»
- **Ожидается:** открытие Z-отчёта кладёт `z_report_nonce.{shift}` в сессию; `POST z-report.printed {nonce wrong}` → **419**, записи нет; с верным nonce → 200, `cash.z_report_printed` (пользователь, subject = смена). (Отметка печати — условие закрытия смены, см. сценарий `TestShift_CloseRequiresZReportPrinted`.)

## Файл: AdminLayoutTest.php

#### GS-416 · P3 · `web` · `TestLayout_SidebarToggle`
**Тест:** `AdminLayoutTest.php` — «the sidebar can be toggled and closed from the admin layout»
- **Ожидается:** `/dashboard`: `id="sidebarToggle"`, `aria-controls="adminSidebar"`, `data-sidebar-close`, ключ `zafiro.sidebarCollapsed`. (Для React — Playwright/визуальный тест.)

#### GS-417 · P3 · `web` · `TestLayout_ThemeToggleBeforeStyles`
**Тест:** `AdminLayoutTest.php` — «the top bar switches the day and night theme, chosen before the styles load»
- **Ожидается:** скрипт `localStorage.getItem('zafiro.theme')==='dark'` стоит раньше подключения `build/assets/app-*`; `data-theme-toggle aria-pressed="false" aria-label="Ночная тема"`, `data-label-light="Дневная тема"`.

## Файл: LandingPageTest.php

#### GS-418 · P3 · `web` · `TestLanding_GuestAndSignedIn`
**Тест:** `LandingPageTest.php` — «the home page presents Zafiro with sign-up for guests and the panel for signed-in users»
- **Ожидается:** гость: «Весь клуб — в одном окне», ссылки на регистрацию, установщик агента (`LandingController::AGENT_DOWNLOAD`), вики; вошедший: ссылка на `dashboard`, «Открыть панель».

#### GS-419 · P3 · `web` · `TestWiki_OwnHost`
**Тест:** `LandingPageTest.php` — «the wiki opens on its own host; the old /wiki and other pages there lead to it»
- **Ожидается:** `route('wiki') = http://wiki.localhost`; `GET` → 200 `text/html; charset=utf-8`; `/wiki` → **301** на хост вики; `{wiki}/login` → redirect на вики; главная ссылается на вики; `resources/wiki/index.html` содержит `<title>Zafiro Wiki</title>` и «Быстрый старт».

## Файл: DashboardTest.php

#### GS-420 · P1 · `cash` · `TestDashboard_TodayCashInventoryComputers`
**Тест:** `DashboardTest.php` — «dashboard displays live cash, inventory, zone, and computer data»
- **Дано:** открытая смена: приход 3200 «Продажа сегодня», расход 700 «Расход сегодня», приход 1000 вчера; зона, ПК1 онлайн, ПК2 забронирован; товары с остатком 2 и 20; `settings.json` с валютой RUB.
- **Ожидается:** `todayIncome 3200.0`, `todayExpenses 700.0`, `todayTransactionCount 2`, `productCount 2`, `lowStockCount 1`, `zoneCount 1`, `computerCount 2`, `onlineComputerCount 1`, `reservedComputerCount 1`, `chartIncome[5] = 1000.0` (вчера), `chartIncome[6] = 3200.0` (сегодня); на странице «3 200 RUB», названия операций.

## Файл: TodoTest.php

#### GS-421 · P3 · `club` · `TestTodos_ManagerCreatesCashierToggles`
**Тест:** `TodoTest.php` — «the manager writes tasks for a day; a cashier marks one done and the operator sees it done»
- **Дано:** `setTestNow('2026-10-16 08:00 UTC')`, пояс клуба UTC.
- **Ожидается:** менеджер создаёт «Протереть ПК в зоне VIP» на 16.10; на 15.10 → ошибка `due_on`; кассир видит задачу без «Добавить задачу», создание → **403**; кассир отмечает выполненной; оператор `todos.state` → `{done true, done_by "Айгерим"}`, страница «Выполнил(а) Айгерим»; снятие отметки → `done_at NULL`.

#### GS-422 · P3 · `club` · `TestTodos_OverdueStayOnToday`
**Тест:** `TodoTest.php` — «undone tasks of earlier days stay on today as overdue; done ones do not»
- **Ожидается:** сегодня (16.10): «Старая открытая» с «Просрочено с 14.10»; нет «Старая закрытая» и «Завтрашняя»; `date=2026-10-17` → «Завтрашняя», без просроченной.

## Файл: ChatTest.php

#### GS-423 · P2 · `notify` · `TestChat_CustomerCashierRoundTrip`
**Тест:** `ChatTest.php` — «a customer writes from the agent; the cashier sees it, answers, and the customer gets the answer»
- **Ожидается:** клиент `POST /api/guest/chat {body}` → **201** `from_staff false`; админ видит сообщение, без поля ответа, непрочитанных 1, ответ админа → **403**; у кассира уведомление `chat-*`; кассир открывает чат → сообщение прочитано, ответ «Сейчас принесём» → **201** `author "Айгерим"`; клиент получает 2 сообщения (второе `from_staff true`); `?after={max id}` → 0.

#### GS-424 · P2 · `notify` · `TestChat_OwnConversationTenantIsolation`
**Тест:** `ChatTest.php` — «customers see only their own conversation and staff of another club do not see it»
- **Ожидается:** клиент не видит сообщений Даны (0); кассир другого клуба не видит «Секрет Даны».

## Файл: NewsTest.php

#### GS-425 · P3 · `notify` · `TestNews_ClubCRUDWithImage`
**Тест:** `NewsTest.php` — «club staff publish club news with a picture, edit and delete it»
- **Ожидается:** создание «Турнир в субботу» с PNG 80×80 → `club_id` текущий, файл `news/{club}/…` существует и отдаётся; правка (`is_published 0`, `remove_image 1`) → картинка удалена; удаление → новостей 0; событий `news.created/updated/deleted` = 3.

#### GS-426 · P2 · `notify` · `TestNews_TenantAndRoleIsolation`
**Тест:** `NewsTest.php` — «a club cannot touch platform news or news of another club; cashiers cannot manage news»
- **Ожидается:** удаление платформенной и чужой → **404**; в списке их нет; кассир → `news.index` **403**.

#### GS-427 · P2 · `saas` · `TestNews_SuperadminPlatformNews`
**Тест:** `NewsTest.php` — «the superadmin publishes Zafiro news for every club»
- **Ожидается:** создание платформенной новости; в списке кабинета нет клубной; правка клубной через кабинет → **404**; правка своей → «Агент 0.0.36»; запись `superadmin.news_created`.

#### GS-428 · P3 · `guest` · `TestNews_AgentFeed`
**Тест:** `NewsTest.php` — «the agent gets published Zafiro news and news of its own club, newest first»
- **Ожидается:** `GET /api/guest/news` → `platform` 2 записи [«Zafiro новая», «Zafiro старая»] (черновик скрыт), `club` 1 — «Наш турнир», `image_url null`; новость чужого клуба не видна.

## Файл: NotificationTest.php

#### GS-429 · P2 · `notify` · `TestNotifications_GuestRedirect`
**Тест:** `NotificationTest.php` — «guests cannot read notifications»
- **Ожидается:** `GET notifications.index` и `POST notifications.seen` → redirect на вход.

#### GS-430 · P3 · `notify` · `TestNotifications_ManagerSet`
**Тест:** `NotificationTest.php` — «managers see activity, stock, offline PCs, subscription and invitation notifications»
- **Дано:** чужой `auth.login_failed`; собственные `settings.updated`; удаление зоны 8 дней назад; обычный `cash.income`; товары Red Bull 2 и Вода 40; ПК: №1 `display_name PC-01` (Windows `PC191`) без связи 10 мин, №2 `offline` 10 мин, №3 онлайн; trial до +2 дней; приглашение истекает через 5 ч.
- **Ожидается:** ровно 5 уведомлений: «Неудачный вход» (тело «Неудачный вход: Кассир Айгерим»), «Низкий остаток: 1 товар» («Red Bull (2 шт.)»), «Не на связи: 1 ПК» («PC-01 (Зал)»), «Пробный период заканчивается», «Приглашение скоро истечёт»; `unread_count 5`.

#### GS-431 · P3 · `notify` · `TestNotifications_SeenUntilNew`
**Тест:** `NotificationTest.php` — «opening the panel marks notifications as seen until something new happens»
- **Ожидается:** `unread_count 1`; `seen` → 0, `items.0.unread false`; через минуту новое `settings.updated` → `unread_count 1`, `items.0 = {title "Изменение настроек", unread true}`.

#### GS-432 · P2 · `notify` · `TestNotifications_ByRole`
**Тест:** `NotificationTest.php` — «notifications are filtered by role»
- **Ожидается:** кассир — только `low-stock`; оператор — только `computers-offline`.

#### GS-433 · P3 · `notify` · `TestNotifications_BellNoPermanentDot`
**Тест:** `NotificationTest.php` — «the bell renders without a permanent unread dot»
- **Ожидается:** `id="notifications-button"`, `data-notifications-dot hidden`, ссылка на `notifications.index`.

#### GS-434 · P3 · `notify` · `TestNotifications_SoundAndPolling`
**Тест:** `NotificationTest.php` — «the notification panel has a sound switch and checks for new notifications»
- **Ожидается:** `data-notifications-sound`, «Звук уведомлений включён», `setInterval(`.

## Файл: ClubInvitationTest.php

#### GS-435 · P2 · `club` · `TestInvitation_OneTimeExpiringLink`
**Тест:** `ClubInvitationTest.php` — «club owners can invite a new account with an expiring one-time link»
- **Ожидается:** `POST club-invitations.store {operator@example.com, operator}` → redirect `users.index` с `invite_url`; `token_hash = sha256(token)`, `expires_at` в будущем.

#### GS-436 · P2 · `club` · `TestInvitation_AcceptCreatesMemberAndBalance`
**Тест:** `ClubInvitationTest.php` — «invitee can create an account and join the invited club»
- **Ожидается:** страница приглашения с e-mail; принятие (`7001234567`) → redirect `dashboard`; членство `operator` в клубе приглашения, строка `member_balances` создана, `accepted_at` заполнен; повторное открытие → **404**.

#### GS-437 · P2 · `club` · `TestInvitation_ManagerCannotInviteAdmin`
**Тест:** `ClubInvitationTest.php` — «managers cannot invite administrators»
- **Ожидается:** **403**; приглашений 0.

## Файл: ClubLogoTest.php

#### GS-438 · P3 · `club` · `TestClubLogo_UploadServeLog`
**Тест:** `ClubLogoTest.php` — «an owner uploads the club logo; it is served publicly by its random name and logged»
- **Ожидается:** до — «Логотип не загружен: на мониторах показывается только название клуба.»; PNG 256×128 → «Логотип сохранён.»; путь `club-logos/{club}/[A-Za-z0-9]{40}.png`, `logoUrl() = route('club-logos.show')`, событие `club.logo_changed`; без входа отдаётся с `X-Content-Type-Options: nosniff`, `Content-Type: image/png`; случайное имя → **404**.

#### GS-439 · P3 · `club` · `TestClubLogo_ReplaceAndRemove`
**Тест:** `ClubLogoTest.php` — «a new upload replaces the old file and removing the logo deletes it»
- **Ожидается:** второй файл с новым именем, старый удалён; удаление → `logo_path NULL`, файла нет.

#### GS-440 · P2 · `club` · `TestClubLogo_Validation/svg`
**Тест:** `ClubLogoTest.php` — «only small PNG or JPEG images are accepted» (набор `svg`)
- **Дано:** SVG с `onload`. **Ожидается:** ошибка `logo`; `logo_path NULL`.

#### GS-441 · P2 · `club` · `TestClubLogo_Validation/webp`
**Тест:** `ClubLogoTest.php` — «only small PNG or JPEG images are accepted» (набор `webp`)
- **Дано:** WEBP. **Ожидается:** ошибка `logo`; `logo_path NULL`.

#### GS-442 · P2 · `club` · `TestClubLogo_Validation/too_large`
**Тест:** `ClubLogoTest.php` — «only small PNG or JPEG images are accepted» (набор `too large`)
- **Дано:** PNG размером 2048 КБ. **Ожидается:** ошибка `logo`; `logo_path NULL`.

#### GS-443 · P2 · `club` · `TestClubLogo_Validation/too_small`
**Тест:** `ClubLogoTest.php` — «only small PNG or JPEG images are accepted» (набор `too small`)
- **Дано:** PNG 8×8. **Ожидается:** ошибка `logo`; `logo_path NULL`.

#### GS-444 · P2 · `club` · `TestClubLogo_Validation/not_an_image`
**Тест:** `ClubLogoTest.php` — «only small PNG or JPEG images are accepted» (набор `not an image`)
- **Дано:** PDF с именем logo.png. **Ожидается:** ошибка `logo`; `logo_path NULL`.

#### GS-445 · P2 · `club` · `TestClubLogo_FrontDeskForbidden/cashier`
**Тест:** `ClubLogoTest.php` — «cashiers and operators cannot change the logo» (набор `cashier`)
- **Ожидается:** **403**; `logo_path NULL`.

#### GS-446 · P2 · `club` · `TestClubLogo_FrontDeskForbidden/operator`
**Тест:** `ClubLogoTest.php` — «cashiers and operators cannot change the logo» (набор `operator`)
- **Ожидается:** **403**; `logo_path NULL`.

#### GS-447 · P3 · `agent` · `TestHeartbeat_ClubNameLogo`
**Тест:** `ClubLogoTest.php` — «the agent heartbeat reports the club name and logo URL»
- **Ожидается:** `club.name`; `club.logo_url null`, после загрузки — URL логотипа.

## Файл: ClubSignInImageTest.php

#### GS-448 · P3 · `club` · `TestSignInImage_SetServeReset`
**Тест:** `ClubSignInImageTest.php` — «the club sets its own sign-in screen picture; the agent gets its public URL; it can go back to the standard one»
- **Ожидается:** «Стандартная картинка Zafiro», якорь `#signin-image`; heartbeat `club.signin_image_url null`; PNG 320×200 → ошибка `image` (мало); 1280×720 → путь `club-signin-images/{club}/[A-Za-z0-9]{40}.png`, событие `club.signin_image_changed`, heartbeat отдаёт URL; без входа `Cache-Control: immutable, max-age=31536000, public`; случайное имя → **404**; удаление → NULL, файла нет, heartbeat null.

#### GS-449 · P2 · `club` · `TestSignInImage_CashierForbidden`
**Тест:** `ClubSignInImageTest.php` — «only managers change the sign-in picture»
- **Ожидается:** кассир → **403**.

## Файл: ClubTimezoneTest.php

#### GS-450 · P1 · `club` · `TestTimezone_StoreUTCShowLocal`
**Тест:** `ClubTimezoneTest.php` — «times are stored in UTC and shown in the club time zone»
- **Ожидается:** операция в `2026-10-14 10:00:00 UTC` хранится как `2026-10-14 10:00:00`; в Asia/Almaty — `15:00 +05:00`; в Europe/Moscow — `13:00 +03:00`; сохранение модели не сдвигает UTC.

#### GS-451 · P1 · `cash` · `TestDashboard_TodayIsClubLocalDay`
**Тест:** `ClubTimezoneTest.php` — «today on the dashboard is the local day of the club»
- **Дано:** Asia/Almaty; приходы 100 в 18:30 UTC (23:30 местн., 14.10) и 250 в 19:30 UTC (00:30, 15.10); сейчас 20:30 UTC (01:30, 15.10).
- **Ожидается:** `todayIncome 250.0`, `todayTransactionCount 1`, последняя метка графика «15.10», два последних значения `[100.0, 250.0]`.

#### GS-452 · P3 · `activity` · `TestActivityLog_DateFilterLocalDays`
**Тест:** `ClubTimezoneTest.php` — «the activity log date filter uses local days»
- **Ожидается:** выход в 19:30 UTC (15.10 местн.): фильтр 15.10–15.10 показывает «Выход из системы: {имя}», 14.10–14.10 — нет.

#### GS-453 · P2 · `club` · `TestTimezone_ManagerSetsLogged`
**Тест:** `ClubTimezoneTest.php` — «a manager sets the club time zone and the change is logged»
- **Ожидается:** страница с «Asia/Almaty» и «(UTC+05:00)»; `Asia/Tashkent` сохранён; `Mars/Olympus` → ошибка `timezone`; `club.timezone_changed.details = {from Asia/Almaty, to Asia/Tashkent}`. (Часовой пояс клуба по умолчанию — Asia/Almaty.)

#### GS-454 · P2 · `club` · `TestTimezone_CashierForbidden`
**Тест:** `ClubTimezoneTest.php` — «a cashier cannot change the time zone»
- **Ожидается:** **403**.

#### GS-455 · P3 · `web` · `TestTopBarClock_ClubTime`
**Тест:** `ClubTimezoneTest.php` — «the top bar clock shows club time»
- **Ожидается:** при `07:08:09 UTC`: `data-timezone="Asia/Almaty"`, `data-now` = мс времени сервера, «12:08».

#### GS-456 · P3 · `integration` · `TestV1_UTCTimestamps`
**Тест:** `ClubTimezoneTest.php` — «API responses keep UTC timestamps»
- **Ожидается:** продажа в 10:00 UTC клуба Asia/Almaty → `GET /api/v1/sales` `data.0.created_at = "2026-10-14T10:00:00+00:00"`.

## Файл: GuestAccountTest.php

#### GS-457 · P2 · `club` · `TestCustomerEdit_CashierNameEmailNotDiscount`
**Тест:** `GuestAccountTest.php` — «a cashier edits the name and email of a customer, not the discount»
- **Ожидается:** в списке «Изменить», «Сбросить пароль»; имя/e-mail меняются, событие `user.updated`; `discount 50` → ошибка `discount`.

#### GS-458 · P2 · `club` · `TestCustomerEdit_StaffAccountsProtected`
**Тест:** `GuestAccountTest.php` — «staff accounts are not edited or reset from the customer list»
- **Ожидается:** владелец правит кассира через `users.update` → **403**; сброс пароля → **403**.

#### GS-459 · P2 · `auth` · `TestCustomerPasswordReset_ShownOnce`
**Тест:** `GuestAccountTest.php` — «a reset password is shown once to the staff and the customer is signed out»
- **Ожидается:** `POST users.password` → 200, `Cache-Control: no-store, private`, «Передайте клиенту новый пароль»; новый пароль — 8 символов `[A-Za-z0-9]`, подходит к хэшу; токенов клиента 0; `user.password_reset.details = {delivery screen}`; пароля нет в сессии.

## Файл: GuestProfileTest.php

#### GS-460 · P3 · `guest` · `TestGuestProfile_ChangeName`
**Тест:** `GuestProfileTest.php` — «a customer changes their name in the agent profile»
- **Ожидается:** `"  Арман Сейтов "` → `{name "Арман Сейтов", message "Имя сохранено."}`; `user.profile_updated.details = {name "Арман → Арман Сейтов"}`; `" "` → **422** `name`.

#### GS-461 · P2 · `auth` · `TestGuestProfile_ChangePasswordRevokesOthers`
**Тест:** `GuestProfileTest.php` — «a customer changes the password with the current one; other devices sign in again»
- **Ожидается:** неверный текущий → **422** `current_password`; несовпадение и `123` → **422** `password`; верно → «Пароль изменён. На других устройствах войдите с новым паролем.»; остаётся только токен `PC-01`; в журнале нет пароля; токен `mobile` → **401**.

## Файл: SettingsTest.php

#### GS-462 · P3 · `club` · `TestSettings_ClubSectionSaved`
**Тест:** `SettingsTest.php` — «club settings can be saved and reused on the next request»
- **Действие:** `POST /settings/save {club, session_idle_timeout 20, pc_restart_after_session_end 0.75, session_auto_terminate_when_pc_unavailable 10, booking_interval 40}`.
- **Ожидается:** flash «Настройки сохранены»; в `clubs.settings.club`: 20, **0.75** (дробное), 10, 40 (типы сохраняются: int/float).

#### GS-463 · P3 · `club` · `TestSettings_AfterSessionActionEnum`
**Тест:** `SettingsTest.php` — «the PC action after a session ends is saved and only known actions are accepted»
- **Ожидается:** `pc_action_after_session_end shutdown`, `pc_restart_after_session_end 2` сохранены; `value="shutdown" selected`; `format_c` → ошибка, значение остаётся `shutdown`.

#### GS-464 · P3 · `club` · `TestSettings_Currency`
**Тест:** `SettingsTest.php` — «finance currency setting can be selected»
- **Ожидается:** последовательно `KZT`, `RUB`, `$` сохраняются как есть.

#### GS-465 · P3 · `club` · `TestSettings_GuestsBool/ShowQrCodeOnLogin`
**Тест:** `SettingsTest.php` — «guest login QR code setting is saved as a boolean»
- **Ожидается:** `guests.show_qr_code_on_login`: `'1'` → `true`, `'0'` → `false` (булево, не строка).

#### GS-466 · P3 · `club` · `TestSettings_GuestsBool/AllowGuestSelfTransfer`
**Тест:** `SettingsTest.php` — «guest self transfer setting is saved as a boolean»
- **Ожидается:** `guests.allow_guest_self_transfer`: `'1'` → `true`, `'0'` → `false` (булево, не строка).

#### GS-467 · P3 · `club` · `TestSettings_GuestsBool/AllowAddingFriends`
**Тест:** `SettingsTest.php` — «adding friends setting is saved as a boolean»
- **Ожидается:** `guests.allow_adding_friends`: `'1'` → `true`, `'0'` → `false` (булево, не строка).

#### GS-468 · P3 · `club` · `TestSettings_GuestsBool/AllowBalanceTransferToFriends`
**Тест:** `SettingsTest.php` — «balance transfer to friends setting is saved as a boolean»
- **Ожидается:** `guests.allow_balance_transfer_to_friends`: `'1'` → `true`, `'0'` → `false` (булево, не строка).

#### GS-469 · P3 · `club` · `TestSettingsAPIPage_DefaultsAndEndpoints`
**Тест:** `SettingsTest.php` — «API settings page displays every setting with saved values»
- **Дано:** `settings.json` (наследие: для клуба `legacy` с `clubs.settings = NULL` настройки читаются из этого файла — `app/Services/CurrentClub.php` `settings()`, ~строка 72) `{finances.currency RUB, guests.allow_adding_friends true}`.
- **Ожидается:** `settings` во view: `club.booking_interval 30` (умолчание), `currency RUB`, `show_qr_code_on_login false`, `allow_guest_self_transfer false`, `allow_adding_friends true`, `allow_balance_transfer_to_friends false`; `endpoints` — 26 штук; `productEndpoint.url = route('api.products.index')`.

#### GS-470 · P3 · `club` · `TestSettingsAPI_PerSettingEndpoint`
**Тест:** `SettingsTest.php` — «each setting has its own public JSON API endpoint»
- **Ожидается:** `GET /api/settings/guests/allow_adding_friends` → `{ip "localhost", setting "guests.allow_adding_friends", name "Возможность добавления в друзья", value true}`; неизвестная настройка → **404**. Маршрут требует `auth:sanctum`, `current-club`, `subscription` (`routes/api.php`, ~строка 164); тест ходит с веб-сессией владельца.

#### GS-471 · P3 · `web` · `TestSettings_AnchorsMenu`
**Тест:** `SettingsTest.php` — «the settings menu lists the sections of the open page as anchors»
- **Ожидается:** на `settings.club` 8 карточек-секций, `data-settings-anchors`, порядок `#club-sessions`, `#agent-settings`, `#agent-pin`; на финансах `#loyalty`, без `#club-sessions`; на «Гостях» якорей нет.

## Файл: LanguageSettingsTest.php

#### GS-472 · P3 · `i18n` · `TestClubLanguage_ManagerSwitches`
**Тест:** `LanguageSettingsTest.php` — «a manager switches the club language and the interface follows»
- **Ожидается:** `POST settings.language {kk}` → flash «Интерфейс тілі өзгертілді.»; `clubs.locale = kk`; `club.language_changed.details = {from ru, to kk}`; страница «Интерфейс тілі», `<html lang="kk">`.

#### GS-473 · P3 · `i18n` · `TestClubLanguage_DisabledOrUnknownRejected`
**Тест:** `LanguageSettingsTest.php` — «a disabled or unknown language cannot be selected»
- **Ожидается:** выключенный `en` и `xx` → ошибка `locale`; `locale NULL`.

#### GS-474 · P3 · `i18n` · `TestLanguages_SuperadminCustomLanguage`
**Тест:** `LanguageSettingsTest.php` — «the superadmin adds a custom language and translates it in the editor»
- **Ожидается:** `uz` «Oʻzbekcha»: не системный, включён; поиск «Склад» в редакторе; перевод «Ombor» → на панели «Ombor», непереведённое — по-английски («Register»); пустое значение → «Warehouse»; 2 записи `translations.updated` в журнале платформы, в журнале клуба нет.

#### GS-475 · P3 · `i18n` · `TestLanguages_CannotDisableSelectedOrLast`
**Тест:** `LanguageSettingsTest.php` — «the selected and the last enabled language cannot be disabled; built-in ones cannot be deleted»
- **Ожидается:** выключить/удалить `ru` → ошибка `language`; когда включён только `kk`, выключить его → ошибка; `kk` включён.

#### GS-476 · P3 · `i18n` · `TestLanguages_DeleteCustomResetsClubs`
**Тест:** `LanguageSettingsTest.php` — «deleting a custom language moves its clubs back to the default language»
- **Ожидается:** удаление `uz` → redirect на `superadmin.languages`; языка нет; `locale` клуба NULL.

#### GS-477 · P2 · `i18n` · `TestLanguages_ClubRolesForbidden/owner`
**Тест:** `LanguageSettingsTest.php` — «club roles cannot manage the languages shared by all clubs» (набор `owner`)
- **Ожидается:** создание, редактор, выключение, сохранение переводов, удаление языка → **403**; `uz` не создан; `kk` включён.

#### GS-478 · P2 · `i18n` · `TestLanguages_ClubRolesForbidden/admin`
**Тест:** `LanguageSettingsTest.php` — «club roles cannot manage the languages shared by all clubs» (набор `admin`)
- **Ожидается:** создание, редактор, выключение, сохранение переводов, удаление языка → **403**; `uz` не создан; `kk` включён.

#### GS-479 · P2 · `i18n` · `TestLanguages_ClubRolesForbidden/manager`
**Тест:** `LanguageSettingsTest.php` — «club roles cannot manage the languages shared by all clubs» (набор `manager`)
- **Ожидается:** создание, редактор, выключение, сохранение переводов, удаление языка → **403**; `uz` не создан; `kk` включён.

#### GS-480 · P2 · `i18n` · `TestLanguages_ClubRolesForbidden/cashier`
**Тест:** `LanguageSettingsTest.php` — «club roles cannot manage the languages shared by all clubs» (набор `cashier`)
- **Ожидается:** создание, редактор, выключение, сохранение переводов, удаление языка → **403**; `uz` не создан; `kk` включён.

#### GS-481 · P3 · `i18n` · `TestLanguages_SettingsTab`
**Тест:** `LanguageSettingsTest.php` — «languages have their own settings tab»
- **Ожидается:** владелец: «Язык интерфейса», без «Добавить язык»; суперадмин на панели клуба — без «Добавить язык» и ссылки в кабинет; в кабинете — «Добавить язык»; `settings.club` ссылается на `settings.languages`.

## Файл: LocalizationTest.php

#### GS-482 · P3 · `i18n` · `TestLanguages_BuiltIn`
**Тест:** `LocalizationTest.php` — «russian, kazakh and english are the built-in languages»
- **Ожидается:** по `sort_order`: `[ru, kk, en]`; системных 3.

#### GS-483 · P3 · `i18n` · `TestLocaleResolver_Guest/kazakh`
**Тест:** `LocalizationTest.php` — «guests get their browser language or english» (набор `kazakh`)
- **Дано:** `Accept-Language: kk-KZ,kk;q=0.9,ru;q=0.8`. **Ожидается:** `LocaleResolver::forGuest()` = `kk`.

#### GS-484 · P3 · `i18n` · `TestLocaleResolver_Guest/russian_region`
**Тест:** `LocalizationTest.php` — «guests get their browser language or english» (набор `russian region`)
- **Дано:** `Accept-Language: ru-RU,ru;q=0.9`. **Ожидается:** `LocaleResolver::forGuest()` = `ru`.

#### GS-485 · P3 · `i18n` · `TestLocaleResolver_Guest/unsupported`
**Тест:** `LocalizationTest.php` — «guests get their browser language or english» (набор `unsupported`)
- **Дано:** `Accept-Language: de-DE,de;q=0.9`. **Ожидается:** `LocaleResolver::forGuest()` = `en`.

#### GS-486 · P3 · `i18n` · `TestLocaleResolver_Guest/empty`
**Тест:** `LocalizationTest.php` — «guests get their browser language or english» (набор `empty`)
- **Дано:** `Accept-Language: `. **Ожидается:** `LocaleResolver::forGuest()` = `en`.

#### GS-487 · P3 · `i18n` · `TestGuestPages_BrowserLanguage`
**Тест:** `LocalizationTest.php` — «guest pages are rendered in the browser language»
- **Ожидается:** `kk-KZ` → `<html lang="kk">`; `fr-FR` → `<html lang="en">`.

#### GS-488 · P3 · `i18n` · `TestLocaleResolver_DisabledNotOffered`
**Тест:** `LocalizationTest.php` — «a disabled language is not offered to guests»
- **Ожидается:** `kk` выключен → для `kk-KZ` возвращается `en`.

#### GS-489 · P3 · `i18n` · `TestSignedIn_ClubLanguageDBTranslations`
**Тест:** `LocalizationTest.php` — «signed-in pages use the club language and database translations»
- **Ожидается:** клуб `kk` → `<html lang="kk">`; перевод из БД «Тестовая строка» → «Сынақ жолы».

#### GS-490 · P3 · `i18n` · `TestTranslation_FallbackEnThenRu`
**Тест:** `LocalizationTest.php` — «missing translations fall back to english, then to the russian source text»
- **Ожидается:** в `kk` нет — берётся `en` «English only»; нигде нет — исходный русский текст.

#### GS-491 · P3 · `i18n` · `TestTranslationSync_KeepsEdits`
**Тест:** `LocalizationTest.php` — «sync adds missing rows but keeps edited translations»
- **Ожидается:** после `sync()` значение «Өңделген» сохранено.

#### GS-492 · P3 · `i18n` · `TestInterface_FullyTranslated/kazakh`
**Тест:** `LocalizationTest.php` — «the interface is fully translated to kazakh and english» (набор `kazakh`)
- **Ожидается:** клуб `kk`: на `/dashboard` «Қош келдіңіз, Aigerim!», «Қойма», нет «Добро пожаловать».

#### GS-493 · P3 · `i18n` · `TestInterface_FullyTranslated/english`
**Тест:** `LocalizationTest.php` — «the interface is fully translated to kazakh and english» (набор `english`)
- **Ожидается:** клуб `en`: на `/dashboard` «Welcome, Aigerim!», «Warehouse», нет «Добро пожаловать».

#### GS-494 · P3 · `i18n` · `TestValidationAndPlurals_ClubLanguage`
**Тест:** `LocalizationTest.php` — «validation errors and plural forms follow the club language»
- **Ожидается:** клуб `kk`: ошибка `name` = ««аты» өрісі міндетті.»; `trans_choice(':count товар|:count товара|:count товаров', 5)`: kk → «5 тауар», en → «5 products», en 1 → «1 product».

## Файл: TranslationCacheTest.php

#### GS-495 · P3 · `i18n` · `TestTranslationCache_MissOneQuery`
**Тест:** `TranslationCacheTest.php` — «a cache miss loads the selected language in one query and only the languages it needs»
- **Ожидается:** клуб `kk`, «новый процесс»: один запрос пакета с `inner join "languages"` и двумя параметрами (kk + en, без ru).

#### GS-496 · P3 · `i18n` · `TestTranslationCache_HitNoQueries`
**Тест:** `TranslationCacheTest.php` — «a cache hit does not query translation tables at all»
- **Ожидается:** второй запрос страницы — 0 запросов к `translations`/`languages`.

#### GS-497 · P3 · `i18n` · `TestTranslationCache_MissingPhraseNoDB`
**Тест:** `TranslationCacheTest.php` — «missing phrases fall back without touching the database»
- **Ожидается:** неизвестная фраза возвращается как есть, `(and :count more errors)` → «(and 2 more errors)»; SQL-запросов 0.

#### GS-498 · P3 · `i18n` · `TestTranslationCache_EditBumpsVersion`
**Тест:** `TranslationCacheTest.php` — «editing a translation is visible on the next request and old packs are removed»
- **Ожидается:** после правки «Қойма (жаңа)» видно со следующего запроса; ключ `translations.v{old}.pack.kk` удалён; версия +1.

#### GS-499 · P2 · `i18n` · `TestTranslationCache_ClubsIsolated`
**Тест:** `TranslationCacheTest.php` — «clubs with different languages never see each other's language»
- **Ожидается:** 3 раунда попеременно: казахский клуб — «Қойма» без «Warehouse», английский — наоборот; `locale` клубов не меняется.

#### GS-500 · P3 · `i18n` · `TestTranslationCache_DBFailureNotCached`
**Тест:** `TranslationCacheTest.php` — «a database failure is not cached as empty translations»
- **Ожидается:** при недоступной таблице — исходный текст «Склад»; после восстановления — «Қойма».

#### GS-501 · P3 · `i18n` · `TestTranslationSync_NoInvalidate`
**Тест:** `TranslationCacheTest.php` — «re-running sync keeps manual edits and does not invalidate the cache»
- **Ожидается:** `sync()` возвращает 0, версия кэша прежняя, ручная правка сохранена.

#### GS-502 · P3 · `i18n` · `TestValidation_Localized/russian`
**Тест:** `TranslationCacheTest.php` — «laravel validation errors are localized in every language» (набор `russian`)
- **Ожидается:** клуб `ru`, пустая зона → ошибка `name` = «Поле «имя» обязательно.».

#### GS-503 · P3 · `i18n` · `TestValidation_Localized/kazakh`
**Тест:** `TranslationCacheTest.php` — «laravel validation errors are localized in every language» (набор `kazakh`)
- **Ожидается:** клуб `kk`, пустая зона → ошибка `name` = ««аты» өрісі міндетті.».

#### GS-504 · P3 · `i18n` · `TestValidation_Localized/english`
**Тест:** `TranslationCacheTest.php` — «laravel validation errors are localized in every language» (набор `english`)
- **Ожидается:** клуб `en`, пустая зона → ошибка `name` = «The name field is required.».

#### GS-505 · P3 · `i18n` · `TestTranslationSave_ConstantQueries`
**Тест:** `TranslationCacheTest.php` — «saving translations costs the same number of queries for 10 or 50 rows»
- **Ожидается:** число SQL при сохранении 10 и 50 строк одинаково; 50 строк обновлены.

#### GS-506 · P2 · `i18n` · `TestTranslationEditor_SuperadminOnly`
**Тест:** `TranslationCacheTest.php` — «the translation editor is limited to the superadmin»
- **Ожидается:** кассир и владелец → **403**.

## Файл: TranslationTransferTest.php

#### GS-507 · P3 · `i18n` · `TestTranslationsExport_CSV`
**Тест:** `TranslationTransferTest.php` — «a language exports as CSV with source, English hint and its translations»
- **Ожидается:** `Content-Type: text/csv; charset=UTF-8`, файл `zafiro-translations-kk-{Y-m-d}.csv`, BOM; заголовок `[id, group, ru, en, kk]`; строка «Склад» = `[hash, *, Склад, Warehouse, Қойма]`; строк = числу исходных ключей.

#### GS-508 · P3 · `i18n` · `TestTranslationsExport_MissingOnly`
**Тест:** `TranslationTransferTest.php` — «only untranslated texts can be exported»
- **Ожидается:** у `uz` переведён «Склад» → в выгрузке `missing=1` его нет, строк на 1 меньше.

#### GS-509 · P3 · `i18n` · `TestTranslationsImport_FilledUpdateEmptyKeep`
**Тест:** `TranslationTransferTest.php` — «an edited export imports back: filled cells update, empty cells keep translations»
- **Ожидается:** «Склад» → «Қойма (CSV)»; пустая ячейка «Касса» перевод не удаляет; неизвестный id пропущен; число строк не меняется; `translations.imported ⊇ {changed 1, empty 1, unknown 1}`; flash содержит «1».

#### GS-510 · P3 · `i18n` · `TestTranslationsImport_NewLanguageCommaCSV`
**Тест:** `TranslationTransferTest.php` — «a new language is translated entirely through a file»
- **Ожидается:** CSV с разделителем `,` для `uz` → переводы `{Склад: Ombor}`.

#### GS-511 · P3 · `i18n` · `TestTranslationsImport_BrokenPlaceholders`
**Тест:** `TranslationTransferTest.php` — «rows with broken placeholders are skipped and listed»
- **Ожидается:** «Создан товар: :product» → «Тауар жасалды: :тауар» пропущено; `import_invalid_count 1`, `import_invalid [source]`; перевод не изменён.

#### GS-512 · P3 · `i18n` · `TestTranslationsImport_HeaderCheck/other_language`
**Тест:** `TranslationTransferTest.php` — «a file for another language or without the header is refused» (набор `other language`)
- **Ожидается:** заголовок `[id, group, ru, en, en]` при импорте в kk → ошибка `file` «Файл выгружен для языка «en», а импорт идёт в «kk».»

#### GS-513 · P3 · `i18n` · `TestTranslationsImport_HeaderCheck/no_header`
**Тест:** то же (набор `no header`). **Ожидается:** `[a, b, c, d, e]` → ошибка «Это не файл переводов Zafiro: нужна первая строка «id;group;ru;en;код языка».»

#### GS-514 · P2 · `i18n` · `TestTranslationsCSV_FormulaInjection`
**Тест:** `TranslationTransferTest.php` — «cells that spreadsheets would run as formulas are neutralised and restored»
- **Ожидается:** значение `=HYPERLINK("http://evil")` выгружается как `'=HYPERLINK(...)`; импорт `'-Қойма` → сохраняется `-Қойма`.

#### GS-515 · P2 · `i18n` · `TestTranslationsTransfer_ClubRolesForbidden/owner`
**Тест:** `TranslationTransferTest.php` — «club roles cannot export or import translations» (набор `owner`)
- **Ожидается:** экспорт и импорт → **403**.

#### GS-516 · P2 · `i18n` · `TestTranslationsTransfer_ClubRolesForbidden/admin`
**Тест:** `TranslationTransferTest.php` — «club roles cannot export or import translations» (набор `admin`)
- **Ожидается:** экспорт и импорт → **403**.

#### GS-517 · P2 · `i18n` · `TestTranslationsTransfer_ClubRolesForbidden/manager`
**Тест:** `TranslationTransferTest.php` — «club roles cannot export or import translations» (набор `manager`)
- **Ожидается:** экспорт и импорт → **403**.

#### GS-518 · P2 · `i18n` · `TestTranslationsTransfer_ClubRolesForbidden/cashier`
**Тест:** `TranslationTransferTest.php` — «club roles cannot export or import translations» (набор `cashier`)
- **Ожидается:** экспорт и импорт → **403**.

## Файл: MobileAppTest.php

#### GS-519 · P2 · `auth` · `TestMobileLogin_ClubsList`
**Тест:** `MobileAppTest.php` — «the app sign-in lists the clubs of the account; the PC agent contract stays as it was»
- **Ожидается:** `api.auth.login {…, device_name mobileclient, club_id Альфа}` → `club_id`, `clubs = [{id Альфа, name "Альфа", current true, role owner}, {id Бета, name "Бета", current false, role owner}]`; вход агента → без `clubs`.

#### GS-520 · P1 · `auth` · `TestMobileSwitchClub_NewTokenPerClubBalance`
**Тест:** `MobileAppTest.php` — «the app moves to another club of the account with a new token; the old one stops working»
- **Дано:** пользователь в legacy, «Второй клуб» (баланс 77, active), «Без подписки» (trial истёк); чужой клуб.
- **Ожидается:** `api.auth.clubs` → 3 клуба, текущий legacy; `switch-club` в чужой → **403**; в истёкший → **402**; во «Второй клуб» → `{club_id, club_name "Второй клуб", user.balance 77}`, `current true`; у пользователя один токен `mobileclient` со способностью `club:{второй}`; событие `auth.api_club_switched`; старый токен → **401**; новый → `user.club_id` второго клуба.

#### GS-521 · P1 · `billing` · `TestFriendTransferCode_TelegramConfirmOnce`
**Тест:** `MobileAppTest.php` — «a transfer from the app is confirmed with a code from Telegram, bound to the friend and the amount, once»
- **Дано:** переводы включены; Gateway настроен; Арман +77011112233 основной 100; Дана 10; дружба.
- **Ожидается:** код на 500 (> баланса) → ошибка `amount`, кодов не отправлено; на 40 → `code_minutes 5`, отправлен 1 код; повторный запрос кода → ошибка `code`, новых отправок нет; подтверждение неверным кодом → `code`; верный код с другой суммой (90) → `code`; `transfer_codes.attempts = 2`, баланс 100.00; верный код и 40 → `balance "60.00"`; Дана **50.00**; кодов 0; повторное подтверждение → `code`, баланс **60.00**.

#### GS-522 · P1 · `billing` · `TestFriendTransferCode_FiveAttemptsAndDisabled`
**Тест:** `MobileAppTest.php` — «transfer codes stop after five wrong attempts and are not sent when transfers are off»
- **Ожидается:** 5 неверных подтверждений → `code`; верный после этого → `code`, баланс 100.00; переводы выключены → запрос кода **403**, отправок по-прежнему 1.

## Файл: PwaTest.php

#### GS-523 · P3 · `web` · `TestPWA_Manifest`
**Тест:** `PwaTest.php` — «the manifest makes the app installable and follows the browser language»
- **Ожидается:** `Content-Type: application/manifest+json`; `display standalone`, `start_url /dashboard`, `scope /`, `lang kk` (по `Accept-Language`), иконки 192×192 и 512×512, `maskable`, ярлык «Касса»; файлы иконок существуют.

#### GS-524 · P3 · `web` · `TestPWA_OfflinePublic`
**Тест:** `PwaTest.php` — «the offline page is public and has no club data»
- **Ожидается:** 200, «Zafiro», нет `csrf`.

#### GS-525 · P3 · `web` · `TestPWA_ManifestLinkedEverywhere`
**Тест:** `PwaTest.php` — «every page links the manifest and the install button»
- **Ожидается:** панель: ссылка на манифест, `apple-touch-icon`, `data-pwa-banner`, нет `data-pwa-install`; страница входа — ссылка на манифест.

#### GS-526 · P2 · `web` · `TestPWA_ServiceWorkerNoCache`
**Тест:** `PwaTest.php` — «the service worker never caches pages or API responses»
- **Ожидается:** `public/sw.js` содержит `request.mode === 'navigate'`, `fetch(request).catch(() => caches.match(OFFLINE_URL))`, `if (request.method !== 'GET') return;`, не содержит `/api/`.

## Файл: Unit/AgentWindowsAccountsTest.php

#### GS-527 · P2 · `agent` · `TestAgentSources_NoWindowsAccounts`
**Тест:** `Unit/AgentWindowsAccountsTest.php` — «the agent, its installer and scripts never create Windows accounts or set up auto sign-in»
- **Ожидается:** в `WindowsAgent/` (без `bin`, `obj`, `ZafiroAgent.Tests`) больше 10 файлов `*.cs, *.ps1, *.iss, *.cmd, *.bat, *.xml`; ни в одном нет `New-LocalUser`, `net user`, `NetUserAdd`, `DirectoryServices`, `PrincipalContext`, `UserPrincipal`, `AutoAdminLogon`, `DefaultUserName`, `DefaultPassword`, `unattend`, `sysprep`. (Остаётся проверкой C#-агента, не Go.)

## Файл: Unit/ExampleTest.php

#### GS-528 · P3 · `—` · `—`
**Тест:** `Unit/ExampleTest.php` — «that true is true»
- **Ожидается:** заглушка `expect(true)->toBeTrue()`; переносить не нужно.

---

# Пробелы покрытия: дополнительные эталонные сценарии

Ниже — денежные правила из `app/Services`, которые старые тесты не проверяют (или проверяют только частично). Каждый сценарий помечен **«нет в старых тестах — посчитать по коду»**: числа посчитаны вручную по формулам старого кода (ссылки рядом). Перед тем как сделать их эталоном Go, их стоит один раз прогнать на старом Laravel (см. открытый вопрос ОВ-1). **Прогнаны 2026-10-30** (Q-006): Pest-тесты `tools/php-oracle/tests/GapScenariosTest.php` на копии Zafiro, PHP 8.4.27 (как требует `composer.lock` старого проекта): 36 из 36 проходят; 33 — числа подтверждены, 3 — исправлены формулировки/побочные утверждения (GAP-001, GAP-010, GAP-014), невыполнимых нет. Эталон — фактический результат старого кода (строка «Оракул» под каждым сценарием). Идентификаторы — `GAP-NNN`, приоритет у всех P1, кроме отмеченных.

Обозначения: «₸» — тенге (как хранит Laravel, `decimal(12,2)`), «тиын» — сотые (как будет в Go, `int64`).

## SessionSettlement (`app/Services/SessionSettlement.php`, `settle()`, строки ~30–80)

#### GAP-001 · P1 · `billing` · `TestSettlement_PartialMinuteRoundsDownPerBalance`
**нет в старых тестах — посчитать по коду**
- **Оракул:** ⚠️ исправлено (2026-10-30): числа подтверждены прогоном Pest; было «остатки деления 0.7 и 0.7 тиына», стало **0.745 и 0.705 тиына** (7 000 000 mod 1020 = 760 → 760/1020; 2 916 900 mod 1020 = 720 → 720/1020).
- **Дано:** зона 500 ₸/час; клиент 100.00 / 50.00; смешанная оплата; покупка 17 мин без выбора баланса (как в GS «a customer buys minutes…»): часть `minutes`, `main_cents 10000`, `bonus_cents 4167`, итого 1020 с.
- **Действие:** завершение сессии через 5 мин 20 с (осталось 700 с).
- **Ожидается:** `refund_main = intdiv(10000 × 700, 1020)` = intdiv(7 000 000, 1020) = **6862** тиын (68.62); `refund_bonus = intdiv(4167 × 700, 1020)` = intdiv(2 916 900, 1020) = **2859** тиын (28.59); балансы 0.00 + 68.62 = **68.62** / 8.33 + 28.59 = **36.92**; остатки деления (0.7 и 0.7 тиына) остаются клубу.

#### GAP-002 · P1 · `billing` · `TestSettlement_RefundReducesLoyaltySpending`
**нет в старых тестах — посчитать по коду** (`settle()` → `LoyaltyProgram::subtractSpending()`)
- **Оракул:** ✅ подтверждено прогоном Pest (2026-10-30, PHP 8.4.27, `tools/php-oracle`)
- **Дано:** клиент 1000.00 / 0, `total_spent 0`; зона 600 ₸/час.
- **Действие:** покупка 60 мин основным (600.00 → `total_spent 600.00`); завершение через 30 мин.
- **Ожидается:** возврат 300.00 на основной (баланс **700.00**); `total_spent = 600 − 300 =` **300.00**.

#### GAP-003 · P1 · `billing` · `TestSettlement_KeptMinutesFloorPartialMinute`
**нет в старых тестах — посчитать по коду** (`intdiv($seconds, 60)`)
- **Оракул:** ✅ подтверждено прогоном Pest (2026-10-30, PHP 8.4.27, `tools/php-oracle`)
- **Дано:** тариф 120 мин, 1000 ₸, `usage_limit_minutes 43200`; куплен основным.
- **Действие:** завершение через 29 мин 30 с (осталось 90 мин 30 с = 5430 с).
- **Ожидается:** `kept_minutes = intdiv(5430, 60)` = **90**; 30 секунд теряются; денег не возвращается; `time_packages.minutes_left 90`.

#### GAP-004 · P1 · `billing` · `TestSettlement_MinutesThenTariffWithLimit_LastPartFirst`
**нет в старых тестах — посчитать по коду**
- **Оракул:** ✅ подтверждено прогоном Pest (2026-10-30, PHP 8.4.27, `tools/php-oracle`)
- **Дано:** 2000.00; тариф 60 мин, 500 ₸, `usage_limit 1440`; затем поминутно 30 мин при 600 ₸/час (300.00). Итого 90 мин.
- **Действие:** завершение через 10 мин (осталось 80 мин).
- **Ожидается:** части с конца: поминутка 30 мин → возврат **300.00** целиком; тариф — осталось 50 мин → пакет **50 мин** до `покупка + 1440 мин`; балансы 2000 − 500 − 300 + 300 = **1500.00**; ответ `{refunded "300.00", refunded_bonus "0.00", kept_minutes 50}`.

#### GAP-005 · P2 · `billing` · `TestSessionExpiry_NoSettlementLeavesPurchases`
**нет в старых тестах — посчитать по коду** (`ClubComputerSessions::expireIfDue()/expireAll()` не вызывают `settle()`)
- **Оракул:** ✅ подтверждено прогоном Pest (2026-10-30, PHP 8.4.27, `tools/php-oracle`) — и командой `club-computers:expire-sessions`, и heartbeat (`expireIfDue()`): строка `session_purchases` остаётся, баланс 700.00 (без возврата).
- **Дано:** сессия поминутно 30 мин; время вышло.
- **Действие:** heartbeat после конца или команда `club-computers:expire-sessions`.
- **Ожидается:** сессия снята, денег не возвращается (время использовано); строки `session_purchases` этой сессии **остаются** в БД (в старом коде их удаляет только `settle()`). См. ОВ-5 — решить, переносить ли это поведение.

## PaymentRules и AgentTariffPurchaseService (`app/Services/PaymentRules.php`, `AgentTariffPurchaseService::sell()`, строки ~236–311)

#### GAP-006 · P1 · `billing` · `TestPayment_MixedNoChoiceMainShortBeyondBonusLimit`
**нет в старых тестах — посчитать по коду** (ветка `elseif ($mixed)`)
- **Оракул:** ✅ подтверждено прогоном Pest (2026-10-30, PHP 8.4.27, `tools/php-oracle`)
- **Дано:** смешанная оплата, `bonus_max_percent 30`; 500.00 / 1000.00; тариф 1000 ₸; запрос без `balance`.
- **Ожидается:** `bonus_payment = min(30000, 100000 − 50000)` = 30000; `main_payment` = 70000 > 50000 → **422** `balance` «Недостаточно средств для покупки тарифа.»; балансы без изменений.

#### GAP-007 · P1 · `billing` · `TestPayment_NoMixedNoChoiceBonusPaysWhole`
**нет в старых тестах — посчитать по коду** (ветка «one balance»)
- **Оракул:** ✅ подтверждено прогоном Pest (2026-10-30, PHP 8.4.27, `tools/php-oracle`)
- **Дано:** без смешанной; `bonus_max_percent 100`; 500.00 / 1500.00; тариф 1000; без `balance`.
- **Ожидается:** основного не хватает, бонусов хватает на всю цену → оплата бонусами целиком: **500.00 / 500.00**; `total_spent` не растёт.

#### GAP-008 · P1 · `billing` · `TestPayment_BonusLimitFloor`
**нет в старых тестах — посчитать по коду** (`bonusLimitCents() = intdiv(price × percent, 100)`)
- **Оракул:** ✅ подтверждено прогоном Pest (2026-10-30, PHP 8.4.27, `tools/php-oracle`)
- **Дано:** смешанная; `bonus_max_percent 30`; тариф 999.99 ₸; 2000.00 / 500.00; `balance=bonus`.
- **Ожидается:** лимит `intdiv(99999 × 30, 100)` = intdiv(2 999 970, 100) = **29999** тиын (299.99, а не 300.00); основным 700.00; балансы **1300.00 / 200.01**.

#### GAP-009 · P2 · `billing` · `TestPaymentRules_Clamps`
**нет в старых тестах — посчитать по коду**
- **Оракул:** ✅ подтверждено прогоном Pest (2026-10-30, PHP 8.4.27, `tools/php-oracle`); дополнительно: `bonus_max_percent 30.7` → **30** (`(int)` отбрасывает дробь).
- **Ожидается:** `bonus_max_percent`: 150 → **100**, −5 → **0**, `"abc"` → **100**, отсутствует → **100**; `auto_billing_pause_minutes`: 90 → **60**, −1 → **0**, отсутствует → **5**; `discount_scope "never"` (в БД) → **tariffs**; `bonusPercentOffered()`: 30% без смешанной → **0**, 100% без смешанной → **100**, тариф `main_balance_only` → **0**.

#### GAP-010 · P1 · `billing` · `TestHourlyDiscount_DoubleRounding`
**нет в старых тестах — посчитать по коду** (`hourlyPrice()` округляет цену часа до тиына, потом `minutesPriceInCents()`)
- **Оракул:** ⚠️ исправлено (2026-10-30): числа 513.37 / 5989 подтверждены прогоном Pest; было «без промежуточного округления было бы 5989.6 → иначе», стало: без промежуточного округления 51 337.5 × 7 / 60 = **5989.375 → тоже 5989** (в этом примере двойное округление на результат не влияет; оно важно для других цен — проверять через `eval.php`, `Purchase.run`).
- **Дано:** зона 555 ₸/час; личная скидка 7.5%; `discount_scope all`; 7 мин.
- **Ожидается:** час: `55500 − intdiv(55500 × 750 + 5000, 10000)` = 55500 − 4163 = **51337** (513.37 ₸); 7 мин: `intdiv(51337 × 7 + 30, 60)` = intdiv(359 389, 60) = **5989** тиын (59.89 ₸). (Без промежуточного округления было бы 5989.6 → иначе.)

#### GAP-011 · P1 · `billing` · `TestDiscount_100PercentFreeTariff`
**нет в старых тестах — посчитать по коду** (`discounted()`: `min(10000, bp)`, `max(0, …)`)
- **Оракул:** ✅ подтверждено прогоном Pest (2026-10-30, PHP 8.4.27, `tools/php-oracle`)
- **Дано:** личная скидка 100%; тариф 1000 ₸ со скидкой; 0.00 / 0.00.
- **Ожидается:** цена **0.00**; покупка проходит (`main_payment 0`), `total_spent` не меняется; сессия 60 мин; в журнале `amount "0.00"`.

#### GAP-012 · P1 · `billing` · `TestLoyalty_ExactThresholdAndMaxOfLevels`
**нет в старых тестах — посчитать по коду** (`PercentLevels::percentAt()`: `>=`, максимум из достигнутых)
- **Оракул:** ✅ подтверждено прогоном Pest (2026-10-30, PHP 8.4.27, `tools/php-oracle`)
- **Дано:** уровни `{1500 → 10%}`, `{3000 → 5%}`; личная 0.
- **Ожидается:** `total_spent 1499.99` → 0%; **1500.00** → 10%; 3000.00 → **10%** (берётся максимум достигнутых, а не последний уровень); тариф 1000 → 900.00.

#### GAP-013 · P1 · `billing` · `TestLoyalty_SubtractNotBelowZero`
**нет в старых тестах — посчитать по коду** (`subtractSpending()`)
- **Оракул:** ✅ подтверждено прогоном Pest (2026-10-30, PHP 8.4.27, `tools/php-oracle`)
- **Дано:** `total_spent 200.00`; отмена заказа на 350.00 (заказ был до миграции учёта).
- **Ожидается:** `total_spent` = **0.00** (не −150).

#### GAP-014 · P1 · `billing` · `TestPackageStart_CappedByExpiry`
**нет в старых тестах — посчитать по коду** (`startPackage()`: `min(minutes_left, intdiv(expires − now, 60))`)
- **Оракул:** ⚠️ исправлено (2026-10-30): 40 мин, удаление пакета и тот же `expires_at` подтверждены; было «пакет с `expires_at` в прошлом → удаляется, 422», стало: **422** `package` «Срок абонемента истёк.», но пакет **остаётся в БД** — `$package->delete()` откатывается вместе с транзакцией `DB::transaction()`, в которой брошено исключение (`AgentTariffPurchaseService::startPackage()`). В списке клиента его не видно (`packagesFor()` фильтрует `expires_at > now`).
- **Дано:** пакет 90 мин, истекает через 40 мин 30 с.
- **Ожидается:** сессия **40 мин**; пакет удалён; при следующем досрочном завершении остаток сохраняется до того же `expires_at`. Пакет с `expires_at` в прошлом → удаляется, **422** `package` «Срок абонемента истёк.»

#### GAP-015 · P2 · `billing` · `TestTariff_UserRolesRestriction`
**нет в старых тестах — посчитать по коду** (`isAvailable()`, `user_roles`)
- **Оракул:** ✅ подтверждено прогоном Pest (2026-10-30, PHP 8.4.27, `tools/php-oracle`)
- **Дано:** тариф `user_roles ["client"]`.
- **Ожидается:** клиенту — в списке и продаётся; сотруднику с ролью `cashier` — нет в списке, покупка **422** `tariff` «Тариф недоступен.»

#### GAP-016 · P1 · `billing` · `TestPurchase_ExtendsActiveSession`
**нет в старых тестах — посчитать по коду** (`sell()`: `session_ends_at = текущий конец + минуты`)
- **Оракул:** ✅ подтверждено прогоном Pest (2026-10-30, PHP 8.4.27, `tools/php-oracle`)
- **Дано:** сессия клиента до 11:00 (сейчас 10:00); покупка 30 мин.
- **Ожидается:** `session_ends_at = 11:30`, тот же `session_id`; в `session_purchases` две части.

#### GAP-017 · P2 · `billing` · `TestWeeklyPeriods_WrapAndInclusiveEnd`
**нет в старых тестах — посчитать по коду** (`app/Support/WeeklyPeriods.php`)
- **Оракул:** ✅ подтверждено прогоном Pest (2026-10-30, PHP 8.4.27, `tools/php-oracle`)
- **Дано:** период сб 22:00 – пн 06:00.
- **Ожидается:** сб 21:59 → нет; сб 22:00 → да; вс 03:00 → да; пн 06:00 → **да** (конец включительно до минуты); пн 06:01 → нет. Период с неизвестным днём игнорируется; пустой список → всегда.

## TopUpBonus (`app/Services/TopUpBonus.php`, `for()`; `app/Support/PercentLevels.php`)

#### GAP-018 · P1 · `cash` · `TestTopUpBonus_FractionalPercentHalfUp`
**нет в старых тестах — посчитать по коду**
- **Оракул:** ✅ подтверждено прогоном Pest (2026-10-30, PHP 8.4.27, `tools/php-oracle`)
- **Дано:** уровень `{500 → 2.5%}`; пополнение 999.99 ₸.
- **Ожидается:** `intdiv(99999 × 250 + 5000, 10000)` = intdiv(25 004 750, 10000) = **2500** тиын (25.00; точное 24.99975).

#### GAP-019 · P1 · `cash` · `TestTopUpBonus_ExactThresholdAndMaxPercent`
**нет в старых тестах — посчитать по коду**
- **Оракул:** ✅ подтверждено прогоном Pest (2026-10-30, PHP 8.4.27, `tools/php-oracle`)
- **Дано:** уровни `{2000 → 5%}`, `{5000 → 3%}`.
- **Ожидается:** 1999.99 → 0; **2000.00** → 100.00; 6000.00 → максимум достигнутых **5%** = 300.00 (не 3%).

#### GAP-020 · P1 · `billing` · `TestOwnerTopUp_NoTopUpBonus`
**нет в старых тестах — посчитать по коду** (`UserController::update()` не вызывает `TopUpBonus`)
- **Оракул:** ✅ подтверждено прогоном Pest (2026-10-30, PHP 8.4.27, `tools/php-oracle`)
- **Дано:** бонус `{1000 → 10%}`; владелец пополняет без кассы 2000.
- **Ожидается:** основной +2000.00, бонусный **без изменений**; записей кассы нет. См. ОВ-3.

#### GAP-021 · P2 · `cash` · `TestTopUpBonus_LevelsNormalizeAndLimit`
**нет в старых тестах — посчитать по коду**
- **Оракул:** ✅ подтверждено прогоном Pest (2026-10-30, PHP 8.4.27, `tools/php-oracle`); текст ошибки 21 уровня: «Уровней бонуса — не больше 20.»; 20 уровней сохраняются.
- **Ожидается:** уровни `[{"1000.555", "10"}]` → `{amount 1000.56, percent 10}` (округление до сотых, целые — int); 21 уровень → ошибка `topup_bonus_levels` (максимум 20, как у лояльности).

## CashRegisterService (`app/Services/CashRegisterService.php`)

#### GAP-022 · P1 · `cash` · `TestRefund_NotEnoughCashInShift`
**нет в старых тестах — посчитать по коду** (`refund()`: сумма > остатка смены)
- **Оракул:** ✅ подтверждено прогоном Pest (2026-10-30, PHP 8.4.27, `tools/php-oracle`)
- **Дано:** смена 0; пополнение 1000 наличными; расход 800 → остаток 200.
- **Ожидается:** возврат пополнения 1000 → ошибка `refund` «В кассе недостаточно средств.»; балансы и касса без изменений.

#### GAP-023 · P1 · `cash` · `TestRefund_ClosedShiftOperation`
**нет в старых тестах — посчитать по коду**
- **Оракул:** ✅ подтверждено прогоном Pest (2026-10-30, PHP 8.4.27, `tools/php-oracle`)
- **Дано:** продажа в смене №1; смена закрыта; открыта смена №2.
- **Ожидается:** возврат продажи смены №1 → ошибка `refund` «Вернуть можно только операцию открытой смены.»

#### GAP-024 · P1 · `cash` · `TestRefund_TopUpBonusPartlySpent`
**нет в старых тестах — посчитать по коду** (`min(bonus, bonus_balance)`)
- **Оракул:** ✅ подтверждено прогоном Pest (2026-10-30, PHP 8.4.27, `tools/php-oracle`)
- **Дано:** бонус `{1000 → 10%}`; пополнение 2000 → 2000.00 / 200.00; клиент потратил бонусы до 50.00.
- **Ожидается:** возврат: основной 2000 → **0.00**; бонус снимается `min(200, 50)` = 50 → **0.00**; `bonus_taken_back 50`; расход кассы **2000.00** (бонус в кассу не входил).

#### GAP-025 · P1 · `cash` · `TestRefund_BonusBalanceTopUp`
**нет в старых тестах — посчитать по коду**
- **Оракул:** ✅ подтверждено прогоном Pest (2026-10-30, PHP 8.4.27, `tools/php-oracle`); текст ошибки при бонусном < 500: «Клиент уже потратил часть пополнения: на балансе меньше суммы возврата.»
- **Дано:** пополнение бонусного 500 наличными (бонус за пополнение не начисляется) → бонусный 500.00.
- **Ожидается:** возврат: бонусный **0.00**, основной без изменений; если бонусный < 500 → ошибка `refund`.

#### GAP-026 · P1 · `cash` · `TestPOSSale_InsufficientStock`
**нет в старых тестах — посчитать по коду**
- **Оракул:** ✅ подтверждено прогоном Pest (2026-10-30, PHP 8.4.27, `tools/php-oracle`); текст: «Недостаточно товара: Cola.» (имя товара + точка).
- **Дано:** товар 2 шт.; продажа 3.
- **Ожидается:** ошибка `items` «Недостаточно товара: …»; ни движений, ни транзакции; остаток 2. Продажа без открытой смены → ошибка `items` «Сначала откройте смену.»

#### GAP-027 · P1 · `cash` · `TestShiftClose_WithRefundsAndExpenses`
**нет в старых тестах — посчитать по коду** (`close()`, `totals()`)
- **Оракул:** ✅ подтверждено прогоном Pest (2026-10-30, PHP 8.4.27, `tools/php-oracle`)
- **Дано:** смена 1000; продажа 2100; пополнение 5000; расход 300; возврат продажи 2100.
- **Ожидается:** приход 7100, расход 2400, `closing_balance = 1000 + 7100 − 2400 =` **5700.00**.

## ShiftReport (`app/Services/ShiftReport.php`, `for()`)

#### GAP-028 · P1 · `cash` · `TestShiftReport_OtherIncomeAndRefunds`
**нет в старых тестах — посчитать по коду**
- **Оракул:** ✅ подтверждено прогоном Pest (2026-10-30, PHP 8.4.27, `tools/php-oracle`)
- **Дано:** смена 0; продажа кола 2×700 = 1400; прочий приход 300 «Разное»; гостевая сессия 600; возврат продажи.
- **Ожидается:** виды: «Продажа товаров 1 · 1400», «Пополнение балансов 0 · 0», «Гостевые сессии 1 · 600», «Прочий приход **1 · 300.00**»; товары — пусто (продажа возвращена); `refunds = {count 1, amount 1400}`; сотрудник: 4 операции, приход 2300, расход 1400.

#### GAP-029 · P2 · `cash` · `TestShiftReport_StaffSortedByIncome`
**нет в старых тестах — посчитать по коду**
- **Оракул:** ✅ подтверждено прогоном Pest (2026-10-30, PHP 8.4.27, `tools/php-oracle`)
- **Дано:** кассир А: приход 500; кассир Б: приход 1500, расход 100.
- **Ожидается:** порядок `[Б (2 оп., 1500, 100), А (1 оп., 500, 0)]`; поле `discount` = 0 (ключ `discount_amount` в чеках нигде не пишется — см. ОВ-6).

## GuestSessions (`app/Services/GuestSessions.php`, `sell()`, `priceFor()`)

#### GAP-030 · P1 · `cash` · `TestGuestSession_MinutesTwoZonesSum`
**нет в старых тестах — посчитать по коду**
- **Оракул:** ✅ подтверждено прогоном Pest (2026-10-30, PHP 8.4.27, `tools/php-oracle`)
- **Дано:** смена; ПК1 в VIP 600 ₸/час, ПК2 в Hall 450 ₸/час; 90 мин, `card`.
- **Ожидается:** ПК1 `intdiv(60000 × 90 + 30, 60)` = 90000; ПК2 `intdiv(45000 × 90 + 30, 60)` = 67500; итог **1575.00** одной записью кассы; скидки клиентов не применяются (гость без аккаунта).

#### GAP-031 · P1 · `cash` · `TestGuestSession_RefusedOnAccountSession`
**нет в старых тестах — посчитать по коду**
- **Оракул:** ✅ подтверждено прогоном Pest (2026-10-30, PHP 8.4.27, `tools/php-oracle`); текст: «ПК PC-01 занят или забронирован.» (`displayName()` ПК).
- **Дано:** на ПК сессия клиента с аккаунтом.
- **Ожидается:** продажа гостевой → ошибка `computer_ids` «ПК … занят или забронирован.»; касса не меняется. Завершение гостевой сессии сотрудником (`end-sessions`) денег не возвращает (`refund 0`, гость без баланса).

## Friends / FriendTransferCodes (`app/Services/Friends.php` `transfer()`, `amountToCents()`; `FriendTransferCodes.php`)

#### GAP-032 · P1 · `billing` · `TestFriendTransfer_AmountFormatAndMax`
**нет в старых тестах — посчитать по коду**
- **Оракул:** ✅ подтверждено прогоном Pest (2026-10-30, PHP 8.4.27, `tools/php-oracle`); `"1000000.01"` → 100 000 001 тиын (`amountToCents` не режет), отказ — по максимуму; `"10.555"` → 0 → отказ «Сумма перевода — от 0,01 до 1 000 000.» (тот же текст).
- **Ожидается:** `"40,50"` → 4050 тиын; `"10.555"` → 0 → ошибка `amount`; `"1000000"` → 100 000 000 тиын — разрешено при балансе; `"1000000.01"` → ошибка `amount` «Сумма перевода — от 0,01 до 1 000 000.»; `" 5 "` → 500 (пробелы обрезаются).

#### GAP-033 · P1 · `billing` · `TestFriendTransfer_NotLoyaltySpending`
**нет в старых тестах — посчитать по коду**
- **Оракул:** ✅ подтверждено прогоном Pest (2026-10-30, PHP 8.4.27, `tools/php-oracle`)
- **Дано:** Арман 100.00, `total_spent 0`; Дана 10.00.
- **Ожидается:** перевод 40 → Арман 60.00, Дана 50.00; `total_spent` обоих **не меняется**; бонусные балансы не участвуют.

#### GAP-034 · P1 · `billing` · `TestFriendTransferCode_ExpiryAndHourlyLimit`
**нет в старых тестах — посчитать по коду** (`CODE_MINUTES 5`, `RESEND_SECONDS 60`, `CODES_PER_HOUR 5`)
- **Оракул:** ✅ подтверждено прогоном Pest (2026-10-30, PHP 8.4.27, `tools/php-oracle`)
- **Ожидается:** код через 5 мин 1 с → ошибка `code`, балансы прежние; 5 кодов с интервалом 61 с — ок, 6-й в течение часа → ошибка `code` «Слишком много запросов кода…»; проверяется только последний код (предыдущий верный → ошибка); код для другой дружбы → ошибка.

## SeatTransfers (`app/Services/SeatTransfers.php`, `transfer()`, `allowedZoneIds()`)

#### GAP-035 · P1 · `billing` · `TestSeatTransfer_MinutesStayInTheirZone`
**нет в старых тестах — посчитать по коду**
- **Оракул:** ✅ подтверждено прогоном Pest (2026-10-30, PHP 8.4.27, `tools/php-oracle`)
- **Дано:** время куплено поминутно в зоне Hall (`time.purchased`, subject = зона Hall); свободный ПК в VIP.
- **Ожидается:** пересадка в VIP → ошибка `target_computer_id`; в Hall — ок, денег не списывается.

#### GAP-036 · P1 · `billing` · `TestSeatTransfer_ThenEndRefundsSame`
**нет в старых тестах — посчитать по коду**
- **Оракул:** ✅ подтверждено прогоном Pest (2026-10-30, PHP 8.4.27, `tools/php-oracle`)
- **Дано:** 60 мин поминутно 600 ₸/час (600.00), пересадка на другой ПК через 10 мин.
- **Ожидается:** `session_id` тот же; завершение на новом ПК ещё через 20 мин → возврат за 30 мин = **300.00** (части сессии переносятся вместе с `session_id`).
