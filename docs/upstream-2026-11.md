# Изменения старого Zafiro после 26.10.2026: инвентаризация для переноса

Перенос в zafirogo делался с коммита `zafirogo-base` (`e3c8d25`, 26.10.2026). Этот документ описывает всё, что
изменилось в старом проекте до `origin/main` = `a680bab` (15.11.2026): **41 коммит, 133 файла, +10 007 / −561 строк**.
Составлен по диффу (`git -C ../Zafiro diff zafirogo-base a680bab`), а не по заголовкам коммитов. Пути — от корня
старого проекта, номера строк — по состоянию `a680bab`.

В zafirogo этим документом ничего не изменено: это задание для исполнителей. Состояние zafirogo («уже так» /
«нет», номера строк Go) — по коммиту `ddbbf6f` ветки; на момент составления в рабочей копии другой исполнитель уже
правил `server/internal/billing/pricing`, `billing/service/purchase.go`, `http/legacy/{auth,routes}.go`,
`telegram/alerts.go`, `sections/auth/register.go`, `sections/superadmin/plans.go` (пункты А2, А3, А4, А7 и снятие
проверки подписки с гостевых маршрутов) — перед работой сверить с `git log`.

## 0. Как читать

- Темы: **А** исправления в уже перенесённом · **Б** счета за подписку, скидки, цена за ПК · **В** каталог и профиль
  клуба · **Г** брони из гостевого приложения · **Д** реселлеры · **Е** лендинг · **Ж** агент Windows · **З** Flutter.
- Новые идентификаторы: правила `BR-170…BR-190` (§3), сценарии `GS-529…GS-573` (§4, продолжение
  `docs/golden-scenarios.md`, последний там — GS-528), пробелы покрытия `GAP-037…GAP-058` (§5, последний был GAP-036),
  вопросы `UQ-1…UQ-24` (§7; в `OPEN_QUESTIONS.md` их переносит оркестратор).
- Объём: **S** — до полудня (одна функция/запрос и тест), **M** — 1–2 дня (сервис + API + экран), **L** — 3+ дня
  (схема, сервис, ledger, API, экран, тесты).
- «Уже так» — в zafirogo поведение совпадает с новым старым; нужен только тест-подтверждение.

### Сводка

| Тема | Коммитов | Новых маршрутов | Изменённых маршрутов | Новых таблиц | Таблиц с новыми колонками | Новых экранов | Изменённых экранов | Сценариев (новых / изменённых) | Объём |
|---|---:|---:|---:|---:|---:|---:|---:|---|---|
| А. Исправления | 7 | 0 | 5 | 0 | 0 | 0 | 2 | 7 / 4 | L (А2) + мелкие S |
| Б. Счета, скидки, цена за ПК | 2 (+1 общий с Е) | 7 | 1 | 1 | 1 | 1 | 4 | 10 / 0 | L |
| В. Каталог и профиль клуба | 2 | 10 | 12 | 1 | 2 | 1 | 2 | 6 / 2 | L |
| Г. Брони из приложения | 5 | 7 | 3 | 0 | 1 | 0 | 2 | 14 / 0 | L (самая большая) |
| Д. Реселлеры | 3 | 8 | 1 | 2 | 2 | 2 | 3 | 6 / 0 | L |
| Е. Лендинг | 14 | 5 | 1 | 0 | 0 | 5 | 1 | 2 / 1 | M |
| Ж. Агент Windows | 1 | — | — | — | — | 1 режим окна входа | — | — | M |
| З. Flutter | 6 | — | — | — | — | — | — | — | не переносится |
| Без переноса (`.gitignore`) | 1 | — | — | — | — | — | — | — | — |
| **Итого** | **41** | **37** | **23** | **4** | **5** (17 колонок, без повторов) | **9** | **11** (без повторов) | **45 / 7** | |

Миграций: 8. Переводов: **537 новых ключей** в группе `*` и **7** в группе `agent` на каждый из языков ru/kk/en
(`database/translations/{ru,kk,en}.php`, +544 строки на файл; один ключ заменён: «Весь клуб — в одном окне» →
«Весь клуб - в одном окне»); 5 ключей — с формами множественного числа; плюс 7 ключей × 3 языка в
`WindowsAgent/Frontend/i18n/*.json`. Новых фоновых задач и консольных команд нет. API v1 (`/api/v1`) и вебхуки
не менялись.

### Главные риски

1. **Деньги гостя уходят с баланса заранее (бронь) и возвращаются при отмене** — новый денежный контур вне
   существующих ledger-операций; делёж бонусной части по ПК может дать отрицательную основную часть (GAP-041).
2. **Гость больше не зависит от подписки клуба**, а вход с `club_id` сам делает аккаунт клиентом любого клуба
   с городом — меняет уже перенесённый слой `server/internal/http/legacy`.
3. **Срок тарифа обрезается концом периода действия** — меняет расчёт в уже перенесённых `PurchaseTariff`,
   `StartPackage`, `SellGuestSession` (деньги те же, минуты другие; остаток уходит в пакет).
4. **Цена тарифа платформы теперь «за ПК в месяц»** — смысл `saas_plans.price_monthly` изменился без миграции данных.
5. **Float в деньгах платформы** (счета, процент реселлера, цена со скидкой) — в zafirogo только тиыны.
6. **Каталог клубов и реселлеры — данные всей платформы**, а переход идёт поклубно: пока клубы на двух серверах,
   каталог и кабинет реселлера на одном сервере неполны (UQ-2, UQ-3).

---

## 1. Коммиты → темы

| # | Коммит | Дата | Тема | Что изменено по факту |
|---:|---|---|---|---|
| 1 | `75d1174` | 27.10 | А1 | `Superadmin\ClubController::extend()` — `addDays((int) $data['days'])` (форма шлёт строку) |
| 2 | `b4f08f6` | 29.10 | А2 | `WeeklyPeriods::endOf()`, `Tariff::validityEndsAt()`, обрезка времени в `AgentTariffPurchaseService::sell()/purchase()/startPackage()`, `GuestSessions::sell()` |
| 3 | `4821225` | 07.11 | Б | `club_subscriptions.discount_*`, `ClubSubscription::discountPercent()/monthlyPrice()`, `POST superadmin/clubs/{club}/subscription/discount`, экраны клубов и «Подписка» |
| 4 | `12363e8` | 07.11 | Е | `layouts/landing`, `product/features`, `product/agent`, меню «О продукте» |
| 5 | `e7a57c2` | 07.11 | Е | белый фон лендинга |
| 6 | `bb61012` | 07.11 | Е | тень под шапкой |
| 7 | `13bb979` | 07.11 | Е | меню «Продукты», `product/guest-app`, `product/staff-app`, `partials/phone-*` |
| 8 | `203700e` | 07.11 | Е | `GET /pricing`, `pricing.blade.php` из `saas_plans` |
| 9 | `66099ea` | 08.11 | Е | `public/images/club-map.webp` в герое |
| 10 | `acf1ae1` | 08.11 | Е | фон без розового свечения |
| 11 | `2879e02` | 08.11 | Е | заголовок героя «Весь клуб - в одном окне» (дефис), без легенды карты |
| 12 | `929d227` | 08.11 | Е | заголовки без градиента, новый бейдж героя |
| 13 | `10bd6f2` | 08.11 | Е | «пробный период» и «облачный сервер» в фактах героя |
| 14 | `2b5a9f6` | 08.11 | Е | тёмный блок «Мобильные приложения» вместо сетки возможностей |
| 15 | `29efab1` | 08.11 | А3 | `Superadmin\PlanController::validated()` — `preg_split('/\R/u', …)` |
| 16 | `5c457c1` | 08.11 | Б/Е | цена тарифа — «за ПК в месяц» (`pricing`, «Подписка») |
| 17 | `1d1fa0c` | 08.11 | Е | подпись «за ПК в месяц» отдельной строкой |
| 18 | `16d1685` | 08.11 | Е | убран блок «в любом тарифе» |
| 19 | `e872af0` | 08.11 | А7 | `TelegramAlerts::clubRegistered()`, вызов из `AuthController::register()` |
| 20 | `77ae201` | 08.11 | Б | `subscription_invoices`, `SubscriptionBilling`, `BillingController::store/cancel`, `Superadmin\InvoiceController`, право `billing.pay`, `TelegramAlerts::invoiceOpened()` |
| 21 | `fcd5d41` | 13.11 | А4 | `Api\AuthController` — без `club_id` первым идёт клуб с активной подпиской |
| 22 | `1a0487a` | 13.11 | В | `clubs.city/address`, `Cities`, `GET /api/catalog/clubs`, вступление в клуб при входе, гость без подписки клуба |
| 23 | `f41cb71` | 13.11 | З | Flutter: список клубов города, геолокация, вход в выбранный клуб |
| 24 | `792d822` | 13.11 | В | `clubs.profile`, `club_zones.devices`, `club_photos`, `ClubProfileController`, `ZoneDevices`, `GET /api/catalog/clubs/{club}` |
| 25 | `4c872d5` | 13.11 | З | Flutter: карточки клубов, страница клуба |
| 26 | `8893dde` | 13.11 | Г | `GuestBookings`, оплата брони с баланса, старт времени по входу на ПК, настройки `app_booking_*`, возврат при отмене |
| 27 | `958dd9b` | 13.11 | З | Flutter: экран броней |
| 28 | `0bf5a42` | 13.11 | Г | бронь тарифом: `computer_reservations.tariff_id`, `bookableTariffs/bookedMinutes/purchasesLeft`, лимит покупок |
| 29 | `2f44607` | 13.11 | З | Flutter: по часам или тариф |
| 30 | `4454219` | 13.11 | Г/З | `GET /api/guest/bookings/computers`, тарифы зоны выбранного ПК |
| 31 | `c16f47a` | 13.11 | Г/З | время в текстах — пояс клуба (`ComputerReservations::local()`, текст «ПК забронирован с :time») |
| 32 | `f09ea7c` | 13.11 | З | Flutter: бронь в два шага |
| 33 | `3e16a3e` | 13.11 | Г | несколько ПК одной оплатой, `computer_reservations.code`, `POST /api/agent/booking-code`, `startByCode()` |
| 34 | `70edcbb` | 13.11 | З | Flutter: выбор нескольких ПК, показ кода |
| 35 | `0da191b` | 13.11 | Ж | WindowsAgent: режим «Есть код брони?» окна входа |
| 36 | `ca5c8f0` | 13.11 | Д | `resellers`, `reseller_payouts`, `clubs.reseller_id`, код в настройках клуба, скидка и процент, кабинет реселлера |
| 37 | `2318bc7` | 13.11 | Д | реселлеров создаёт суперадмин; на хосте реселлера только вход |
| 38 | `9c2df5f` | 13.11 | Д | удаление реселлера суперадмином |
| 39 | `1490462` | 13.11 | А5 | `AgentSignIns::NOT_A_PC = ['mobile','mobileclient','Zafiro Staff']` |
| 40 | `ff3c6c5` | 13.11 | А6 | `StockMovement::product()->withTrashed()`, `warehouse.blade.php` `?->name ?? '—'` |
| 41 | `a680bab` | 15.11 | — | `.gitignore` (локальные файлы, `xanexe`) — переносить нечего |

---

## 2. Темы

### А. Исправления и изменённые правила в уже перенесённом

| № | Коммит | Что в старом | Что в zafirogo | Затронуто | Объём |
|---|---|---|---|---|---|
| А1 | `75d1174` | `ClubController.php:177` `addDays((int) $data['days'])`: строка `"10"` из формы давала ошибку | **Уже так**: `days` — целое в OpenAPI (`api/superadmin.yaml`), `sections/superadmin/clubs.go:355–360` | тест GS-379 (изм.): тело с `days` числом; добавить проверку, что строка отклоняется 422, а не 500 | S |
| А2 | `b4f08f6` | Время тарифа кончается с периодом действия (BR-170…BR-173) | **Нет**: `pricing.QuoteTariffPurchase` (`purchase.go:77`), `QuoteStartPackage` (`:182`), `QuoteGuestSession` (`guest.go:52`) периода не обрезают; `StartPackage` удаляет пакет целиком (`DeleteTimePackage`) | `billing/pricing/weekly.go` (+`EndOf`), `tariff.go` (+`ValidityEndsAt`), `purchase.go`, `guest.go`; `billing/service/purchase.go` (`PurchaseTariff`, `StartPackage`, `sell`), `service/guest.go` (`SellGuestSession`); sqlc `db/queries/purchases.sql` (+`UpdateTimePackageMinutes`, +`InsertTimePackage` в покупке); ledger минут (`package_kept`/`package_start`); gRPC `GuestService.PurchaseTariff/StartPackage` — новый текст ошибки | **L** |
| А3 | `29efab1` | `PlanController.php:70` `preg_split('/\R/u')` — без `u` резались кириллические буквы | **Уже так**: `sections/superadmin/plans.go:30` `lineSplitRe` работает по рунам | тест GS-382 (изм.): `"До 20 ПК\r\n\nАгент Windows\nСклад и касса для всех ПК"` → 3 строки | S |
| А4 | `fcd5d41` | `Api/AuthController.php:80–84`: без `club_id` членство выбирается `orderByDesc(есть активная подписка) → orderBy('id')` | **Нет**: `legacy/auth.go:627–633` берёт первое членство по id | `legacy/auth.go` `doLogin`; sqlc `MobileUserClubs` (`db/queries/mobile_api.sql:42`) — добавить признак активной подписки; gRPC `GuestAuthService.Login` не затронут (ПК знает клуб) | S |
| А5 | `1490462` | `AgentSignIns.php:20` — токены приложений (`mobile`, `mobileclient`, `Zafiro Staff`) не считаются «другим ПК» | **Уже так по устройству**: токены ПК — Valkey (`agent/guestapi/tokens.go` `SignedInElsewhere`), токены приложений — отдельная таблица `mobile_api_tokens` | тест GS-081 (изм.): свежий токен приложения не мешает входу на ПК | S |
| А6 | `ff3c6c5` | `StockMovement.php:29–32` `->withTrashed()`; `warehouse.blade.php` `{{ $movement->product?->name ?? '—' }}` | **Уже так**: `WarehouseRecentMovements` (`db/queries/warehouse_screens.sql:29`) соединяет без фильтра `deleted_at` | тест GS-143 (изм.): склад открывается и показывает имя удалённого товара | S |
| А7 | `e872af0` | `TelegramAlerts::clubRegistered()` (`TelegramAlerts.php:93–111`), вызов после ответа: `AuthController.php:208` `app()->terminating(...)`; текст на `superadmin/telegram` | **Нет** | `telegram/alerts.go` (+`ClubRegistered` через `SendKey`), `sections/auth/register.go:76` (после фиксации транзакции — через outbox, не в запросе), `web/src/features/superadmin/TelegramPage.tsx` (+абзац «Ещё бот сообщает о каждом новом клубе…») | S |
| А8 | `ca5c8f0` | `Monitoring/Certificates.php:15` — в список хостов по умолчанию добавлен `app.reseller_domain` | **Частично**: `sections/superadmin/dashboard.go:326` читает `Superadmin.SSLHosts` | `config/config.go` — хост реселлера в умолчании списка | S |
| А9 | `77ae201` | `ClubPermissions.php:44–45`: `'billing' => ['view' => MANAGERS, 'pay' => []]` — платить может владелец, остальным — только выданным правом; подпись «Выставить и отменить счёт» | **Нет** | `auth/permissions/permissions.go:59,201`, `routes.go:61` (+`billing.pay` → `billing.invoices.store`, `billing.invoices.cancel`), экран прав `web/src/routes/_admin/settings/permissions` | S (вместе с Б) |
| А10 | `8893dde`, `0bf5a42` | `GuestHistory.php:20,23,27,81–93`: события `reservation.paid` (kind `purchase`), `reservation.refunded` (kind `refund`) в «своих», «полученных» и фильтре `purchases` | **Нет** | `legacy/guest.go:159–162,282,377,422`; `agent/guestapi/history.go:24–28,132,216,234` | S (вместе с Г) |
| А11 | `8893dde` | `ComputerReservations::markArrivedBy()` (`:127–135`) закрывает только **неоплаченные** брони (`->unpaid()`) | **Нет**: `MarkReservationsArrived` (`purchases.sql:114`) закрывает любые свои | sqlc + тест GS-546 | S (вместе с Г) |
| А12 | `c16f47a` | Время брони в ошибке покупки — в поясе клуба (`AgentTariffPurchaseService.php:307`) | **Уже так** (решение SV-9) | тест GS-549 | — |
| А13 | `0bf5a42` | `purchaseCounts()` (`AgentTariffPurchaseService.php:409–430`): лимит количества = покупки в агенте + неотменённые брони этим тарифом | **Нет**: `tariff_sales` (Q-007) броней не знает | `billing/service/purchase.go` (чтение лимита), sqlc (+счётчик броней), `pricing.Tariff.Availability` | S (вместе с Г) |

