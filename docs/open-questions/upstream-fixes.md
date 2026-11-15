# Исправления старого проекта после тега `zafirogo-base` (ноябрь 2026)

Перенос делался с тега `zafirogo-base` (e3c8d25, 26.10.2026). Старый проект с тех пор ушёл вперёд (main a680bab,
41 коммит). Здесь — что из этого затрагивает **уже перенесённый** код: что перенесено, что уже было верно, что
осталось исполнителям новых функций, и вопросы владельцу. Новые функции (брони из приложения, реселлеры, счета Kaspi,
каталог клубов, лендинг) описывает `docs/upstream-2026-11.md`.

## 1. Перенесено

| Коммит Zafiro | Что | Где в zafirogo | Тесты |
|---|---|---|---|
| b4f08f6 «Tariffs: time ends with the validity period» | Время тарифа заканчивается вместе с периодом действия (куплен в 15:00 на пн 08:00–18:00 — до 18:00, цена та же): покупка в агенте, запуск абонемента, гостевая продажа с кассы (панель и приложение сотрудника — один сервис). Тариф с лимитом использования оставляет урезанное время пакетом. | `billing/pricing`: `WeeklyPeriods.EndOf`/`IsAlways`, `Tariff.ValidityEndsAt`, `Computer.SessionEndUntil`, `QuoteTariffPurchase`, `QuoteStartPackage`, `QuoteGuestSession`, ошибки `ErrTariffValidUntil`, `ErrGuestTariffValidUntil`; `billing/service`: `PurchaseTariff` (+ `keepCutTime`), `StartPackage`, `sell`; запрос `SetTimePackageMinutes`; gRPC — код `TARIFF_UNAVAILABLE` | GS-529…GS-533 (`docs/golden-scenarios.md`): `pricing/validity_end_test.go`, сверка `endOf()` с PHP (`tools/php-oracle/gen-weekly-fixture.php`), `billing/service/validity_end_integration_test.go` |
| fcd5d41 «API login: without a chosen club…» | Вход приложения без `club_id`: сначала клуб аккаунта с активной подпиской, затем по id членства. | `http/legacy/auth.go` `doLogin` | `http/legacy` `TestAuth/login_without_a_club_picks_a_club_with_an_active_subscription` |
| 1a0487a (независимая от каталога часть) | Гость приложения не зависит от подписки клуба: вход и смена клуба проверяют подписку только у сотрудников; `/api/auth/me`, друзья (включая перевод другу), новости, журнал — без `subscription`; вход на ПК по QR — с ней. | `http/legacy/auth.go` `needsSubscription`, `routes.go`; `docs/mobile-compat.md` §1.1–1.2 | `http/legacy` `TestAuth/GS-230_inactive_subscription` |
| e872af0 «Telegram: report every new club…» | Бот оповещений платформы пишет в чат о каждом клубе, зарегистрированном на сайте (после ответа, в фоне); строка об этом на экране Telegram кабинета. | `telegram.Alerts.ClubRegistered`, `http/sections/auth/register.go` `reportNewClub`; `web/src/features/superadmin/TelegramPage.tsx`; переводы в `db/seed/translations-new.*.json` | `http` `TestTelegram_NewClubReported`, `sections/auth` `TestCabinetClubURL` |

Автобиллинг и «на весь баланс» — покупка **минут** (`purchaseMinutes()`), период действия тарифа их не касается
(в Laravel `sell()` для минут вызывается без `$until`) — без изменений. Предпросмотр гостевой продажи на карте клуба
считает только цену (она не изменилась); расчёта времени в TypeScript нет.

## 2. Уже было верно (добавлены тесты-фиксаторы)

