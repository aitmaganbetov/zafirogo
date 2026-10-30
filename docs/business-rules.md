# Бизнес-правила Zafiro (Laravel) для переноса в zafirogo (Go)

> Этап 0, задача 2. Источник правды — старый код `../Zafiro` (только чтение).
> Все ссылки на код ведут к `/home/user/Zafiro/` (`app/...`). Номера строк — на момент Этапа 0
> (коммит, склонированный оркестратором); если строка сдвинулась, ищите по имени метода.
> Открытые вопросы по этому документу: [`docs/open-questions/business-rules.md`](open-questions/business-rules.md).

## 0. Как читать документ

- **Приоритет**: P1 — деньги (баланс, бонусы, касса, цены, возвраты, траты лояльности), расчёт должен
  совпасть до тиына; P2 — доступ, сессии, брони, безопасность, подписка; P3 — сервисные правила (чат,
  уведомления, сообщения на ПК).
- **Псевдокод** записан буквально: `intdiv(a, b)` — целочисленное деление с отбрасыванием дробной части
  к нулю (PHP `intdiv`, Go `/` для int64 — совпадает, оба усекают к нулю); `round(x)` — PHP `round()`
  (половина — от нуля, с «предокруглением» десятичного представления, см. BR-001);
  `max/min` — как есть. Порядок операций сохранён.
- **Единицы**: «центы»/«тиыны» — целые сотые доли валюты клуба (в коде PHP везде `cents`; для KZT это тиыны).
  В БД Laravel суммы хранятся в `decimal(12,2)` / `decimal(14,2)` **в тенге** (кроме `session_purchases.main_cents/bonus_cents`
  и `transfer_codes.amount_cents` — там уже центы). В Go всё — `int64` центов (правило `CLAUDE.md`).
- **Время**: в БД — UTC (`config/app.php` `timezone = UTC`); показ и «локальные» правила — в часовом поясе
  клуба `clubs.timezone` (по умолчанию `Asia/Almaty`, миграция `2026_10_14_060000_add_timezone_to_clubs_table.php`).
- Каждое правило заканчивается чек-листом `- [ ]` для переноса.
- Раздел «Найденные странности» в конце (§ 15) — поведение, которое при переносе **повторяем**, пока владелец
  не решит иначе (вопросы продублированы в `open-questions/business-rules.md`).

## 1. Оглавление и сводная таблица правил