Маршруты А: новых нет; меняется поведение `POST /api/auth/login`, `POST /api/tariffs/{tariff}/purchase`,
`POST /api/guest/packages/{package}/start`, `POST /club-computers/guest-session` (+`/api/staff/computers/guest-session`),
`POST /register`. Таблицы: нет. Настройки: нет. Задачи: нет.

### Б. Счета за подписку (Kaspi), скидки клубам, цена «за ПК в месяц»

Клуб сам выставляет себе счёт (тариф × число ПК × месяцы − скидка), платит по ссылке Kaspi Pay с номером счёта
в комментарии; суперадмин, увидев деньги, отмечает счёт оплаченным — подписка продлевается. Онлайн-эквайринга и
вебхуков банка нет.

**Маршруты**

| Метод, путь | Имя | Контроллер@метод | Middleware / лимит | Право |
|---|---|---|---|---|
| `POST /billing/invoices` | `billing.invoices.store` | `BillingController@store` | `auth`, `current-club`, `club-area`, `throttle:10,1`; **без** `subscription` (`RequireActiveSubscription.php:14`) | `billing.pay` |
| `POST /billing/invoices/{invoice}/cancel` | `billing.invoices.cancel` | `BillingController@cancel` | то же, без throttle; чужой счёт — 404 (скоуп клуба) | `billing.pay` |
| `GET /billing` (изм.) | `billing.show` | `BillingController@show` | как было | `billing.view` |
| `POST /superadmin/clubs/{club}/subscription/discount` | `superadmin.clubs.discount` | `Superadmin\ClubController@discount` | `superadmin` | суперадмин |
| `GET /superadmin/invoices` | `superadmin.invoices.index` | `Superadmin\InvoiceController@index` | `superadmin` | — |
| `POST /superadmin/invoices/kaspi` | `superadmin.invoices.kaspi` | `@kaspi` | `superadmin` | — |
| `POST /superadmin/invoices/{invoice}/pay` | `superadmin.invoices.pay` | `@pay` | `superadmin`, `whereNumber` | — |
| `POST /superadmin/invoices/{invoice}/cancel` | `superadmin.invoices.cancel` | `@cancel` | `superadmin`, `whereNumber` | — |

Валидация: `saas_plan_id` ∈ активные тарифы с ценой (`payablePlans()`), `months` ∈ `{1,3,6,12}`
(`SubscriptionInvoice::MONTHS`); Kaspi не настроен → ошибка `invoice` «Оплата пока не настроена…»; скидка:
`discount_percent` целое 0…100, `discount_until` дата ≥ сегодня, `discount_note` ≤ 200; Kaspi: `url` `https`, ≤ 500,
`recipient` ≤ 200, пустой `url` выключает оплату.

**Таблицы**

| Таблица | Колонка | Тип MySQL | В Postgres | Примечание |
|---|---|---|---|---|
| `club_subscriptions` | `discount_percent` | `unsignedTinyInteger` default 0 | `smallint NOT NULL DEFAULT 0 CHECK (0..100)` | |
| | `discount_until` | `date` null | `date` | последний день действия включительно |
| | `discount_note` | `string(200)` null | `varchar(200)` | |
| `subscription_invoices` (новая) | `id` | bigint | bigint identity | номер счёта `ZF-` + id с нулями до 5 знаков (`SubscriptionInvoice.php:35`) |
| | `club_id` | FK cascade | FK, RLS | тенантная: индекс `(club_id, created_at)` |
| | `saas_plan_id` | FK | FK | |
| | `months` | `unsignedTinyInteger` | `smallint CHECK IN (1,3,6,12)` | |
| | `computers` | `unsignedSmallInteger` | `integer CHECK (>= 1)` | снимок числа ПК |
| | `price_per_computer` | `decimal(12,2)` | `bigint` тиыны (×100) | снимок цены тарифа |
| | `discount_percent` | `unsignedTinyInteger` | `smallint` | снимок скидки |
| | `amount` | `decimal(12,2)` | `bigint` тиыны (×100) | всегда целые тенге (BR-180) |
| | `reseller_id`, `reseller_percent decimal(5,2)`, `reseller_amount decimal(12,2)` | (миграция реселлеров) | FK `ON DELETE SET NULL`; `integer` базисные пункты (×100); `bigint` тиыны | тема Д |
| | `status` | `string(16)` default `pending` | `text CHECK IN ('pending','paid','canceled')` | + **частичный уникальный индекс `(club_id) WHERE status = 'pending'`** (GAP-046) |
| | `created_by`, `confirmed_by` | FK users nullOnDelete | то же | |
| | `paid_at`, `period_ends_at` | timestamp null | `timestamptz` | |
| | индексы | `(club_id, created_at)`, `(status, created_at)`, `(reseller_id, status, paid_at)` | те же | |
| `platform_settings` | ключ `billing_kaspi` = `{url, recipient}` | JSON | как прочие настройки платформы | не секрет |

Миграции: `2026_11_07_100000_add_discount_to_club_subscriptions_table.php`,
`2026_11_08_100000_create_subscription_invoices_table.php`.

**Экраны**

| Blade | Что изменилось | В `web/src` |
|---|---|---|
| `billing/index.blade.php` (переписан, 22 → 117 строк) | `max-w-xl` → `max-w-3xl`; строка «Стоимость в месяц»: «`N ₸ за ПК` · `M ₸ за K ПК`» + «со скидкой P% [до дата]»; жёлтая карточка неоплаченного счёта (`border-amber-300 bg-amber-50`, сумма, состав, 3 шага оплаты, получатель, кнопка «Оплатить в Kaspi» `bg-[#f14635]`, «Отменить счёт»); форма «Продлить подписку» (`select` тарифа, радио срока 1/3/6/12, «ПК в клубе: N…», «К оплате», кнопка «Выставить счёт»); таблица «Счета» (12 последних); сообщения «Продлить подписку может владелец клуба.» и «Оплата пока не настроена…»; inline-скрипт пересчёта суммы | есть `features/billing/BillingPage.tsx` — **переписать**; API `api/billing.yaml` `getBilling` расширить, + `createInvoice`, `cancelInvoice` |
| `superadmin/invoices/index.blade.php` (новый, 66 строк) | «Оплаты»: форма Kaspi (ссылка, получатель), список ожидающих счетов с «Оплачен»/«Отменить», таблица закрытых (пагинация 30) | **нет** — новый маршрут `web/src/routes/superadmin/invoices.tsx` |
| `superadmin/clubs/index.blade.php` | бейдж «−P%» (`rounded-full bg-violet-100 … text-violet-800`, `title` = причина) рядом со статусом подписки | есть `ClubsPage.tsx` |
| `superadmin/clubs/show.blade.php` | «к оплате N ₸ в месяц (без скидки M ₸)»; форма «Индивидуальная скидка» (процент, «Действует до», «Причина», «0% — снять скидку.», подсказка «Скидка P% закончилась дата.») | есть `ClubPage.tsx` |
| `partials/superadmin-sidebar.blade.php` | пункты «Реселлеры» (`fa-handshake`), «Оплаты» (`fa-receipt`) после «Тарифы платформы» | есть `SuperadminLayout.tsx` |
| `pricing.blade.php` | см. тему Е; цена — «N ₸» + отдельной строкой «за ПК в месяц» | нет |

**Настройки**: платформа — `billing_kaspi` (экран «Оплаты»). Клуб — нет.

**События и уведомления**

| Журнал | Событие | Где |
|---|---|---|
| клуб (`activity_logs`) | `billing.invoice_opened` «Выставлен счёт :number на :amount ₸» (subject — счёт) | `BillingController.php:54` |
| клуб | `billing.invoice_canceled` «Отменён счёт :number» | `BillingController.php:65` |
| платформа (`platform_logs`) | `superadmin.invoice_paid` «Счёт :number оплачен: подписка клуба :club продлена до :date», детали `{amount (float), months}` | `SubscriptionBilling.php:119–123` |
| платформа | `superadmin.invoice_canceled`, `superadmin.billing_kaspi_changed`, `superadmin.subscription_discount_changed` `{percent, until}` | `InvoiceController.php:41,54`, `ClubController.php:243` |
| Telegram (чат алертов) | «💳 Счёт :number: :club / :plan, :months мес., :computers ПК / К оплате :amount ₸ / ссылка на «Оплаты»» — после ответа (`app()->terminating`) | `TelegramAlerts.php:114–132`, `BillingController.php:57` |

В `ActivityLog::EVENTS` новые события счетов **не добавлены** (подписи в журнале клуба нет — GAP-052).

**Права**: `billing.pay` (см. А9). Суперадминские маршруты — только суперадмин.

**Затронуто в zafirogo**: `db/migrations` (новая), `db/queries/billing.sql`, `superadmin.sql`
(`SuperadminClubs`, `SuperadminClub`, `SuperadminLockSubscription`); `api/billing.yaml`, `api/superadmin.yaml`;
`http/sections/billing/billing.go`, `sections/superadmin/clubs.go` (+скидка), новый `sections/superadmin/invoices.go`;
`http/subscription.go:32` (`/api/billing` уже исключён из проверки подписки — проверить, что и запись счёта проходит);
`http/readonly.go` (запись счёта в режиме просмотра клуба суперадмином — запрещена, как любая запись);
`telegram/alerts.go`; `worker/expire_subscriptions.go` (не меняется: оплаченный счёт ставит `active` и дату);
`web`: `BillingPage.tsx`, `ClubsPage.tsx`, `ClubPage.tsx`, `SuperadminLayout.tsx`, `PlansPage.tsx` (подпись цены),
новая страница «Оплаты»; `server/cmd/migrate-from-mysql` (новая таблица и колонки). **Объём: L.**

### В. Каталог и профиль клуба

Клуб указывает город (из фиксированного списка 25 городов Казахстана) и адрес, заполняет описание, контакты, фото
и оборудование зон. Гостевое приложение до входа показывает клубы города и страницу клуба со свободными ПК.
Вход с `club_id` клуба из каталога делает аккаунт клиентом этого клуба. Гость приложения не зависит от подписки
клуба; сотрудники и агент ПК — зависят.

**Маршруты**

| Метод, путь | Имя | Контроллер@метод | Middleware / лимит | Право |
|---|---|---|---|---|
| `GET /api/catalog/clubs?city=` | `api.catalog.clubs` | `Api\ClubCatalogController@index` | публичный, `throttle:30,1` | — |
| `GET /api/catalog/clubs/{club}` | `api.catalog.club` | `@show` | публичный, `throttle:60,1`, `whereNumber`; клуб без города — 404 | — |
| `GET /club-photos/{club}/{file}` | `club-photos.show` | `ClubProfileController@photo` | публичный; `file` = `[A-Za-z0-9]{40}\.(png\|jpg\|webp)`; заголовки `Cache-Control: public, max-age=31536000, immutable`, `X-Content-Type-Options: nosniff`, `CSP: default-src 'none'` | — |
| `POST /settings/location` | `settings.location` | `SettingsController@location` | `auth`, `current-club`, `subscription`, `club-area` | `settings.update` |
| `GET /settings/profile` | `settings.profile` | `ClubProfileController@show` | то же | `settings.view` |
| `POST /settings/profile` | `settings.profile.update` | `@update` | то же | `settings.update` |
| `POST /settings/profile/photos` | `settings.photos.store` | `@storePhotos` | + `throttle:20,1` | `settings.update` |
| `DELETE /settings/profile/photos/{photo}` | `settings.photos.destroy` | `@destroyPhoto` | | `settings.update` |
| `POST /settings/profile/photos/{photo}/cover` | `settings.photos.cover` | `@coverPhoto` | | `settings.update` |
| `PUT /settings/profile/zones/{zone}` | `settings.profile.zone` | `@updateZone` | | `settings.update` |
| `POST /api/auth/login` (изм.) | `api.auth.login` | `Api\AuthController` | как было | вступление в клуб, подписка только для сотрудников и агента (`AuthController.php:86–90,103–108,218–221`) |
| `POST /api/auth/switch-club` (изм.) | `api.auth.switch-club` | `@switchClub` | как было | то же (`:182–194`) |
| `GET /api/auth/me` (изм.) | `api.auth.me` | `@me` | **снят** `subscription` (`routes/api.php:57–58`) | |
| `GET/POST /api/guest/friends*` (7), `GET /api/guest/news`, `GET /api/guest/history` (изм.) | — | — | **вынесены из группы `subscription`** (`routes/api.php:142–157`); лимиты прежние | включая перевод другу по коду — деньги двигаются и без подписки клуба |

Остаются под подпиской: `/api/tariffs*`, `/api/guest/minutes*`, `/api/guest/packages*`, `/api/guest/chat`,
`/api/guest/shop/purchase`, `/api/guest/profile/*`, `/api/guest/seats`, `/api/guest/seat-transfer`,
`/api/guest/qr-login/*`, `/api/guest/bookings/{computers,tariffs,options}`, `POST /api/guest/bookings`.

Ответы каталога (поля, которые читает приложение — `mobileclient/lib/api/models.dart:91–255`):

- `index`: `{cities: [{key, name, lat, lng, clubs}], city, clubs: [{id, name, address, logo_url, photo_url, hours,
  pcs, free_pcs, price_from}]}`; города отсортированы по имени на языке запроса; `clubs` пуст без `city`;
  неизвестный `city` — 422; до 500 клубов, по имени; `price_from` — **float тенге** (`(float) min(price_per_hour)`),
  `null` без цен.
- `show`: `{id, name, city (имя), address, logo_url, description, phone, hours, instagram, photos: [url],
  zones: [{id, name, description, price_per_hour (float), pcs, free_pcs, devices: [{key, label, value}]}]}`.
- «Свободный ПК» (`ClubCatalogController.php:96–102`): `last_seen_at >= now − HEARTBEAT_TIMEOUT_SECONDS`,
  `is_reserved = false`, нет сессии или она кончилась, нет сотрудника (`staff_until`). Брони **не** учитываются.
- Оборудование зоны: значение клуба, иначе найденное агентами (`ZoneDevices::detected()`): самый частый
  `cpu_model`/`gpu_model` зоны, `MAX(ram_total_mb)` → `round(MB/1024)` «:gb ГБ»; имя процессора чистится
  (`ZoneDevices::cpu()`: «11th Gen Intel(R) Core(TM) i5-11400F @ 2.60GHz» → «Intel Core i5-11400F»).

**Таблицы**

| Таблица | Колонка | Тип | В Postgres |
|---|---|---|---|
| `clubs` | `city` | `string(32)` null, индекс | `varchar(32)`, индекс; значение — ключ `App\Support\Cities` (25 ключей) |
| | `address` | `string(255)` null | `varchar(255)` |
| | `profile` | `json` null: `{description ≤2000, phone ≤32, hours ≤100, instagram ≤64 без «@»}` | `jsonb` |
| `club_zones` | `devices` | `json` null: ключи `cpu,gpu,ram,monitor,keyboard,mouse,headset,chair`, значения ≤ 120 | `jsonb` |
| `club_photos` (новая, тенантная) | `id`, `club_id` FK cascade, `path`, `sort_order unsignedSmallInteger`, timestamps; индекс `(club_id, sort_order)` | | хранение файла — как логотип и картинка входа (`0019_club_images.sql`, `SettingsPutImage`); `sort_order` может стать отрицательным (обложка = `min − 1`, `ClubProfileController.php:116`) → `integer` со знаком |

Фото: до 10 на клуб (`ClubPhoto::LIMIT`), `jpg/jpeg/png/webp`, ≤ 3 МБ, 200…6000 px; имя файла — 40 случайных
символов. Миграции: `2026_11_13_100000_add_city_and_address_to_clubs_table.php`, `2026_11_14_100000_create_club_profile.php`.

**Экраны**

| Blade | Что изменилось | В `web/src` |
|---|---|---|
| `settings/club.blade.php` (+65 строк) | 3 новые карточки `<x-settings.card>`: `#app-booking` «Бронь из приложения» (тема Г), `#location` «Город и адрес» (отдельная форма → `settings.location`: `select` города, поле адреса, «Сохранить»), `#reseller` «Реселлер» (тема Д); 3 новых якоря в меню страницы; карточек-секций стало **11** (было 8) | есть `features/settings/ClubSettingsPage.tsx`, `SettingsAnchors.tsx` |
| `settings/layout.blade.php` | пункт меню «Профиль клуба» (`fa-mobile-screen`) + якоря страницы | есть (меню настроек) |
| `settings/profile.blade.php` (новый, 105 строк) | «Профиль клуба»: описание и контакты; фото (загрузка нескольких, «Сделать обложкой», удалить); «Зоны и оборудование» — форма на зону (`#zone-{id}`), подсказки-`placeholder` из данных агентов | **нет** — новый маршрут `web/src/routes/_admin/settings/profile.tsx` |

**Настройки клуба**: город, адрес, профиль, оборудование зон — колонки, не `clubs.settings`.

**События**: `club.location_changed` (только если город/адрес изменились), `club.profile_changed`,
`club.photos_added`, `club.photo_deleted`, `club.zone_devices_changed`, `user.joined_club` (описание «Клиент стал
клиентом клуба в мобильном приложении: :user»; код события уже был — регистрация на ПК). Смена обложки в журнал
не пишется.