| Коммит Zafiro | Почему правка не нужна | Тест |
|---|---|---|
| 75d1174 — продление подписки не падает на `days` из формы | Ошибка типов PHP (Carbon 3 `addDays(string)`). В REST `days` — целое JSON, форма шлёт `Number(...)`. | существующий `stage3_g6` (продление на 30 дней) |
| 29efab1 — строки возможностей тарифа режутся как UTF-8 | Go разбирает UTF-8: `U+0085` в регулярном выражении — символ NEL, а не байт внутри кириллической буквы. Разбор вынесен в `splitFeatures`. | `sections/superadmin` `TestSplitFeatures_UTF8` (строка из теста Laravel) |
| 1490462 — токены приложений не ПК | Токены приложений (`mobile_api_tokens`) и токены гостя на ПК (Valkey, по ПК) хранятся раздельно; правило «один ПК на клиента» (`Tokens.SignedInElsewhere`) смотрит только на токены ПК. | `agent/guestapi` `TestGS081OnePCAtATimeAndTokens` (свежие токены `mobile`, `mobileclient`, `Zafiro Staff`) |
| ff3c6c5 — склад после удаления товара с историей | `WarehouseRecentMovements` соединяет `products` без фильтра `deleted_at`: имя удалённого товара показывается. | `http` `TestWarehouseScreens` (история после удаления) |
| c16f47a (часть) — время брони в текстах по часам клуба | `reservations` уже форматирует время в поясе клуба (SV-9). | существующие |

## 3. Не перенесено: части новых функций

Изменённые файлы, существовавшие на `zafirogo-base` (по `git diff --stat zafirogo-base HEAD -- app routes resources/views
database lang config tests`), правки которых относятся к новым функциям. Их переносят исполнители этих функций.

| Новая функция | Файлы старого проекта (M) |
|---|---|
| Брони из приложения (8893dde, 958dd9b, 0bf5a42, 2f44607, 4454219, c16f47a, f09ea7c, 3e16a3e, 70edcbb, 0da191b) | `Services/ComputerReservations.php` (оплаченная бронь, код брони, `startForSignedIn`, `markArrivedBy` только неоплаченные, блокировка ПК в `close()`), `Services/AgentTariffPurchaseService.php` (`bookableTariffs`, `bookedMinutes`, `purchasesLeft`, `tariffPrice`, брони в счётчике лимита продаж, `isAvailable($validAt)`), `Services/GuestHistory.php` (`reservation.paid`/`reservation.refunded`), `Models/ComputerReservation.php`, `Models/ActivityLog.php` (подписи событий), `ClubComputerController.php` (heartbeat запускает оплаченную бронь), `SettingsController.php` + `SaveSettingsRequest.php` (`app_booking_enabled`, `app_booking_cancel`), `Providers/AppServiceProvider.php` (лимит `agent-booking-code`), `reservations/index.blade.php` («Из приложения, оплачено…», текст подтверждения отмены), `settings/club.blade.php`, `routes/api.php`, миграции `computer_reservations` |
| Каталог клубов и профиль клуба (1a0487a, f41cb71, 792d822, 4c872d5) | `Api/AuthController.php` (`joinClub` — вступление в клуб из списка города), `SettingsController.php` (`location`), `Models/Club.php` (`profile`, `photos`, `zones`, `computers`), `Models/ClubZone.php` (`devices`), `RequireClubArea.php` (`settings.location`, `settings.profile*`, `settings.photos*`), `settings/layout.blade.php` («Профиль клуба»), `settings/club.blade.php` (город и адрес), `routes/*`, миграции `clubs` |
| Счета Kaspi (77ae201) | `BillingController.php`, `billing/index.blade.php`, `RequireActiveSubscription.php` (`billing.invoices.*`), `RequireClubArea.php` и `Support/ClubPermissions.php` (право `billing.pay`), `Monitoring/TelegramAlerts.php` (сообщение о счёте), `superadmin-sidebar.blade.php` («Оплаты») |
| Скидка клуба на тариф платформы (4821225) | `Superadmin/ClubController.php` (`discount`), `Models/ClubSubscription.php` (`discountPercent`, `monthlyPrice`), `superadmin/clubs/index.blade.php` (значок «−N%»), `superadmin/clubs/show.blade.php` (форма скидки, «к оплате … ₸ в месяц»), `billing/index.blade.php` |
| Реселлеры (ca5c8f0, 2318bc7, 9c2df5f) | `AuthController.php` (вход на хосте реселлера), `auth/login.blade.php` (подпись на хосте реселлера), `SettingsController.php` (`reseller`), `Models/Club.php` (`reseller`), `Monitoring/Certificates.php` и `config/app.php` (`reseller_domain`), `bootstrap/app.php`, `superadmin-sidebar.blade.php` («Реселлеры»), `settings/club.blade.php` |
| Лендинг и цены (12363e8 … 16d1685) | `LandingController.php`, `welcome.blade.php`, `layouts/landing.blade.php`, удалённый перевод «Весь клуб — в одном окне» |