| ID | Правило | Приоритет | Основной файл (Laravel) |
|---|---|---|---|
| **§2 Общие правила денег** | | | |
| [BR-001](#br-001) | Хранение сумм, `amountToCents` / `formatCents`, округление PHP | P1 | `app/Services/AgentTariffPurchaseService.php` |
| [BR-002](#br-002) | Суммы переводов друзьям: свой парсер, отрицательные балансы | P1 | `app/Services/Friends.php` |
| [BR-003](#br-003) | Настройки клуба: где читаются и значения по умолчанию | P1 | `app/Services/CurrentClub.php`, `SettingsController.php` |
| [BR-004](#br-004) | Часовой пояс клуба в правилах | P2 | `app/Services/CurrentClub.php`, `Models/Concerns/UsesClubTimezone.php` |
| [BR-005](#br-005) | Блокировки и транзакции денежных операций (сводка) | P1 | разные |
| **§3 Цены и оплата времени** | | | |
| [BR-010](#br-010) | Область скидки `discount_scope` (tariffs/hourly/all) | P1 | `app/Services/PaymentRules.php` |
| [BR-011](#br-011) | Скидка клиента = max(персональная, накопительная) | P1 | `app/Services/LoyaltyProgram.php` |
| [BR-012](#br-012) | Формула скидки `discounted()` | P1 | `AgentTariffPurchaseService.php` |
| [BR-013](#br-013) | Цена тарифа для клиента | P1 | `AgentTariffPurchaseService.php` |
| [BR-014](#br-014) | Цена часа зоны и поминутная цена | P1 | `AgentTariffPurchaseService.php` |
| [BR-015](#br-015) | Эффективные минуты тарифа (лимит использования) | P1 | `AgentTariffPurchaseService.php` |
| [BR-016](#br-016) | Доступность тарифа (продажа, лимит количества, роли, период, зона) | P1 | `AgentTariffPurchaseService.php`, `Models/Tariff.php` |
| [BR-017](#br-017) | Недельные периоды `WeeklyPeriods` | P1 | `app/Support/WeeklyPeriods.php` |
| [BR-018](#br-018) | Выбор баланса: бонусный лимит, смешанная оплата, «старые агенты» | P1 | `AgentTariffPurchaseService::sell()`, `PaymentRules.php` |
| [BR-019](#br-019) | Что агент предлагает оплатить бонусами | P2 | `PaymentRules::bonusPercentOffered()` |
| [BR-020](#br-020) | Покупка тарифа в агенте (старт/продление сессии) | P1 | `AgentTariffPurchaseService::purchase()` |
| [BR-021](#br-021) | Покупка минут (поминутно) | P1 | `AgentTariffPurchaseService::purchaseMinutes()` |
| [BR-022](#br-022) | «Игра на весь баланс» | P1 | `WindowsAgent/Frontend/main.js`, `GuestMinutesController.php` |
| [BR-023](#br-023) | Автобиллинг с паузой | P1 | `PaymentRules.php`, `WindowsAgent/Frontend/main.js` |
| [BR-024](#br-024) | Запуск сохранённого пакета (абонемента) | P1 | `AgentTariffPurchaseService::startPackage()` |
| **§4 Сессии ПК** | | | |
| [BR-030](#br-030) | Модель сессии ПК, «оплаченная сессия» | P1 | `app/Models/ClubComputer.php` |
| [BR-031](#br-031) | Досрочное завершение сессии | P1 | `app/Services/ClubComputerSessions.php` |
| [BR-032](#br-032) | Расчёт возврата при завершении (SessionSettlement) | P1 | `app/Services/SessionSettlement.php` |
| [BR-033](#br-033) | Истечение сессий (каждую минуту и в heartbeat) | P1 | `ExpireComputerSessions.php`, `ClubComputerSessions.php` |
| [BR-034](#br-034) | Очистка частей сессий и истёкших пакетов | P1 | `routes/console.php` |
| [BR-035](#br-035) | Пересадка на другой ПК | P1 | `app/Services/SeatTransfers.php` |
| [BR-036](#br-036) | Heartbeat агента и состояние ПК | P2 | `ClubComputerSessions::heartbeat()` |
| [BR-037](#br-037) | Сброс гостевой сессии при перезагрузке ПК | P1 | `ClubComputerSessions::resetGuestSession()` |
| [BR-038](#br-038) | Гостевая сессия с кассы (без аккаунта) | P1 | `app/Services/GuestSessions.php` |
| [BR-039](#br-039) | Действие с ПК после сессии, выход по бездействию | P3 | `ClubComputerController::afterSessionAction()`, `GuestFeatures.php` |
| **§5 Бонусы и лояльность** | | | |
| [BR-040](#br-040) | Уровни «порог → процент» (`PercentLevels`) | P1 | `app/Support/PercentLevels.php` |
| [BR-041](#br-041) | Бонус за пополнение | P1 | `app/Services/TopUpBonus.php` |
| [BR-042](#br-042) | Накопительные траты (`total_spent`) | P1 | `app/Services/LoyaltyProgram.php` |
| **§6 Касса** | | | |
| [BR-050](#br-050) | Открытие смены | P1 | `app/Services/CashRegisterService.php` |
| [BR-051](#br-051) | Итоги и остаток смены | P1 | `CashRegisterService::totals()` |
| [BR-052](#br-052) | Продажа товаров и услуг (POS) | P1 | `CashRegisterService::sale()` |
| [BR-053](#br-053) | Пополнение баланса клиента на кассе | P1 | `CashRegisterService::topUpBalance()` |
| [BR-054](#br-054) | Приход / расход кассы | P1 | `CashRegisterService::transact()` |
| [BR-055](#br-055) | Возврат по чеку | P1 | `CashRegisterService::refund()` |
| [BR-056](#br-056) | Закрытие смены и обязательная печать Z-отчёта | P1 | `CashRegisterService::close()`, `CashRegisterController.php` |
| [BR-057](#br-057) | Состав X/Z-отчёта | P1 | `app/Services/ShiftReport.php` |
| [BR-058](#br-058) | Пополнение и списание баланса владельцем без кассы | P1 | `app/Http/Controllers/UserController.php` |
| [BR-059](#br-059) | Новый клиент: начальный баланс и скидка | P1 | `UserController::store()` |
| [BR-060](#br-060) | Лимит кассиров/операторов на смене | P3 | `app/Services/StaffShifts.php` |
| [BR-061](#br-061) | Дашборд и «отчёт дня» (суммы по локальным дням) | P2 | `DashboardController.php`, `Api/Staff/MeController.php` |
| **§7 Склад** | | | |
| [BR-070](#br-070) | Приход / списание товара | P2 | `app/Actions/AdjustStockAction.php` |
| [BR-071](#br-071) | Создание товара с начальным остатком | P2 | `app/Actions/CreateProductAction.php` |
| [BR-072](#br-072) | Изменение товара (без остатка) | P2 | `app/Actions/UpdateProductAction.php` |
| [BR-073](#br-073) | Удаление товара (мягкое) | P3 | `ProductDeleteController.php` |
| **§8 Заказы из агента** | | | |
| [BR-075](#br-075) | Заказ товаров с ПК (оплата с основного баланса) | P1 | `app/Services/AgentShop.php` |
| [BR-076](#br-076) | Выдача заказа | P2 | `AgentShop::handOver()` |
| [BR-077](#br-077) | Отмена заказа (возврат денег и товара) | P1 | `AgentShop::cancel()` |
| **§9 Брони** | | | |
| [BR-080](#br-080) | Интервал между бронями | P2 | `app/Services/ComputerReservations.php` |
| [BR-081](#br-081) | Создание брони (одной и пачкой) | P2 | `ComputerReservations::create()`, `ComputerReservationController.php` |
| [BR-082](#br-082) | Бронь блокирует продажу времени | P1 | `ComputerReservations::blocking()` |
| [BR-083](#br-083) | Отмена брони, отметка прихода, состояния | P2 | `ComputerReservations.php`, `Models/ComputerReservation.php` |
| [BR-084](#br-084) | Ручной резерв ПК (`is_reserved`) | P2 | `ClubComputerController::control()` |
| **§10 Друзья и переводы** | | | |
| [BR-090](#br-090) | Заявки в друзья | P3 | `app/Services/Friends.php` |
| [BR-091](#br-091) | Перевод основного баланса другу (с паролем) | P1 | `Friends::transfer()`, `GuestFriendController.php` |
| [BR-092](#br-092) | Перевод с кодом из Telegram (моб. приложение) | P1 | `app/Services/FriendTransferCodes.php` |
| **§11 Вход, QR, регистрация** | | | |
| [BR-100](#br-100) | Вход клиента в агенте, «один ПК за раз» | P2 | `Api/AuthController.php`, `app/Services/AgentSignIns.php` |
| [BR-101](#br-101) | Регистрация клиента на ПК | P2 | `Api/AgentRegisterController.php` |
| [BR-102](#br-102) | QR-вход на ПК | P2 | `app/Services/QrLogins.php` |
| [BR-103](#br-103) | Восстановление пароля кодом Telegram | P3 | `app/Services/PasswordRecovery.php` |
| [BR-104](#br-104) | Вход сотрудника в панель | P2 | `app/Http/Controllers/AuthController.php` |
| **§12 ПК и агент** | | | |
| [BR-120](#br-120) | Команды питания и Wake-on-LAN | P3 | `app/Services/ComputerPower.php` |
| [BR-121](#br-121) | Сообщение на экран ПК | P3 | `ComputerPower::message()` |
| [BR-122](#br-122) | PIN выхода из агента | P2 | `Api/AgentExitPinController.php`, `SettingsController::agentPin()` |
| [BR-123](#br-123) | Техрежим (визит сотрудника) | P2 | `ClubComputerSessions::startStaffVisit()` |
| [BR-124](#br-124) | Регистрация ПК (карточка по MAC) | P2 | `app/Services/ClubComputerRegistrar.php` |
| [BR-125](#br-125) | Ключ установки клуба и enroll | P2 | `ClubInstallationKey.php`, `Api/AgentEnrollController.php` |
| [BR-126](#br-126) | Токен устройства агента | P2 | `ClubComputerSessions::issueAgentToken()`, `AuthenticateAgentDevice.php` |
| [BR-127](#br-127) | Нормализация MAC и телефона | P3 | `app/Support/MacAddress.php`, `app/Support/PhoneNumber.php` |
| [BR-128](#br-128) | Добавление/удаление ПК и зон | P2 | `ClubZoneComputerService.php`, `ClubComputerController.php`, `ClubZoneController.php` |
| [BR-129](#br-129) | Автообновление агента | P3 | `app/Services/AgentUpdate.php` |
| **§13 Подписка SaaS** | | | |
| [BR-130](#br-130) | Активна ли подписка клуба | P2 | `app/Models/ClubSubscription.php` |
| [BR-131](#br-131) | Пробный период при регистрации клуба | P2 | `AuthController::register()` |
| [BR-132](#br-132) | Продление, приостановка, смена тарифа (суперадмин) | P2 | `Superadmin/ClubController.php` |
| [BR-133](#br-133) | Пометка истёкших подписок (`subscriptions:expire`) | P2 | `Console/Commands/ExpireSubscriptions.php` |
| [BR-134](#br-134) | Лимит ПК по тарифу SaaS, тарифы платформы | P2 | `ClubComputerRegistrar.php`, `Superadmin/PlanController.php` |
| **§14 Права, роли, тарифы, журнал** | | | |
| [BR-140](#br-140) | Права панели `ClubPermissions` (алгоритм + полная матрица) | P2 | `app/Support/ClubPermissions.php` |
| [BR-141](#br-141) | Роли клуба `ClubRoles` | P2 | `app/Support/ClubRoles.php` |
| [BR-142](#br-142) | Права приложения сотрудника `StaffPermissions` | P2 | `app/Support/StaffPermissions.php` |
| [BR-143](#br-143) | Кто меняет права и роли | P2 | `PermissionController.php`, `WorkerController.php` |
| [BR-144](#br-144) | Маршрут → право (`RequireClubArea`) | P2 | `app/Http/Middleware/RequireClubArea.php` |
| [BR-145](#br-145) | Чат с клиентами | P3 | `ChatController.php`, `Api/GuestChatController.php` |
| [BR-146](#br-146) | Колокольчик уведомлений | P3 | `app/Services/ClubNotifications.php` |
| [BR-150](#br-150) | Создание/изменение тарифа (единицы, лимиты) | P1 | `TariffController.php`, `StoreTariffRequest.php` |
| [BR-151](#br-151) | Удаление тарифа (мягкое) | P2 | `TariffController::destroy()` |
| [BR-160](#br-160) | Журнал действий как источник данных для правил | P1 | `app/Services/ActivityLogger.php` |
| **§15** | [Найденные странности и возможные баги](#s15) | — | — |

Итого: **90 правил** (BR-001…BR-160, нумерация с пропусками по разделам), из них **P1 — 46**, P2 — 33, P3 — 11.

## 2. Общие правила денег

<a id="br-001"></a>
### BR-001. Хранение сумм, `amountToCents` / `formatCents`, округление PHP — P1

**Код:** `app/Services/AgentTariffPurchaseService.php` — `amountToCents()` (стр. 385–390), `formatCents()` (стр. 392–395);
касты `decimal:2` в моделях `MemberBalance` (стр. 16–19), `Tariff` (стр. 27), `ClubZone` (стр. 22), `ClubMembership`
(стр. 20), `Product`, `ClubService`, `CashShift`, `CashTransaction`, `ShopOrder`.

**Где деньги в БД (Laravel):**

| Таблица.поле | Тип MySQL | Единица |
|---|---|---|
| `member_balances.balance`, `.bonus_balance` | `decimal(12,2)` | тенге |
| `club_memberships.discount` | `decimal(5,2)` | процент |
| `club_memberships.total_spent` | `decimal(12,2)` | тенге |
| `tariffs.price`, `club_zones.price_per_hour`, `products.price`, `club_services.price`, `shop_orders.amount` | `decimal(12,2)` | тенге |
| `cash_shifts.opening_balance/closing_balance`, `cash_transactions.amount` | `decimal(14,2)` | тенге |
| `saas_plans.price_monthly` | `decimal(12,2)` nullable | тенге |
| `session_purchases.main_cents`, `.bonus_cents` | `unsigned bigint` | центы |
| `transfer_codes.amount_cents` | `unsigned bigint` | центы |

**Алгоритм (точно):**
```
amountToCents(s string) int:             // s — строка decimal из БД, например "1234.50"
    [whole, fraction] = split(s, ".", 2); если нет "." → fraction = "00"
    return (int)whole * 100 + (int) str_pad(substr(fraction, 0, 2), 2, "0")
    // (int)"..." в PHP — ведущие цифры строки; "12.999" → 1299 (усечение, не округление)
formatCents(c int) string:
    return sprintf("%d.%02d", intdiv(c, 100), c % 100)
```
- Денежные расчёты сервисов агента (тарифы, минуты, магазин, возвраты, траты) идут в целых центах
  через эти две функции.
- Касса (`CashRegisterService`), пополнения владельцем (`UserController`) и отчёты работают **во float**
  и полагаются на округление MySQL `decimal(…,2)` при записи (см. BR-052…BR-058).
- PHP `round()` (8.3) делает «предокругление» десятичного представления: проверено `php -r`
  (scratchpad): `round(12.345, 2) = 12.35`, `round(100.005*100) = 10001`, `round(1.005*100) = 101`,
  `round(2.675, 2) = 2.68`. Наивный Go `math.Round(x*100)` даст 1234 / 10000 / 100 / 267 — **другие** числа.

**Граничные случаи:**
- Отрицательные строки считаются неверно: `amountToCents("-1.50") = -100 + 50 = -50` (должно быть −150);
  `formatCents(-50) = "0.-50"` (проверено). Сервисы агента не допускают отрицательного баланса, но данные
  с отрицательным балансом возможны (см. BR-002 — `Friends::storedCents` явно рассчитан на «possibly negative»).
- Больше двух знаков после точки — усечение (в БД такого не бывает, `decimal(…,2)`).
- Переполнение: `decimal(12,2)` максимум 9 999 999 999.99; запись больше — ошибка MySQL (strict) и откат транзакции.

**Пример:** `amountToCents("1234.5") = 1234*100 + 50 = 123450`; `formatCents(123450) = "1234.50"`;
`amountToCents("12.999") = 1299`.

**Перенос:**
- [ ] В Go — `int64` центов; парсер decimal-строк и ввода пользователя без float.
- [ ] Для ввода с >2 знаками (кассовые суммы проходят `numeric` без ограничения знаков) — округлять
      «половина от нуля» по **десятичной** записи, как PHP `round()` (см. OQ-BR-01).
- [ ] Повторить ошибку с отрицательными суммами или исправить — решение владельца (OQ-BR-02).
- [ ] Тест: `"1234.5"→123450`, `"0.07"→7`, `"12.999"→1299`, `100.005 тенге → 10001 центов`.

<a id="br-002"></a>
### BR-002. Суммы переводов друзьям: свой парсер — P1

**Код:** `app/Services/Friends.php` — `amountToCents()` (стр. 176–186), `storedCents()` (стр. 189–192), `formatCents()` (стр. 194–197).

```
Friends.amountToCents(s):
    s = replace(trim(s), ",", ".")
    если !preg_match('/^\d{1,10}(\.\d{1,2})?$/', s) → return 0       // минус, пробелы, >2 знаков → 0
    [whole, fraction] = split(s, "."); fraction по умолчанию "00"
    return (int)whole*100 + (int) str_pad(fraction, 2, "0")
Friends.storedCents(s):  return (int) round((float)s * 100)             // баланс из БД, может быть отрицательным
Friends.formatCents(c):  return (c<0 ? "-" : "") + sprintf("%d.%02d", intdiv(|c|,100), |c| % 100)
```
**Пример:** `"1000,5" → 100050`; `"1 000" → 0` (ошибка «Сумма перевода — от 0,01 до 1 000 000.»); `storedCents("-12.34") = -1234`,
`formatCents(-1234) = "-12.34"`.

- [ ] Перенести как отдельный парсер ввода перевода (запятая допустима, пробелы — нет).

<a id="br-003"></a>
### BR-003. Настройки клуба: где читаются и значения по умолчанию — P1

**Код:** `app/Services/CurrentClub.php` — `settings()` (стр. 71–82); `app/Http/Controllers/SettingsController.php` —
`defaultSettings()` (стр. 331–376), `save()` (стр. 208–313), `loadSettings()` (стр. 315–329); `SaveSettingsRequest::rules()`.

- `clubs.settings` — JSON. Сервисы читают **сырой** JSON (`CurrentClub::settings()`), а не слитый с умолчаниями,
  поэтому у каждого сервиса **своё** умолчание (они совпадают с `defaultSettings()`).
- Особый случай: клуб со `slug = 'legacy'` и `settings = null` читает `storage/app/settings.json`.

| Ключ | Тип | По умолчанию | Ограничения при сохранении | Где используется |
|---|---|---|---|---|
| `club.session_idle_timeout` | число (мин) | 15 | 0…10080 | только отображение/API настроек |
| `club.pc_action_after_session_end` | `none\|restart\|shutdown` | `none` | — | BR-039 |
| `club.pc_restart_after_session_end` | число (мин, дробное) | 0.5 | 0…10080 | BR-039 |
| `club.session_auto_terminate_when_pc_unavailable` | число (мин) | 5 | 0…10080 | не используется в логике (только хранится) |
| `club.booking_interval` | число (мин) | 30 | 0…10080 | BR-080 |
| `club.max_cashiers_on_shift`, `club.max_operators_on_shift` | int | 0 (нет лимита) | 0…100 | BR-060 |
| `club.agent_idle_logout_minutes` | число | 5 | 0…1440 | BR-039 |
| `club.agent_shop_enabled` | bool | true | — | BR-075 |
| `club.agent_auto_update` | bool | true | — | BR-129 |
| `finances.currency` | `KZT\|RUB\|$` | `KZT` | — | подписи, `tariffs.currency` при создании |
| `finances.loyalty_enabled` | bool | false | — | BR-011, BR-042 |
| `finances.loyalty_levels` | `[{spent, percent}]` | `[]` | ≤ 20 строк; spent 0.01…999 999 999, distinct; percent 0.01…100 | BR-040 |
| `finances.topup_bonus_enabled` | bool | false | — | BR-041 |
| `finances.topup_bonus_levels` | `[{amount, percent}]` | `[]` | ≤ 20; amount 0.01…999 999 999, distinct; percent 0.01…100 | BR-041 |
| `finances.discount_scope` | `tariffs\|hourly\|all` | `tariffs` | — | BR-010 |
| `finances.bonus_max_percent` | int | 100 | 0…100 | BR-018 |
| `finances.allow_mixed_payment` | bool | false | — | BR-018 |
| `finances.allow_whole_balance` | bool | false | — | BR-022 |
| `finances.auto_billing` | bool | false | — | BR-023 |
| `finances.auto_billing_pause_minutes` | int | 5 | 0…60 | BR-023 |
| `guests.show_qr_code_on_login` | bool | false | — | BR-102 (флаг для агента) |
| `guests.allow_guest_self_transfer` | bool | false | — | BR-035 |
| `guests.allow_adding_friends` | bool | false | — | BR-090 |
| `guests.allow_balance_transfer_to_friends` | bool | false | — | BR-091, BR-092 |
| `guests.guest_mode_enabled` | bool | true | — | BR-038 |

Отдельные поля таблицы `clubs` (не JSON): `timezone` (BR-004), `agent_exit_pin_hash`, `agent_exit_pin_changed_at` (BR-122),
`agent_staff_minutes` (1…240, по умолчанию 15, BR-123), `installation_key*` (BR-125).

**Сохранение (`save()`):** булевы — `$request->boolean()`; «минутные» — `number((float)x)` (целое, если без дробной части);
уровни — `PercentLevels::normalize()`; `bonus_max_percent`, `auto_billing_pause_minutes` — `(int)`; прочее как пришло.
Пустые строки уровней (оба поля null) отбрасываются ещё до валидации (`SaveSettingsRequest::prepareForValidation()`).
Изменения пишутся в журнал `settings.updated` с «было → стало».
Чтение старых строковых значений «минут» (`minutes()`, стр. 416–426): из строки берётся первое число; если в тексте есть «сек» — делится на 60.

- [ ] В Go — типизированная структура настроек с теми же умолчаниями **на чтении** (не только при сохранении).
- [ ] Перенос `legacy`-клуба с `settings.json` — уточнить (OQ-BR-03).

<a id="br-004"></a>
### BR-004. Часовой пояс клуба в правилах — P2

**Код:** `CurrentClub::localTimezone()` (стр. 55–60), `timezone()` (стр. 63–68), `useUtc()`; трейт `UsesClubTimezone`.
- Локальное время клуба (`localTimezone`, даже в API) используется для: недельных периодов тарифов (BR-017),
  разбора времени брони (`ComputerReservationController::store()` — `Carbon::createFromFormat(..., $this->currentClub->timezone())`),
  «локальных дней» дашборда и отчёта дня (BR-061).
- Ответы API агента — в UTC (`AuthenticateAgentDevice` вызывает `useUtc()`).
- Вне клуба (очереди, планировщик) — UTC.
- Смена часового пояса пишет журнал `club.timezone_changed` (`SettingsController::timezone()`).
- [ ] Хранить `timestamptz`; правила дней недели — по `clubs.timezone`.

<a id="br-005"></a>
### BR-005. Блокировки и транзакции денежных операций (сводка) — P1

Все денежные операции выполняются в `DB::transaction` с `SELECT … FOR UPDATE` (`lockForUpdate()`):

| Операция | Что блокируется (порядок) |
|---|---|
| Покупка тарифа | `tariffs` → `club_memberships` (текущего клиента) → `club_computers` → `member_balances` |
| Покупка минут | `club_computers` → `member_balances` |
| Запуск пакета | `time_packages` → `club_computers` → `member_balances` |
| Завершение сессии | `club_computers` → (в `settle`) `member_balances` |
| Пересадка | оба `club_computers` в порядке `id` |
| Гостевая сессия | открытая `cash_shifts` → все выбранные `club_computers` в порядке `id` |
| Продажа POS | открытая `cash_shifts` → каждый `products` по очереди строк чека (порядок — как в запросе!) |
| Пополнение на кассе | открытая `cash_shifts` → `member_balances` |
| Возврат по чеку | открытая `cash_shifts` → `cash_transactions` (приход) → `member_balances` / `products` |
| Заказ из агента | `products` в порядке `id` → `member_balances` |
| Отмена заказа | `shop_orders` → `products` в порядке `id` → `member_balances` |
| Перевод другу | `friendships` → оба `member_balances` в порядке `user_id` |
| Подтверждение кода перевода | последний `transfer_codes` пользователя → далее как перевод |
| Бронь | `club_computers` |
| Склад | `products` |
| Открытие/закрытие смены | `cash_shifts` (+ уникальный индекс `(club_id, open_marker)`) |
| Регистрация ПК | `clubs` (вся транзакция повторяется до 3 раз при deadlock) |

Идемпотентности (ключей) в старом коде нет, кроме интеграционного API v1 (`api.idempotent`).
- [ ] В Go: каждое движение денег — запись ledger с ключом идемпотентности (требование ТЗ), блокировки — тот же порядок.
- [ ] Продажа POS блокирует товары не по `id` — возможен deadlock; в Go блокировать по `id` (не меняет суммы).

## 3. Цены и оплата времени

<a id="br-010"></a>
### BR-010. Область скидки `discount_scope` — P1

**Код:** `app/Services/PaymentRules.php` — `discountScope()` (стр. 17–22), `discountOnTariffs()` (24–27), `discountOnHourly()` (29–32).
```
scope = settings.finances.discount_scope; если не из {tariffs, hourly, all} → "tariffs"
discountOnTariffs = scope != "hourly"      // tariffs, all
discountOnHourly  = scope != "tariffs"     // hourly, all
```
- Скидка на тариф применяется, только если у тарифа `discount_enabled = true` **и** `discountOnTariffs` (BR-013).
- Скидка на поминутное время — для цены часа зоны (BR-014).
- Гостевые сессии с кассы, магазин в агенте и POS — **без скидки**.

**Пример (тест `SessionTimeTest`, «the club chooses where the discount works»):** тариф 1000.00, час зоны 600.00,
скидка 10 %: `tariffs` → 900.00 / 600.00; `hourly` → 1000.00 / 540.00; `all` → 900.00 / 540.00.
- [ ] Перенести с умолчанием `tariffs` для неизвестных значений.

<a id="br-011"></a>
### BR-011. Скидка клиента = max(персональная, накопительная) — P1

**Код:** `app/Services/LoyaltyProgram.php` — `discountFor()` (стр. 39–42), `percentFor()` (33–36), `levels()` (27–30).
```
levels = loyalty_enabled ? PercentLevels.normalize(settings.finances.loyalty_levels, "spent") : []
cumulative = PercentLevels.percentAt(levels, "spent", amountToCents(membership.total_spent ?? "0"))   // float, %
discount   = max((float) membership.discount, cumulative)                                          // float, %
```
- `membership.discount` — персональная скидка `decimal(5,2)` 0…100 (задаётся при создании клиента и в карточке, право `customer_money.update`).
- В приложении сотрудника показывается `max(...)` и флаг `discount_cumulative = cumulative > personal` (`Api/Staff/CustomerController`).
- В ответе входа агента `user.discount` — **только персональная** (`Api/AuthController::userData()`).

**Пример:** персональная 5 %, уровни `[{spent:10000, percent:7}]`, `total_spent = 12000.00` → `cumulative = 7` → скидка 7 %.
- [ ] Скидку хранить в базисных пунктах (`int`), сравнение — как max двух процентов.

<a id="br-012"></a>
### BR-012. Формула скидки `discounted()` — P1

**Код:** `AgentTariffPurchaseService::discounted()` (стр. 371–376).
```
discounted(price int /*центы*/, percent float) int:
    bp = min(10000, max(0, (int) round(percent * 100)))         // базисные пункты
    return max(0, price - intdiv(price * bp + 5000, 10000))     // скидка округляется половина-вверх
```
**Примеры (проверено `php -r`):**
- 100000 центов (1000.00) при 10 %: `bp=1000`, скидка `intdiv(100000000+5000,10000)=10000` → **90000** (900.00).
- 999 центов при 12.5 %: `bp=1250`, `intdiv(999*1250+5000, 10000) = intdiv(1253750,10000) = 125` → **874**.
- 1000 центов при 33.333 %: `bp = round(3333.3) = 3333`, `intdiv(3333000+5000,10000)=333` → **667**.

**Граничные:** процент > 100 → 100 % → цена 0; отрицательный → 0 %.
- [ ] Go: `price - (price*bp+5000)/10000` с int64; проверить переполнение `price*bp` (цена ≤ 10^12 центов × 10^4 < 9.2·10^18 — помещается).

<a id="br-013"></a>
### BR-013. Цена тарифа для клиента — P1

**Код:** `AgentTariffPurchaseService::priceInCents()` (стр. 361–368); в выдаче агенту — `representation()` (стр. 341–359).
```
price = amountToCents(tariff.price)
if tariff.discount_enabled && discountOnTariffs():  price = discounted(price, LoyaltyProgram.discountFor(membership))
```
- Агенту отдаются `price` (со скидкой, строкой `formatCents`) и `original_price` (как в БД).
- Цена **не зависит** от зоны/дня недели: зоны и недельные периоды только ограничивают доступность (BR-016/BR-017);
  отдельной «цены по времени суток» в коде нет.
- [ ] Перенести; цена в ответе — строка с 2 знаками.

<a id="br-014"></a>
### BR-014. Цена часа зоны и поминутная цена — P1

**Код:** `AgentTariffPurchaseService::hourlyPrice()` (стр. 141–150), `minutesPriceInCents()` (стр. 206–209);
`GuestMinutesController::show()` (стр. 20–43).
```
hourlyPrice(user, zone) string:
    price = amountToCents(zone.price_per_hour)
    m = user.currentMembership
    if m == null || !discountOnHourly(): return formatCents(price)
    return formatCents(discounted(price, discountFor(m)))
minutesPriceInCents(pricePerHour string, minutes int) int:
    return intdiv(amountToCents(pricePerHour) * minutes + 30, 60)          // половина вверх
purchaseMinutes: price = minutesPriceInCents(hourlyPrice(user, zone), minutes)
```
- `zone.price_per_hour = null` → поминутно в зоне не продаётся («В этой зоне время поминутно не продаётся.»).
- Диапазон минут в агенте: 1…720 (`GuestMinutesController::MAX_MINUTES = 720`), гостевые сессии — 1…720.
- Агент дублирует формулу на клиенте: `Math.floor((hourCents * minutes + 30) / 60)` (`WindowsAgent/Frontend/main.js` ~стр. 961, 970).

**Пример:** час 600.00, скидка 10 %, область `hourly` → цена часа 540.00 (54000); 7 мин:
`intdiv(54000*7 + 30, 60) = intdiv(378030, 60) = 6300` → **63.00** (точно 6300.5 → 6300).
Час 500.00, 1 мин: `intdiv(50030, 60) = 833` → 8.33.
- [ ] Скидка применяется к цене **часа**, затем поминутная цена считается от уже округлённой цены часа — порядок важен.

<a id="br-015"></a>
### BR-015. Эффективные минуты тарифа (лимит использования) — P1

**Код:** `AgentTariffPurchaseService::effectiveMinutes()` (стр. 378–383); `purchase()` стр. 81–86.
```
effectiveMinutes(t) = (t.usage_limit_enabled && t.usage_limit_minutes != null)
                      ? min(t.duration_minutes, t.usage_limit_minutes) : t.duration_minutes
expires_at части (только тариф с лимитом) = now + usage_limit_minutes
```
- Сессия получает `effectiveMinutes`, а не `duration_minutes`.
- Неиспользованное время тарифа с лимитом сохраняется пакетом до `expires_at` (BR-032).
- Гостевая сессия тоже использует `effectiveMinutes` (`GuestSessions::sell()` стр. 47), но без пакетов.

**Пример:** тариф 300 мин, лимит 120 мин → сессия 120 мин, `expires_at = now + 120 мин`. Тариф 120 мин, лимит 30 дней (43200) →
120 мин, остаток сохраняется до `now + 30 дней` (тест «a tariff with a usage limit keeps its unused time»).
- [ ] Перенести формулу буквально.

<a id="br-016"></a>
### BR-016. Доступность тарифа — P1

**Код:** `AgentTariffPurchaseService::availableFor()` (стр. 34–56), `isAvailable()` (313–329), `purchaseCount()` (331–338);
`Tariff::onSale()` (стр. 41–44), `Tariff::inValidityPeriod()` (47–50); `purchase()` стр. 69–77.
```
isAvailable(t, membership, purchaseCount):
    if !WeeklyPeriods.contains(t.sale_periods, nowLocal) → false            // продажа
    if t.quantity_limit_enabled && t.quantity_limit != null && purchaseCount >= t.quantity_limit → false
    if t.user_roles — непустой массив && membership.role ∉ user_roles → false
    return WeeklyPeriods.contains(t.validity_periods, nowLocal)                // период действия
purchaseCount(t) = COUNT(activity_logs WHERE event='tariff.purchased' AND subject = t)  // за всё время, все клиенты клуба
список агенту: is_active = true; сорт. duration_minutes, name; фильтр по зоне ПК:
    zoneId == null || t.zones пусто || zoneId ∈ t.zones
при покупке дополнительно: если у тарифа есть зоны и зона ПК не в них → «Тариф недоступен для этого компьютера.»
```
- Лимит количества — **глобальный** по тарифу (не на клиента), считается по журналу; гостевые продажи с кассы
  (`computer.guest_session`) **не** считаются и сами лимит не проверяют.
- `user_roles` есть в касте модели, но форма тарифа его не заполняет (не в `Fillable`, нет в `StoreTariffRequest`) — фактически всегда пусто.
- Удалённый (soft delete) тариф не виден (глобальный скоуп SoftDeletes).
- Период действия проверяется **только в момент покупки**; уже купленная сессия идёт дальше за пределы периода.
- [ ] Счётчик продаж в Go — не из журнала, а из ledger/таблицы покупок, но с тем же смыслом (OQ-BR-04).

<a id="br-017"></a>
### BR-017. Недельные периоды `WeeklyPeriods` — P1

**Код:** `app/Support/WeeklyPeriods.php` — `contains()` (стр. 17–39), `isAlways()` (42–46), `minutes()` (48–51).
```
contains(periods, localNow):
    if periods не массив или пуст → true                       // «всегда»
    m = (localNow.isoWeekday - 1) * 1440 + hour*60 + minute    // секунды отброшены; пн=0
    for p in periods:
        sd = DAYS[p.start_day], ed = DAYS[p.end_day]  (mon=0…sun=6); неизвестный день → пропуск периода
        start = sd*1440 + minutes(p.start_time); end = ed*1440 + minutes(p.end_time)
        minutes("HH:MM") = HH*60+MM, иначе 0                   // строго 2+2 цифры
        if start <= end: hit = (m >= start && m <= end)        // конец ВКЛЮЧИТЕЛЬНО (вся минута end)
        else:            hit = (m >= start || m <= end)        // через конец недели
        if hit → true
    return false
isAlways = периодов нет ИЛИ есть период mon 00:00 – sun 23:59
```
**Пример:** период «сб 22:00 – пн 06:00»: start = 5*1440+1320 = 8520, end = 0*1440+360 = 360, start > end.
Вс 03:15 → m = 6*1440+195 = 8835 ≥ 8520 → действует. Пн 06:00:59 → m = 360 ≤ 360 → действует. Пн 06:01 → нет.
- Время берётся в часовом поясе клуба (`localTimezone`), см. BR-004.
- [ ] Перенести с включительной правой границей и без секунд.

<a id="br-018"></a>
### BR-018. Выбор баланса: бонусный лимит, смешанная оплата — P1

**Код:** `AgentTariffPurchaseService::sell()` (стр. 236–311, расчёт оплаты — стр. 238–269); `PaymentRules::bonusMaxPercent()` (35–40),
`bonusLimitCents()` (84–87), `mixedPaymentAllowed()` (67–70).
```
bonusMaxPercent = is_numeric(x) ? clamp((int)x, 0, 100) : 100         // x = finances.bonus_max_percent, умолч. 100
bonusLimit = paymentMethod == "main_balance_only" ? 0 : intdiv(price * bonusMaxPercent, 100)   // вниз
mixed = finances.allow_mixed_payment
main, bonus = центы балансов (под FOR UPDATE)
switch choice:                                    // запрос: balance = main | bonus | (нет — агенты до 0.0.57)
  "bonus":
      if bonusLimit == 0 && price > 0 → ошибка (тариф только с основного / «В клубе бонусами нельзя оплачивать время.»)
      bonusPay = min(bonus, bonusLimit)
      if !mixed && bonusPay < price → ошибка (если bonusLimit < price: «Бонусами можно оплатить только N%…»,
                                              иначе «Бонусов не хватает на всю цену…»)
  "main":  bonusPay = 0
  null && mixed:  bonusPay = min(bonusLimit, max(0, price - main))           // сначала основной, остаток бонусами
  null && !mixed: bonusPay = (main < price && bonusLimit >= price && bonus >= price) ? price : 0
mainPay = price - bonusPay
if main < mainPay || bonus < bonusPay → ошибка «Недостаточно средств для покупки тарифа.»
main -= mainPay; bonus -= bonusPay
```
- Поминутная покупка передаёт `paymentMethod = 'main_or_bonus'`; пакет — `'main_balance_only'` и `choice='main'`, цена 0.
- Значения `balance` вне `{main, bonus}` отклоняются валидацией (422).

**Примеры:**
1. Цена 100000, лимит 30 %, mixed = да, бонусы 50000, основной 80000, `choice=bonus` → `bonusLimit = 30000`,
   `bonusPay = min(50000, 30000) = 30000`, `mainPay = 70000` → остаток 10000 / 20000.
2. Тот же, mixed = нет → `bonusPay = 30000 < 100000` → ошибка «Бонусами можно оплатить только 30% цены…».
3. Старый агент, mixed = нет, лимит 100 %, основной 50000, бонусы 200000, цена 100000 → `bonusPay = 100000`, `mainPay = 0`.
4. Тест «picks the balance» (`SessionTimeTest`): 60 мин по 600.00/ч, бонус-лимит 50 %, mixed = да, балансы 1000/1000, `choice=bonus` →
   цена 60000, `bonusLimit = 30000`, итог 700.00 / 700.00.
- [ ] Перенести все ветки и тексты ошибок (ключ ошибки `balance`).

<a id="br-019"></a>
### BR-019. Что агент предлагает оплатить бонусами — P2

**Код:** `PaymentRules::bonusPercentOffered()` (стр. 76–81); `representation()` стр. 352–355; `GuestMinutesController::show()`.
```
percent = mainBalanceOnly ? 0 : bonusMaxPercent
offered = (percent < 100 && !mixed) ? 0 : percent
bonus_allowed = offered > 0
```
- Чисто подсказка для UI агента; сервер всё равно проверяет в `sell()` (BR-018).
- [ ] Отдавать агенту `bonus_max_percent`, `mixed_payment`, `bonus_allowed`.

<a id="br-020"></a>
### BR-020. Покупка тарифа в агенте — P1

**Код:** `AgentTariffPurchaseService::purchase()` (стр. 62–101), `sell()` (236–311), `ensureComputerFor()` (212–221);
`AgentTariffController::purchase()` (стр. 91–107 в файле), `PurchaseAgentTariffRequest`.

**Алгоритм:**
1. Контроллер: `computer_id` должен быть ПК текущего клуба; если у ПК есть `agent_token_hash`, заголовок `X-Agent-Token`
   должен совпасть (`hash_equals(sha256)`) — иначе 403 `wrong_computer` («Тариф можно купить только на самом компьютере.»).
2. Транзакция: блокировки по BR-005; `isAvailable` (BR-016) с актуальным счётчиком; `ensureComputerFor`:
   `is_reserved` → ошибка; есть оплаченная сессия другого клиента → ошибка.
3. Проверка зоны тарифа; `price = priceInCents()` (BR-013); часть сессии `kind=tariff`, `expires_at` по BR-015.
4. `sell()`: расчёт оплаты (BR-018); затем
   - `hasActive = computer.hasPaidSession()`; `sessionId = hasActive ? current_session_id : uuid()`;
   - `sessionEndsAt = (hasActive ? session_ends_at : now) + minutes` (**продление складывается**);
   - бронь другого клиента в `[now, sessionEndsAt + gap)` → ошибка «ПК забронирован с HH:MM…» (BR-082);
   - свои брони, пересекающиеся с этим интервалом, помечаются `arrived_at = now` (BR-083);
   - `member_balances` ← новые суммы (`formatCents`);
   - `LoyaltyProgram.addSpending(user, mainPay)` (бонусы в траты не идут, BR-042);
   - `session_purchases` ← `{session_id, user_id, kind, tariff_id, name, minutes, main_cents=mainPay, bonus_cents=bonusPay, expires_at}`;
   - `club_computers` ← `current_session_id, current_session_user_id=user, session_guest=false, session_ends_at`.
5. Журнал `tariff.purchased` (subject = тариф): `amount`, `currency`, `main_balance_amount`, `bonus_balance_amount`, `duration_minutes`,
   `computer_id`, `session_id`, `session_ends_at` (ISO). **Этот журнал — источник лимита количества и зон пересадки** (BR-160).
6. Ответ: `new_balance`, `new_bonus`, `minutes_added`, `session_id`, `session_ends_at`.

**Граничные:** покупка на ПК с истёкшей, но ещё не очищенной сессией → `hasPaidSession=false` → новая сессия с `now`.
Покупка поверх гостевой сессии на том же ПК: у гостевой `current_session_user_id = null`, проверка
`hasPaidSession && user_id !== user` срабатывает → ошибка «На компьютере уже идёт сессия другого пользователя.».
Лимит: `throttle:10,1` на маршруте.

**Пример:** баланс 1500.00/0, тариф 60 мин 1000.00, скидка 10 %, scope `tariffs`, `discount_enabled` → цена 90000,
`choice=main` → баланс 600.00, `total_spent += 900.00`, сессия `now+60`.
- [ ] Перенести; продление = к `session_ends_at`, а не к `now`.

<a id="br-021"></a>
### BR-021. Покупка минут (поминутно) — P1

**Код:** `AgentTariffPurchaseService::purchaseMinutes()` (стр. 110–138); `GuestMinutesController::purchase()` (стр. 45–61).
- Валидация: `minutes` 1…720, `balance` ∈ {main, bonus} или нет; проверка `X-Agent-Token` как в BR-020.
- Блокировка ПК; зона ПК; `price_per_hour` null → ошибка; `ensureComputerFor`; `price = minutesPriceInCents(hourlyPrice(...), minutes)`.
- Часть `kind=minutes`, `tariff_id=null`, `name = zone.name`, `expires_at=null`; `paymentMethod='main_or_bonus'`.
- Журнал `time.purchased` (subject = зона): как в BR-020 + `price_per_hour` (строка со скидкой).
- Минуты не зависят от `allow_whole_balance` на сервере — сервер продаёт любые 1…720.
**Пример (тест):** час 600.00, 60 мин бонусами 50 %/mixed → 300.00 + 300.00.
- [ ] Перенести.

<a id="br-022"></a>
### BR-022. «Игра на весь баланс» — P1

**Код:** `PaymentRules::wholeBalanceAllowed()` (стр. 61–64); `GuestMinutesController::show()` поле `whole_balance`;
`WindowsAgent/Frontend/main.js` `affordableMinutes()` (~стр. 972–979), обработчик `minuteWholeBuy` (~стр. 988–999).
- Сервер только сообщает флаг; минуты считает **агент** и покупает обычной `purchaseMinutes(minutes, balance='main')`:
```
hourCents = Math.round(price_per_hour * 100); balanceCents = Math.round(balance * 100)
if !available || hourCents <= 0 || balanceCents <= 0 → 0
minutes = min(max_minutes (720), floor(balanceCents * 60 / hourCents))
while minutes > 0 && floor((hourCents*minutes + 30)/60) > balanceCents: minutes--
```
**Пример (тест «play on the whole balance»):** баланс 1400.00, час 600.00 → `floor(140000*60/60000) = 140` мин, цена
`floor((60000*140+30)/60) = 140000` ≤ 140000 → 140 мин за 1400.00. Тест покупает 200 мин и затем завершает сессию — возврат 1400.00.
- Неиспользованные минуты возвращаются при завершении (BR-032).
- [ ] Новый агент считает так же (или сервер отдаёт готовое число минут — решение по API, OQ-BR-05).

<a id="br-023"></a>
### BR-023. Автобиллинг с паузой — P1

**Код:** `PaymentRules::autoBillingEnabled()` (стр. 47–50), `autoBillingPauseMinutes()` (53–58: `clamp((int)x, 0, 60)`, умолч. 5);
агент `WindowsAgent/Frontend/main.js` (~стр. 1001–1046).
- Сервер только отдаёт `auto_billing`, `auto_billing_pause_minutes`. Логика в агенте (раз в секунду):
  - работает, если клиент вошёл, нет активной сессии, не остановлено вручную, `auto_billing && available`, `affordableMinutes() > 0`;
  - после входа — ждёт паузу `pause*60000 мс` от первого «тика» без сессии, затем покупает `affordableMinutes()` минут с основного баланса;
  - после окончания тарифа (`autoAfterTariff`) — сразу, без паузы;
  - если клиент сам завершил сессию или её завершили в клубе — до следующего входа автобиллинг не стартует (`autoStopped`).
- Неиспользованные минуты возвращаются при завершении (BR-032). Сервер не отличает автопокупку от ручной.
- [ ] В новой архитектуре решить, где живёт автобиллинг: в агенте (как сейчас) или на сервере (OQ-BR-05).

<a id="br-024"></a>
### BR-024. Запуск сохранённого пакета — P1

**Код:** `AgentTariffPurchaseService::startPackage()` (стр. 171–203), `packagesFor()` (153–163); `GuestPackageController`.
```
package = time_packages WHERE user_id = user FOR UPDATE (иначе 404)
minutes = min(package.minutes_left, intdiv(max(0, expires_at - now (сек)), 60))
if minutes <= 0: удалить пакет; ошибка «Срок абонемента истёк.»     // удаление коммитится? нет — исключение откатывает транзакцию
ensureComputerFor; тариф пакета (withTrashed): если есть зоны и зона ПК не в них → ошибка; если тариф есть и вне периода действия → ошибка
sell(price=0, 'main_balance_only', 'main', minutes, part{kind=tariff, tariff_id, name, expires_at=package.expires_at})
удалить пакет (целиком, даже если minutes < minutes_left из-за срока)
журнал time.package_started (subject = ПК)
```
- Список пакетов агенту: `expires_at > now`, по `expires_at` ↑; `expires_at` в UTC ISO.
- Удалённый тариф: пакет работает в зонах и периодах удалённого тарифа (`withTrashed`); если тариф удалён физически (`tariff_id` null) — без ограничений.
**Пример:** пакет 90 мин, до истечения 50 мин 30 с → `min(90, intdiv(3030,60)=50) = 50` мин; пакет удаляется, сессия +50 мин.
- [ ] Перенести; учесть, что исключение откатывает удаление просроченного пакета (OQ-BR-06 / §15).

## 4. Сессии ПК

<a id="br-030"></a>
### BR-030. Модель сессии ПК, «оплаченная сессия» — P1

**Код:** `app/Models/ClubComputer.php` — `hasPaidSession()` (стр. 97–102), `hasStaffVisit()` (53–56), `currentStatus()` (69–86),
`connectionStatus()` (88–94), `HEARTBEAT_TIMEOUT_SECONDS = 90`; `ClubComputerSessions::sessionPayload()` (стр. 263–276).

Сессия хранится **в строке ПК** (`club_computers`): `current_session_id` (uuid строкой), `current_session_user_id`,
`session_guest`, `session_ends_at`. Части оплаты — `session_purchases` по `session_id`.
```
hasPaidSession = current_session_id != null
                 && (current_session_user_id != null || session_guest == true)
                 && session_ends_at != null && session_ends_at > now
hasStaffVisit  = staff_until != null && staff_until > now
connectionStatus = last_seen_at != null && last_seen_at >= now - 90 c ? online : offline
currentStatus: is_reserved || bookedNow → reserved; status='offline' → offline;
               last_seen_at null → (status='available' ? offline : status); иначе online/offline по 90 с
```
- Сервер — единственный источник правды о конце сессии: агент не может её продлить/укоротить (BR-036).
- Агенту отдаётся `session = {id, ends_at (UTC ISO), user_id, guest}` или `null`, а также `server_time` для отсчёта.
- [ ] В Go: сессия — отдельная сущность или те же поля; инвариант `hasPaidSession` тот же.

<a id="br-031"></a>
### BR-031. Досрочное завершение сессии — P1

**Код:** `ClubComputerSessions::end()` (стр. 203–229); контроллеры `ClubComputerController::endSession()` (агент/приложение,
~стр. 413–427), `endSessions()` (карта клуба, ~стр. 356–375); маршруты `api.club-computers.session.end`,
`api_staff computers.end-session` (роли `STAFF_ROLES`), `club-computers.end-sessions` (право `pc_control.delete`).
```
транзакция; ПК FOR UPDATE
if current_session_id == null || current_session_id != sessionId → ошибка «На компьютере нет такой активной сессии.»
if !isStaff && current_session_user_id != actor → 403 «Завершить сессию может только её владелец или сотрудник клуба.»
endsAt = session_ends_at; customer = current_session_user_id
ПК: current_session_id=null, current_session_user_id=null, session_guest=false, session_ends_at=null
settled = customer == null ? {0,0,0} : SessionSettlement.settle(ПК, sessionId, customer, endsAt, actor)   // BR-032
журнал computer.session_ended (subject = ПК): session_id, planned_end (UTC ISO или «—»)
```
- `isStaff`: в API агента — `Gate::allows('register', ClubComputer)` = роль ∈ `STAFF_ROLES` = {owner, admin, manager, operator}
  (кассир **не** считается); на карте клуба — всегда `true` (право проверено маршрутом).
- Гостевая сессия (без клиента) завершается без возврата — деньги гостя в кассе не возвращаются.
- Карта клуба: перебирает выбранные ПК с `current_session_id != null` (до 200), **каждую в своей транзакции** (не атомарно).
- Ответ агенту: `refunded`, `refunded_bonus` (строки), `kept_minutes`.
- [ ] Перенести; завершение нескольких ПК — по одной транзакции на ПК (как сейчас) или атомарно — OQ-BR-07.

<a id="br-032"></a>
### BR-032. Расчёт возврата при завершении (SessionSettlement) — P1

**Код:** `app/Services/SessionSettlement.php` — `settle()` (стр. 26–79).
```
result = {refund_main: 0, refund_bonus: 0, kept_minutes: 0}
parts = session_purchases WHERE session_id = sid ORDER BY id DESC          // от последней покупки к первой
left  = endsAt == null ? 0 : max(0, endsAt.unixSeconds - now.unixSeconds)  // целые секунды (микросекунды now отброшены)
for p in parts:
    seconds = min(left, p.minutes * 60)
    left -= seconds
    if seconds <= 0 || p.user_id != customer: continue                 // чужая часть «съедает» время, но не возвращается
    if p.kind == "minutes":
        total = p.minutes * 60
        result.refund_main  += intdiv(p.main_cents  * seconds, total)   // пропорционально, вниз
        result.refund_bonus += intdiv(p.bonus_cents * seconds, total)
    elif p.expires_at != null && p.expires_at > now && (m = intdiv(seconds, 60)) > 0:
        INSERT time_packages {user_id, tariff_id, name, minutes_left = m, expires_at = p.expires_at}
        result.kept_minutes += m
    // тариф без лимита использования (expires_at = null) или истёкший — не возвращается
DELETE session_purchases WHERE session_id = sid                          // все части, включая чужие
if refund_main + refund_bonus > 0:
    balance = member_balances WHERE user_id = customer FOR UPDATE
    if balance != null: balance += refund_main; bonus_balance += refund_bonus   // через amountToCents/formatCents
    LoyaltyProgram.subtractSpending(customer, refund_main)               // BR-042, бонусы не трогают траты
журналы: time.refunded (если >0; subject = клиент; main_balance_amount, bonus_balance_amount, session_id)
         time.package_kept (если kept > 0; minutes, session_id); автор — actor (клиент или сотрудник)
```
- Остаток секунд < 60 у пакета пропадает (`intdiv(seconds, 60)`); каждая часть — отдельный пакет (не объединяются).
- Запущенный пакет снова пишется частью `kind=tariff` с `expires_at` пакета → при раннем завершении снова сохраняется.
- Возврат в балансы — **без** записи в кассу (деньги внутри клуба).

**Пример 1 (дробный возврат).** Части сессии (id ↑): P1 тариф 60 мин без лимита (main 50000); P2 минуты 60 (main 30000, bonus 30000);
P3 минуты 7 (main 5251, bonus 0). Конец сессии через 300 с: `left = 300`.
P3: `seconds = min(300, 420) = 300` → `refund_main += intdiv(5251*300, 420) = intdiv(1575300, 420) = 3750` (3750.71…); `left = 0`.
P2, P1: `seconds = 0` → пропуск. Итог: **37.50** основного, 0 бонусов (проверено `php -r`).

**Пример 2.** Те же части, осталось 4600 с: P3 → 420 с → 5251; `left = 4180`; P2 → 3600 с → 30000 + 30000; `left = 580`;
P1 — тариф без лимита → ничего. Итог: основной +352.51, бонусы +300.00; `total_spent −= 352.51`.

**Пример 3 (пакет, тест `SessionTimeTest`).** Тариф 120 мин с лимитом 30 дней, через 30 мин завершён: `left = 5400` →
пакет `intdiv(5400, 60) = 90` мин, `expires_at` = покупка + 30 дней, возврат 0.00. Если бы `left = 5430` — тоже 90 мин (30 с теряются).

**Пример 4 (тест).** Тариф без лимита 60 мин + сверху 30 мин поминутно за 300.00, завершено сразу → минуты возвращаются (300.00), тариф — нет.

**Граничные:** `endsAt` в прошлом (сессия уже истекла, но ещё не очищена) → `left = 0`, возврата нет, части удаляются.
Нет `member_balances` у клиента → деньги не начисляются, но траты уменьшаются (см. §15). Сессия без частей (гостевая, старые агенты) — ничего.
Часовые пояса не влияют (секунды Unix).
- [ ] Перенести буквально, порядок `id DESC`, `intdiv` по каждой части отдельно (не по сумме).
- [ ] Возврат — проводка ledger с ключом `session_id`.

<a id="br-033"></a>
### BR-033. Истечение сессий — P1

**Код:** `app/Console/Commands/ExpireComputerSessions.php` (`club-computers:expire-sessions`, каждую минуту, `routes/console.php`);
`ClubComputerSessions::expireAll()` (стр. 189–195), `expireIfDue()` (177–186), вызов в `heartbeat()` стр. 55.
```
expireAll: UPDATE club_computers (все клубы) SET current_session_id=null, current_session_user_id=null, session_guest=false,
           session_ends_at=null, updated_at=now WHERE session_ends_at IS NOT NULL AND session_ends_at <= now
expireIfDue(ПК под блокировкой): если session_ends_at != null && session_ends_at <= now → те же поля = null; return true
```
- **Без возврата и без журнала**: время израсходовано полностью. Части `session_purchases` не удаляются (чистит BR-034).
- В ответе heartbeat `session_expired = true`, и если агент прислал `in_session` — состояние записывается как `idle`.
- Сессия «без оплаты» от старых агентов (`session_ends_at = null`) не истекает этим правилом.
- [ ] Воркер Go раз в минуту + проверка в обработчике статуса агента.

<a id="br-034"></a>
### BR-034. Очистка частей сессий и истёкших пакетов — P1

**Код:** `routes/console.php`, задача `session-purchases:prune` (ежедневно).
```
DELETE session_purchases (все клубы) WHERE created_at < now - 8 дней
       AND session_id NOT IN (SELECT current_session_id FROM club_computers WHERE current_session_id IS NOT NULL)
DELETE time_packages (все клубы) WHERE expires_at < now
```
Также `agent-qr-logins:prune`: `DELETE agent_qr_logins WHERE expires_at < now − 1 день`.
- Сессия, длящаяся > 8 дней, не теряет части (защищена условием NOT IN).
- [ ] Перенести как фоновые задачи.

<a id="br-035"></a>
### BR-035. Пересадка на другой ПК — P1

**Код:** `app/Services/SeatTransfers.php` — `freeSeatsFor()` (24–43), `map()` (51–81), `transfer()` (83–120),
`ensureOwnSession()` (122–127), `allowedZoneIds()` (135–152); `Api/GuestSeatController.php`.
- Включается настройкой `guests.allow_guest_self_transfer` (иначе 403 «Эта возможность отключена в клубе.»).
- Запрос — только с самого ПК (`X-Agent-Token` исходного ПК, как BR-020).
```
ensureOwnSession(from): from.hasPaidSession && from.current_session_user_id == user, иначе ошибка
allowedZoneIds(from):
    last = последний (по id) журнал tariff.purchased|time.purchased с details.session_id == from.current_session_id
    last.event == time.purchased → [last.subject_id]            // поминутное время — только в своей зоне
    тариф не найден или без зон → null (любая зона); иначе зоны тарифа
target свободен: !is_reserved && online(90 c) && !hasPaidSession && !hasStaffVisit
                 && нет чужой брони в [now, from.session_ends_at + gap) (BR-082) && (зоны == null || target.zone ∈ зоны)
transfer: оба ПК FOR UPDATE по id; target != from;
    target.{current_session_id, current_session_user_id=user, session_ends_at} = from.{…}
    from.{current_session_id, current_session_user_id, session_ends_at} = null
    журнал session.transferred (subject = target): from, to, session_id
```
- Время и деньги не меняются: `session_id` и `session_ends_at` переносятся, части оплаты остаются привязаны к `session_id`.
- Карта для выбора: до 200 свободных ПК, до 500 ПК на карте.
- `session_guest` не копируется и на исходном ПК не сбрасывается (см. §15).
- [ ] Перенести; зону последней покупки брать из таблицы покупок, а не из журнала (OQ-BR-04).

<a id="br-036"></a>
### BR-036. Heartbeat агента и состояние ПК — P2

**Код:** `ClubComputerSessions::heartbeat()` (45–65), `needsWrite()` (72–85), `changes()` (93–134), `sessionUpdates()` (143–156);
`ClubComputerController::agentHeartbeat()` (~стр. 301–326), `heartbeat()` (user-token, ~274–292), `customerState()` (~335–354).
- Запись в БД нужна, если: `fresh_boot`; сессия должна была закончиться; `last_seen_at` null или старше 20 с (`SEEN_WRITE_SECONDS`);
  или heartbeat что-то меняет. Иначе ответ без транзакции.
- Поля: `last_seen_at=now`, `status='online'`, `remote_ip`; `agent_state` (если прислан; `in_session` сразу после истечения → `idle`);
  железо (`agent_version, local_ip, cpu_model, cpu_cores, ram_total_mb, gpu_model, os_version`, `hardware_reported_at`);
  `device_name` если не пуст.
- Сессия: при оплаченной сессии агент **ничего не меняет**; `in_session` с другим `session_id` → 422.
  Без оплаченной: `in_session` → `current_session_id = присланный, user_id=null, guest=false, ends_at=null` (старые агенты);
  другое состояние → поля сессии очищаются.
- Состояния клиента (`idle, locked, in_session`) снимают визит сотрудника (`staff_user_id/kind/until = null`).
- MAC: заполняется, только если у карточки MAC пуст и он не занят другой карточкой клуба; другой MAC — игнор + лог warning.
- Старый heartbeat с токеном пользователя: запрещён, если у ПК есть токен устройства (403 `use_device_token`); не-сотрудник
  может передать только `state, session_id, session_ends_at, agent_version`.
- В ответе: `customer` (баланс, бонусы — float; число заявок в друзья; id последнего ответа чата), `club.*`
  (BR-039), `agent_update` (BR-129), команды питания/сообщение (BR-120/121).
- [ ] По ТЗ: heartbeat в память/Valkey, в Postgres — только изменения состояния.

<a id="br-037"></a>
### BR-037. Сброс гостевой сессии при перезагрузке ПК — P1

**Код:** `ClubComputerSessions::resetGuestSession()` (стр. 162–174), вызов при `fresh_boot` (стр. 56–58).
- Если `fresh_boot = true` и на ПК идёт оплаченная **гостевая** сессия → поля сессии очищаются, журнал
  `computer.guest_session_reset` от имени владельца клуба (первый `owner` по id; если владельца нет — без журнала).
- Деньги гостю не возвращаются (оплачено в кассе). Сессии клиентов с аккаунтом перезагрузку переживают.
- [ ] Перенести.

<a id="br-038"></a>
### BR-038. Гостевая сессия с кассы (без аккаунта) — P1

**Код:** `app/Services/GuestSessions.php` — `sell()` (34–96), `priceFor()` (98–113); `GuestSessionController::store()`;
маршруты `club-computers.guest-session` (право `cash.create`, `throttle:30,1`), staff `computers.guest-session`.
- Включено, если `guests.guest_mode_enabled` (умолч. **true**), иначе 403.
- Вход: `computer_ids` 1…50 (ПК клуба, distinct), `tariff_id` **или** `minutes` 1…720, `payment_method ∈ {cash, card, qr}`.
```
транзакция; открытая смена FOR UPDATE (нет → «Сначала откройте смену в кассе.»)
tariff: is_active, onSale, inValidityPeriod (иначе «Тариф недоступен.»)   // лимит количества и роли НЕ проверяются
duration = tariff ? effectiveMinutes(tariff) : minutes
ПК FOR UPDATE по id; все должны найтись
for ПК (по id):
    price = tariff ? amountToCents(tariff.price)   // БЕЗ скидки; зона тарифа должна совпасть
                   : minutesPriceInCents(zone.price_per_hour, duration)   // price_per_hour null → ошибка
    extend = hasPaidSession; if is_reserved || (extend && !session_guest) → «ПК … занят или забронирован.»
    endsAt = (extend ? session_ends_at : now) + duration
    чужая бронь в [now, endsAt + gap) (userId=0 → любая бронь) → ошибка
    ПК: session_id = extend ? старый : uuid; user_id=null; session_guest=true; ends_at = endsAt
    total += price
cash_transactions: income, amount = formatCents(total), description «Гостевая сессия: …, ПК … (наличные|карта|QR-код)»
журналы: cash.income (subject = транзакция, details.computers …) и computer.guest_session (без subject)
```
- **Нет** `session_purchases` → при досрочном завершении возврата нет.
- Способ оплаты хранится только в тексте описания и журнале (нет поля).
**Пример:** 2 ПК, зона 500.00/ч, 90 мин: цена на ПК `intdiv(50000*90+30, 60) = 75000`; итог 1500.00 в смену.
- [ ] Перенести; отметить, что продление гостевой сессии добавляет время к текущему концу.

<a id="br-039"></a>
### BR-039. Действие с ПК после сессии, выход по бездействию — P3

**Код:** `ClubComputerController::afterSessionAction()` (~стр. 511–522); `GuestFeatures::idleLogoutMinutes()`.
```
action = pc_action_after_session_end ∈ {restart, shutdown} ? it : "none"
delay_seconds = is_numeric(m) ? (int) round(max(0, (float)m) * 60) : 30      // m = pc_restart_after_session_end, умолч. 0.5
idle_logout_minutes = is_numeric(x) ? max(0, (int) ceil((float)x)) : 5        // x = agent_idle_logout_minutes; 0 — никогда
```
Выполняет агент. Пример: 0.5 мин → 30 с; 2.3 мин бездействия → `ceil` → 3.
- [ ] Отдавать агенту те же значения.

## 5. Бонусы и лояльность

<a id="br-040"></a>
### BR-040. Уровни «порог → процент» (`PercentLevels`) — P1

**Код:** `app/Support/PercentLevels.php` — `normalize()` (стр. 12–26), `percentAt()` (стр. 29–39).
```
normalize(levels, key):            // key = "spent" (лояльность) | "amount" (бонус за пополнение)
    если не массив → []
    number(v) = r = round((float)v, 2); floor(r) == r ? (int)r : r        // 7.50 → 7.5; 10.00 → 10
    оставить элементы-массивы, где levels[key] и percent — числа
    → [{key: number(level[key]), percent: number(level.percent)}], сортировка по key ↑ (стабильная)
percentAt(levels, key, cents int) float:
    p = 0.0
    for l in levels: if cents >= (int) round(l[key] * 100): p = max(p, (float) l.percent)
    return p
```
- Берётся **максимальный процент среди достигнутых** порогов, а не процент самого высокого порога (важно, если проценты не монотонны).
- `round(…, 2)` — PHP-округление (BR-001): `round(12.345, 2) = 12.35`.
**Пример:** уровни `[{amount: 1000, percent: 5}, {amount: 5000, percent: 7.5}]`: пополнение 5000.00 → 500000 ≥ 500000 → 7.5;
4999.99 → 5. Немонотонные `[{spent:1000, percent:10}, {spent:5000, percent:5}]`, траты 6000 → **10**.
- [ ] Хранить пороги в центах, проценты в базисных пунктах; `percentAt` — max по достигнутым.

<a id="br-041"></a>
### BR-041. Бонус за пополнение — P1

**Код:** `app/Services/TopUpBonus.php` — `enabled()` (17–20), `levels()` (23–26), `for()` (29–35); вызовы:
`CashRegisterService::topUpBalance()` (стр. 114–117), `CashRegisterController::topUpBalance()` (73–90), `Api/Staff/CashController::topUp()`.
```
for(amount float /*тенге из запроса*/):
    cents   = (int) round(amount * 100)
    percent = PercentLevels.percentAt(levels(), "amount", cents)       // levels() = [] если topup_bonus_enabled = false
    bonus   = intdiv(cents * (int) round(percent * 100) + 5000, 10000)  // центы, половина вверх
```
- Только при пополнении **основного** баланса (`balance_type = balance`) через кассу (веб и приложение сотрудника).
  Пополнение бонусного баланса, пополнение владельцем без кассы (BR-058), переводы друзей — без бонуса.
- Бонус начисляется в `bonus_balance` (не кассовый приход), в чеке `receipt_data.top_up.bonus = cents/100` (float).
- До 20 уровней (`MAX_LEVELS`), пороги 0.01…999 999 999, проценты 0.01…100.

**Примеры (проверено `php -r`):** 5000.00 при 7.5 % → `cents = 500000`, `bp = 750`, `intdiv(375000000 + 5000, 10000) = 37500` → **375.00**.
333.33 при 7.5 % → `intdiv(33333*750 + 5000, 10000) = intdiv(25004750, 10000) = 2500` → **25.00** (точно 24.99975).
Ввод 100.005 → `round(10000.4999…) = 10001` (PHP), т.е. порог/бонус считаются от 100.01.
- [ ] Перенести; ввод суммы — десятичная строка, без float.

<a id="br-042"></a>
### BR-042. Накопительные траты (`club_memberships.total_spent`) — P1

**Код:** `LoyaltyProgram::addSpending()` (45–50), `subtractSpending()` (53–63).
```
addSpending(user, cents): if cents > 0: UPDATE club_memberships SET total_spent = total_spent + formatCents(cents)
                                         WHERE user_id = user AND club_id = текущий
subtractSpending(user, cents):
    if cents <= 0: return
    n = UPDATE … SET total_spent = total_spent - amount WHERE user_id = user AND total_spent >= amount
    if n == 0: UPDATE … SET total_spent = 0                     // не ниже нуля
```
- Растут на: оплату тарифа/минут **с основного баланса** (`mainPay`, BR-020/021), заказ в магазине агента (BR-075).
- Уменьшаются на: возврат основного баланса при завершении сессии (BR-032), отмену заказа (BR-077).
- **Не** меняются: оплата бонусами, POS-продажи, пополнения, возврат пополнения на кассе, переводы друзьям, гостевые сессии.
- Траты считаются **всегда**, даже если программа лояльности выключена (влияют только на скидку при включении).
**Пример:** тариф 900.00 с основного + 200.00 бонусами → `total_spent += 900.00`; ранний выход с возвратом 150.00 основного → `−150.00`.
- [ ] Перенести как проводки ledger «траты» с тем же условием «не ниже 0».

## 6. Касса

> Касса работает с float/`decimal` в тенге (не центами). Все операции требуют **открытой смены** текущего клуба;
> в клубе одновременно может быть только одна открытая смена (уникальный индекс `(club_id, open_marker)`, `open_marker = true` у открытой, `null` у закрытой).
> Способ оплаты (`cash/card/qr`) в смене **не разделяется**: всё идёт в один остаток смены.

<a id="br-050"></a>
### BR-050. Открытие смены — P1

**Код:** `CashRegisterService::open()` (стр. 87–98); `OpenCashShiftRequest` (`opening_balance` numeric ≥ 0).
```
транзакция; если есть открытая смена (FOR UPDATE) → «Смена уже открыта.»
INSERT cash_shifts {opened_by, status=open, open_marker=true, opening_balance, opened_at=now}
журнал cash.shift_opened {opening_balance}
```
- [ ] Перенести; гонку двух открытий ловит уникальный индекс.

<a id="br-051"></a>
### BR-051. Итоги и остаток смены — P1

**Код:** `CashRegisterService::totals()` (стр. 269–282), `balance()` (284–287).
```
income  = SUM(amount WHERE type=income)   по транзакциям смены
expense = SUM(amount WHERE type=expense)
balance = (float) opening_balance + income - expense
```
- Возвраты — это `expense` с `refund_of_id` (входят в `expense`).
**Пример:** открыта с 10000.00; продажи 2500.00; пополнение 5000.00; расход 1200.00 → `balance = 10000 + 7500 − 1200 = 16300.00`.
- [ ] В Go — SQL SUM по центам.

<a id="br-052"></a>
### BR-052. Продажа товаров и услуг (POS) — P1

**Код:** `CashRegisterService::sale()` (стр. 19–85); `StorePosSaleRequest` (`items` ≥ 1; строка — `product_id` **или** `service_id`
(distinct, своего клуба, не удалённый / активная услуга), `quantity` int ≥ 1).
```
транзакция; открытая смена FOR UPDATE (нет → «Сначала откройте смену.»)
total = 0.0 (float)
for item:
  service: total += (float)service.price * qty; строка чека {service_id, name, quantity, price(float), total(float)}
  product (FOR UPDATE): if qty > product.quantity → «Недостаточно товара: …»
           total += (float)product.price * qty; строка {product_id, name, quantity, price, total}
           products.quantity -= qty; INSERT stock_movements {type=write_off, quantity, balance_after, note='Продажа POS'}
           журнал stock.written_off; вебхук stock.changed {change: -qty, reason: sale}
INSERT cash_transactions {type=income, amount=total, description='Продажа POS' (маркер, не переводится), receipt_data={items}}
журнал product.purchased {amount, item_count}; вебхук sale.completed {sale_id, cash_shift_id, amount "0.00", items}
```
- Покупатель и способ оплаты **не записываются**. Скидок в POS нет (поле `discount_amount` в отчёте — наследие, всегда 0).
- Сумма — float; MySQL округляет при записи в `decimal(14,2)`. Для цен с 2 знаками и целых количеств float-сумма даёт тот же результат, что сумма в центах.
**Пример:** 2 × «Cola» 650.00 + 1 × услуга «Печать» 50.00 → `total = 1350.00`; остаток Cola −2.
- [ ] Перенести в центах: `Σ price_cents × qty`; в `receipt_data` сохранить числа как сейчас (float в JSON).

<a id="br-053"></a>
### BR-053. Пополнение баланса клиента на кассе — P1

**Код:** `CashRegisterService::topUpBalance()` (стр. 100–143); `StoreBalanceTopUpRequest` (`user_id` — член клуба,
`amount` numeric > 0, ≤ 9 999 999 999.99, `balance_type ∈ {balance, bonus_balance}`, `payment_method ∈ {cash, card, qr}`).
```
транзакция; открытая смена FOR UPDATE; member_balances клиента FOR UPDATE (нет → 404)
UPDATE member_balances SET <balance_type> = <balance_type> + amount             // float → decimal(12,2)
bonus = balance_type == balance ? TopUpBonus.for(amount) : {0, 0}
if bonus.cents > 0: bonus_balance += bonus.cents / 100
INSERT cash_transactions {income, amount, description «Пополнение основной|бонусный баланс: Имя (наличные|карта|QR-код)»,
                          receipt_data = {top_up: {user_id, balance_type, bonus: bonus.cents/100}}}
журналы cash.income (subject = транзакция) и user.balance_topped_up (subject = клиент):
   {amount, balance_type (подпись!), payment_method (подпись), new_balance, [bonus_amount, bonus_percent, new_bonus_balance]}
```
- Пополнение **бонусного** баланса тоже кассовый приход (деньги клиента) — без бонуса.
- Траты лояльности не меняются.
- Отчёт смены относит приход к «Пополнение балансов» по наличию `details.balance_type` в журнале `cash.income` (BR-057).
**Пример:** 5000.00 наличными, уровни `[{amount: 5000, percent: 7.5}]` → основной +5000.00, бонусный +375.00, касса +5000.00.
- [ ] Перенести; переполнение `decimal(12,2)` → ошибка и откат.

<a id="br-054"></a>
### BR-054. Приход / расход кассы — P1

**Код:** `CashRegisterService::transact()` (стр. 229–253); `StoreCashTransactionRequest` (`type ∈ {income, expense}`, `amount` numeric > 0
**без верхней границы**, `description` ≤ 255).
- Расход больше остатка смены (BR-051) → «В кассе недостаточно средств.»
- Журнал `cash.income` («Пополнение кассы: …») или `cash.expense` («Расход из кассы: …») с `amount`.
- Такой приход в отчёте — «Прочий приход» (нет `balance_type`/`computers` в журнале и не `Продажа POS`).
- Возврат «прочего прихода» невозможен (`refundKind() = null`).
- [ ] Перенести; ограничить сумму по `decimal(14,2)` (сейчас — ошибка БД).

<a id="br-055"></a>
### BR-055. Возврат по чеку — P1

**Код:** `CashRegisterService::refund()` (стр. 150–204), `returnToStock()` (207–227); `CashTransaction::refundKind()` (стр. 45–55);
`CashRegisterController::refund()` / `canRefund()` (право `cash.delete`; по умолчанию admin, manager, владелец).
```
refundKind(t): t.type != income → null
               t.description == 'Продажа POS' && receipt_data.items — массив → "sale"
               receipt_data.top_up.user_id задан → "top_up"; иначе null   // гостевая сессия, прочий приход — нельзя
транзакция: открытая смена FOR UPDATE; приход FOR UPDATE
kind == null → «Эту операцию вернуть нельзя.»; приход не из открытой смены → «Вернуть можно только операцию открытой смены.»
уже есть возврат (refund_of_id) → «Эта операция уже возвращена.»; amount > остаток смены → «В кассе недостаточно средств.»
sale:   для каждой строки с product_id (или, для старых чеков, по name): products.quantity += qty;
        stock_movements {receipt, note='Возврат POS'}; журнал stock.received; вебхук stock.changed {reason: refund}
        // удалённый (soft) товар не находится → остаток не возвращается; услуги пропускаются
top_up: field = top_up.balance_type == bonus_balance ? bonus_balance : balance
        balance FOR UPDATE; если нет или balance[field] < amount → «Клиент уже потратил часть пополнения…»
        balance[field] -= amount
        bonusBack = min(top_up.bonus ?? 0, bonus_balance после списания); if > 0: bonus_balance -= bonusBack
        журнал user.balance_refunded (subject = клиент) {amount, receipt, balance_field, new_balance, bonus_taken_back}
INSERT cash_transactions {refund_of_id, type=expense, amount = income.amount, description «Возврат по чеку №ID»}
журнал cash.refund
```
**Пример:** пополнение 5000.00 + бонус 375.00; клиент потратил 300.00 бонусов (осталось 75.00) → основной −5000.00,
бонусный −min(375.00, 75.00) = −75.00; касса −5000.00.
- Траты лояльности не меняются.
- [ ] Перенести; возврат — отдельная проводка, привязанная к исходной (ключ идемпотентности = id прихода).

<a id="br-056"></a>
### BR-056. Закрытие смены и обязательная печать Z-отчёта — P1

**Код:** `CashRegisterService::close()` (стр. 255–267); `CashRegisterController::close()` (135–145), `zReport()` (147–159),
`markZReportPrinted()` (178–190); `Api/Staff/CashController::close()`.
```
веб: открыть Z-отчёт → в сессию кладётся nonce (40 симв.) → браузер после печати шлёт nonce →
     проверка hash_equals, смена открыта → флаг z_report_printed.{id} в сессии + журнал cash.z_report_printed
     закрытие без флага → редирект на Z-отчёт с ошибкой «Сначала распечатайте Z-отчёт…»
close: смена FOR UPDATE; не open → «Смена уже закрыта.»
       closed_by, status=closed, open_marker=null, closing_balance = balance(смены), closed_at = now
       журнал cash.shift_closed {closing_balance}
```
- Приложение сотрудника закрывает смену **без** печати Z-отчёта и сразу возвращает итоги и отчёт.
- Закрыть смену может любой с правом `cash.create` (не только открывший).
- [ ] Перенести; флаг печати хранить не в сессии браузера (решение по UI — OQ-BR-08).

<a id="br-057"></a>
### BR-057. Состав X/Z-отчёта — P1

**Код:** `app/Services/ShiftReport.php` — `for()` (стр. 22–101); `CashRegisterController::xReport()` (128–133, только открытая смена),
`zReport()` (любая смена), `people()` (167–176).
```
income = транзакции смены type=income
kinds:
  «Продажа товаров»     = income WHERE description = 'Продажа POS'
  «Пополнение балансов» = income WHERE id ∈ (activity_logs event=cash.income, subject=CashTransaction, details.balance_type IS NOT NULL)
  «Гостевые сессии»     = income WHERE id ∈ (… details.computers IS NOT NULL)
  для каждого: count, SUM(amount)
  «Прочий приход» (только если count(all) > Σcount): count = all − Σ, amount = round(all_sum − Σsum, 2)
refunds = транзакции смены с refund_of_id: count, SUM(amount)
items: по чекам 'Продажа POS' с receipt_data, кроме возвращённых; группировка по name: quantity += , amount += line.total (float);
       product_id — первый встреченный; stock = текущий остаток товара (withTrashed), у услуг null
       сортировка: amount ↓, затем name ↑ (strcmp)
items_total = round(Σ amount, 2); discount = round(Σ receipt.discount_amount, 2)
staff: по user_id: count, income, expense; сортировка по income ↓; имя или «—»
шапка: кассир (opened_by), закрывший (если другой), оператор = текущий пользователь (формирует отчёт)
```
- Возвращённые пополнения остаются в «Пополнение балансов» (приход не исчезает), а сумма возвратов — отдельной строкой.
- Классификация зависит от журнала действий (BR-160).
**Пример:** смена: 2 продажи (1350.00, 650.00), пополнение 5000.00, гостевая 1500.00, приход «размен» 1000.00, возврат продажи 650.00 →
kinds: товары 2 / 2000.00; пополнения 1 / 5000.00; гостевые 1 / 1500.00; прочий 1 / 1000.00; refunds 1 / 650.00;
items — только из чека 1350.00.
- [ ] Перенести; классификацию в Go сделать полем «вид прихода» в транзакции (OQ-BR-04), отчёт должен совпасть.

<a id="br-058"></a>
### BR-058. Пополнение и списание баланса владельцем без кассы — P1

**Код:** `UserController::update()` (стр. 89–165), `canTopUpWithoutCash()` (33–36: только роль `owner`).
```
top_up_amount: numeric 0.01…10 000 000 (иначе для не-владельца — prohibited); top_up_balance ∈ {balance, bonus_balance};
balance_action ∈ {top_up, write_off} (умолч. top_up)
amount = round((float) top_up_amount, 2)
write_off: balance FOR UPDATE; balance[field] < amount → ошибка «На балансе клиента меньше суммы списания: …»
           balance[field] -= amount; журнал user.balance_written_off {amount, balance_field, balance_type, new_balance, without_cash: true}
top_up:    balance[field] += amount; журнал user.balance_topped_up {…, payment_method: «без кассы (владелец)», without_cash: true}
```
- Нет кассовой операции, нет бонуса за пополнение, траты не меняются.
- Клиент — только не-сотрудник этого клуба (`customer()`); сотрудник другого клуба — можно менять только скидку/баланс.
**Пример:** владелец пополняет основной на 1234.565 → `round(…, 2) = 1234.57`.
- [ ] Перенести как проводку ledger «корректировка владельцем».

<a id="br-059"></a>
### BR-059. Новый клиент: начальный баланс и скидка — P1

**Код:** `UserController::store()` (стр. 56–83), `canSetMoney()` (право `customer_money.update`); `StoreUserRequest`.
- С правом: `discount`, `balance`, `bonus_balance` задаются при создании (без кассы, без бонуса, без журнала суммы — только `user.created`).
- Без права: любое ненулевое значение → 403; иначе 0.
- Создаются `users`, `club_memberships {role=client, discount}`, `member_balances {balance, bonus_balance}`.
- Изменение персональной скидки позже — `UserController::update()` (0…100), журнал `user.updated` «было → стало».
- [ ] Начальный баланс — проводка ledger «начальный остаток».

<a id="br-060"></a>
### BR-060. Лимит кассиров/операторов на смене — P3

**Код:** `app/Services/StaffShifts.php` — `limit()` (18–23), `onShift()` (26–45), `hasPlace()` (48–57); `AuthController::login()` стр. 108–117.
```
limit(role) = (int) settings.club[max_cashiers_on_shift|max_operators_on_shift] > 0 ? значение : null
onShift = distinct user_id из таблицы web-сессий (sessions) с last_activity >= now − session.lifetime мин,
          роль = role в этом клубе, кроме самого пользователя; если драйвер сессий не database → []
hasPlace = role ∉ {cashier, operator} || limit == null || count(onShift) < limit
```
- Проверяется только при входе в **веб-панель**; приложение сотрудника не ограничено.
- [ ] В Go — по активным refresh-сессиям панели (OQ-BR-09).

<a id="br-061"></a>
### BR-061. Дашборд и «отчёт дня» — P2

**Код:** `DashboardController::__invoke()`; `Api/Staff/MeController::today()`.
- 7 локальных дней клуба (сегодня и 6 предыдущих), каждый день → интервал UTC `[начало дня, +1 день)`; суммы income/expense и
  количество транзакций — одним SQL по `cash_transactions` (все смены).
- Кассир с дашборда перенаправляется в кассу, оператор — в список ПК.
- Отчёт дня (owner/admin/manager): плюс ПК (всего/online/занято/резерв), товары с остатком < 5 (до 20), последние 10 транзакций.
- [ ] Перенести; учесть смену часового пояса и переход через полночь.

## 7. Склад

<a id="br-070"></a>
### BR-070. Приход / списание товара — P2

**Код:** `app/Actions/AdjustStockAction.php` — `handle()` (стр. 16–66). Вызывают: склад (`warehouse.stock-movements.store`,
право `warehouse.update`), «Приход товара» (`stock.receive`, право `stock.create`, только `receipt`, qty 1…100000), API сотрудника, API v1.
```
транзакция; товар FOR UPDATE
write_off && qty > quantity → «Нельзя списать больше, чем есть на складе.»
quantity += (receipt ? qty : -qty)
INSERT stock_movements {product_id, user_id, type, quantity, balance_after, note}
журнал stock.received | stock.written_off {quantity, balance_after, note|«—»}; вебхук stock.changed {change ±qty, quantity, reason = type}
```
- Деньги не затрагиваются (себестоимости нет).
- [ ] Перенести.

<a id="br-071"></a>
### BR-071. Создание товара с начальным остатком — P2

**Код:** `app/Actions/CreateProductAction.php`; `StoreProductRequest` (sku уникален в клубе ≤ 100, name ≤ 150, категория клуба,
price 0…9 999 999 999.99, quantity 0…1 000 000).
- `category` (строка) = имя категории; если `quantity > 0` → движение `receipt` «Начальный остаток при создании товара» + журнал + вебхук `stock.changed {reason: initial}`.
- Журнал `product.created`; вебхук `product.created`.
- [ ] Перенести.

<a id="br-072"></a>
### BR-072. Изменение товара — P2

**Код:** `app/Actions/UpdateProductAction.php`; API v1 `ProductController::update()` (`quantity` — prohibited).
- Меняются sku/name/категория/price; остаток — только движениями. Нет изменений → ничего не пишется.
- Журнал `product.updated` с «было → стало», вебхук `product.updated`.
- Новая цена действует для новых продаж; старые чеки хранят цену в `receipt_data`.
- [ ] Перенести.

<a id="br-073"></a>
### BR-073. Удаление товара — P3

**Код:** `ProductDeleteController::__invoke()`; право `warehouse.delete` (по умолчанию только владелец).
- `sku = substr(sku, 0, 200) + '~deleted-' + id` (освобождает SKU), soft delete, журнал `product.deleted {sku, quantity}`.
- Удалённый товар пропадает из кассы, склада и магазина агента; возврат по старому чеку остаток ему не возвращает (BR-055).
- [ ] Перенести.

## 8. Заказы из агента (магазин)

<a id="br-075"></a>
### BR-075. Заказ товаров с ПК — P1

**Код:** `app/Services/AgentShop.php` — `purchase()` (стр. 37–108); `Api/GuestShopController::purchase()`.
- Включено, если `club.agent_shop_enabled` (умолч. true), иначе 403 `shop_disabled`. Только с самого ПК (`X-Agent-Token`).
- Вход: `items` 1…30 строк (`MAX_LINES`), `quantity` 1…50 (`MAX_QUANTITY`); одинаковые `id` **суммируются** (может стать > 50).
```
ksort(quantities); транзакция; товары (не удалённые) FOR UPDATE по id; не все нашлись → «Товар больше не продаётся. Обновите магазин.»
total = Σ amountToCents(price) * qty  (центы); qty > quantity → «Недостаточно товара: …»
balance FOR UPDATE; нет или main < total → «Недостаточно средств на основном счёте.»   // только основной баланс, без скидки
balance.balance = formatCents(main - total); LoyaltyProgram.addSpending(user, total)
для строк: quantity -= qty; stock_movements {write_off, note='Заказ из агента'}; вебхук stock.changed {reason: sale}
журнал shop.purchased (subject = ПК) {amount, items, computer}
INSERT shop_orders {user_id, club_computer_id, computer_name, items, amount, status=new}
```
- Нет требования активной сессии; бонусы не принимаются; скидка не применяется; кассовой операции нет.
**Пример:** 2 × 650.00 + 1 × 400.00 = 170000 центов; основной 2000.00 → 300.00; `total_spent += 1700.00`.
- [ ] Перенести.

<a id="br-076"></a>
### BR-076. Выдача заказа — P2

**Код:** `AgentShop::handOver()` (111–120), `lockNew()` (155–163); право `orders.update`.
- Заказ FOR UPDATE; статус не `new` → «Заказ уже обработан.»; `status=done, handled_by, handled_at`; журнал `shop.order_done`.
- [ ] Перенести.

<a id="br-077"></a>
### BR-077. Отмена заказа — P1

**Код:** `AgentShop::cancel()` (стр. 123–153); право `orders.delete`.
```
заказ FOR UPDATE, только new; строки с product_id и quantity; существующие (не удалённые) товары FOR UPDATE по id:
   quantity += qty; stock_movements {receipt, note='Отмена заказа из агента'}; вебхук stock.changed {reason: receipt}
balance клиента FOR UPDATE: если есть → balance += amountToCents(order.amount)
LoyaltyProgram.subtractSpending(user, amountToCents(order.amount))
status=cancelled, handled_by, handled_at; журнал shop.order_cancelled (subject = клиент) {amount, order_id}
```
- [ ] Перенести; возврат — проводка с ключом `order_id`.

## 9. Брони

<a id="br-080"></a>
### BR-080. Интервал между бронями — P2

**Код:** `ComputerReservations::gapMinutes()` (стр. 23–28).
```
gap = is_numeric(v) ? max(0, (int) round((float) v)) : 30        // v = settings.club.booking_interval, умолч. 30
```
- Используется: зазор между бронями, зазор между сессией и бронью, ПК «удерживается» за `gap` минут до брони.
- [ ] Перенести.

<a id="br-081"></a>
### BR-081. Создание брони — P2

**Код:** `ComputerReservations::create()` (33–76); `ComputerReservationController::store()`, `storeMany()`;
`StoreComputerReservationRequest` (ПК клуба, клиент клуба, `starts_at` формата `Y-m-d\TH:i` во **времени клуба**, длительность 15…1440 мин).
```
startsAt (часовой пояс клуба) < now − 5 мин → «Нельзя забронировать ПК на прошедшее время.»
транзакция; ПК FOR UPDATE
конфликт = ожидающая бронь этого ПК, пересекающая [startsAt − gap, endsAt + gap)   // overlapping: starts_at < to && ends_at > from
          → «ПК уже забронирован с … до … (между бронями нужно N мин).»
ПК hasPaidSession && session_ends_at > startsAt − gap → «На ПК идёт сессия до …»
INSERT computer_reservations {club_computer_id, user_id, customer_name, customer_phone (копия на момент брони), note, starts_at, ends_at, created_by}
журнал reservation.created
```
- Пачкой с карты (`storeMany`): до 50 ПК, «все или ничего» в одной транзакции; ошибки — JSON 422.
- [ ] Перенести.

<a id="br-082"></a>
### BR-082. Бронь блокирует продажу времени — P1

**Код:** `ComputerReservations::blocking()` (93–101), `markArrivedBy()` (104–111), `heldComputerIds()` (118–126);
`ComputerReservation::scopeOverlapping()` (стр. 51–57), `scopePending()` (45–48).
```
blocking(ПК, userId, until) = первая (по starts_at) ожидающая бронь ПК, пересекающая [now, until + gap),
                              у которой user_id IS NULL или user_id != userId
pending = cancelled_at IS NULL AND arrived_at IS NULL
markArrivedBy(ПК, userId, until): UPDATE свои ожидающие брони, пересекающие [now, until + gap) SET arrived_at = now
heldComputerIds = ПК с ожидающими бронями, пересекающими [now, now + gap)    // статус «reserved» на карте
```
- Проверяется при покупке тарифа/минут/пакета (BR-020), гостевой сессии (`userId = 0` → любая бронь, BR-038), пересадке (BR-035).
- Время продаётся, только если сессия **с зазором** заканчивается до чужой брони.
**Пример:** бронь другого клиента 18:00–20:00, gap 30, сейчас 16:00, сессия до 17:20: `until+gap = 17:50 < 18:00` → можно;
сессия до 17:40 → `18:10 > 18:00` → «ПК забронирован с 18:00.»
- [ ] Перенести.

<a id="br-083"></a>
### BR-083. Отмена брони, отметка прихода, состояния — P2

**Код:** `ComputerReservations::cancel()`, `arrive()`, `close()` (78–144); `ComputerReservation::state()` (60–69).
```
state = cancelled_at → cancelled; arrived_at → arrived; ends_at < now → finished; starts_at < now → active; иначе pending
cancel/arrive: бронь FOR UPDATE; state ∉ {pending, active} → «Эта бронь уже закрыта.»; поле = now; журнал reservation.cancelled|arrived
```
- «Пришёл» освобождает ПК для любого клиента, вошедшего на нём.
- [ ] Перенести.

<a id="br-084"></a>
### BR-084. Ручной резерв ПК (`is_reserved`) — P2

**Код:** `ClubComputerController::control()` (стр. 160–175), действие `reserve` (право `pc_control.update`); API v1 `ComputerController::reservation()`.
- Переключатель: если среди выбранных есть хоть один не зарезервированный → всем `is_reserved = true`, иначе всем `false`.
- `is_reserved = true` запрещает продажу времени и пересадку на ПК; журнал `computer.controlled (reserve|unreserve)`, вебхук `computer.reservation_changed`.
- [ ] Перенести.

## 10. Друзья и переводы

<a id="br-090"></a>
### BR-090. Заявки в друзья — P3

**Код:** `app/Services/Friends.php` — `overview()` (21–40), `request()` (43–78), `accept()` (80–90), `remove()` (93–106), `maskPhone()` (159–166);
`Api/GuestFriendController`.
- Включено `guests.allow_adding_friends` (для `store`, `accept`; удалить/смотреть можно всегда).
- Телефон: цифры; 11 цифр с 7/8 в начале → без первой; `+7` + 10 цифр (`/^\+7\d{10}$/`).
- Друг — пользователь с этим телефоном и членством в **текущем** клубе; себя нельзя.
- Транзакция, пара FOR UPDATE: уже друзья → ошибка; своя заявка уже есть → ошибка; встречная заявка → **принимается** (`accepted_at = now`);
  иначе новая заявка. Журналы `friend.requested`, `friend.accepted`, `friend.removed`.
- Список: до 300 записей, телефон друга маскируется `+7 XXX ••• •• YY` (иначе `•••`).
- [ ] Перенести.

<a id="br-091"></a>
### BR-091. Перевод основного баланса другу (агент, с паролем) — P1

**Код:** `Friends::transfer()` (стр. 113–156); `GuestFriendController::transfer()` (61–79); `MAX_TRANSFER_CENTS = 100 000 000`.
```
включено guests.allow_balance_transfer_to_friends (иначе 403); пароль аккаунта обязателен и проверяется (Hash::check)
cents = Friends.amountToCents(amount)  (BR-002); cents <= 0 || cents > 100 000 000 → «Сумма перевода — от 0,01 до 1 000 000.»
транзакция: дружба FOR UPDATE; пользователь не участник или не принята → «Переводить можно только друзьям.»
оба member_balances FOR UPDATE ORDER BY user_id; нет любого → та же ошибка
from = storedCents(from.balance); from < cents → «Недостаточно средств на основном счёте.»
from.balance = formatCents(from − cents); to.balance = formatCents(storedCents(to.balance) + cents)
журнал balance.transferred (автор = отправитель, subject = друг) {amount, to_user_id, friendship_id}
```
- Только основной баланс; бонусы не переводятся; траты лояльности не меняются; кассы нет.
**Пример:** у A 1500.00, перевод «250,5» → 25050 центов → A 1249.50, B +250.50.
- [ ] Перенести; лимит `throttle:10,1`.

<a id="br-092"></a>
### BR-092. Перевод с кодом из Telegram (моб. приложение) — P1

**Код:** `app/Services/FriendTransferCodes.php` — `send()` (230–261), `confirm()` (270–301), `checkedCents()` (304–319); константы
`CODE_MINUTES=5`, `RESEND_SECONDS=60`, `CODES_PER_HOUR=5`, `MAX_ATTEMPTS=5`.
```
send: Telegram Gateway должен быть настроен; checkedCents (сумма, дружба, баланс ≥ суммы — без блокировки)
      удалить коды пользователя старше суток; последний код моложе 60 с → «Новый код можно запросить через N с.» (N = max(1, 60 − прошло))
      кодов за час ≥ 5 → ошибка; code = random_int(100000, 999999); отправка не удалась → ошибка
      INSERT transfer_codes {user_id, friendship_id, amount_cents, code_hash = bcrypt(code), expires_at = now + 5 мин, attempts 0}
confirm: транзакция: последний (по id) неистёкший код пользователя FOR UPDATE; нет или attempts ≥ 5 → «Код неверный или устарел…»
      friendship_id/amount_cents не совпали или код неверный → ВНЕ транзакции attempts += 1, та же ошибка
      иначе Friends.transfer(...) (BR-091) и удалить ВСЕ коды пользователя
```
- Проверяется только самый новый код; старые хранятся для лимита в час.
- `transfer_codes` без `club_id` (лимиты — на аккаунт во всех клубах).
- [ ] Перенести; ограничения `throttle:5,1` (код) и `10,1` (подтверждение).

## 11. Вход, QR, регистрация

<a id="br-100"></a>
### BR-100. Вход клиента в агенте, «один ПК за раз» — P2

**Код:** `Api/AuthController::issueUserLoginResponse()` (стр. 31–140); `app/Services/AgentSignIns.php` — `elsewhere()` (20–33), `ACTIVE_MINUTES = 2`.
```
пользователь по телефону + пароль, иначе 401 (журнал auth.api_login_failed во всех клубах аккаунта, BR-160)
заблокирован → 403 account_blocked
club_key задан → клуб по ключу (BR-125), несовпадение с club_id → 403
membership: в указанном клубе, иначе ПЕРВОЕ членство аккаунта; нет → 403 not_club_member
подписка клуба неактивна → 402
агент + device_name: elsewhere = имя другого токена пользователя (name != 'mobile', != этот device_name, не истёк,
                     last_used_at ≥ now − 2 мин), для ролей owner/admin/manager/operator/cashier — не ограничено
                     → 409 signed_in_elsewhere «Вы уже вошли на другом ПК (…)»
токены с тем же device_name удаляются; новый токен Sanctum: abilities ['club:<id>'], срок 30 дней
журнал auth.api_login {device}
```
- Ответ агенту — `access_token`, `user` (баланс/бонусы float, **персональная** скидка), `club_id/name`.
- [ ] Перенести логику «один ПК» (в Go — по активным сессиям агента, OQ-BR-09).

<a id="br-101"></a>
### BR-101. Регистрация клиента на ПК — P2

**Код:** `Api/AgentRegisterController::__invoke()`.
- Телефон нормализуется (как BR-090); имя 2…255, пароль ≥ 6, `device_name` ≤ 120.
- Телефон уже есть: уже клиент этого клуба → ошибка; заблокирован → ошибка; пароль не тот → ошибка (+ `auth.api_login_failed`, reason `join_wrong_password`);
  верный → аккаунт **присоединяется** к клубу с прежними именем/паролем.
- Создаётся `club_memberships {role=client, discount=0}`, `member_balances {0, 0}`, токен 30 дней (старые токены с этим `device_name` удаляются),
  журнал `user.registered` или `user.joined_club`.
- [ ] Перенести.

<a id="br-102"></a>
### BR-102. QR-вход на ПК — P2

**Код:** `app/Services/QrLogins.php` — `create()` (138–152), `poll()` (159–194), `findOpen()` (197–205), `approve()` (208–233);
`AgentQrLogin::LIFETIME_MINUTES = 5`, `hashCode = sha256`; `Api/GuestQrLoginController` (моб. приложение), веб `pc-login/{code}`.
```
create(ПК): удалить прежние QR этого ПК; code = random(40, [A-Za-z0-9]); хранится sha256; expires_at = now + 5 мин
findOpen(code): по хешу в любом клубе; открыт = consumed_at null && expires_at > now && approved_user_id null
approve(login, user): заблокирован → ошибка; нет членства в клубе QR → ошибка; «один ПК» (BR-100) → ошибка;
      подписка клуба неактивна → ошибка;
      UPDATE … SET approved_user_id, approved_at WHERE id AND approved_user_id IS NULL AND consumed_at IS NULL AND expires_at > now
      0 строк → «QR-код устарел…»
poll(ПК, id): login этого ПК FOR UPDATE; нет/закрыт → expired; не подтверждён → pending;
      подтверждён → consumed_at = now; пользователь заблокирован или не член клуба → expired;
      токены с этим device_name удалить; новый токен 30 дней ['club:<id>']; журнал auth.qr_login → approved + токен (один раз)
```
- В моб. приложении: код другого клуба → 403 `other_club`; устаревший → 410 `qr_expired`.
- Флаг показа QR на экране входа — `guests.show_qr_code_on_login` (только для агента).
- [ ] Перенести (в новом API — через стрим агента).

<a id="br-103"></a>
### BR-103. Восстановление пароля кодом Telegram — P3

**Код:** `app/Services/PasswordRecovery.php`. Константы как в BR-092 (5 мин, 60 с, 5 в час, 5 попыток).
- Неизвестный/заблокированный номер — тот же ответ, что и известный (код не шлётся).
- Верный код: новый пароль, удаляются **все** токены и веб-сессии пользователя, все коды; журнал платформы `user.password_recovered`.
- Неверный код: `attempts += 1`.
- [ ] Перенести.

<a id="br-104"></a>
### BR-104. Вход сотрудника в панель — P2

**Код:** `app/Http/Controllers/AuthController.php` — `login()` (стр. 34–131), `normalizePhone()` (~стр. 200–213).
- Телефон: только `7XXXXXXXXXX` (11 цифр, начинается с 7) → без первой; **`8…` не обрезается** (в отличие от агента).
- Членство: сначала роль сотрудника, затем по id. Клиент может войти, только если цель — `/pc-login/…` (подтверждение QR).
- Лимит смены (BR-060), журнал `auth.login`; ошибки — `auth.login_failed` с причиной.
- Суперадмин без клуба — в кабинет платформы.
- [ ] Перенести; нормализацию телефона унифицировать — OQ-BR-10.

## 12. ПК и агент

<a id="br-120"></a>
### BR-120. Команды питания и Wake-on-LAN — P3

**Код:** `app/Services/ComputerPower.php` — `queue()` (38–57), `take()` (111–133), `wakeTargets()` (141–152), `carriesOut()` (99–108);
`ClubComputerController::control()` (147–207), `agentHeartbeat()` (301–326). Константы: `COMMAND_TTL_SECONDS = 120`, `AGENT_VERSION = 0.0.66`,
`SERVICE_AGENT_VERSION = 0.0.72`, `MESSAGE_AGENT_VERSION = 0.0.73`.
```
действия: restart → status 'restarting', команда 'restart'; power_off → status 'offline', 'shutdown'; service → 'service' (BR-123);
          power_on → wake_requested_at = now для ПК с MAC
take (heartbeat с commands=true): команда отдаётся один раз, если power_command_at ≥ now − 120 с; просроченная просто стирается
wakeTargets(ретранслятор): MAC ПК клуба с wake_requested_at ≥ now − 120 с, offline (last_seen null или старше 90 с), не сам, до 50
outdated = версия агента < минимальной (isOutdated) — в сообщении панели
```
- Журнал `computer.controlled`, вебхук `computer.controlled`. Право `pc_control.update`.
- [ ] Перенести (в Go — команды через стрим, без опроса).

<a id="br-121"></a>
### BR-121. Сообщение на экран ПК — P3

**Код:** `ComputerPower::message()` (65–72), `takeMessage()` (75–96); `ClubComputerController::notify()` (~377–411).
- До 200 ПК, текст ≤ 300 символов (trim); `staff_message`, `staff_message_at = now`; отдаётся агенту один раз, если не старше 300 с.
- Журнал `computer.messaged`. Право `pc_control.update`.
- [ ] Перенести.

<a id="br-122"></a>
### BR-122. PIN выхода из агента — P2

**Код:** `SettingsController::agentPin()` (стр. 58–82, право `workers.update`); `Api/AgentExitPinController::__invoke()`;
лимитер `agent-exit-pin` (`AppServiceProvider`: 5 попыток / 5 мин на ПК, 100 в час на клуб).
```
установка: PIN 4–8 цифр (/^\d{4,8}$/), подтверждение; хранится bcrypt в clubs.agent_exit_pin_hash; пусто → удалить
           agent_exit_pin_changed_at = now; журнал club.agent_pin_changed
проверка (по токену устройства): PIN не задан → 409 pin_not_configured; неверный → 422 invalid_pin
      журнал agent.staff_exit | agent.exit_pin_failed — только если на ПК есть вошедший пользователь клуба (иначе в системный лог)
      верный → startStaffVisit(ПК, сотрудник = вошедший пользователь, если он из STAFF_ROLES, иначе null, 'pin');
               ответ {valid: true, staff_minutes}
```
- [ ] Перенести; хеш — Argon2id по ТЗ (PIN короткий — лимиты обязательны).

<a id="br-123"></a>
### BR-123. Техрежим (визит сотрудника) — P2

**Код:** `ClubComputerSessions::startStaffVisit()` (стр. 236–242), `DEFAULT_STAFF_MINUTES = 15`; `SettingsController::agentStaffTime()` (85–102).
```
minutes = clubs.agent_staff_minutes ?? 15          // 1…240, изменение пишет club.agent_staff_time_changed
ПК: staff_user_id, staff_kind ∈ {pin, signin, panel}, staff_until = now + minutes
```
- Источники: PIN (BR-122), вход сотрудника на ПК с `claim` (выдача токена, журнал `agent.staff_signin`), команда «Техрежим» из панели
  (`kind=panel` + команда `service`, агенту `staff_minutes`).
- Снимается, когда агент присылает клиентское состояние (BR-036). Во время визита ПК не предлагается для пересадки.
- [ ] Перенести.

<a id="br-124"></a>
### BR-124. Регистрация ПК (карточка по MAC) — P2

**Код:** `app/Services/ClubComputerRegistrar.php` — `register()` (21–86), `find()` (97–115), `adoptMac()` (118–125), `defaultZone()` (127–147).
```
find(data): mac = MacAddress.normalize
    mac != null: карточка с этим MAC, иначе (если device_id задан) карточка с LOWER(device_id) и пустым MAC
    mac == null: по LOWER(device_id), иначе по LOWER(device_name)
register: транзакция (до 3 попыток), clubs FOR UPDATE
    найдено → adoptMac (MAC пишется только в пустое поле) → {created: false}
    новый MAC при занятом device_id → device_id = 'MAC-' + MAC без двоеточий
    лимит ПК по тарифу SaaS (BR-134) → «Достигнут лимит компьютеров по тарифу клуба.»
    зона: указанная или «Не назначена» (icon desktop; создаётся при отсутствии, 390×240)
    capacity зоны заполнена → «В зоне достигнут лимит мест.»
    number = max(number в зоне) + 1; INSERT club_computers {device_name, device_id, mac_address, status=available}
    журнал computer.registered
    дубликат ключа (MySQL 1062 / SQLite 19) → повторный find, иначе 409 «Этот device_id уже зарегистрирован.»
```
- Регистрирует сотрудник роли `STAFF_ROLES` (`ClubComputerPolicy::register`) или enroll (BR-125).
- [ ] Перенести.

<a id="br-125"></a>
### BR-125. Ключ установки клуба и enroll — P2

**Код:** `Club::booted()` (стр. 25–45); `app/Services/ClubInstallationKey.php` — `resolve()`; `Api/AgentEnrollController::__invoke()`.
```
при создании клуба: key = random(64); installation_key (зашифрован), installation_key_hash = sha256(key), prefix = key[0:12]
                    изменение этих полей запрещено (LogicException)
resolve(key): клубы с prefix = key[0:12], затем hash_equals(sha256(key))
enroll: club_key (64 симв.), device_id, device_name?, mac?; клуб не найден → 401 invalid_club_key; подписка неактивна → 402
        find по MAC/UUID → нет: register от имени владельца (первый owner) → ошибка лимита → 422 computer_not_added
        есть: adoptMac; выдать новый токен устройства (старый отзывается); системный лог (без журнала клуба)
```
- [ ] В Go заменяется одноразовым кодом регистрации + mTLS (ТЗ), но правило «одна карточка = один MAC» сохраняется.

<a id="br-126"></a>
### BR-126. Токен устройства агента — P2

**Код:** `ClubComputerSessions::issueAgentToken()` (245–251), `findByAgentToken()` (253–260); `AuthenticateAgentDevice::handle()`.
- Токен `zag_` + 48 символов `[A-Za-z0-9]`; хранится `sha256` в `agent_token_hash`; новый отзывает старый; поиск только по формату `^zag_[A-Za-z0-9]{48}$`.
- Заголовок `X-Agent-Device` (если прислан) должен совпасть с `device_id` или `MAC-…` карточки, иначе 401 `device_mismatch`.
- Подписка клуба неактивна → 402. Ротация: `POST /agent/token/rotate`.
- [ ] Заменяется клиентским сертификатом (ТЗ); проверку «тот же ПК» сохранить.

<a id="br-127"></a>
### BR-127. Нормализация MAC и телефона — P3

**Код:** `app/Support/MacAddress.php` — `normalize()` (стр. 240–253), `deviceId()`; `app/Support/PhoneNumber.php` — `normalize()`.
```
MAC: только hex-символы, upper; длина ≠ 12 или < 2 различных символов (000000000000, FFFFFFFFFFFF) → null; иначе AA:BB:CC:DD:EE:FF
deviceId(mac) = "MAC-" + mac без ":"
Phone: цифры; пусто → ""; 11 цифр, начало 7 или 8 → без первой; return "+7" + digits (неверная длина отсеется валидацией)
```
**Пример:** `"aa-bb-cc-dd-ee-ff"` → `AA:BB:CC:DD:EE:FF`; `"8 (700) 123-45-67"` → `+77001234567`.
- [ ] Перенести.

<a id="br-128"></a>
### BR-128. Добавление/удаление ПК и зон — P2

**Код:** `ClubZoneComputerService::add()`; `ClubComputerController::update()` (65–119), `destroy()` (125–145); `ClubZoneController::destroy()` (стр. 151–162).
- Добавление в зону: зона и её ПК FOR UPDATE; `capacity` заполнена → «Достигнута вместимость зоны.»; номер = max + 1; позиция —
  первая свободная ячейка сетки `x = 5 + (i % 6)*47`, `y = 5 + floor(i/6)*42`; имя `PC-NN`.
- Перенос ПК в другую зону: capacity; свободная позиция в сетке шагом 47×42 в пределах `width−66`, `height−166`.
- Удаление ПК: запрещено при оплаченной сессии; брони и QR удаляются каскадом.
- Удаление зоны: **каскадом удаляет её ПК** (FK `cascadeOnDelete`) без проверки сессий и связи «тариф–зона» (см. §15).
- [ ] Перенести.

<a id="br-129"></a>
### BR-129. Автообновление агента — P3

**Код:** `app/Services/AgentUpdate.php` — `isOutdated()` (55–65), `offerFor()` (124–149).
- `isOutdated(v, latest)`: нет latest или v → false; v без `\d+.\d+.\d+` в начале → true; иначе `version_compare(v, latest, '<')`.
- Предложение установщика: клуб разрешил (`club.agent_auto_update`, умолч. true), версия устарела, на ПК нет оплаченной сессии и
  агент не в `idle`/`in_session`; не чаще интервала и не больше `MAX_OFFERS` раз на версию.
- [ ] Перенести (подпись Ed25519 по ТЗ).

## 13. Подписка SaaS

<a id="br-130"></a>
### BR-130. Активна ли подписка клуба — P2

**Код:** `app/Models/ClubSubscription.php` — `isActive()` (стр. 31–35), `isPastPaidPeriod()` (38–41), скоуп `active()` (45–51).
```
isActive = (status == active && (current_period_ends_at == null || current_period_ends_at > now))
        || (status == trialing && trial_ends_at != null && trial_ends_at > now)
```
- Статусы: `trialing`, `active`, `expired`, `suspended` (`canceled_at`).
- Неактивная подписка: панель → редирект на `billing.show` (JSON → 402); агент, вход клиента, enroll, QR-подтверждение → 402/ошибка.
  Исключения: `billing.show`, выход, смена клуба.
- [ ] Перенести.

<a id="br-131"></a>
### BR-131. Пробный период при регистрации клуба — P2

**Код:** `AuthController::register()` (стр. 143–198).
- Создаются пользователь, клуб (`slug = Str::slug(name) или 'club'` + `-` + 8 случайных строчных), членство `owner`, `member_balances` владельца,
  план `trial` (`firstOrCreate`: «Пробный период», без цены, 14 дней), подписка `trialing`, `trial_ends_at = now + (trial_days ?: 14) дней`.
- [ ] Перенести.

<a id="br-132"></a>
### BR-132. Продление, приостановка, смена тарифа (суперадмин) — P2

**Код:** `Superadmin/ClubController::extend()` (~стр. 161–189), `suspend()` (~192–204), `changePlan()` (~206–219).
```
extend(days 1…3650, plan?): подписка FOR UPDATE (или новая с started_at = now)
    from = current_period_ends_at > now ? current_period_ends_at : now
    plan = указанный ?? текущий ?? самый дешёвый активный (price_monthly ↑)
    status = active; current_period_ends_at = from + days; canceled_at = null; журнал платформы superadmin.subscription_extended
suspend: status = suspended, canceled_at = now
changePlan: saas_plan_id
```
- Оплаты онлайн нет («pay-as-agreed»). Пробный период при продлении не учитывается (`from` — от `current_period_ends_at` или now).
**Пример:** оплачено до 10.11, сегодня 30.10, +30 дней → до 10.12; если истекла 20.10 → от сегодня: до 29.11.
- [ ] Перенести.

<a id="br-133"></a>
### BR-133. Пометка истёкших подписок — P2

**Код:** `Console/Commands/ExpireSubscriptions.php` — `handle()` (стр. 20–41); каждые 5 минут, `withoutOverlapping`.
- `status = active AND current_period_ends_at IS NOT NULL AND current_period_ends_at <= now` → `status = expired`, журнал платформы
  `subscription.expired`. Доступ уже закрыт по `isActive()`; команда только меняет статус. Пробные периоды не помечаются.
- [ ] Перенести как воркер.

<a id="br-134"></a>
### BR-134. Лимит ПК по тарифу SaaS, тарифы платформы — P2

**Код:** `ClubComputerRegistrar::register()` стр. 44–47; `Superadmin/PlanController::validated()` (стр. 58–74).
- Лимит: `data_get(plan.features, 'max_computers')` — если число и `COUNT(ПК клуба) ≥ лимит` → отказ регистрации (только регистрация/enroll, не ручное добавление в зону).
- Тарифы платформы: slug (alpha_dash, уникален), name, `price_monthly` 0…99 999 999 или null, `trial_days` 0…365, `features` — **список строк**
  (по строке текста), `is_active`. Тарифы не удаляются, только выключаются.
- Ключа `max_computers` в списке строк не бывает → через UI лимит не задать (см. §15).
- [ ] Решить формат `features` (OQ-BR-11).

## 14. Права, роли, тарифы, журнал

<a id="br-140"></a>
### BR-140. Права панели `ClubPermissions` — P2

**Код:** `app/Support/ClubPermissions.php` — `SECTIONS` (стр. 24–45), `allows()` (48–58), `exists()`, `byRole()` (67–72), `given()` (75–80).
```
allows(m, right): m == null || right не существует → false; m.role == owner → true
                  return given(m, right) ?? byRole(m.role, right)
given: club_memberships.permissions[right] если bool, иначе null (как у роли)
```
**Полная матрица по умолчанию** (владелец — всё; «✓» — роль имеет право; пусто — нет):

| Право | admin | manager | cashier | operator | Смысл |
|---|---|---|---|---|---|
| `cash.view` | ✓ | ✓ | ✓ | | Касса, чеки, X/Z-отчёты |
| `cash.create` | ✓ | ✓ | ✓ | | Смены, продажи, пополнения, приход/расход, тариф гостю |
| `cash.delete` | ✓ | ✓ | | | Возвраты |
| `customers.view` | ✓ | ✓ | ✓ | ✓ | Список клиентов |
| `customers.create` | ✓ | ✓ | ✓ | ✓ | Добавить клиента |
| `customers.update` | ✓ | ✓ | ✓ | ✓ | Изменить клиента, сбросить пароль |
| `customer_money.update` | ✓ | ✓ | | | Персональная скидка и начальный баланс |
| `reservations.view/create/update/delete` | ✓ | ✓ | ✓ | ✓ | Брони (список, создать, приход, отмена) |
| `club_map.view` | ✓ | ✓ | ✓ | ✓ | Карта и список ПК |
| `club_map.create/update/delete` | ✓ | ✓ | | | Зоны и ПК |
| `pc_control.update` | ✓ | ✓ | ✓ | ✓ | Питание, резерв, техрежим, сообщения |
| `pc_control.delete` | ✓ | ✓ | | ✓ | Завершить сессию клиента |
| `tariffs.view/create/update` | ✓ | ✓ | | | Тарифы |
| `tariffs.delete` | | | | | Только владелец |
| `warehouse.view/create/update` | ✓ | ✓ | | | Каталог, товар/категория, списание/оприходование |
| `warehouse.delete` | | | | | Только владелец |
| `services.view/create/update/delete` | ✓ | ✓ | | | Услуги |
| `stock.view`, `stock.create` | ✓ | ✓ | ✓ | ✓ | Остатки, приём товара |
| `orders.view/update/delete` | ✓ | ✓ | ✓ | ✓ | Заказы из агента |
| `chat.view` | ✓ | ✓ | ✓ | ✓ | Читать чат |
| `chat.create` | | | ✓ | ✓ | Отвечать (и помечать прочитанным) |
| `todos.view`, `todos.update` | ✓ | ✓ | ✓ | ✓ | Задачи, отметка |
| `todos.create`, `todos.delete` | ✓ | ✓ | | | |
| `news.view/create/update/delete` | ✓ | ✓ | | | Новости |
| `activity_logs.view` | ✓ | ✓ | | | Журнал |
| `settings.view`, `settings.update` | ✓ | ✓ | | | Настройки клуба |
| `workers.view/create/update/delete` | ✓ | | | | Работники, PIN агента, время техрежима |
| `billing.view` | ✓ | ✓ | | | Подписка |

Права, проверяемые **не** через `ClubPermissions`:
- `settings.permissions*` — роль `owner` или `admin` (`club-role:owner,admin`).
- `settings.integrations*` — `can:manage-integrations` = owner или `club_memberships.can_manage_integrations`.
- Дашборд, уведомления — любая роль сотрудника; завершение сессии в агенте/приложении — `STAFF_ROLES` (без кассира).
- Пополнение/списание без кассы — только `owner` (BR-058); удаление тарифа — UI показывает только владельцу.
- Отчёт дня приложения — `StaffPermissions::REPORTS` (owner, admin, manager).
- [ ] Перенести матрицу и переопределения на сотрудника (JSON `permissions`).

<a id="br-141"></a>
### BR-141. Роли клуба `ClubRoles` — P2

**Код:** `app/Support/ClubRoles.php`.
- Сотрудники: `owner, admin, manager, cashier, operator`; смена (лимит): `cashier, operator`; новый клиент — `client`
  (старые не-сотруднические роли тоже считаются клиентами). Назначаемые: admin, manager, cashier, operator.
- `ClubComputerSessions::STAFF_ROLES = owner, admin, manager, operator` (кассир не «сотрудник ПК»); `ChatMessage::ANSWERING_ROLES = cashier, operator`.
- [ ] Перенести.

<a id="br-142"></a>
### BR-142. Права приложения сотрудника `StaffPermissions` — P2

**Код:** `app/Support/StaffPermissions.php` — `for()`.
```
cash          = allows(cash.view) && allows(cash.create)
guest_session = guest_mode_enabled && allows(cash.create)
end_session   = role ∈ STAFF_ROLES
control       = allows(pc_control.update)
chat_reply    = allows(chat.create)
stock_receive = allows(stock.create); stock_manage = allows(warehouse.update)
reports       = role ∈ {owner, admin, manager}
```
- [ ] Перенести.

<a id="br-143"></a>
### BR-143. Кто меняет права и роли — P2

**Код:** `PermissionController::update()` (46–71), `reset()` (74–81), `mayEdit()` (83–91); `WorkerController::store/update/destroy`,
`assignableRoles()`, `ensureManageable()`.
- Права: владелец — любому сотруднику (кроме владельца); admin — manager/cashier/operator, не себе и не другим admin.
  Сохраняются только отличия от роли; всё как у роли → `permissions = null`. Журнал `permissions.updated` («Раздел: действие» → да/нет).
- Роли: добавить сотрудника по телефону существующего аккаунта (иначе — приглашение); назначать `admin` может только владелец;
  свою роль менять нельзя; admin меняет только владелец; «убрать» = роль `client` (баланс сохраняется). Журналы `worker.*`.
- [ ] Перенести.

<a id="br-144"></a>
### BR-144. Маршрут → право (`RequireClubArea`) — P2

**Код:** `app/Http/Middleware/RequireClubArea.php` — `ROUTES` (стр. 18–70), `handle()` (83–91).
- Каждый маршрут группы `club-area` обязан быть в таблице (иначе 403 — «новая страница сначала получает право»).
- Соответствие (сокращённо): касса — `cash.view` (страницы, чеки, X/Z), `cash.create` (смены, продажа, пополнение, приход/расход, закрытие,
  отметка печати Z, гостевая сессия), `cash.delete` (возврат); клиенты; брони; карта; `pc_control.update` (control, notify);
  `pc_control.delete` (end-sessions); тарифы; склад; услуги; приход товара; заказы; чат; задачи; новости; журнал; настройки;
  работники (+ PIN агента и время техрежима — `workers.update`); подписка.
- [ ] В Go — middleware с той же таблицей; тест «каждый маршрут имеет право».

<a id="br-145"></a>
### BR-145. Чат с клиентами — P3

**Код:** `ChatController` (`reply()` 126–146, `markRead()` 172–175, `conversations()` 178–195); `Api/GuestChatController`; `ChatMessage::MAX_LENGTH = 1000`.
- Клиент пишет из агента (≤ 1000, trim, ПК своего клуба или null). Сотрудник с `chat.create` отвечает, **только если клиент уже писал**.
- Открытие диалога сотрудником с `chat.create` помечает сообщения клиента прочитанными; получение ответов агентом помечает ответы прочитанными.
- Список диалогов — 100 последних по `MAX(id)`; непрочитанные — `from_staff = false AND read_at IS NULL`.
- [ ] Перенести (push вместо опроса).

<a id="br-146"></a>
### BR-146. Колокольчик уведомлений — P3

**Код:** `app/Services/ClubNotifications.php` — `for()` (44–73) и источники.
- Менеджеры (owner/admin/manager): события журнала за 7 дней (`auth.login_failed`, `auth.api_login_failed`, `agent.exit_pin_failed`,
  `computer.controlled`, `computer.registered`, `settings.updated`, `zone.deleted`, `invitation.accepted`; не свои; до 15),
  подписка (за 3 дня до конца; `at = конец − 3 дня`), приглашения, истекающие в течение суток, новая версия агента.
- Все сотрудники: непрочитанный чат, новые заказы. owner/admin/manager/cashier: товары с остатком < 5. owner/admin/manager/operator: ПК не на связи > 90 с (`status != offline`).
- Непрочитано: `at > club_memberships.notifications_seen_at` (или seen null).
- [ ] Перенести.

<a id="br-150"></a>
### BR-150. Создание/изменение тарифа — P1

**Код:** `TariffController::store()` (34–51), `update()` (54–71), `attributes()` (120–133), `splitMinutes()` (161–168); `StoreTariffRequest`.
```
duration_minutes     = duration_value (1…10080) × {minutes:1, hours:60, days:1440}
usage_limit_minutes  = usage_limit_enabled ? usage_limit_value (1…10080) × фактор : null
quantity_limit       = quantity_limit_enabled ? (1…1 000 000) : null
price 0…9 999 999 999.99; payment_method ∈ {main_balance_only, main_or_bonus}; discount_enabled; is_active
validity_periods — обязательны, 1…50; sale_periods — 0…50; зоны — только зоны с ПК, до 100
currency = finances.currency на момент создания (при изменении не меняется)
имя уникально в клубе среди не удалённых
```
- Изменение действует на новые покупки; проданное время и пакеты не меняются. Журнал `tariff.created/updated` (цена, длительность, изменённые поля).
- В форме длительность показывается крупнейшей целой единицей (`splitMinutes`).
**Пример:** «3 часа» → 180 мин; лимит «30 дней» → 43200 мин; максимум «10080 дней» → 14 515 200 мин.
- [ ] Перенести.

<a id="br-151"></a>
### BR-151. Удаление тарифа — P2

**Код:** `TariffController::destroy()` (74–85); право `tariffs.delete` (только владелец).
- Soft delete: снят с продажи везде; история и пакеты работают (`withTrashed`). Журнал `tariff.deleted`.
- [ ] Перенести.

<a id="br-160"></a>
### BR-160. Журнал действий как источник данных для правил — P1

**Код:** `app/Services/ActivityLogger.php` — `record()` (21–60), `recordFailedLogin()` (68–99).
- Каждая запись: `event, description, user_id (обязателен, член клуба — ограничение БД), subject_type/id, details (JSON), ip_address (обязателен), user_agent`;
  при вызове из API v1 в `details.api_key` — префикс ключа.
- Неудачный вход пишется во **все** клубы аккаунта; неизвестный телефон — в системный лог.
- **Журнал читается бизнес-логикой:**
  1. лимит количества продаж тарифа — `COUNT(tariff.purchased)` (BR-016);
  2. зоны пересадки — последний `tariff.purchased`/`time.purchased` по `details.session_id` (BR-035);
  3. классификация приходов в X/Z-отчёте — `cash.income` с `details.balance_type` / `details.computers` (BR-057);
  4. история клиента в агенте (`GuestHistory`) — суммы из `details.*_amount` (строки/float);
  5. уведомления (BR-146).
- [ ] В Go эти данные брать из своих таблиц (ledger, покупки, вид прихода), журнал — только аудит (OQ-BR-04); при переносе данных
      пересчитать счётчики из старого журнала.

<a id="s15"></a>
## 15. Найденные странности и возможные баги старого кода

> Не исправлять при переносе без решения владельца: «повторяем поведение». Номер в скобках — вопрос в
> [`open-questions/business-rules.md`](open-questions/business-rules.md).

### 15.1. Деньги (P1)

1. **Отрицательные суммы ломают `amountToCents`/`formatCents`** (BR-001): `"-1.50" → -50`, `-50 → "0.-50"`. Сервисы агента
   (`sell`, `settle`, `AgentShop`) читают баланс этими функциями; при отрицательном балансе (возможен из старых данных или ручной
   правки) расчёт неверен. `Friends` использует свою корректную версию. (OQ-BR-02)
2. **Нет строки `member_balances` → возврат за время теряется, а траты всё равно уменьшаются** (`SessionSettlement::settle()` стр. 51–61). (OQ-BR-12)
3. **Касса во float** (`CashRegisterService` — `$total = 0.0`, `increment($field, $amount)`): совпадает с центами только при
   ≤ 2 знаках; `amount` прихода/расхода и пополнения валидируется как `numeric` без ограничения знаков — MySQL округляет
   при записи, а бонус считается через PHP `round()` (сейчас согласованно, но хрупко). (OQ-BR-01)
4. **Способ оплаты (наличные/карта/QR) не хранится отдельно и не разделяется в смене**: пополнение картой увеличивает «остаток в кассе»,
   возврат ограничен этим общим остатком; POS-продажа вообще без способа оплаты. (OQ-BR-13)
5. **Приход/расход без верхней границы** — сумма > `decimal(14,2)` даёт ошибку БД, а не валидации.
6. **Лимит количества тарифа считается по журналу действий** и глобально по тарифу (не на клиента); гостевые продажи с кассы
   лимит не проверяют и не увеличивают. (OQ-BR-04)
7. **Удаление зоны каскадом удаляет её ПК** (FK `cascadeOnDelete`) — без проверки идущих сессий и без возврата; тариф, привязанный
   **только** к этой зоне, теряет связь и становится тарифом «без зон», т.е. **продаётся во всех зонах**. (OQ-BR-14)
8. **Возврат POS-продажи удалённого товара** молча не возвращает остаток (товар не находится из-за soft delete).
9. **Возврат пополнения**: если клиент потратил бонус за пополнение, забирается только остаток бонуса (`min`), разница остаётся клиенту.
   Это осознанно по коду, но стоит подтвердить. (OQ-BR-15)
10. **Покупка времени/заказ/пересадка «только с самого ПК»** проверяется лишь если у ПК есть токен устройства
    (`agent_token_hash !== null`); у ПК без токена любой клиент клуба может купить время на нём удалённо
    (`AgentTariffController::purchase()`, `GuestMinutesController::purchase()`, `GuestShopController`, `GuestSeatController`). (OQ-BR-19)
11. **Магазин агента**: одинаковые товары в нескольких строках суммируются после валидации — итоговое количество может превысить
    `MAX_QUANTITY = 50`.
12. **Продажа POS блокирует товары в порядке строк чека** (не по id) — возможен deadlock двух параллельных продаж; на суммы не влияет.
13. **`ShiftReport` суммирует `receipt_data.discount_amount`**, которое нигде не записывается (наследие) — всегда 0.
14. **Приложение сотрудника закрывает смену без печати Z-отчёта**, веб — только после печати (флаг в сессии браузера). (OQ-BR-08)
15. **Владелец пополняет/списывает баланс без кассы** — без бонуса за пополнение и без влияния на траты; пополнение бонусного
    баланса на кассе — кассовый приход без бонуса. Подтвердить, что так и задумано. (OQ-BR-15)

### 15.2. Сессии и ПК (P1/P2)

16. **Пересадка не переносит/не сбрасывает `session_guest`** (`SeatTransfers::transfer()` стр. 105–107). Если на целевом ПК осталась
    истёкшая, ещё не очищенная гостевая сессия (`session_guest = true`, окно до минуты), перенесённая сессия клиента станет «гостевой»:
    агент откроет ПК без входа, а `fresh_boot` (BR-037) **сотрёт её без возврата**. (OQ-BR-16)
17. **Естественное истечение** (BR-033) не пишет журнал и не удаляет `session_purchases` (их чистит суточная задача через 8 дней).
18. **Завершение сессий с карты не атомарно** — каждая в своей транзакции; ошибка на одном ПК оставляет предыдущие завершёнными. (OQ-BR-07)
19. **Кассир с выданным правом `pc_control.delete`** может завершать сессии на карте в панели, но не в приложении сотрудника
    (там проверка по роли `STAFF_ROLES`, без кассира).
20. **Покупка времени во время техрежима** (визит сотрудника) не запрещена — `ensureComputerFor()` проверяет только резерв и чужую сессию.
21. **Запуск просроченного пакета**: `delete()` перед исключением откатывается транзакцией — пакет удалит только суточная очистка.
22. **Настройки `session_idle_timeout` и `session_auto_terminate_when_pc_unavailable`** сохраняются и показываются, но **нигде не используются**
    (ни сервером, ни агентом — поиск по `app`, `routes`, `WindowsAgent`). (OQ-BR-17)
23. **`user_roles` тарифа** учитывается при продаже, но задать его через UI нельзя (нет в форме и `Fillable`).
24. **Период действия тарифа проверяется только при покупке**: купленное время продолжает идти вне периода.

### 15.3. Вход, подписка, прочее (P2/P3)

25. **Вход клиента без `club_key`/`club_id` берёт «первое» членство** (`->first()` без `orderBy`) — порядок зависит от БД. (OQ-BR-18)
26. **Нормализация телефона различается**: панель (`AuthController::normalizePhone`) отрезает только ведущую `7`, агент и `PhoneNumber` — `7` и `8`.
    «8 700 …» в панели не войдёт. (OQ-BR-10)
27. **Лимит ПК по тарифу SaaS (`features.max_computers`) невозможно задать через UI** — `features` сохраняется списком строк. (OQ-BR-11)
28. **Суперадминское продление пробного клуба** начинает отсчёт от «сейчас», остаток пробного периода теряется; `subscriptions:expire`
    не помечает истёкшие пробные (`trialing` остаётся, но `isActive()` уже false).
29. **Лимит кассиров/операторов** проверяется только при входе в веб-панель и только при драйвере сессий `database`; приложение сотрудника не ограничено. (OQ-BR-09)
30. **`AgentSignIns` освобождает кассира от правила «один ПК»**, хотя кассир не входит в `STAFF_ROLES` для ПК — непоследовательно, но безвредно.
31. **Валюта тарифа** фиксируется при создании; смена валюты клуба не меняет старые тарифы (в журнале покупки — валюта тарифа).
32. **`TransferCode` без `club_id`** — коды и лимиты на аккаунт во всех клубах (по коду — безопасно, проверяется дружба текущего клуба).
