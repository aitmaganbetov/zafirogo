# Перенос темы Б: счета за подписку (Kaspi), скидка клуба, цена «за ПК в месяц» (ноябрь 2026)

Исполнитель П2 (`docs/upstream-2026-11.md`, тема Б, §3.3, §8; правила BR-180…BR-184, BR-186; сценарии GS-536, GS-537,
GS-552…GS-559; GAP-037…GAP-040, GAP-046, GAP-052, GAP-054, GAP-055; решения UQ-5, UQ-6, UQ-7). Источник — старый Laravel
`a680bab`: `app/Services/SubscriptionBilling.php`, `Models/{SubscriptionInvoice,ClubSubscription}.php`,
`BillingController`, `Superadmin/{InvoiceController,ClubController::discount}`, `Monitoring/TelegramAlerts::invoiceOpened`,
`billing/index.blade.php`, `superadmin/invoices/index.blade.php`, `superadmin/clubs/{index,show}.blade.php`,
`tests/Feature/{SubscriptionPaymentTest,SuperadminCabinetTest}.php`.

## 1. Что перенесено

| Старое | Новое |
|---|---|
| `SubscriptionBilling::amount()`, `computers()`, `discountFor()`; `ClubSubscription::discountPercent()`, `monthlyPrice()`; `SubscriptionInvoice::number()`, `MONTHS` | `server/internal/subscription/calc.go` — целые тиыны и проценты, сверка с PHP 8.4 (`calc_test.go`, фикстура `testdata/php_subscription.json`, генератор `tools/php-oracle/gen-subscription-fixture.php`) |
| `SubscriptionBilling::open()`, `markPaid()`, `cancel()`, `kaspi()`, `payablePlans()` | `server/internal/subscription/service.go` (`Open`, `CancelOwn`, `MarkPaid`, `Cancel`, `ClubTerms`, `ClubKaspi`); запросы `db/queries/subscription_billing.sql` |
| `GET /billing` | `GET /api/billing` (`api/billing.yaml` `getBilling`, расширен) |
| `POST /billing/invoices` (`billing.pay`, `throttle:10,1`, без `subscription`) | `POST /api/billing/invoices` (`createInvoice`) |
| `POST /billing/invoices/{invoice}/cancel` | `POST /api/billing/invoices/{invoiceId}/cancel` (`cancelInvoice`) |
| `POST superadmin/clubs/{club}/subscription/discount` | `POST /api/superadmin/clubs/{clubId}/subscription/discount` (`saSetClubDiscount`) |
| `GET superadmin/invoices` | `GET /api/superadmin/invoices` (`saListInvoices`), страница `web/src/routes/superadmin/invoices.tsx` |
| `POST superadmin/invoices/kaspi`, `/{invoice}/pay`, `/{invoice}/cancel` | `POST /api/superadmin/invoices/kaspi`, `/{invoiceId}/pay`, `/{invoiceId}/cancel` |
| `ClubPermissions`: `'billing' => […, 'pay' => []]`, `RequireClubArea` (`billing.invoices.*`), `RequireActiveSubscription` (исключения) | `auth/permissions` (`ActionPay`, `RouteRights`), `/api/billing/*` уже исключён из проверки подписки |
| `TelegramAlerts::invoiceOpened()` (после ответа) | `telegram.Alerts.InvoiceOpened`, вызов в фоне из `sections/billing/invoices.go` `reportInvoice` |
| `billing/index.blade.php` | `web/src/features/billing/BillingPage.tsx` |
| `superadmin/invoices/index.blade.php` | `web/src/features/superadmin/InvoicesPage.tsx` |
| `superadmin/clubs/index.blade.php` (бейдж «−P%»), `show.blade.php` (цена со скидкой, форма скидки) | `ClubsPage.tsx`, `ClubPage.tsx` |
| `partials/superadmin-sidebar.blade.php` — «Оплаты» (`fa-receipt`) | `SuperadminLayout.tsx`, `host.ts` (путь `/invoices` на хосте кабинета) |