**Права**: `settings.view` / `settings.update` (`RequireClubArea.php:63–64`).

**Вступление в клуб** (`AuthController::joinClub()`, `:224–235`): членство `role = client`, `discount = 0`;
`MemberBalance::firstOrCreate` с нулями; журнал `user.joined_club`. Условия: нет членства, запрос не от агента ПК,
клуб с непустым `city`. Клуб без города — 403 `no_club_access`, как раньше. Повторный вход членство не дублирует.

**Затронуто в zafirogo**: миграция; `db/queries/settings.sql`, `clubs.sql`, `mobile_api.sql`, новый
`catalog.sql`; `api/settings.yaml` (+6 операций), `api/public.yaml` не трогать — каталог живёт в `legacy`
(контракт старого REST); `http/sections/settings/{handlers,images,zones}.go`; `http/legacy/routes.go:29,36`
(снять `a.subscription` с `/api/auth/me` и девяти гостевых маршрутов), `legacy/auth.go` `doLogin` (`:627–649`),
`switchClub` (`:739–743`) — вступление и «подписка только сотрудникам»; `auth/ratelimit` (2 новых лимита);
присутствие ПК для «свободных» — `agent/presence` (не `last_seen_at` в Postgres); `club/info.go`;
`web`: `ClubSettingsPage.tsx`, меню настроек, новая `ProfilePage.tsx`; i18n; перенос данных. **Объём: L.**

### Г. Брони из гостевого приложения

Гость выбирает ПК (до 10), время и способ — по часам (длительность × цена часа зоны) или тарифом; платит сразу
с балансов. Оплаченное время начинается, когда гость входит на забронированный ПК (с интервала до начала),
сотрудник отмечает приход, или кто-то вводит 6-значный код брони на экране входа ПК (сессия без аккаунта).
Отмена до начала возвращает деньги; пропущенная бронь — нет.

**Маршруты**

| Метод, путь | Имя | Контроллер@метод | Middleware / лимит |
|---|---|---|---|
| `GET /api/guest/bookings` | `api.guest.bookings.index` | `Api\GuestBookingController@index` | `auth:sanctum`, `current-club` (**без** подписки), `throttle:60,1` |
| `POST /api/guest/bookings/{reservation}/cancel` | `api.guest.bookings.cancel` | `@cancel` | то же, `throttle:10,1`; чужая бронь — 404 |
| `GET /api/guest/bookings/computers` | `api.guest.bookings.computers` | `@computers` | + `subscription`, `throttle:60,1`; выключено в клубе — 403 |
| `GET /api/guest/bookings/tariffs?starts_at=&computer_id=&computer_ids[]=` | `api.guest.bookings.tariffs` | `@tariffs` | то же |
| `GET /api/guest/bookings/options?starts_at=&duration_minutes=\|tariff_id=` | `api.guest.bookings.options` | `@options` | то же |
| `POST /api/guest/bookings` | `api.guest.bookings.store` | `@store` | + `subscription`, `throttle:10,1`; 201 |
| `POST /api/agent/booking-code` | `api.agent.booking-code` | `Api\AgentBookingCodeController` | `agent.device`, `throttle:agent-booking-code` (5/мин на ПК + 200/час на клуб, `AppServiceProvider.php:87–91`) |
| `POST /api/agent/heartbeat` (изм.) | `api.agent.heartbeat` | `ClubComputerController@agentHeartbeat` (`:303–309`) | с токеном клиента в `Authorization` запускает его оплаченную бронь |
| `POST /reservations/{reservation}/cancel`, `/arrive` (изм.) | `reservations.cancel/arrive` | `ComputerReservations::close()` | отмена оплаченной возвращает деньги; «Клиент пришёл» запускает время |

Тело `POST /api/guest/bookings`: `club_computer_ids[]` (1…10) **или** `club_computer_id` (старые версии приложения),
`starts_at` (ISO 8601, округляется до минуты, UTC), `duration_minutes` (30…720; обязателен без `tariff_id`) или
`tariff_id`. Ответы: `index` — `{enabled, can_cancel, gap_minutes, currency, min_minutes: 30, max_minutes: 720,
max_days_ahead: 7, bookings: [...]}`; бронь — `{id, code, computer, computers[], zone, tariff, starts_at, ends_at
(UTC ISO), state, paid, paid_bonus (строки «900.00»), can_cancel}`; ПК, забронированные вместе, — одна запись
(группа по `code`). `computers` — `{zones: [{id, name, price_per_hour (строка со скидкой гостя | null),
computers: [{id, name}]}]}` (без `is_reserved`, только зоны с ПК). `tariffs` — `[{id, name, description, minutes,
price, zone_ids}]`. `options` — `{minutes, zones: [{id, name, price_per_hour, price, computers: [{id, name,
available}]}]}`. `booking-code`: тело `{code: "^\d{6}$"}` → `{message, session: {id, ends_at, user_id: null, guest: true}}`.

**Таблицы** — `computer_reservations`:

| Колонка | Тип MySQL | В Postgres | Примечание |
|---|---|---|---|
| `from_app` | boolean default false | boolean | |
| `paid_main_cents` | `unsignedInteger` default 0 | `bigint NOT NULL DEFAULT 0 CHECK (>= 0)` | **уже тиыны** (центы тенге) — при переносе **не** умножать на 100 |
| `paid_bonus_cents` | `unsignedInteger` default 0 | то же | то же |
| `tariff_id` | FK nullOnDelete | FK `ON DELETE SET NULL` | бронь тарифом |
| `code` | `string(8)` null | `varchar(8)` | 6 цифр, один на группу ПК; открытым текстом (сотрудник видит его в списке броней) |
| индексы | `(club_id, user_id, starts_at)`, `(club_id, code)` | те же | |

Миграции: `2026_11_15_…add_app_payment…`, `2026_11_16_…add_tariff…`, `2026_11_17_…add_code…`.

**Экраны**

| Blade | Что изменилось | В `web/src` |
|---|---|---|
| `settings/club.blade.php` | карточка `#app-booking`: переключатели `app_booking_enabled` (умолч. выкл.) и `app_booking_cancel` (умолч. вкл.) с пояснениями | есть `ClubSettingsPage.tsx` |
| `reservations/index.blade.php` | под именем клиента строка `text-xs text-gray-500`: «Из приложения, оплачено N» или «Из приложения» + «· код 123456»; подтверждение отмены оплаченной брони — другой текст («Отменить бронь? Оплата вернётся…») | есть `features/reservations/ReservationsPage.tsx`; API `listReservations` — добавить `from_app`, `paid` (тиыны), `code` |

**Настройки клуба** (`clubs.settings.club`): `app_booking_enabled` (bool, false), `app_booking_cancel` (bool, true);
обе — булевы, в «минутные» поля не входят (`SettingsController.php:273–274,316–317,378,397–398,445–446`,
`SaveSettingsRequest.php:54–55`). Используется существующий `booking_interval`.

**События (журнал клуба)**: `reservation.paid` «Оплата брони в приложении» (subject — первая бронь группы; детали
`amount, currency, main_balance_amount, bonus_balance_amount, computer, zone, tariff, starts_at (время клуба),
duration_minutes`); `reservation.refunded` «Возврат за отменённую бронь» (subject — пользователь; детали
`main_balance_amount, bonus_balance_amount, reservation_id`); `reservation.arrived` с `by_code: true` («ПК :computer
открыт по коду брони на :from»); `reservation.created`, `reservation.cancelled` — как раньше, на каждый ПК.
Вебхуков и событий API v1 нет.

**Права**: гость — свой токен клуба; сотрудники — прежние `reservations.update` / `reservations.delete`.

**Затронуто в zafirogo**: миграция (+ новые виды операций ledger); `db/queries/reservations.sql`,
`purchases.sql` (`MarkReservationsArrived`, `GetBlockingReservation` не меняется), новый `bookings.sql`;
`internal/reservations/reservations.go` (`closeBooking`: возврат и старт времени); новый пакет
`internal/billing/service` — `BookFromApp`, `CancelBooking`, `StartBooking`, `StartBookingByCode`;
`ledger/types.go` (виды `reservation_payment`, `reservation_refund`, `Refs.ReservationID`); `billing/spending.go`
(накопительные траты); `http/legacy/routes.go`, новый `legacy/bookings.go`; `http/sections/reservations`,
`sections/settings/{values,save}.go`; `agent/guestapi` (история, запуск брони при входе гостя на ПК),
`proto/agent/v1` (тема Ж); `worker` (запуск броней вошедших гостей — UQ-11); `realtime` (`computer.state`,
`reservation.changed`); `web`: `ReservationsPage.tsx`, `ClubSettingsPage.tsx`; i18n. **Объём: L.**

### Д. Реселлеры

Реселлер — аккаунт (`users`) с записью в `resellers`: код, имя, процент с оплат, скидка клубам, признак «код
действует». Создаёт и удаляет суперадмин. Клуб один раз вводит код в настройках; его счета идут со скидкой
`max(своя скидка, скидка реселлера)`, а реселлеру с каждой оплаты начисляется процент. Выплаты записывает суперадмин.
Кабинет реселлера — на отдельном хосте `RESELLER_DOMAIN` (в проде `resseler.zafiro.kz`), без хоста — `/reseller`.

**Маршруты**

| Метод, путь | Имя | Контроллер@метод | Middleware | Право |
|---|---|---|---|---|
| `GET /` на хосте реселлера (или `/reseller`) | `reseller.dashboard` | `ResellerController@dashboard` | `auth`; не реселлер — 403 | свой кабинет |
| `GET /superadmin/resellers?q=` | `superadmin.resellers.index` | `Superadmin\ResellerController@index` | `superadmin` | |
| `POST /superadmin/resellers` | `…store` | `@store` | | |
| `PATCH /superadmin/resellers/{reseller}` | `…update` | `@update` | | |
| `POST /superadmin/resellers/{reseller}/payouts` | `…payout` | `@payout` | | |
| `DELETE /superadmin/resellers/{reseller}` | `…destroy` | `@destroy` | | |
| `DELETE /superadmin/resellers/{reseller}/clubs/{club}` | `…detach` | `@detach` | клуб не этого реселлера — 404 | |
| `POST /settings/reseller` | `settings.reseller` | `SettingsController@reseller` (`:69–87`) | `club-area`, `throttle:10,1` | `settings.update` |
| `POST /login` (изм.) | `auth.login` | `AuthController@login` (`:60–71`) | на хосте реселлера входят только реселлеры: иначе выход, журнал `auth.login_failed` с причиной `not_reseller`, ошибка «Здесь входят только реселлеры Zafiro.» | |

Middleware `RestrictResellerHost` (в группе `web`, `bootstrap/app.php:61`): на хосте реселлера доступны только
`reseller.*`, `auth.show-login`, `auth.login`, `auth.logout`, `auth.password.*`; всё остальное — редирект в кабинет
(в том числе `/register` и `/dashboard`).

Валидация: `code` — после `mb_strtolower(trim())` — `^[a-z0-9][a-z0-9-]{2,31}$`, уникален; `phone` → `+7XXXXXXXXXX`;
`password` ≥ 6, обязателен только для нового аккаунта; `commission_percent` numeric 0…100 (хранится 2 знака);
`discount_percent` целое 0…100; выплата `amount` numeric 0.01…999 999 999, `note` ≤ 255.

**Таблицы**

| Таблица | Колонка | Тип MySQL | В Postgres |
|---|---|---|---|
| `resellers` (новая, платформенная — без `club_id`) | `id`; `user_id` FK unique cascade; `code string(32)` unique; `name string(120)`; `commission_percent decimal(5,2)` default 0; `discount_percent unsignedTinyInteger` default 0; `is_active` bool default true; timestamps | | `code` — `citext`/уникальность по `lower()` (Q-025); `commission_percent` → `integer` базисные пункты (0…10000); `discount_percent smallint` |
| `reseller_payouts` (новая, платформенная) | `id`; `reseller_id` FK cascade; `amount decimal(12,2)`; `note string(255)` null; `created_by` FK nullOnDelete; timestamps; индекс `(reseller_id, created_at)` | | `amount bigint` тиыны (×100) |
| `clubs` | `reseller_id` FK nullOnDelete; `reseller_attached_at` timestamp null | | то же |
| `subscription_invoices` | `reseller_id`, `reseller_percent`, `reseller_amount`, индекс `(reseller_id, status, paid_at)` | | см. тему Б |

Миграция: `2026_11_18_100000_create_resellers.php`. Конфигурация: `RESELLER_DOMAIN` (`config/app.php:60–61`),
в тестах `resseler.localhost`.

**Экраны**

| Blade | Что | В `web/src` |
|---|---|---|
| `reseller/dashboard.blade.php` (новый, 91 строка) | 4 плитки (код; процент и скидка; «Заработано всего» + «Выплачено»; «К выплате» `border-violet-200 bg-violet-50`), предупреждение о приостановленном коде, таблицы «N клубов» (пагинация 30), «Начисления» (20), список «Выплаты» (20 последних) | **нет** |
| `layouts/reseller.blade.php` (36), `partials/reseller-sidebar.blade.php` (7) | свой каркас кабинета | **нет** |
| `superadmin/resellers/index.blade.php` (новый, 74) | пояснение, форма «Новый реселлер», поиск, карточки реселлеров (итоги, «Удалить», форма процента/скидки/«Код действует», форма выплаты с подставленным остатком, раскрывающийся список клубов с «Отвязать») | **нет** |
| `settings/club.blade.php` | карточка `#reseller`: форма кода или «Клуб подключён через реселлера :name (код :code).» + скидка + «Сменить реселлера можно через поддержку Zafiro.» | есть `ClubSettingsPage.tsx` |
| `auth/login.blade.php` | на хосте реселлера вместо «Нет аккаунта? Зарегистрироваться» — «Кабинет реселлера Zafiro. Аккаунт реселлера создаёт Zafiro.» | есть `routes/_auth/login.tsx` |
| `partials/superadmin-sidebar.blade.php` | пункт «Реселлеры» | есть |

**События**: клуб — `club.reseller_attached`; платформа — `superadmin.reseller_created`, `…_updated` (детали —
изменённые поля строками), `…_payout` `{amount (float)}`, `…_detached`, `…_deleted` `{code, user_id, clubs}`.

**Правила** (деньги — §3, BR-185…BR-189): код вводится один раз; повторная попытка — ошибка «Клуб уже привязан…»;
несуществующий или остановленный код — «Реселлер с таким кодом не найден.»; остановленный реселлер
(`is_active = false`) не даёт скидки и процента **в новых счетах**; отвязка и удаление — то же; при удалении:
аккаунт остаётся, клубы отвязываются, **выплаты удаляются каскадом**, в оплаченных счетах `reseller_id` становится
`NULL`, а `reseller_amount` остаётся; аккаунт можно сделать реселлером снова.

**Затронуто в zafirogo**: миграция; новые `db/queries/resellers.sql`; `api/superadmin.yaml` (+6), `api/settings.yaml`
(+1), новый `api/reseller.yaml`; новый `http/sections/reseller`, `sections/superadmin/resellers.go`;
`http/superadmin_host.go` → рядом `reseller_host.go` (ограничение маршрутов по хосту); `sections/auth` (вход на
хосте реселлера); `config` (`reseller.domain`); `sections/settings`; `web`: новые `features/reseller/*`,
`features/superadmin/ResellersPage.tsx`, `host.ts` (третий хост), `router.tsx`, `ClubSettingsPage.tsx`,
страница входа; `deploy` (домен, TLS); перенос данных. **Объём: L** (после Б).

### Е. Лендинг и страницы цен/продуктов

**Маршруты** (все публичные, без лимитов; `LandingController`):

| Метод, путь | Имя | Метод | Шаблон |
|---|---|---|---|
| `GET /` (изм.) | `home` | `__invoke` | `welcome.blade.php` (231 → 143 строки, на общем `layouts/landing`) |
| `GET /product/features` | `product.features` | `features` | `product/features.blade.php` (84) |
| `GET /product/agent` | `product.agent` | `agent` | `product/agent.blade.php` (134) |
| `GET /product/guest-app` | `product.guest-app` | `guestApp` | `product/guest-app.blade.php` (76) |
| `GET /product/staff-app` | `product.staff-app` | `staffApp` | `product/staff-app.blade.php` (76) |
| `GET /pricing` | `pricing` | `pricing` | `pricing.blade.php` (68) |

Данные всех страниц: `trialDays` = `max(trial_days)` активных тарифов или 14; `downloadUrl` агента. `pricing`:
активные тарифы, сортировка `price_monthly IS NOT NULL`, `price_monthly`, `id` (бесплатные и «по запросу» первыми);
карточка: цена `number_format(…, 0, '.', ' ') ₸` + «за ПК в месяц» | «Бесплатно» (нет цены и есть пробные дни) |
«По запросу»; пробные дни; `features` списком или «Все возможности Zafiro»; 4 вопроса об оплате.

**Экраны**