Зацеплено за брони, остаток для их исполнителя:

- счётчик лимита количества тарифа должен учитывать неотменённые брони-тарифы (`purchaseCounts()`); у нас счётчик —
  `tariff_sales` (Q-007);
- `ComputerReservations::close()` теперь сначала блокирует ПК, затем бронь (порядок блокировок с покупками). У нас
  закрытие брони ПК не трогает; когда «Клиент пришёл» начнёт запускать оплаченное время — блокировать ПК первым;
- `bookedMinutes()` использует тот же `WeeklyPeriods::endOf()` — в Go готов (`WeeklyPeriods.EndOf`).

## 4. Вопросы владельцу

### UF-1. Запуск абонемента: минуты, урезанные сроком пакета

После b4f08f6 `startPackage()` считает остаток как `minutes_left − minutes_added` и оставляет его в пакете. Это задумано
для времени дальше конца периода действия тарифа, но так же остаются и минуты, урезанные **сроком** пакета. Пример:
пакет 90 мин, до срока 40 мин. Запуск → 40 мин, в пакете 50; сразу второй запуск → ещё 40 (сессия продлевается за
срок), третий → 10. Пакет выбран целиком, срок его не ограничил. До b4f08f6 пакет удалялся, 50 минут сгорали.

- **Сделано (по принципам 2 и 4, без изменения Решения Q-011 и GAP-014):** в пакете остаётся только урезанное концом
  периода действия — `min(minutes_left, минут до срока) − minutes_added`; минуты, не помещающиеся до срока, сгорают
  при запуске (ledger `package_start`, в `meta.lost_minutes`). Тест `TestTariffEndsWithValidityPeriod/UF-1`.
- **Если нужно буквально как в Laravel:** в `service.StartPackage` заменить `quote.FullMinutes` на `pkg.MinutesLeft`
  в расчёте `rest` и обновить GAP-014.
- **Решение (подтверждено владельцем 16.11.2026: «что считаешь правильным — прими сам»):** оставить как сделано — минуты пакета, не помещающиеся до
  конца срока пакета, сгорают при запуске (как до b4f08f6, Q-011), а не остаются в пакете, как в свежем Laravel.
  Осознанное отличие от старого; запись — `docs/DECISIONS.md`.

### UF-2. Последняя минута периода действия

`WeeklyPeriods::contains()` включает последнюю минуту периода («до 18:00» действует 18:00:00–18:00:59), а
`endOf()` считает диапазон `[начало, конец)`. В эту минуту тариф «доступен», но `validityEndsAt()` = 18:00:00 ≤ now, и
покупка отклоняется текстом «Тариф действует до 18:00, а время на ПК уже оплачено дальше.» (хотя время на ПК не
оплачено). Перенесено буквально; денег не теряется. Исправлять — в старом проекте или решением здесь.

### UF-3. Гость приложения и подписка клуба

Перенесено правило 1a0487a: гость клуба с истёкшей подпиской входит в приложение, видит баланс, новости, журнал,
друзей и **может перевести деньги другу** (маршруты перевода в Laravel вынесены из группы `subscription`). Правило
не зависит от каталога клубов, поэтому перенесено сейчас; если владелец хочет дождаться каталога — вернуть
`a.subscription` в `legacy/routes.go` и проверку в `doLogin`/`switchClub` (тест `GS-230`).

- **Решение (подтверждено владельцем 16.11.2026: «что считаешь правильным — прими сам»):** оставить как сделано, как в свежем Laravel — гость клуба с
  истёкшей подпиской входит в приложение и может перевести деньги другу.

### UF-4. Адрес клуба в сообщении о новом клубе

`route('superadmin.clubs.show', $club)` в Laravel строится от адреса запроса. В Go: `https://<SUPERADMIN_DOMAIN>/clubs/{id}`,
без домена кабинета — `https://<хост запроса>/superadmin/clubs/{id}` (схема всегда https: панель работает только по
HTTPS). Дата конца пробного периода — по UTC, как в Laravel (у только что созданного клуба в модели ещё нет
часового пояса). Сообщение о **счёте** (77ae201) — часть функции «счета Kaspi».