События: журнал клуба — `billing.invoice_opened`, `billing.invoice_canceled`; журнал платформы — `superadmin.invoice_paid`,
`superadmin.invoice_canceled`, `superadmin.billing_kaspi_changed`, `superadmin.subscription_discount_changed`; `audit_log` —
`money / invoice.opened|canceled|paid`, `tariff / subscription.discount_changed`, `settings / billing.kaspi_changed`.
Журнал денег платформы — `invoice_paid:<id счёта>` и `reseller_accrual:<id счёта>` в транзакции оплаты.

Колокольчика уведомлений о счетах в старом нет — не добавлен. Подписей новых событий в `ActivityLog::EVENTS` и
`PlatformLog::EVENT_LABELS` в старом нет — не добавлены (UB-4).

## 2. Отличия от старого (осознанные)

| Что | Старое | Здесь | Почему |
|---|---|---|---|
| Арифметика | float, `round()` | целые тиыны и базисные пункты | решение UQ-7; см. UB-1 |
| «К оплате» в форме | JS `Math.round(price * computers * months * (100 − discount) / 100)` | суммы по тарифам и срокам отдаёт сервер (`plans[].amounts`) | во фронте нет float и второй копии формулы |
| Второй неоплаченный счёт при гонке | проходит, если у клуба нет строки подписки | уникальный индекс → тот же текст ошибки | GAP-046, §3.6 |
| Журнал денег | нет | `platform_money_log` | решение UQ-6 |
| Аудит | нет | `audit_log` на выставление, отмену, оплату, скидку, настройку Kaspi | CLAUDE.md «Безопасность» |
| Ссылка Kaspi | `url:https` при сохранении | то же + фронт рисует кнопку только для `https://…` | в базе после переноса может лежать что угодно |
| Права базы | — | пул клуба только читает `club_subscriptions` (миграция `0039`) | замечание Ш0: клуб не должен мочь дать себе скидку или активную подписку |
| Экран «Подписка»: даты | в поясе клуба (`UsesClubTimezone`) | в поясе клуба | до переноса экран показывал UTC — исправлено |

## 3. Вопросы владельцу

### UB-1. Цена тарифа с тиынами: сумма ровно на половине тенге

При цене в целых тенге (все нынешние тарифы) суммы совпадают со старым кодом всегда. Если цена тарифа — с тиынами
(999,99 ₸), а произведение попадает ровно на «…,5 ₸», float PHP иногда даёт на 1 ₸ **меньше** точного округления
(31 случай из 2014 таких в фикстуре), у нас — всегда точное «половина вверх». То же для цены со скидкой (на 1 тиын) и
начисления реселлеру (на 1 тиын; процент с сотыми долями).

- **Сделано:** точная целочисленная формула (UQ-7); тесты фиксируют, что расхождение возможно только в этих случаях.
- **Если нужно байт-в-байт со старым:** повторить вычисление во float (`money.PHPRound`) в `subscription.Amount`,
  `MonthlyPrice`, `ResellerEarning` — одна строка в каждой; фикстура уже содержит ответы PHP.
- Проще всего — не заводить тарифы платформы с тиынами (сейчас форма «Тарифы платформы» их принимает).
- **Решение (подтверждено владельцем 16.11.2026: «что считаешь правильным — прими сам»):** сумма счёта — точное целочисленное округление «половина
  вверх»; при цене тарифа с тиынами она может отличаться от float-расчёта Laravel на 1 ₸ (скидка и начисление
  реселлеру — на 1 тиын). Осознанное отличие, байт-в-байт со старым не повторяем.

### UB-2. Последний день скидки: пояс (GAP-055)

`discount_until` — дата; в старом «конец дня» считается в поясе текущего клуба, а в кабинете суперадмина (вне клуба) —
по UTC. Клуб в Алматы (UTC+5): скидка «по 25.11» для счёта клуба кончается 25.11 в 19:00 UTC, а в кабинете бейдж «−20%» и
«к оплате … в месяц» показывают её ещё 5 часов.