| Blade | Что | В `web/src` |
|---|---|---|
| `layouts/landing.blade.php` (новый, 123) | общий каркас: липкая шапка с тенью и выпадающими меню «О продукте» (Возможности) и «Продукты» (Агент на ПК, Приложение для гостей, Приложение для сотрудников), ссылки «Тарифы», «Wiki», «Войти», «Попробовать»/«Открыть панель»; мобильное меню; розовый блок призыва; подвал; белый фон | нет (сейчас всё в `features/public/LandingPage.tsx`) |
| `partials/landing-menu-link.blade.php`, `phone-guest.blade.php`, `phone-staff.blade.php` | пункт меню; макеты телефонов | нет |
| `welcome.blade.php` | герой: «Весь клуб - в одном окне» (дефис), новый бейдж, факты «пробный период»/«облачный сервер», снимок карты `public/images/club-map.webp` (31 КБ) без легенды; тёмный блок «Мобильные приложения для гостей и сотрудников»; «Клиент сам покупает время, а ПК защищён»; «Три шага до первой сессии»; «Частые вопросы»; заголовки без градиента | есть `LandingPage.tsx` — **переверстать** |
| `product/*.blade.php`, `pricing.blade.php` | новые | нет — 5 новых публичных маршрутов |
| `public/images/logo-dark.svg` | новый логотип для светлой шапки | — |

Внешние ресурсы в Blade: Font Awesome 6.4.0 с cdnjs, шрифты (`partials/fonts`) — в zafirogo из npm (Q-064).
Эталоны визуального сравнения главной (`task visual`) устареют.

**Затронуто в zafirogo**: `api/public.yaml` (`getLanding` + тарифы для `/pricing`), `db/queries/public_pages.sql`
(`PublicLanding`), `sections/public/public.go`; `web/src/features/public/*`, `web/src/routes/index.tsx`, новые
маршруты `product/*`, `pricing`; `web/e2e` (эталоны); i18n. Проверить, что новые пути не перехватываются
правилами хостов суперадмина, wiki и реселлера. **Объём: M.**

### Ж. Агент Windows

Изменения `WindowsAgent/` (один коммит `0da191b`):

| Файл | Что |
|---|---|
| `ZafiroAgent/Views/ClubSetupWindow.xaml(.cs)` | на экране входа подключённого ПК — ссылка «Есть код брони?»; режим: панель `BookingCodePanel` с полем 6 цифр (`MaxLength=6`, только ASCII-цифры, `FontSize=22`) вместо телефона и пароля; заголовок «Вход по коду брони», пояснение, кнопка «Начать по коду», «Вернуться ко входу»; при успехе — сразу проверка гостевой сессии (`CheckGuestSessionAsync` → `ApplyGuestSession`); режим выключает регистрацию и восстановление пароля |
| `Services/ClubSetupService.cs:372–377`, `ZafiroApiClient.cs:35` | `EnterBookingCodeAsync` → `POST /api/agent/booking-code` с токеном ПК |
| `Service/VaultProtocol.cs:54` | путь `agent/booking-code` в списке запросов, куда служба подставляет токен ПК |
| `Frontend/i18n/{ru,kk,en}.json` | 7 ключей: «Код брони», «Есть код брони?», «Вход по коду брони», «Введите код из приложения Zafiro…», «Начать по коду», «Код брони — 6 цифр.», «Не удалось проверить код…» |
| `ZafiroAgent.Tests/DeviceVaultTests.cs` | случай для нового пути |

Запуск оплаченной брони вошедшего гостя агент **не инициирует**: сервер делает это сам на heartbeat с токеном
клиента и возвращает сессию обычным ответом — код агента для этого не менялся.

**Что перенести в новый агент и протокол**

| Где | Что | Объём |
|---|---|---|
| `proto/agent/v1/guest.proto`, `GuestAuthService` | новый RPC `StartByBookingCode(StartByBookingCodeRequest{code}) → {message, SessionState}`; ПК — из сертификата mTLS; лимит 5/мин на ПК и 200/час на клуб; ошибки — поле `code` с текстами BR-178 | S |
| `docs/agent-protocol.md` | раздел RPC; правило «сервер сам начинает оплаченную бронь вошедшего гостя и присылает `SessionChanged`» | S |
| `server/internal/agent/guestapi` | обработчик RPC → `StartBookingByCode`; при `Login`/подтверждении QR и по таймеру — `StartBooking` (UQ-11); `ListHistory` — два новых вида записей | M |
| `agent/src/Zafiro.Agent.UI/Frontend` (`index.html`, `lock.js`, `main.js`, `core.js`, `i18n/*.json`) | режим «код брони» экрана блокировки; 7 ключей | M |
| `agent/src/Zafiro.Agent.Core/Guest`, `Zafiro.Agent.Protocol/Ipc` | вызов RPC из службы, сообщение IPC UI ↔ служба | S |
| `agent/tests` | код не из 6 цифр; успех открывает ПК как гостевую сессию | S |

### З. Flutter-приложения

`mobileemployee/` — без изменений. `mobileclient/` (7 коммитов): первый экран — клубы города
(`catalog_screen.dart`, геолокация `geolocator`, ближайший из городов по координатам — считает телефон, `geo.dart`),
страница клуба (`club_screen.dart`), вход в выбранный клуб (`login_screen.dart`; `club_picker.dart` удалён),
экран броней (`bookings_screen.dart`, 1041 строка). Приложения **не переносятся**; ниже — что должен отдавать
слой совместимости (`docs/mobile-compat.md`, `server/internal/http/legacy`).

| Вызов (Dart, `mobileclient/lib/api/api_client.dart`) | Маршрут | Новое / изменено | В `legacy` сейчас |
|---|---|---|---|
| `catalog({city})` `:45–47` | `GET /api/catalog/clubs` | новое, без токена | нет |
| `club(id)` `:50–51` | `GET /api/catalog/clubs/{id}` | новое, без токена | нет |
| `login(phone, password, clubId)` `:30–43` | `POST /api/auth/login` | **всегда** шлёт `club_id`; сервер вступает в клуб | есть, без вступления |
| `switchClub(id)` `:84–86` | `POST /api/auth/switch-club` | клуб из каталога — вступление | есть, без вступления |
| `me()`, `news()`, `history()`, друзья (7 вызовов) | прежние | больше не дают 402 в клубе без подписки | под `a.subscription` |
| `bookings()` `:127–128` | `GET /api/guest/bookings` | новое | нет |
| `bookingComputers()` `:131–137` | `GET /api/guest/bookings/computers` | новое | нет |
| `bookingTariffs(start, computerIds)` `:140–156` | `GET /api/guest/bookings/tariffs` (`computer_ids[]`) | новое | нет |
| `bookingOptions(start, minutes\|tariffId)` `:159–173` | `GET /api/guest/bookings/options` | новое | нет |
| `book(computerIds, start, …)` `:176–193` | `POST /api/guest/bookings` (`club_computer_ids`) | новое; `Idempotency-Key` не шлёт | нет |
| `cancelBooking(id)` `:195–198` | `POST /api/guest/bookings/{id}/cancel` | новое | нет |

Как приложение читает суммы (`models.dart`): `paid`, `price`, `price_per_hour` броней — через `_amount()` (строка
или число); `price_from`, `price_per_hour` каталога — число (`num`), `lat/lng` — число. Формат ответов — §В и §Г.
Картинки (`logo_url`, `photo_url`, `photos[]`) — абсолютные адреса сервера.

`docs/mobile-compat.md` нужно дополнить: §1.2 (новые маршруты и снятая подписка), §1.4 (каталог — без токена:
правило «токен чужого сервера → пересылка» к нему неприменимо, UQ-2).

---

## 3. Деньги

Обозначения: `amountToCents`/`formatCents` — существующие (BR-001); «центы» старого кода = тиыны.

### 3.1. Срок тарифа и период действия (коммит `b4f08f6`)

**BR-170. Конец периода действия `WeeklyPeriods::endOf()`** — `app/Support/WeeklyPeriods.php:50–87`.
Вход: периоды тарифа и местное время клуба. `null`, если периодов нет, период — вся неделя (`isAlways`) или
момент вне периодов. Иначе каждый период переводится в минуты от понедельника 00:00:
`$start = $startDay * 1440 + minutes(start_time)`,
`$end = $endDay * 1440 + minutes(end_time) + (end_time === '23:59' ? 1 : 0)`, `if ($end < $start) $end += 7*1440`;
каждый период берётся трижды — со сдвигом −неделя, 0, +неделя. Текущая минута
`($localNow->dayOfWeekIso - 1) * 1440 + hour * 60 + minute` (секунды отброшены). Цикл: пока есть диапазон с
`rangeStart <= end && rangeEnd > end`, `end = rangeEnd` — смежные и пересекающиеся периоды сливаются. Результат:
понедельник этой недели 00:00 + `end` минут, в поясе клуба. Типы — целые минуты.

**BR-171. Покупка тарифа в агенте обрезается периодом** — `AgentTariffPurchaseService.php:281–292` (`sell()`),
`:122–139` (`purchase()`).

```php
$startsAt = $hasActiveSession ? $computer->session_ends_at->copy() : now();
$sessionEndsAt = $startsAt->copy()->addMinutes($minutes);
if ($until !== null && $sessionEndsAt->greaterThan($until)) {
    if ($until->lessThanOrEqualTo($startsAt)) { throw … 'Тариф действует до :time, а время на ПК уже оплачено дальше.' }
    $sessionEndsAt = $until->copy();
    $minutes = (int) ceil(($until->getTimestamp() - $startsAt->getTimestamp()) / 60);
}
```

- `$until` = `Tariff::validityEndsAt()` (`Tariff.php:53–57`) — конец периода, в котором находится **текущий момент**
  (не начало продлеваемой сессии).
- Проверка идёт **до** блокировки баланса и списания; ошибка — поле `tariff`, время `H:i` в поясе клуба.
- **Цена не меняется**: списывается полная цена тарифа. В части сессии (`session_purchases.minutes`) и в ответе
  `minutes_added` — обрезанные минуты; `ceil` по секундам.
- Остаток (`:136–138`): `if ($part['expires_at'] !== null && ($rest = $minutes - $result['minutes_added']) > 0)`
  создаётся `TimePackage{minutes_left: rest, expires_at: part.expires_at}` — **только у тарифа с лимитом
  использования**; без лимита обрезанные минуты пропадают.
- Возврат при досрочном завершении (существующий BR-032) считается от обрезанных минут части.

**BR-172. Запуск пакета обрезается периодом, остаток остаётся** — `AgentTariffPurchaseService.php:214–247`.
`$minutes = min($package->minutes_left, intdiv(max(0, expires_at − now), 60))` (как было), затем `sell(…, $until =
$tariff?->validityEndsAt())`; после: `$rest = $package->minutes_left - $result['minutes_added']; $rest > 0 ?
$package->update(['minutes_left' => $rest]) : $package->delete();`. Раньше пакет удалялся всегда. Побочное следствие:
пакет, обрезанный **сроком годности**, теперь тоже остаётся с остатком (сгорит по сроку, BR-034).

**BR-173. Гостевая сессия с кассы обрезается периодом** — `GuestSessions.php:47–76`.
`$duration = min($duration, (int) ceil(max(0, $until − now()) / 60))`; по каждому ПК: `$startsAt` = конец идущей
гостевой сессии или сейчас; `$until <= $startsAt` → ошибка `tariff_id` «Тариф действует до :time, а время на ПК
:computer уже оплачено дальше.»; `$endsAt = min($startsAt + $duration, $until)`. Сумма продажи — полная цена тарифа
× число ПК (как было).

Ledger: видов операций не добавляется. Меняются минуты в `tariff_purchase` и движение минут: остаток покупки —
`+rest` в ledger минут (как `package_kept`), запуск пакета — `−minutes_added`, а не весь остаток.

### 3.2. Брони из приложения

**BR-174. Цена брони** — `GuestBookings.php:292–297`.

```php
return $plan['tariff'] !== null
    ? $this->purchases->tariffPrice($user, $plan['tariff'])
    : AgentTariffPurchaseService::minutesPriceInCents($this->purchases->hourlyPrice($user, $zone), $plan['minutes']);
```

- По часам: `intdiv(amountToCents($pricePerHour) * $minutes + 30, 60)` (`AgentTariffPurchaseService.php:250–253`),
  где `$pricePerHour` — цена часа зоны со скидкой гостя (`hourlyPrice()`, `:183–192`, существующие BR-012, BR-014).
  Целые тиыны; округление до тиына «половина вверх» на 60.
- Тарифом: `tariffPrice()` (`:87–90`) = `priceInCents()` — цена тарифа со скидкой, если область скидки её
  разрешает (BR-013).
- Цена считается **на каждый ПК**; итог `array_sum($prices)` (`GuestBookings.php:225`). ПК из разных зон в одной
  брони могут стоить по-разному.
- В zafirogo: `pricing.MinuteOfferFor` и `Tariff.Offer` уже дают эти числа.

**BR-175. Длительность брони тарифом** — `AgentTariffPurchaseService.php:70–76`.
`min(effectiveMinutes($tariff), (int) ceil(($until − $localStart) / 60))`, `$until = WeeklyPeriods::endOf(периоды,
местное время **начала брони**)`. `<= 0` → ошибка «Этот тариф нельзя забронировать на это время.». Цена — полная.
Какие тарифы можно бронировать (`bookableTariffs()`, `:60–67`, `isAvailable()`, `:379–395`): `is_active`,
в продаже **сейчас** (`onSale()`), роль гостя, лимит количества не исчерпан, период действия содержит **начало
брони**; с выбранными ПК — тариф без зон или с зонами **всех** ПК (`GuestBookings.php:115`).

**BR-176. Оплата брони: бонусная часть и её делёж** — `GuestBookings.php:225–259`.

```php
$price = array_sum($prices);
$bonus = $this->purchases->bonusPayment($price, $tariff?->payment_method ?? 'main_or_bonus', null, $mainBalance, $bonusBalance, …);
$main = $price - $bonus;
…
$pcBonus = $index === $computers->count() - 1 ? $bonusLeft : ($price === 0 ? 0 : intdiv($bonus * $prices[$computer->id], $price));
$bonusLeft -= $pcBonus;
'paid_main_cents' => $prices[$computer->id] - $pcBonus, 'paid_bonus_cents' => $pcBonus,
```

- `bonusPayment()` (`AgentTariffPurchaseService.php:346–376`) вызывается с `choice = null` — ветка «старых агентов»:
  при смешанной оплате `min($bonusLimit, max(0, $price - $mainBalance))`, иначе бонусами вся цена, только если
  основного не хватает, а бонусного лимита и бонусов хватает (существующий BR-018). Лимит бонусов — от **общей**
  цены; «только основной баланс» тарифа обнуляет лимит. Не хватает — ошибка поля `balance` «Недостаточно средств:
  бронь стоит :price.».
- Делёж: ПК в порядке `id`; доля `floor(bonus × цена_ПК / итог)`, последнему — остаток. Все числа — целые тиыны.
- Балансы: `balance −= main`, `bonus_balance −= bonus` одним обновлением строки (`:256–259`).
- Накопительные траты: `addSpending(user, $main)` — только основная часть, вся сумма брони (`:260`).
- Журнал `reservation.paid` — один на группу.
- ⚠️ GAP-041: при трёх и более ПК и почти полной оплате бонусами остаток последнего ПК может превысить его цену,
  и `paid_main_cents` станет отрицательным (в MySQL — ошибка беззнаковой колонки, бронь не создаётся).

**BR-177. Пределы брони** — `GuestBookings.php:25–34,184–223,369–385`.
ПК в брони: 1…10, без повторов; длительность по часам: 30…720 мин; начало: не раньше `now − 1 мин`, не дальше
7 дней; броней впереди (ожидающих, `ends_at > now`): не больше **2**, группа по `code` считается одной (проверка при
`>= 2` — третья отклоняется, поле `starts_at`); лимит количества тарифа: `purchasesLeft < число ПК` → ошибка
`tariff_id` «Тариф «:tariff» осталось купить на :count ПК.»; ПК не найден / `is_reserved` / зона без цены часа
(по часам) / зона не из тарифа → ошибка `club_computer_ids`; пересечение с ожидающей бронью ± интервал или с идущей
оплаченной сессией → ошибка `starts_at` (существующая проверка `ComputerReservations::create()`).

**BR-178. Запуск оплаченного времени** — `ComputerReservations.php:271–321` (`startPaidSession()`).

```php
$duration = $reservation->durationMinutes();   // max(1, round((ends_at − starts_at) / 60))
$minutes = min($duration, intdiv(max(0, $reservation->ends_at->getTimestamp() - now()->getTimestamp()) + 59, 60));
…
'main_cents'  => intdiv($reservation->paid_main_cents  * $minutes, $duration),
'bonus_cents' => intdiv($reservation->paid_bonus_cents * $minutes, $duration),
```

- Пришёл заранее (в интервале до начала) или вовремя — `minutes = duration`, сессия идёт `duration` минут **от
  текущего момента** (может закончиться раньше конца брони). Опоздал — минуты до конца брони, округление вверх.
  `minutes <= 0` — не запускается.
- Часть сессии (`session_purchases`): вид `minutes` (по часам) или `tariff` (тарифом; `tariff_id`, имя тарифа,
  `expires_at = now + usage_limit_minutes` при лимите использования); имя «Бронь» для почасовой. Деньги части —
  пропорционально `minutes / duration`, округление **вниз** раздельно по основной и бонусной части. Пропущенная
  доля не возвращается (остаётся выручкой).
- На ПК уже идёт **своя** сессия гостя — время брони добавляется к её концу, `session_id` тот же. Чужая или
  гостевая сессия — запуск невозможен.
- Досрочное завершение: почасовая часть возвращает неиспользованное (существующий BR-032), тарифная — по правилам
  тарифа.
- Три пути запуска:
  1. **Вход гостя на ПК** — `startForSignedIn()` (`:141–167`), вызывается на каждом heartbeat с токеном клиента:
     бронь этого гостя на этом ПК, ожидающая, оплаченная, `starts_at <= now + интервал`, `ends_at > now`; самая
     ранняя. Журнал `reservation.arrived` от имени гостя.
  2. **Сотрудник «Клиент пришёл»** — `close()` (`:235–263`): оплаченная бронь запускается сразу, **в любой момент до
     конца брони** (хоть за сутки); ПК занят другим — ошибка «На ПК идёт сессия другого клиента: оплаченное время
     брони начнётся, когда ПК освободится.». Неоплаченная — как раньше, только отметка.
  3. **Код брони** — `startByCode()` (`:189–226`): бронь с этим кодом на **этом** ПК, ожидающая, `ends_at > now`;
     код от другого ПК — «Этот код брони не для этого ПК: сядьте за забронированный ПК.»; нет такого кода — «Код
     брони не найден или бронь уже закончилась.»; рано — «Бронь начнётся в :time. Код заработает за :count мин до
     начала.»; на ПК любая оплаченная сессия — «На этом ПК уже идёт сессия.». Открывается **гостевая сессия**
     (`session_guest = true`, пользователь `NULL`, новый `session_id`, конец `now + minutes`), **без** части сессии:
     возврата при досрочном завершении нет.
- Покупка времени гостем на забронированном ПК **не** закрывает его оплаченную бронь (`markArrivedBy` — только
  неоплаченные, `:132`).

**BR-179. Отмена и возврат** — `ComputerReservations.php:98–101,235–263,333–349`; `GuestBookings.php:303–319`.

```php
'balance'       => formatCents(amountToCents((string) $balance->balance)       + $reservation->paid_main_cents),
'bonus_balance' => formatCents(amountToCents((string) $balance->bonus_balance) + $reservation->paid_bonus_cents),
$this->loyalty->subtractSpending($reservation->user_id, $reservation->paid_main_cents);
```

- Возврат — **полный**, на те балансы, с которых платили; накопительные траты уменьшаются на основную часть.
- Гость: только если клуб разрешил (`app_booking_cancel`), только своя бронь (чужая — 404), только пока **все** ПК
  группы в состоянии `pending` (до начала); отменяются все ПК группы одной транзакцией.
- Сотрудник: любую бронь в состоянии `pending` или `active` (началась, гость не пришёл) — возврат полный, даже
  если клуб запретил отмену гостям; отменяет по одному ПК.
- Нет строки баланса — возврат молча пропускается (`:336–338`), бронь отменена, деньги потеряны (GAP-043).
- Пропущенная бронь (состояние `finished`): деньги остаются у клуба, события в журнале нет, фоновой задачи нет.

**BR-190. Лимит количества тарифа с бронями** — `AgentTariffPurchaseService.php:409–430`.
Число покупок = события `tariff.purchased` (покупки в агенте) + брони с `tariff_id` и `cancelled_at IS NULL`
(по одной на ПК). Отменённая бронь лимит освобождает; запуск брони на ПК `tariff.purchased` не пишет (двойного
счёта нет). Кассовые продажи лимит не расходуют (Q-007, как было).

### 3.3. Подписка платформы

Суммы здесь — **float** PHP и `decimal(12,2)` в MySQL. В zafirogo — `int64` тиыны и целочисленные формулы;
равенство со старым результатом для каждой формулы — обязательный тест на граничных значениях (GAP-037…GAP-040).

**BR-180. Сумма счёта** — `SubscriptionBilling.php:51–54`, вызов `:89`.

```php
return round($pricePerComputer * $computers * $months * (100 - $discountPercent) / 100);
```

- Типы: `float` × `int` × `int` × `int` / `100` → `round()` **без знаков** = целые тенге (в PHP 8.4 совпадает
  с `math.Round`, половина — от нуля).
- Порядок: цена × ПК × месяцы × (100 − скидка), потом деление на 100, потом округление **итога** (не цены за ПК).
- В тиынах: `amount = ((P × C × M × (100 − d) + 5000) / 10000) × 100`, `P` — цена за ПК в тиынах, деление целое.
  При цене в целых тенге float-вычисление точно; при цене с тиынами — сверить с `money.PHPRound` (GAP-037).
- Число ПК (`:46–49`): `max(1, число ПК клуба)` — все строки `club_computers` клуба (в сети или нет), минимум 1.
- Снимок на момент выставления: `computers`, `price_per_computer`, `discount_percent`, `reseller_id`,
  `reseller_percent`. Позже добавленные ПК и изменённая цена на счёт не влияют.
- На экране (`billing/index.blade.php:110`) то же считает JS: `Math.round(price * computers * months * (100 −
  discount) / 100)` — в zafirogo сумму считает сервер, во фронте float нет.

**BR-181. Скидка клуба** — `ClubSubscription.php:30–33`, `SubscriptionBilling.php:60–66`.

```php
return $this->discount_until === null || ! $this->discount_until->copy()->endOfDay()->isPast() ? $this->discount_percent : 0;
return max($subscription?->discountPercent() ?? 0, $reseller?->discount_percent ?? 0);
```

Целые проценты. Скидки **не складываются** — берётся большая. Своя действует бессрочно или по последний день
`discount_until` включительно (конец дня). Скидка реселлера — только пока реселлер `is_active`. `0%` стирает дату
и причину.

**BR-182. Цена тарифа со скидкой для показа** — `ClubSubscription.php:36–41`.

```php
return $price === null ? null : round((float) $price * (100 - $this->discountPercent()) / 100, 2);
```

`float`, `round(…, 2)` — случай, где наивный `math.Round` расходится с PHP: нужен `money.PHPRound` или целочисленно
`(P × (100 − d) + 50) / 100` в тиынах. Только показ, в базу не пишется. Учитывает **только свою** скидку клуба (не
реселлера). На экранах выводится `number_format(…, 0, '.', ' ')` — до целых тенге; «за K ПК» —
`number_format(monthlyPrice() * $computers, 0)` (`billing/index.blade.php:22`): произведение уже округлённой цены,
может на тенге отличаться от суммы счёта на 1 месяц (GAP-038).

**BR-183. Оплата счёта и срок подписки** — `SubscriptionBilling.php:96–127`.

```php
$from = collect([now(),
    $subscription->status === 'active'   ? $subscription->current_period_ends_at : null,
    $subscription->status === 'trialing' ? $subscription->trial_ends_at : null,
])->filter()->max();
$until = $from->copy()->addMonthsNoOverflow($invoice->months);
```

Подписка: тариф счёта, `status = active`, `current_period_ends_at = until`, `canceled_at = null`; нет строки
подписки — создаётся (`started_at = now`). Счёт: `paid`, `paid_at`, `confirmed_by`, `period_ends_at = until`.
Истёкшая или приостановленная — от сегодня. `addMonthsNoOverflow`: 31 января + 1 месяц = 28/29 февраля. Повторная
оплата — ошибка «Счёт уже закрыт.», срок не меняется. Активная подписка без даты окончания (бессрочная) после
оплаты получает дату `now + N месяцев` (GAP-039).

**BR-184. Один неоплаченный счёт на клуб** — `SubscriptionBilling.php:69–75`: проверка `exists(pending)` под
блокировкой строки подписки. Отмена — только `pending` → `canceled` (клуб с правом `billing.pay` или суперадмин).

### 3.4. Реселлеры

**BR-185. Скидка реселлера** — BR-181: участвует в `max()`, фиксируется в счёте (`discount_percent`).

**BR-186. Начисление реселлеру** — `SubscriptionBilling.php:116`.

```php
$earning = $invoice->reseller_id === null ? null : round((float) $invoice->amount * (float) $invoice->reseller_percent / 100, 2);
```

- `float × float / 100`, `round(…, 2)` → `money.PHPRound`; в целых: `reseller_amount = (A × bp + 5000) / 10000`,
  `A` — сумма счёта в тиынах, `bp` — процент в базисных пунктах (GAP-040 — сверка на границах).
- Процент — **снимок при выставлении** счёта (`:83`), начисление — **при оплате**, от фактической суммы счёта
  (уже со скидкой).
- Реселлер фиксируется в счёте, только если он активен при выставлении (`:79`); остановка после выставления
  начисление не отменяет; удаление реселлера до оплаты обнуляет `reseller_id` → начисления нет.

**BR-187. Заработано, выплачено, к выплате** — `ResellerController.php:24–26`,
`Superadmin/ResellerController.php:36–37`, `superadmin/resellers/index.blade.php:29`.

```php
$earned  = (float) $paid()->sum('reseller_amount');      // счета реселлера со status = 'paid'
$paidOut = (float) ResellerPayout::query()->where('reseller_id', …)->sum('amount');
$balance = (float) $reseller->earned - (float) $reseller->paid_out;
```

Агрегаты — в SQL; разность — float. Показ `number_format(…, 0)` — целые тенге. В zafirogo — разность `bigint`.

**BR-188. Выплата** — `Superadmin/ResellerController.php:109–122`. Запись `{amount, note, created_by}`;
`amount` `numeric`, 0.01…999 999 999, число знаков **не ограничено** (MySQL округлит до 2). Превышение остатка
**не проверяется** — «к выплате» может стать отрицательным. Исправление или удаление выплаты не предусмотрено.
В форме подставляется `round($balance, 2)`.

**BR-189. Отвязка и удаление** — `:125–152`. Отвязка клуба: новые счета без скидки и процента; уже выставленный
неоплаченный счёт сохраняет снимок (реселлер получит процент). Удаление реселлера: клубы отвязаны, выплаты удалены
каскадом, в счетах `reseller_id = NULL`, `reseller_amount` сохраняется; в журнале платформы остаются записи
`superadmin.reseller_payout` с суммами.

### 3.5. Что идёт через ledger в zafirogo

Журнал `internal/ledger` — клубный (счета клиента, кассы, выручки клуба). По образцу `billing.Charge` /
`billing.Credit` (`docs/schema-changes.md`, «покупки: `member_main −m → выручка +m`; `member_bonus −b → bonus_fund +b`»):

| Операция | Вид (`ledger.Kind`) | Проводки | Ключ идемпотентности | Ссылки / мета |
|---|---|---|---|---|
| Оплата брони (вся группа ПК — одна операция) | **новый** `reservation_payment` | `member_main:U −main` → `revenue_time +main`; `member_bonus:U −bonus` → `bonus_fund +bonus` | заголовок `Idempotency-Key`, если есть; иначе серверный, как у остальных операций приложения (`docs/mobile-compat.md` §1.3). Повтор запроса на те же ПК и время отсекает проверка пересечения броней | новый `Refs.ReservationID` (первая бронь группы); мета: код, делёж по ПК, тариф, минуты |
| Возврат отменённой брони (на каждый ПК) | **новый** `reservation_refund` | обратные: `revenue_time −main` → `member_main +main`; `bonus_fund −bonus` → `member_bonus +bonus` | `reservation_refund:<id брони>` (`ledgerKey`) — вторая отмена невозможна | `Refs.ReservationID`, `ReversesTransactionID` = оплата |
| Запуск времени брони (вход, сотрудник) | — (денег нет) | — | — | часть сессии с `main`/`bonus` тиынами (`InsertSessionPurchase`) |
| Запуск по коду | — | — | — | гостевая сессия без части |
| Досрочный конец сессии, начатой бронью | существующий `session_refund` | как сейчас (`service/end.go:229`) | `session_end:<session_id>` | выручка уже признана при оплате брони — возврат из `revenue_time` корректен |
| Покупка тарифа, обрезанная периодом | существующий `tariff_purchase` | деньги без изменений | как сейчас | + движение минут `+rest` (пакет) |
| Запуск пакета с остатком | существующий `package_start` | только минуты: `−minutes_added` | как сейчас | пакет не удаляется при `rest > 0` |
| Пропущенная бронь | — | — | — | деньги уже в выручке |

Нужно: миграция с новыми значениями `CHECK ledger_transactions.kind` (как `0014`), `ledger/types.go`
(`validKinds`, тест сверки `Kinds()` со схемой), учёт новых видов в сверке балансов (`worker/ledger_reconcile.go`),
в отчётах смены они **не** участвуют (оплата с баланса — не касса, как покупки в агенте).

**Счета за подписку, начисления и выплаты реселлерам** — деньги платформы, а не клуба; в клубный ledger не
ложатся (нет ни счёта клиента, ни кассы клуба). Предложение (UQ-6): документ-таблицы `subscription_invoices` и
`reseller_payouts` + журнал платформы (только добавление) с суммами в тиынах; переходы состояния счёта — под
блокировкой строки и с проверкой статуса (идемпотентность оплаты = `status = 'pending'`); уникальность
неоплаченного счёта — индексом.

### 3.6. Гонки и двойное списание

| Где | Риск | Как закрыто в старом | Что сделать в zafirogo |
|---|---|---|---|
| `GuestBookings::book()` | две брони одного ПК на одно время; два списания | блокировки по порядку: тариф → ПК (по `id`) → строка баланса; пересечение проверяется под блокировкой ПК | тот же порядок (как в `sell()`: ПК, затем баланс); одна транзакция на всю группу; ledger |
| Лимит количества тарифа | бронь и покупка в агенте одновременно | обе блокируют строку тарифа (`lockForUpdate`) | `LockTariffForPurchase` в обоих путях |
| Лимит «2 брони впереди» | две брони одновременно с двух устройств | строка баланса гостя блокируется до подсчёта | считать после блокировки баланса/членства |
| Двойной тап «Забронировать» | две оплаты | вторая упадёт на пересечении с первой | то же + `Idempotency-Key`, если придёт |
| Отмена vs запуск времени | возврат денег и идущая сессия одновременно | обе операции: блокировка ПК → блокировка брони → проверка состояния | сохранить порядок; состояние проверять под блокировкой |
| Отмена группы гостем | часть ПК отменена, часть — нет | состояния проверяются **до** транзакции, затем каждая — под блокировкой; ошибка откатывает всё | проверять только под блокировкой |
| Повторная отмена | двойной возврат | `close()` отклоняет закрытую бронь | + ключ `reservation_refund:<id>` |
| Запуск брони: heartbeat, сотрудник, код — одновременно | два запуска одной брони | блокировка ПК и брони, условие `pending` | то же; запуск идемпотентен по `arrived_at IS NULL` |
| Запуск на каждом heartbeat | лишние запросы (дешёвый `exists` на каждый пинг) | — | не на каждом пинге: при входе гостя и по таймеру (UQ-11); присутствие — в Valkey |
| Подбор кода брони | 10⁶ вариантов | лимит 5/мин на ПК, 200/час на клуб; код годен только на своих ПК и с интервала до начала | те же лимиты на RPC; ответы «не найден» и «не для этого ПК» различаются — оставить как в старом |
| Выставление счёта | два `pending` счёта, если у клуба нет строки подписки (блокировать нечего) | не закрыто | частичный уникальный индекс `(club_id) WHERE status = 'pending'` |
| Оплата счёта | двойное продление | блокировка счёта + проверка `pending` | то же |
| Привязка реселлера | двойная привязка | проверка без блокировки (безвредно) | `UPDATE … WHERE reseller_id IS NULL` |
| Выплата реселлеру | двойной клик — две выплаты | не закрыто | `Idempotency-Key` формы суперадмина |
| Вступление в клуб при входе | два членства при параллельных входах | не закрыто (нет блокировки) | уникальный индекс `(club_id, user_id)` уже есть — `ON CONFLICT DO NOTHING` |

---

## 4. Эталонные сценарии

Формат — как в `docs/golden-scenarios.md`. Суммы — тенге, в скобках тиыны. Источник — `../Zafiro/tests`.

### Сводка