- **Сделано как в старом** (принцип 2): панель клуба и счёт — по часам клуба, кабинет — по UTC. На деньги влияет только
  первое. Проверка «дата не раньше сегодня» при сохранении — по UTC, как в старом.
- Мелочь старого кода: для поясов западнее UTC дата при чтении сдвигалась на день назад (скидка кончалась на сутки
  раньше). Здесь дата читается как есть. Клубов западнее UTC нет.
- **Вопрос:** считать последний день по часам клуба везде (и в кабинете)? Это одна строка в `sections/superadmin/clubs.go`.

### UB-3. Право `billing.pay` нельзя выдать из панели

В старом право добавлено с пометкой «others get it only when the owner grants it», но у страницы «Права» четыре
фиксированных столбца (`ClubPermissions::ACTIONS`: просмотр, создание, изменение, удаление) — действия `pay` среди них
нет, чекбокс не рисуется. Сохранение страницы прав сбрасывает выданное вручную `billing.pay`. Фактически платит только
владелец.

- **Сделано как в старом:** право существует (`permissions.ActionPay`), проверяется, выданное в
  `club_memberships.permissions` — работает (тест GS-554), но столбца на странице «Права» нет.
- **Вопрос:** нужен ли чекбокс «Выставить и отменить счёт» в строке «Подписка»? Это правка экрана прав (не тема Б).

### UB-4. Подписи новых событий в журналах (GAP-052)

`billing.invoice_opened`, `billing.invoice_canceled` (журнал клуба) и `superadmin.invoice_paid`,
`superadmin.invoice_canceled`, `superadmin.billing_kaspi_changed`, `superadmin.subscription_discount_changed` (журнал
платформы) не имеют подписей в `ActivityLog::EVENTS` / `PlatformLog::EVENT_LABELS` старого кода: в фильтре событий их
нет, в строке журнала — описание. Перенесено так же. Подписи — это новые переводы на три языка; добавить по решению.

### UB-5. Суперадмин продлевает подписку «на N дней» без счёта

Остался, как в старом (UQ-5): в журнал денег платформы такое продление не попадает (денег в нём нет). Если продление
«на N дней» — это оплата мимо счёта, её стоит оформлять счётом; иначе сверка «деньги платформы = оплаченные счета»
неполна по смыслу, хотя формально верна.

### UB-6. Выставленный счёт и смена тарифа или числа ПК

Счёт хранит снимок (число ПК, цена, скидка). Клуб может выставить счёт, пока у него 1 ПК, добавить 50 ПК и оплатить —
подписка продлится по старой сумме. В старом так же; лимита срока жизни неоплаченного счёта нет. Перенесено как есть;
защита — суперадмин видит в «Оплатах» число ПК счёта и может его отменить.

## 4. Реселлеры (тема Д, П5): что уже есть в счетах

- При выставлении счёт получает `reseller_id` и `reseller_bp` активного реселлера клуба и скидку `max(своя, реселлера)`
  (`subscription.ClubTerms`, запрос `SubscriptionClubReseller`).
- При оплате `MarkPaid` считает `reseller_amount` (`subscription.ResellerEarning`) и пишет `reseller_accrual:<id>` в
  `platform_money_log` (`accrueReseller` в `service.go`). Это сделано здесь, а не оставлено П5: клуб, перенесённый из
  MySQL уже с реселлером, иначе оплатил бы счёт без начисления, и деньги реселлера потерялись бы без следа.
- П5 остаётся: кабинет и суперадминка реселлеров, привязка по коду, выплаты, сценарии GS-560…GS-565; на экране «Подписка»
  текст о скидке реселлера, если он есть в `ca5c8f0` (в `billing/index.blade.php` на `a680bab` его нет — «Ваша скидка: P%»
  уже учитывает реселлера).

## 5. Не сделано

- Визуальное сравнение страниц `billing`, `billing-expired`, `superadmin-invoices`, `superadmin-clubs`, `superadmin-club` —
  см. отчёт исполнителя (стенд общий для параллельных исполнителей).
- Flutter и старый REST мобильных: маршрутов счёта в `/api` старого проекта нет — нечего переносить.