| Файл теста | Новых тестов | Сценарии | P1 / P2 / P3 | Пакет Go |
|---|---:|---|---|---|
| `Feature/SessionTimeTest.php` | 2 | GS-529…GS-530 | 2 / 0 / 0 | `billing` |
| `Feature/GuestSessionTest.php` | 1 | GS-531 | 1 / 0 / 0 | `cash` |
| `Unit/WeeklyPeriodsTest.php` (новый файл) | 2 | GS-532…GS-533 | 2 / 0 / 0 | `billing` |
| `Feature/ApiAuthTest.php` | 1 | GS-534 | 0 / 1 / 0 | `auth` |
| `Feature/SubscriptionTest.php` | 1 | GS-535 | 0 / 0 / 1 | `notify` |
| `Feature/SuperadminCabinetTest.php` | 2 | GS-536…GS-537 | 1 / 1 / 0 | `saas` |
| `Feature/GuestBookingTest.php` (новый) | 14 | GS-538…GS-551 | 9 / 5 / 0 | `billing`, `club`, `agent` |
| `Feature/SubscriptionPaymentTest.php` (новый) | 8 | GS-552…GS-559 | 4 / 3 / 1 | `saas`, `notify` |
| `Feature/ResellerTest.php` (новый) | 6 | GS-560…GS-565 | 1 / 5 / 0 | `saas`, `auth` |
| `Feature/ClubCatalogTest.php` (новый) | 6 | GS-566…GS-571 | 0 / 4 / 2 | `club`, `auth` |
| `Feature/LandingPageTest.php` | 2 | GS-572…GS-573 | 0 / 0 / 2 | `web` |
| **Итого** | **45** | GS-529…GS-573 | **20 / 19 / 6** | |

Изменённые существующие сценарии: GS-081, GS-143, GS-379, GS-382, GS-418, GS-469, GS-471 (в конце раздела).

### Срок тарифа и период действия

#### GS-529 · P1 · `billing` · `TestTariff_EndsWithValidityPeriod`
**Тест:** `SessionTimeTest.php` — «tariff time ends with its validity period: bought at 15:00 for Mon 08:00–18:00, it ends at 18:00»
- **Дано:** понедельник 26.10.2026 15:00 по времени клуба; `timeCustomer()` — клиент с балансом 1000.00 / 0.00, ПК №4 в зоне «Hall» (600 ₸/ч); `timeTariff()` — тариф «День» 600 мин, 1000 ₸, период `mon 08:00 – mon 18:00`.
- **Действие:** `POST /api/tariffs/{id}/purchase {computer_id, balance: "main"}`.
- **Ожидается:** 200, `minutes_added = 180`; `session_ends_at` = 26.10 18:00 клуба; балансы `0.00 / 0.00` (списана полная цена 1000 ₸ = 100 000 тиын за 180 минут вместо 600); пакета нет.
- **Далее:** `session_ends_at` ПК = 18:30, баланс 1000.00 → покупка — 422, ошибка поля `tariff`; балансы `1000.00 / 0.00`.

#### GS-530 · P1 · `billing` · `TestTariff_UsageLimitKeepsRestPastValidity`
**Тест:** `SessionTimeTest.php` — «a usage limit keeps the time past the validity period; the package starts again only up to its end»
- **Дано:** понедельник 17:00; баланс 1000.00; тариф «Пакет» 120 мин, 1000 ₸, лимит использования 30 суток (43 200 мин), периоды `mon 08:00–18:00`, `tue 08:00–18:00`.
- **Действие 1:** покупка → `minutes_added = 60`; пакет `minutes_left = 60`.
- **Действие 2:** вторник 17:30, сессии нет: `POST /api/guest/packages/{id}/start` → `minutes_added = 30`; пакет остаётся, `minutes_left = 30`; `session_ends_at` = вторник 18:00.

#### GS-531 · P1 · `cash` · `TestGuestSession_TariffEndsWithValidity`
**Тест:** `GuestSessionTest.php` — «a guest tariff ends with its validity period, at the same price»
- **Дано:** понедельник 15:00; открытая смена; ПК №1; тариф «День» 600 мин, 2000 ₸, период `mon 08:00–18:00`.
- **Действие:** `POST /club-computers/guest-session {computer_ids: [pc], tariff_id, payment_method: "cash"}`.
- **Ожидается:** 200, `amount = "2000.00"` (200 000 тиын); `session_ends_at` = понедельник 18:00.

#### GS-532 · P1 · `billing` · `TestWeeklyPeriods_EndOf`
**Тест:** `Unit/WeeklyPeriodsTest.php` — «the end of the period now is in; periods following each other run on as one»
- Таблица (момент → периоды → ожидание):

| Момент | Периоды | `endOf` |
|---|---|---|
| пн 26.10.2026 15:00 | `mon 08:00–mon 18:00` | 26.10 18:00 |
| пн 15:00 | `mon 08:00–18:00`, `mon 18:00–22:00` | 26.10 22:00 |
| пн 15:00 | `mon 08:00–mon 23:59` | 27.10 00:00 |
| вс 25.10 10:00 | `sat 22:00–mon 06:00` | 26.10 06:00 |
| пн 26.10 05:00 | `sat 22:00–mon 06:00` | 26.10 06:00 |

#### GS-533 · P1 · `billing` · `TestWeeklyPeriods_EndOfNone`
**Тест:** `Unit/WeeklyPeriodsTest.php` — «no end without a limit or outside the periods»
- пн 15:00: периоды `null` → `null`; `mon 00:00–sun 23:59` → `null`; `tue 08:00–18:00` → `null`.

### Вход, уведомления, скидка

#### GS-534 · P2 · `auth` · `TestMobileLogin_PicksClubWithActiveSubscription`
**Тест:** `ApiAuthTest.php` — «mobile login without a club picks a club of the account with an active subscription»
- **Дано:** аккаунт +79990000005; клуб 1 (членство из фабрики) — пробный период истёк вчера; клуб «Active Club» — подписка `active`, членство `client` (id больше).
- **Действие:** `POST /api/auth/login {phone, password}` без `club_id`.
- **Ожидается:** 200, `club_id` = «Active Club». После `status = expired` у второго клуба — 402 «Подписка клуба не активна.».

#### GS-535 · P3 · `notify` · `TestRegister_NewClubTelegram`
**Тест:** `SubscriptionTest.php` — «a new club is reported to the platform telegram chat»
- Без настроенного бота регистрация клуба ничего не отправляет. С настройкой (`chat_id 42`, язык `ru`): `sendMessage` с `chat_id = "42"`, текст содержит «Новый клуб: Cyber Arena», «Асель, +77001234568», ссылку на карточку клуба в суперадминке.

#### GS-536 · P1 · `saas` · `TestCabinet_ClubDiscount`
**Тест:** `SuperadminCabinetTest.php` — «the superadmin gives a club an individual discount»
- **Дано:** тариф «Pro» 50 000 ₸; подписка клуба продлена на 30 дней на этом тарифе.
- **Ожидается:** `discount_percent 101` → ошибка поля; `discount_until` вчера → ошибка поля; `{20, +10 дней, «Партнёр»}` → `discountPercent() = 20`, `monthlyPrice() = 40000.0` (4 000 000 тиын), причина сохранена; список клубов — «−20%»; карточка клуба и «Подписка» владельца — «40 000 ₸»; за минуту до конца последнего дня — ещё 20; через 2 дня — `monthlyPrice() = 50000.0`; `{0, note: "x"}` → `discount_percent 0`, дата и причина `null`; в журнале платформы 2 события `superadmin.subscription_discount_changed`.

#### GS-537 · P2 · `saas` · `TestCabinet_DiscountNeedsSubscription`
**Тест:** `SuperadminCabinetTest.php` — «a club without a subscription cannot get a discount» — клуб без подписки → 404.

### Брони из приложения (`GuestBookingTest.php`)

Подготовка: `bookingClub()` — пояс клуба `UTC`, `booking_interval = 10`, `app_booking_enabled = true`;
`bookingComputer(n, price = "600.00")` — ПК в зоне «VIP» с ценой часа 600 ₸; `bookingGuest(balance = "1000.00",
bonus = "0.00")` — гость и токен приложения `mobile`.

#### GS-538 · P2 · `club` · `TestAppBooking_OffByDefault`
**Тест:** «booking from the app is off until the club switches it on»
- `app_booking_enabled = false`: `GET /api/guest/bookings` → 200, `enabled: false`; `POST /api/guest/bookings` → 403; броней 0.

#### GS-539 · P1 · `billing` · `TestAppBooking_PaysZonePrice`
**Тест:** «a guest books a free PC and pays the zone price from the balance»
- **Дано:** 15.11.2026 10:00 UTC; ПК-01, ПК-02 (VIP, 600 ₸/ч); баланс 1000.00.
- **Действие:** `POST /api/guest/bookings {club_computer_id: ПК-01, starts_at: "2026-11-15T17:00:00+05:00", duration_minutes: 90}`.
- **Ожидается:** 201; `booking.computer "PC-01"`, `zone "VIP"`, `starts_at "2026-11-15T12:00:00+00:00"`, `paid "900.00"`, `state "pending"`; в БД `from_app = true`, `paid_main_cents = 90000`, `ends_at "2026-11-15 13:30:00"`; баланс `100.00` (10 000 тиын); журнал `reservation.paid` от гостя.
- **Далее:** `GET options {starts_at 13:35 UTC, 60 мин}` → `zones[0].price "600.00"`; ПК-01 `available: false` (интервал 10 мин), ПК-02 `true`.
- **Далее:** бронь ПК-02 на 15:00, 60 мин при балансе 100.00 → 422, ошибка поля `balance`; броней 1.

#### GS-540 · P2 · `club` · `TestAppBooking_Limits`
**Тест:** «a guest has at most two bookings ahead and cannot book in the past or too far»
- 15.11 10:00 UTC, баланс 10000.00, брони по 60 мин: начало 09:00 → 422 `starts_at`; 30.11 09:00 → 422 `starts_at`; 12:00 → 201; 14:00 → 201; 16:00 → 422 `starts_at` (уже 2 брони).

#### GS-541 · P1 · `billing` · `TestAppBooking_GuestCancelRefund`
**Тест:** «the guest cancels a booking before it starts and gets the money back, when the club allows it»
- Бронь 12:00, 60 мин: баланс `400.00`. `POST /api/guest/bookings/{id}/cancel` → 200; `cancelled_at` задан; баланс `1000.00`; `GET /api/guest/history` — первые два `kind`: `refund`, `purchase`.
- `app_booking_cancel = false`: бронь 14:00 → 201, `can_cancel: false`; отмена → 422; баланс `400.00`.

#### GS-542 · P2 · `club` · `TestAppBooking_CancelOnlyOwnBeforeStart`
**Тест:** «a guest cannot cancel a booking of someone else or one that has started»
- Бронь 10:30. Чужой гость отменяет → 404. В 10:40 владелец отменяет → 422.

#### GS-543 · P1 · `billing` · `TestAppBooking_StaffCancelRefunds`
**Тест:** «staff cancelling a paid booking give the money back»
- `app_booking_cancel = false`; бронь 12:00, 60 мин (баланс 400.00). Сотрудник: `POST /reservations/{id}/cancel` → без ошибок; баланс `1000.00`.

#### GS-544 · P1 · `agent` · `TestAppBooking_StartsWhenGuestSignsIn`
**Тест:** «the paid time starts when the guest signs in on the booked PC during the booking»
- Бронь 12:00–13:00. Heartbeat ПК с токеном гостя в 10:00 → `session: null` (рано). В 11:55 с токеном **другого** гостя → `session: null`. В 11:55 с токеном гостя → `session.user_id` = гость, `session.ends_at "2026-11-15T12:55:00+00:00"`; `arrived_at` задан; часть сессии `{minutes: 60, main_cents: 60000}`.

#### GS-545 · P1 · `billing` · `TestAppBooking_LateArrivalByStaff`
**Тест:** «a guest coming late gets the rest of the booking; staff marking the arrival start it too»
- Бронь 12:00–13:00 (600 ₸). В 12:30 сотрудник: `POST /reservations/{id}/arrive` → ПК: `current_session_user_id` = гость, конец 13:00; часть сессии `{minutes: 30, main_cents: 30000}` (300 ₸ — пропорционально, остальное не возвращается).

#### GS-546 · P1 · `billing` · `TestAppBooking_PurchaseDoesNotCloseBooking`
**Тест:** «buying time on the PC does not close the guest's paid booking»
- Баланс 2000.00; бронь 11:00, 60 мин; в 10:00 гость покупает 60 минут на том же ПК (`POST /api/guest/minutes/purchase`) → 200; `arrived_at` брони остаётся `null`.

#### GS-547 · P1 · `billing` · `TestAppBooking_AsTariff`
**Тест:** «a guest books a PC as a tariff of its zone: the tariff price, its time, its purchase limit»
- **Дано:** понедельник 16.11.2026 10:00 UTC; ПК-01 (VIP 600 ₸/ч), ПК-02 (зона «Общий» 300 ₸/ч); баланс 5000.00; тариф «3 часа»: 180 мин, 1200 ₸, зона VIP, лимит количества 1; тариф «Ночь»: 600 мин, 2000 ₸, период `mon 22:00 – tue 08:00`.
- `GET tariffs {starts_at 12:00}` → 1 тариф: `{id «3 часа», minutes 180, price "1200.00", zone_ids [VIP]}`.
- `GET tariffs {starts_at 23:00}` → у «Ночь» `minutes = 540` (обрезан в 08:00).
- `GET options {12:00, tariff_id}` → `minutes 180`, зоны `{VIP: "1200.00"}`.
- Бронь ПК-02 этим тарифом → 422 `club_computer_ids`. Бронь ПК-01 → 201, `tariff "3 часа"`, `paid "1200.00"`, `ends_at 15:00`; `tariff_id` в БД; баланс `3800.00`.
- Вторая бронь тарифом на 16:00 → 422 `tariff_id` (лимит 1 исчерпан).
- В 12:00 heartbeat с токеном гостя → `session.ends_at 15:00`; часть сессии `{kind: "tariff", tariff_id, minutes: 180}`.

#### GS-548 · P2 · `club` · `TestAppBooking_ComputersFirst`
**Тест:** «the guest picks a PC first: the club PCs by zone, then only the tariffs of its zone»
- VIP (600 ₸): ПК-01; «Общий» (без цены): ПК-02, ПК-03 (`is_reserved`). Тарифы: «VIP 3 часа» (зона VIP), «Час» 60 мин 300 ₸ (без зон).
- `GET computers` → зоны `["VIP", "Общий"]`; `price_per_hour`: `"600.00"`, `null`; ПК «Общего»: `[{id ПК-02, name "PC-02"}]`.
- `GET tariffs {computer_id}`: для ПК-02 — `[«Час»]`; для ПК-01 — `[«Час», «VIP 3 часа»]` (порядок: длительность, имя).

#### GS-549 · P2 · `club` · `TestAppBooking_ClubTimezoneInTexts`
**Тест:** «the journal and the agent show booking times in the club time zone»
- 16.11 03:00 UTC; пояс клуба `Asia/Almaty` (UTC+5); бронь на 05:30 UTC, 60 мин → детали `reservation.paid.starts_at = "16.11.2026 10:30"`. Другой гость покупает 150 минут на этом ПК → 422, `errors.computer_id[0]` содержит «10:30».

#### GS-550 · P1 · `billing` · `TestAppBooking_SeveralPCsOneCode`
**Тест:** «a guest books several PCs together: one payment, one code, cancelled together»
- 17.11 10:00 UTC; ПК-01, ПК-02 (VIP 600 ₸/ч); баланс 2000.00.
- `POST {club_computer_ids: [1, 2], starts_at 12:00, duration_minutes 60}` → 201, `computers ["PC-01","PC-02"]`, `paid "1200.00"`, `code` — 6 цифр, один на обе брони; баланс `800.00`.
- `GET /api/guest/bookings` → 1 запись. Отмена по `booking.id` → обе брони отменены; баланс `2000.00`.

#### GS-551 · P1 · `agent` · `TestBookingCode_OpensOnlyBookedPCs`
**Тест:** «the booking code opens only the booked PCs, from the interval before the booking, without an account»
- Бронь ПК-01 и ПК-02 на 12:00, 60 мин; ПК-03 не забронирован; интервал 10 мин.
- 10:00: `POST /api/agent/booking-code {code}` с ПК-01 → 422 `code` (рано).
- 11:55: с ПК-03 → 422, текст содержит «не для этого ПК»; с ПК-01 неверный код → 422; с ПК-01 верный → 200, `session.guest: true`, `session.ends_at "2026-11-17T12:55:00+00:00"`; с ПК-02 → 200, `guest: true`.
- Повтор кода на ПК-01 → 422; ожидающих броней 0; на ПК-03 сессии нет.

### Счета за подписку (`SubscriptionPaymentTest.php`)

Подготовка: `billingClub(n = 3)` — клуб с `n` ПК, тариф «Базовый» 1000 ₸, Kaspi `{url: "https://pay.kaspi.kz/pay/zafiro", recipient: "ИП Zafiro"}`.

#### GS-552 · P1 · `saas` · `TestInvoice_OwnerOpensWithDiscount`
**Тест:** «the owner opens a bill for all the club PCs, less the discount, and sees how to pay it»
- 3 ПК, скидка клуба 10 %. «Подписка»: «Продлить подписку», «ПК в клубе: 3».
- `POST /billing/invoices {saas_plan_id, months: 3}` → счёт: `amount = 8100.0` (1000 × 3 × 3 − 10 % = 810 000 тиын), `computers 3`, `discount_percent 10`, `status pending`.
- Страница: номер счёта, «8 100 ₸», ссылка Kaspi, «ИП Zafiro»; кнопки «Выставить счёт» нет.
- Второй счёт → ошибка `invoice`, счетов 1. `POST …/cancel` → `canceled`.

#### GS-553 · P2 · `saas` · `TestInvoice_ValidMonthsPlansKaspi`
**Тест:** «only allowed months, payable plans and a configured Kaspi link make a bill»
- `months: 2` → ошибка `months`; тариф без цены («Пробный период») → ошибка `saas_plan_id`; Kaspi не настроен → ошибка `invoice`; счетов 0.

#### GS-554 · P2 · `saas` · `TestInvoice_PayRightAndTenant`
**Тест:** «staff without the pay right cannot open or cancel bills, and other clubs bills are out of reach»
- Менеджер: страница — «Продлить подписку может владелец клуба»; `POST /billing/invoices` → 403. Владелец отменяет счёт другого клуба → 404, счёт остаётся `pending`.

#### GS-555 · P1 · `saas` · `TestInvoice_ExpiredClubCanPay`
**Тест:** «an expired club can still pay»
- Пробный период истёк: `GET /dashboard` → редирект на «Подписку»; `POST /billing/invoices {months: 1}` → редирект на «Подписку», счёт создан.

#### GS-556 · P1 · `saas` · `TestInvoice_PaidExtendsFromCurrentEnd`
**Тест:** «the superadmin marks a bill paid and the subscription runs on from its current end»
- 2 ПК; подписка `active` до `now + 10 дней`; счёт на 6 месяцев, 12 000 ₸. «Оплаты»: номер, «12 000 ₸». `POST /superadmin/invoices/{id}/pay` → подписка `active`, тариф счёта, конец = прежний конец `+ 6 месяцев` (`addMonthsNoOverflow`); счёт `paid`, `confirmed_by` = суперадмин; 1 событие `superadmin.invoice_paid`. Повтор → ошибка `invoice`, конец не меняется.

#### GS-557 · P1 · `saas` · `TestInvoice_ExpiredCountsFromToday`
**Тест:** «a paid bill of an expired club counts from today»
- Подписка `expired`, конец месяц назад; счёт на 1 месяц → после оплаты конец = `now + 1 месяц`.

#### GS-558 · P2 · `saas` · `TestInvoice_KaspiAndCancelBySuperadmin`
**Тест:** «the superadmin sets the Kaspi link and cancels bills; clubs cannot reach the cabinet»
- Владелец клуба: `POST /superadmin/invoices/{id}/pay` → 403. Суперадмин: `url: "http://…"` → ошибка `url`; `https://pay.kaspi.kz/pay/new` + «ТОО Zafiro» → сохранено; `POST …/cancel` → `canceled`.

#### GS-559 · P3 · `notify` · `TestInvoice_TelegramAlert`
**Тест:** «a new bill is reported to the platform telegram chat»
- 2 ПК, счёт на 1 месяц: `sendMessage`, текст содержит «Счёт ZF-0000N: {клуб}», «Базовый, 1 мес., 2 ПК», «К оплате 2 000 ₸», ссылку на «Оплаты».

### Реселлеры (`ResellerTest.php`)

Подготовка: `makeReseller(code = "arman-games", percent = 10, discount = 15, active = true)`.

#### GS-560 · P2 · `saas` · `TestReseller_CreatedBySuperadmin`
**Тест:** «the superadmin creates resellers: a new account, or an existing one by phone»
- Занятый код → ошибка `code`; новый телефон без пароля → ошибка `password`; `{phone: "8 700 123-45-67", code: "Arman-Games", 10, 15}` → реселлер `arman-games`, телефон `+77001234567`, процент `10.0`, скидка `15`. Существующий аккаунт без пароля → реселлер `partner`; ещё раз тот же телефон → ошибка `phone`. Не суперадмин → 403.

#### GS-561 · P2 · `auth` · `TestReseller_HostSignInOnly`
**Тест:** «only resellers sign in on the reseller host; there is no sign-up there»
- Хост реселлера: `/login` — без «Зарегистрироваться»; вход не-реселлера → ошибка `phone`, гость; вход реселлера → редирект в кабинет; кабинет: код и «Пока ни один клуб не ввёл ваш код.»; `/dashboard` и `/register` на этом хосте → редирект в кабинет.

#### GS-562 · P2 · `saas` · `TestReseller_ClubEntersCodeOnce`
**Тест:** «a club enters the reseller code once; unknown or stopped codes are refused»
- `nobody` → ошибка `reseller_code`; `stopped` (остановлен) → ошибка; `" Arman-Games "` → привязан; настройки: «Клуб подключён через реселлера Arman (код arman-games).»; повторный ввод → ошибка, привязка прежняя.

#### GS-563 · P1 · `saas` · `TestReseller_DiscountAndCommission`
**Тест:** «a club of a reseller pays with the reseller discount and the reseller earns their percent»
- 2 ПК, тариф 1000 ₸; своя скидка клуба 5 %; реселлер: процент 10, скидка 20.
- Счёт на 3 месяца: `amount = 4800.0` (1000 × 2 × 3 − 20 % = 480 000 тиын), `discount_percent 20`, `reseller_id` задан.
- Оплата: `reseller_amount = 480.0` (48 000 тиын).
- Выплата 300 ₸ («Kaspi»): суперадмин видит «к выплате 180 ₸»; кабинет реселлера — «480 ₸», «180 ₸», имя клуба.

#### GS-564 · P2 · `saas` · `TestReseller_UpdateStopDetach`
**Тест:** «the superadmin sets the percent and discount, stops a code and unlinks a club; others cannot»
- Не суперадмин → 403. `{12.5, 10, is_active: 0}` → процент `12.5`, скидка `10`, не активен; `discountFor(клуб) = 0`. Отвязка → `reseller_id = null`.

#### GS-565 · P2 · `saas` · `TestReseller_Delete`
**Тест:** «the superadmin deletes a reseller: the account stays, clubs are unlinked, payouts go»
- Реселлер с выплатой 100 ₸ и клубом. Не суперадмин → 403. Удаление → реселлеров 0, аккаунт есть, клуб отвязан, выплат 0. Тот же аккаунт снова становится реселлером с тем же кодом.

### Каталог и профиль клуба (`ClubCatalogTest.php`)

#### GS-566 · P3 · `club` · `TestClubLocation_Saved`
**Тест:** «the club owner sets the city and address of the club»
- Настройки: «Город и адрес», «Актобе». `city: "atlantis"` → ошибка; `{aktobe, "пр. Абилкайыр хана, 1"}` → сохранено, 1 событие `club.location_changed`. Кассир → 403.

#### GS-567 · P2 · `club` · `TestCatalog_ClubsOfCity`
**Тест:** «anyone sees the clubs of a city, with the cities and their coordinates»
- Клубы: «Бета» (aktobe, адрес), «Альфа» (aktobe), «Алматинский» (almaty), «Без города».
- `GET /api/catalog/clubs?city=aktobe` → `city "aktobe"`, 2 клуба, первый «Альфа», у второго адрес; «Без города» нет; город `aktobe`: `{name: "Актобе", clubs: 2, lat: число}`. Без `city` → `clubs: []`. `city=atlantis` → 422.

#### GS-568 · P2 · `auth` · `TestCatalog_LoginJoinsClub`
**Тест:** «a guest signs in to a club of the list, joins it and does not depend on its subscription»
- Клуб с городом **без подписки**; клуб без города. Вход с `club_id` клуба без города → 403. Вход с `club_id` клуба с городом (`device_name: "mobileclient"`) → 200, `club_id`, `user.balance = 0`; членство `client`, строка баланса, журнал `user.joined_club`; подписки нет.
- С токеном: `GET /api/auth/me`, `/api/guest/news`, `/api/guest/history` → 200; `GET /api/tariffs` → 402. Повторный вход → членство одно.

#### GS-569 · P2 · `auth` · `TestCatalog_SwitchClubJoins`
**Тест:** «a signed-in guest moves to a club of the list and joins it; staff still need the subscription»
- `POST /api/auth/switch-club {club_id}` → 200, клуб в списке `{current: true, role: "client"}`. Сотрудник (`admin`) клуба без подписки: вход → 402 «Подписка клуба не активна.».

#### GS-570 · P3 · `club` · `TestClubProfile_Saved`
**Тест:** «the club fills its profile for the app: description, contacts, photos and zone equipment»
- Зона VIP 800 ₸, ПК с `cpu_model "11th Gen Intel(R) Core(TM) i5-11400F @ 2.60GHz"`, `ram_total_mb 16253`: страница — «Зоны и оборудование», `placeholder="Intel Core i5-11400F"`, `placeholder="16 ГБ"`.
- Профиль `{description, phone "+7 700 000 00 00", hours, instagram "@asia.game"}` → в БД `instagram: "asia.game"`; `phone: "<script>"` → ошибка.
- 2 PNG → сохранены; не картинка → ошибка; «обложка» второй → она первая по `sort_order`; удаление первой → файла нет.
- Зона: `{description: "Тихая комната", devices: {monitor: "ASUS 240 Гц", gpu: "", mouse: "Logitech G102"}}` → `devices = {monitor, mouse}`.
- Фото по адресу → 200; другое имя (40 символов) → 404. Кассир → 403.

#### GS-571 · P2 · `club` · `TestCatalog_CardsAndClubPage`
**Тест:** «the app shows the club cards of a city and the club page with zones, free PCs and equipment»
- Клуб `aktobe`, профиль; VIP (800 ₸, `devices.monitor`): ПК1 в сети с `gpu_model`, ПК2 в сети `is_reserved`; Main (500 ₸): ПК3, не в сети час; 1 фото.
- Список: `pcs 3`, `free_pcs 1`, `price_from 500`, `hours`, `photo_url`.
- Страница: `city "Актобе"`, описание, `photos [url]`, `zones[0]`: VIP, `pcs 2`, `free_pcs 1`, `devices = [{gpu, "Видеокарта", "NVIDIA GeForce RTX 3060"}, {monitor, "Монитор", "ASUS 240 Гц"}]`; `zones[1].free_pcs 0`. Клуб без города → 404.

### Лендинг (`LandingPageTest.php`)

#### GS-572 · P3 · `web` · `TestLanding_MenusAndProductPages`
**Тест:** «the header menus lead to the features, agent and app pages»
- Главная: «О продукте», «Продукты», ссылки на 4 страницы. `/product/features`: «Возможности Zafiro», «Касса и деньги», «Клиенты и лояльность», ссылка на агента и `#faq` главной. `/product/agent`: «Агент Zafiro на ПК», «Обойти оплату не получится», ссылка на установщик и регистрацию. `/product/guest-app`: «Приложение для гостей», «Вход на ПК по QR-коду». `/product/staff-app`: «Приложение для сотрудников», «Права как в панели». Вошедший — ссылка на панель.

#### GS-573 · P3 · `web` · `TestPricing_ListsActivePlans`
**Тест:** «the pricing page lists the plans open for subscription»
- Тарифы: «Клуб до 30 ПК» 25 000 ₸, `features ["До 30 ПК"]`, активен; «Старый тариф» не активен. Главная ссылается на `/pricing`; страница: название, «25 000 ₸», «за ПК в месяц», «До 30 ПК», без «Старый тариф», ссылка на регистрацию.

### Изменённые существующие сценарии

| ID | Тест | Что изменилось |
|---|---|---|
| GS-081 | `AgentShopTest.php` — «…one PC at a time for customers» | добавлено: токен приложения `mobileclient`, использованный только что, не мешает входу на `PC-02` → 200 |
| GS-143 | `ProductDeleteTest.php` | добавлено: после удаления товара `GET /warehouse` → 200 и содержит «Coca Cola» |
| GS-379 | `SuperadminCabinetTest.php` — «extends, suspends…» | второе продление шлёт `days: "10"` (строка формы) |
| GS-382 | `SuperadminCabinetTest.php` — «creates and edits platform plans» | `features: "До 20 ПК\r\n\nАгент Windows\nСклад и касса для всех ПК"` → `["До 20 ПК", "Агент Windows", "Склад и касса для всех ПК"]` |
| GS-418 | `LandingPageTest.php` | заголовок «Весь клуб - в одном окне» (дефис вместо тире) |
| GS-469 | `SettingsTest.php` | число адресов на `/settings/api`: 26 → 28 (экран не переносится, Q-059) |
| GS-471 | `SettingsTest.php` | на «Настройка клуба» карточек-секций: 8 → **11** |

---

## 5. Правила без тестов (GAP)

Числа посчитать по коду и один раз прогнать на старом Laravel (оракул `tools/php-oracle`, PHP 8.4), как GAP-001…036.

| ID | Приор. | Правило | Что проверить |
|---|---|---|---|
| GAP-037 | P1 | BR-180 | Сумма счёта при цене с тиынами и на границе «…,5 ₸»: цена 1250 ₸, 3 ПК, 1 мес., скидка 15 % → 3187,5 → **3188 ₸**; цена 999,99 ₸; скидка 100 % → 0 ₸; 0 ПК → считается как 1 |
| GAP-038 | P2 | BR-182 | `monthlyPrice()` на границе третьего знака; расхождение «N ₸ за K ПК» на экране и суммы счёта на 1 месяц |
| GAP-039 | P1 | BR-183 | Оплата счёта: подписка `trialing` (от конца пробного периода); бессрочная `active` без даты (получает дату); `suspended`; нет строки подписки; конец 31.01 + 1 мес. |
| GAP-040 | P1 | BR-186 | Начисление реселлеру на границе: сумма 4810 ₸ × 12,35 % = 594,035 → 594,04 или 594,03; процент 0; процент 100 |
| GAP-041 | P1 | BR-176 | Делёж бонусов по ПК: 3 ПК по 600 ₸, смешанная оплата, бонусный лимит 100 %, основной баланс 0,01 ₸ → последнему ПК достаётся бонусов больше его цены (`paid_main_cents < 0`). Числа для обычных случаев: 2 ПК разных зон (600 и 300 ₸), бонусы 450 ₸ |
| GAP-042 | P1 | BR-176 | Бронь при выключенной смешанной оплате; тариф «только основной баланс»; бонусный лимит в процентах от общей суммы |
| GAP-043 | P1 | BR-179 | Возврат, когда у гостя нет строки баланса: деньги теряются (аналог Q-012 — исправлять) |
| GAP-044 | P1 | BR-178 | Запуск на ПК, где у гостя уже идёт своя сессия (время добавляется); сотрудник отмечает приход задолго до начала; запуск за секунду до конца брони (`minutes = 1`) |
| GAP-045 | P1 | BR-178, BR-032 | Досрочное завершение сессии, начатой бронью: возврат по почасовой части при опоздании (`main_cents` уже уменьшен) |
| GAP-046 | P1 | BR-184 | Два одновременных счёта у клуба без строки подписки |
| GAP-047 | P1 | BR-171 | Обрезка при продлении идущей сессии (начало — конец сессии, а период — текущий); `ceil` при неполной минуте; тариф без лимита использования теряет минуты за полную цену |
| GAP-048 | P1 | BR-172 | Пакет, обрезанный сроком годности, остаётся с остатком |
| GAP-049 | P2 | BR-190 | Лимит количества: бронь на 2 ПК при остатке 1; отменённая бронь освобождает лимит; бронь + покупка в агенте |
| GAP-050 | P2 | BR-179 | Сотрудник отменяет один ПК из группы: возврат только его части; гость после этого отменяет остальное |
| GAP-051 | P2 | BR-177 | Бронь на ПК с идущей сессией, которая кончится до начала брони минус интервал; ПК в зоне без цены часа; 11 ПК |
| GAP-052 | P3 | Б, В, Д | Новые события журнала клуба (`billing.invoice_*`, `club.location_changed`, `club.profile_changed`, `club.photos_added`, `club.photo_deleted`, `club.zone_devices_changed`, `club.reseller_attached`) не имеют подписей в `ActivityLog::EVENTS` — как они показываются в «Журнале действий» |
| GAP-053 | P2 | BR-188 | Выплата больше остатка (отрицательный остаток); сумма с тремя знаками |
| GAP-054 | P2 | BR-189 | Реселлер остановлен или удалён между выставлением и оплатой счёта |
| GAP-055 | P2 | BR-181 | Последний день скидки: в каком поясе считается «конец дня» (`ClubSubscription` использует `UsesClubTimezone`) |
| GAP-056 | P2 | В | «Свободные ПК» каталога: ПК с бронью на сейчас считается свободным; ПК с сотрудником — нет |
| GAP-057 | P2 | В | Вступление в клуб: заблокированный аккаунт; параллельные входы; запрос от агента ПК с `club_id` чужого клуба |
| GAP-058 | P2 | Г | Код брони: уникальность только среди броней клуба с `ends_at > now − 1 день`; один код на бронь из одного ПК |

---

## 6. Что затронуто в zafirogo (сводно)

| Область | Файлы / объекты | Темы | Объём |
|---|---|---|---|
| Схема | новая миграция `db/migrations/0034_upstream_2026_11.sql` (или несколько): `club_subscriptions` +3, `subscription_invoices`, `clubs` +5, `club_zones` +1, `club_photos`, `computer_reservations` +5, `resellers`, `reseller_payouts`, `CHECK ledger_transactions.kind`; RLS для `subscription_invoices`, `club_photos`; `docs/schema-changes.md` | Б В Г Д | M |
| Перенос данных | `server/cmd/migrate-from-mysql`, `server/internal/migrate/*`, `tools/migrate-from-mysql/README.md` — новые таблицы и колонки, деньги ×100 (кроме `paid_*_cents`), проценты → базисные пункты | Б В Г Д | M |
| Цены и время | `billing/pricing/weekly.go`, `tariff.go`, `purchase.go`, `guest.go` + тесты и оракул | А2, Г | L |
| Сервис биллинга | `billing/service/purchase.go` (`PurchaseTariff:77`, `StartPackage:336`, `sell:533`), `guest.go` (`SellGuestSession:107`), `end.go`, `packages.go`; новые `booking.go` | А2, Г | L |
| Брони | `internal/reservations/reservations.go` (`closeBooking:163`), `db/queries/reservations.sql`, `purchases.sql` (`MarkReservationsArrived:114`, `GetTariffSales:63`) | Г | L |
| Ledger | `ledger/types.go` (виды, `Refs`), `ledger/post.go`, `worker/ledger_reconcile.go`, `billing/balance.go`, `spending.go` | Г | M |
| Старый REST | `http/legacy/routes.go`, `auth.go` (`doLogin:558`, `switchClub`, `subscription:294`), `guest.go` (история), новые `catalog.go`, `bookings.go`; `db/queries/mobile_api.sql`; `docs/mobile-compat.md` | А4 В Г З | L |
| Агент (сервер) | `agent/guestapi/{auth,history,tokens}.go`, `agent/stream`, `agent/presence`; `proto/agent/v1/guest.proto`; `docs/agent-protocol.md` | Г Ж | M |
| Агент (C#) | `agent/src/Zafiro.Agent.UI/Frontend/*`, `Zafiro.Agent.Core/Guest`, `Zafiro.Agent.Protocol` | Ж | M |
| Панель клуба (сервер) | `http/sections/billing`, `settings` (`handlers.go`, `save.go`, `values.go`, `images.go`, `zones.go`), `reservations`; `api/billing.yaml`, `settings.yaml`, `reservations.yaml`; `auth/permissions/{permissions,routes}.go`; `http/subscription.go` | А9 Б В Г Д | L |
| Суперадминка (сервер) | `http/sections/superadmin/clubs.go`, новые `invoices.go`, `resellers.go`; `api/superadmin.yaml`; `db/queries/superadmin.sql`; `telegram/alerts.go`; `sections/auth/register.go` | А7 Б Д | L |
| Реселлер | новый `http/sections/reseller`, `api/reseller.yaml`, `http/reseller_host.go`, `config` | Д | M |
| Публичные страницы | `http/sections/public/public.go`, `api/public.yaml`, `db/queries/public_pages.sql` | Е | S |
| Фронт: панель | `features/billing/BillingPage.tsx`, `features/settings/{ClubSettingsPage,SettingsAnchors}.tsx` + новая `ProfilePage.tsx`, `features/reservations/ReservationsPage.tsx`, экран прав | А9 Б В Г Д | L |
| Фронт: суперадминка | `features/superadmin/{ClubsPage,ClubPage,PlansPage,TelegramPage,SuperadminLayout,host}.tsx` + новые `InvoicesPage.tsx`, `ResellersPage.tsx` | А7 Б Д | M |
| Фронт: реселлер и вход | новые `features/reseller/*`, `routes/_auth/login.tsx`, `router.tsx` | Д | M |
| Фронт: лендинг | `features/public/LandingPage.tsx` → каркас + 5 страниц; `web/e2e` (эталоны) | Е | M |
| Переводы | импорт 537 + 7 ключей × 3 языка (`server/internal/i18n`), 7 ключей агента | все | S |
| Документы | `business-rules.md` (BR-170…BR-190), `golden-scenarios.md` (GS-529…GS-573, GAP-037…058), `MIGRATION.md`/`parts`, `mobile-compat.md`, `agent-protocol.md`, `schema-changes.md`, `deploy.md` (домен реселлера) | все | S |

---

## 7. Открытые вопросы

Рекомендации — по принципам `docs/DECISIONS.md` (1 — деньги точно; 2 — поведение старого кода; 3 — без новых
функций; 4 — безопасность по умолчанию).

| ID | Вопрос | Рекомендация |
|---|---|---|
| UQ-1 | **«Мобильные приложения отложены» (CLAUDE.md, решение 3) против новых маршрутов приложения.** Сами приложения не переносятся, но новая версия `mobileclient` без `GET /api/catalog/clubs` не открывается, а вход всегда идёт с `club_id`. | Серверную часть (каталог, брони, вступление) переносить в слой `legacy` — это требование Q-058 («приложения не меняются»). Flutter не трогать. |
| UQ-2 | **Каталог клубов при поклубном переходе.** Каталог — список клубов всей платформы, запрос без токена; клубы какое-то время живут на двух серверах, а у приложения один адрес. | До переноса последнего клуба каталог отдаёт тот сервер, куда смотрит приложение, со своими клубами + пересланным списком второго сервера (объединение по городу). Проще: клубы с городом (участники каталога) переносить последними и одной волной (Q-058 уже требует этого для клубов с активными мобилками). Нужен ответ владельца. |
| UQ-3 | **Реселлеры и счета — данные платформы.** Реселлер видит клубы и начисления; при поклубном переходе они разнесены по двум серверам. | Источник правды по реселлерам, счетам и выплатам — старый сервер до конца перехода; в новом — код готов, данные переносятся в последней волне. Кабинет реселлера на новом сервере не открывать раньше. Нужен ответ владельца. |
| UQ-4 | **Граница переноса.** Старый проект продолжает меняться (41 коммит за 20 дней, все — новые функции). | Зафиксировать новую отметку (`zafirogo-base-2` = `a680bab`), повторять такую инвентаризацию перед каждым этапом; принцип 3 относится к нашим добавлениям, а не к функциям источника правды. |
| UQ-5 | **Цена «за ПК в месяц».** Смысл `saas_plans.price_monthly` изменился без миграции данных; суперадмин по-прежнему может продлевать подписку «на N дней» без счёта. | Перенести как есть: подписи «за ПК в месяц» в `PlansPage`, `BillingPage`, `/pricing`; оба способа продления остаются. Лимит ПК по тарифу (PD-2) не связан с ценой — не менять. |
| UQ-6 | **Ledger для денег платформы** (счета, начисления и выплаты реселлерам). CLAUDE.md: «все движения денег — через ledger». | Клубный ledger не расширять: это не балансы клиентов и не касса клуба. Документ-таблицы в тиынах + журнал платформы (только добавление). Подтвердить у владельца. |
| UQ-7 | **Float в деньгах платформы** (BR-180, BR-182, BR-186, BR-187). | Целочисленные формулы в тиынах и базисных пунктах; совпадение со старым — тестами GAP-037, 038, 040 через оракул PHP 8.4. Ввод суммы выплаты и процента с лишними знаками — отклонять (Q-002). |
| UQ-8 | **Отрицательная основная часть при дележе бонусов** (GAP-041). В старом — ошибка базы, бронь не создаётся. | Исправить (принцип 2, баг): доля бонусов ПК не больше его цены, излишек — предыдущим ПК; для всех случаев, где старый код работает, числа те же. Тест и запись в DECISIONS. |
| UQ-9 | **Возврат без строки баланса** (GAP-043) — деньги теряются. | Исправить по образцу Q-012: создать строку баланса и зачислить. |
| UQ-10 | **Удаление реселлера стирает выплаты.** | Как в старом (принцип 2): суммы выплат остаются в журнале платформы (`superadmin.reseller_payout`). Журнал платформы — только добавление; проверить, что его чистка по сроку (`worker/log_retention.go`) такие записи не удаляет. |
| UQ-11 | **Запуск оплаченной брони вошедшего гостя.** В старом — проверка на каждом heartbeat ПК (в базу на каждый пинг — против правил производительности). | При входе гостя на ПК (`Login`, подтверждение QR) и фоновой задачей раз в минуту по гостям, которые уже сидят на ПК (`internal/worker`, один исполнитель — Q-086); агенту — обычное событие сессии. Поведение для гостя то же. |
| UQ-12 | **Гость не зависит от подписки клуба**: перевод другу и отмена брони с возвратом работают в клубе без подписки; вход клиента в клуб без подписки разрешён. | Перенести как есть; агент ПК и сотрудники по-прежнему получают 402. Проверить, что gRPC `GuestService` (агент) подписку проверяет. |
| UQ-13 | **Вступление в клуб по `club_id` из запроса** против правила «клуб берётся из аутентификации, никогда из параметров запроса». | Это вход (до аутентификации), `club_id` там был и раньше. Членство создаётся через пул платформы в одной транзакции; условие «клуб с городом» проверять на сервере; лимит входа `customer-login` уже есть. Заблокированный аккаунт — не вступает (проверка блокировки идёт раньше). |
| UQ-14 | **Код брони**: 6 цифр, хранится открытым текстом, сотрудник видит его в списке броней. | Как в старом + лимиты RPC (5/мин на ПК, 200/час на клуб, принцип 4). Не хешировать: код показывается гостю и сотруднику повторно. |
| UQ-15 | **Сессия по коду — без аккаунта и без части сессии**: досрочное завершение ничего не возвращает; деньги брони остаются выручкой. В zafirogo гостевые сессии кассы имеют операцию `guest_session`. | Как в старом: при запуске по коду денежной операции нет (деньги уже проведены `reservation_payment`); в `Refs` события — id брони. |
| UQ-16 | **Сотрудник может отметить приход по оплаченной брони задолго до начала** — время начнётся сразу. | Как в старом (принцип 2). |
| UQ-17 | **Пропущенная бронь**: деньги у клуба, записи в журнале нет. | Как в старом; событие не добавлять (принцип 3). |
| UQ-18 | **Тариф без лимита использования, обрезанный периодом, стоит полную цену** (BR-171) — клиент платит за минуты, которых не получит. | Как в старом — это заявленное правило коммита («at the same price»); агент показывает время окончания до покупки. Не «исправлять». |
| UQ-19 | **Фото клуба**: в старом — файлы на локальном диске и публичный адрес `/club-photos/{club}/{file}`. | Хранить как логотип (`0019_club_images.sql`), адрес отдавать тот же (его кэширует приложение). |
| UQ-20 | **«Свободные ПК» каталога** в старом — по `last_seen_at` в базе. | Считать по присутствию (Valkey) и кэшировать ответ каталога на 30–60 с (запрос публичный, без токена, по всем клубам города). |
| UQ-21 | **Хост реселлера** `resseler.zafiro.kz` (написание — как в старом `config/app.php:60`). | Перенести как есть: третий хост по образцу суперадмина (`http/superadmin_host.go`, `web/.../host.ts`); в `deploy.md` — домен и сертификат; хост — в список проверки сертификатов (А8). |
| UQ-22 | **Лендинг**: 5 новых страниц и новый вид главной; эталоны визуального сравнения устареют; в старом Font Awesome с CDN. | Переверстать по новым Blade, классы — без изменений; ресурсы из npm (Q-064); эталоны переснять со старого `a680bab`. |
| UQ-23 | **Счёт клуба в момент перехода**: неоплаченный счёт и история счетов. | Переносить вместе с клубом (id сохраняются — Q-029, номер `ZF-…` не меняется); оплату отмечать на том сервере, где клуб сейчас. Связано с UQ-3. |
| UQ-24 | **Старые агенты (REST) и новый агент (gRPC)**: вход по коду брони есть только в старом агенте ≥ версии от 13.11. | В `proto` добавить RPC сейчас; экран нового агента — в теме Ж; в `docs/agent-protocol.md` отметить минимальную версию. |

---

## 8. Порядок переноса

### Зависимости

```
Ш0 схема + sqlc + перенос данных ──┬─► П1 А2 срок тарифа ───────────────┐
                                   ├─► П2 Б счета и скидки ──► П5 Д реселлеры
                                   ├─► П3 В профиль и каталог ──┐
                                   └────────────────────────────┴─► П4 Г брони ──► П6 Ж агент
П7 Е лендинг — независимо        П8 мелкие А (А1, А3–А8) — независимо
```

| Шаг | Что | Зависит от | Параллельно с | Зона файлов (чтобы исполнители не пересекались) |
|---|---|---|---|---|
| Ш0 | Миграции (все таблицы и колонки тем Б–Д одной серией), виды ledger в `CHECK`, `docs/schema-changes.md` | — | ничем (первым, один исполнитель) | `db/migrations/**`, `server/internal/ledger/types.go` |
| П1 | А2: `EndOf`, обрезка покупки/пакета/гостевой сессии, остаток в пакет; BR-170…173, GS-529…533, GAP-047, 048 | Ш0 не нужен | П2, П3, П7, П8 | `server/internal/billing/pricing/**`, `billing/service/{purchase,guest,packages}.go`, `db/queries/purchases.sql` |
| П2 | Б: скидка, счета, Kaspi, право `billing.pay`, Telegram о счёте; BR-180…184, GS-536, 537, 552…559 | Ш0 | П1, П3, П7, П8 | `http/sections/billing/**`, `sections/superadmin/{clubs,invoices}.go`, `db/queries/{billing,superadmin}.sql`, `api/{billing,superadmin}.yaml`, `web/src/features/{billing,superadmin}/**`, `auth/permissions/**` |
| П3 | В: город/адрес, профиль, фото, оборудование зон, каталог, вступление, подписка только сотрудникам, А4; GS-534, 566…571 | Ш0 | П1, П2, П7 | `http/sections/settings/**`, `api/settings.yaml`, `db/queries/{settings,mobile_api}.sql`, новый `catalog.sql`, `http/legacy/{routes,auth}.go` + новый `catalog.go`, `web/src/features/settings/**` |
| П4 | Г: брони — сервис, ledger, старый REST, экран броней, настройки; BR-174…179, 190; GS-538…551; GAP-041…045, 049…051 | Ш0, П1 (обрезка периодом и `EndOf`), П3 (файлы `legacy` и настроек — после слияния П3) | П5, П7 | `server/internal/reservations/**`, `billing/service/booking.go`, `db/queries/{reservations,bookings}.sql`, `http/legacy/{bookings,guest}.go`, `http/sections/reservations/**`, `api/reservations.yaml`, `web/src/features/reservations/**` |
| П5 | Д: реселлеры — суперадмин, кабинет, хост, код в настройках, скидка и процент в счетах; BR-185…189, GS-560…565 | П2 (счета), П3 (карточка настроек клуба — тот же файл) | П4, П7 | новые `http/sections/reseller/**`, `sections/superadmin/resellers.go`, `db/queries/resellers.sql`, `api/reseller.yaml`, `web/src/features/reseller/**`, `http/reseller_host.go`, `config` |
| П6 | Ж: RPC кода брони, запуск брони вошедшего гостя, экран агента | П4 | П5, П7 | `proto/agent/v1/**`, `server/internal/agent/**`, `agent/**`, `docs/agent-protocol.md` |
| П7 | Е: каркас лендинга, 4 страницы продукта, `/pricing` | — (для `/pricing` — только чтение `saas_plans`) | всем | `web/src/features/public/**`, `web/src/routes/{index,product,pricing}*`, `http/sections/public/**`, `api/public.yaml`, `web/e2e/**` |
| П8 | Мелкие А: А1, А3, А5, А6 (тесты-подтверждения), А7 (Telegram о новом клубе), А8 (хост сертификатов) | — | всем | `telegram/alerts.go`, `sections/auth/register.go`, тесты; `sections/superadmin/TelegramPage` |

### Общие файлы — один исполнитель или строгая очередь

- `api/gen.sh`, сгенерированный код (`server/internal/http/api/**`, `server/internal/store/**`, `web/src/api/**`) —
  генерировать после каждого слияния, не править руками.
- `web/src/features/settings/ClubSettingsPage.tsx` — три новые карточки от трёх тем (В: город и адрес; Г: бронь
  из приложения; Д: реселлер): порядок П3 → П4 → П5.
- `server/internal/http/legacy/routes.go` — П3, затем П4.
- `http/sections/settings/{values,save}.go` — П3 (нет ключей настроек), П4 (`app_booking_*`).
- Импорт переводов (537 + 7 ключей × 3 языка) — один раз, в Ш0 или отдельным шагом до экранов.
- `docs/business-rules.md`, `docs/golden-scenarios.md` — дополняет исполнитель темы своим диапазоном ID из этого
  документа.

### Что должно быть в каждом шаге

Сначала тесты сценариев из §4 и GAP из §5 своей темы (деньги — табличные, ID сценария в имени подтеста), затем код;
`task gen`, `task test`, `task lint`; для экранов — визуальное сравнение со старым `a680bab`.
