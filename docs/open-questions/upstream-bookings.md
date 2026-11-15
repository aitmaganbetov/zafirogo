# Перенос темы Г: брони из гостевого приложения (ноябрь 2026)

Исполнитель П4 (`docs/upstream-2026-11.md`, тема Г, §3.2, §3.5, §3.6, §8; правила BR-174…BR-179, BR-190; сценарии
GS-538…GS-551; GAP-041…GAP-045, GAP-049…GAP-051, GAP-058; решения UQ-8, UQ-9, UQ-11, UQ-14…UQ-17). Источник — старый
Laravel `a680bab`: `app/Services/GuestBookings.php`, `ComputerReservations.php`, `AgentTariffPurchaseService.php`
(`bookableTariffs()`, `bookedMinutes()`, `purchasesLeft()`, `purchaseCounts()`, `bonusPayment()`), `GuestHistory.php`,
`Models/ComputerReservation.php`, `Api/GuestBookingController.php`, `Api/AgentBookingCodeController.php`,
`reservations/index.blade.php`, `settings/club.blade.php` (`#app-booking`), `tests/Feature/GuestBookingTest.php`.

## 1. Что перенесено

| Старое | Новое |
|---|---|
| `GuestBookings::plan()`, `computers()`, `tariffs()`, `options()`, `book()`, `cancel()`, `present()`, `presentAll()`, `newCode()` | `server/internal/reservations/app.go` — `AppComputers`, `AppTariffs`, `AppOptions`, `Book`, `CancelByGuest`, `ListForGuest`, `Group` |
| `ComputerReservations::startPaidSession()`, `startForSignedIn()`, `startByCode()`, `refund()`, `close()` (оплаченная бронь), `appBookingEnabled()`, `guestMayCancel()` | `reservations/paid.go` — `StartForSignedIn`, `StartByCode`, возврат и запуск внутри `Cancel` / `Arrive`; `reservations.go` — `closeBooking` (блокировки ПК → бронь) |
| делёж бонусов по ПК, `durationMinutes()`, минуты и суммы запуска, `bookedMinutes()` | `reservations/split.go` — `SplitBonus`, `DurationMinutes`, `PlanStart`, `BookedMinutes` (целые тиыны; сверка с PHP 8.4 — `testdata/php_booking.json`, генератор `tools/php-oracle/gen-booking-fixture.php`) |
| `markArrivedBy()->unpaid()`, `purchaseCounts()` (брони в лимите тарифа) | `db/queries/purchases.sql` `MarkReservationsArrived`, `GetTariffSales`; `guest.sql` `GuestListTariffs` |
| `GET /api/guest/bookings`, `…/computers`, `…/tariffs`, `…/options`, `POST /api/guest/bookings`, `POST …/{reservation}/cancel` | `server/internal/http/legacy/bookings.go`, маршруты — `legacy/routes.go`; лимиты — `auth/ratelimit/rules.go` (`api.guest.bookings*`) |
| `GuestHistory` — `reservation.paid`, `reservation.refunded` | `legacy/guest.go` (`historyOwn`, `historyReceived`, `historyFilters`, `historyEntry`) |
| `POST /reservations/{id}/cancel`, `/arrive` (оплаченная бронь) | `http/sections/reservations` — `CancelReservation`, `ArriveReservation` (возврат, запуск времени, уведомления агенту после коммита); `api/reservations.yaml` — `from_app`, `paid`, `code` |
| `reservations/index.blade.php` — «Из приложения, оплачено N · код …», текст подтверждения отмены | `web/src/features/reservations/ReservationsPage.tsx` |
| `settings/club.blade.php` `#app-booking`, `SettingsController` (`app_booking_enabled`, `app_booking_cancel`) | `web/src/features/settings/AppBookingCard.tsx` (+ строка и якорь в `ClubSettingsPage.tsx`), `sections/settings/{values,handlers}.go`, `api/settings.yaml` |
| `ActivityLog::EVENTS` — `reservation.paid`, `reservation.refunded` | `activity/labels.go` |
| heartbeat ПК с токеном клиента → `startForSignedIn()` | `worker/start_app_bookings.go` (задача `app-bookings:start`) + событие входа гостя (тема Ж) — UB-6 |
| `throttle:agent-booking-code` | правило `ratelimit.AgentBookingCode` (5/мин на ПК, 200/час на клуб) — подключает RPC темы Ж |

Миграция `0040_app_booking_guards.sql` (с Down, `up → down → up` проверено) — UB-2, UB-3.

## 2. Решения и отличия от старого

| # | Старое | Новое | Почему |
|---|---|---|---|
| UB-1 | сервис `GuestBookings` рядом с покупками | пакет `reservations` (не `billing/service/booking.go`, как предлагал §8) | пакет `club` зависит от `billing/service`, а `reservations` — от `club`: сервис броней в `billing/service` дал бы цикл импортов. В `billing/service/booking.go` — только экспорт того, что нужно броням из сервиса покупок (правила клуба, разбор строки тарифа, id сессии, событие ПК) |
| UB-2 | две брони одного ПК на одно время не даёт создать только блокировка строки ПК | та же блокировка + ограничение базы `computer_reservations_pending_no_overlap` (ожидающие брони одного ПК не пересекаются) | §3.6: второй рубеж для денег клиентов. Интервал между бронями в ограничение не входит (настройка клуба) — его проверяет код под блокировкой. На перенесённых данных нарушений быть не может (старый код проверял то же условие с интервалом ≥ 0); если в базе клуба они всё же есть, перенос клуба остановится на этой строке — см. §4, вопрос 3 |
| UB-3 | `newCode()` проверяет занятость кода без блокировки — две одновременные брони могут получить один код (GAP-058) | выдача кодов клуба сериализована транзакционной advisory-блокировкой (`AppBookingLockCodes`), под ней — проверка занятости и вставка | уникальным индексом не выразить: код один на несколько строк (группа ПК), а «занят» — только пока `ends_at > now − 1 день` |
| UB-4 | делёж бонусов: последнему ПК — остаток, он может превысить его цену → `paid_main_cents < 0`, ошибка MySQL, бронь не создаётся (GAP-041) | доля бонусов ПК не больше его цены, излишек — предыдущим ПК с конца | UQ-8 (принцип 2: баг). Где старая формула работала — числа те же (сверка с PHP на 1508 случаях, из них с ошибкой старой формулы — отдельно). Нужна запись в `docs/DECISIONS.md` (оркестратор) |
| UB-5 | возврат без строки баланса молча пропускался — деньги гостя терялись (GAP-043) | строка баланса создаётся, возврат зачисляется | UQ-9 (образец Q-012). Не вернуть можно только тому, кого в клубе больше нет: аккаунт удалён (`user_id` обнулён) или членства нет — как в старом, бронь отменяется без возврата (зачислять некому) |
| UB-6 | `startForSignedIn()` на каждом heartbeat ПК с токеном клиента (запрос в базу на каждый пинг) | (1) событие входа гостя на ПК зовёт `reservations.StartForSignedIn` (тема Ж); (2) задача `app-bookings:start` раз в минуту: одним запросом по частичному индексу берёт оплаченные ожидающие брони, до начала которых осталось не больше интервала клуба, и для каждой спрашивает присутствие агентов (Valkey), на каком ПК вошёл гость; совпало — запуск в транзакции клуба | UQ-11. Условия запуска те же и проверяются под блокировкой ПК и брони; запуск идемпотентен по `arrived_at`. Отличие для гостя: если он вошёл раньше интервала и просто сидит, время начнётся не на ближайшем пинге (секунды), а в пределах минуты после начала интервала. Один исполнитель — advisory lock (Q-086) |
| UB-7 | двойной тап «Забронировать»: вторая бронь упадёт на пересечении с первой | то же + `Idempotency-Key` (если клиент пришлёт): повтор с тем же ключом и телом возвращает первую бронь, с другим телом — 409; ключ ledger — `reservation_payment:<id первой брони>`, возврата — `reservation_refund:<id брони>` | §3.5. Мобильное приложение ключ не шлёт (`docs/mobile-compat.md`) |
| UB-8 | правило `required_without` отвечает ключом `validation.required_without` (перевода в каталоге старого проекта нет) | то же | ответы 1:1 (снято со стенда). Остальные тексты правил — через общий каталог переводов, порядок полей `errors` — порядок правил |
| UB-9 | `starts_at` — любой формат `Carbon::parse` | ISO 8601 с поясом или без (без пояса — UTC), с секундами и долями или без, и дата; остальное — 422 «…must be a valid date» | приложение шлёт `toUtc().toIso8601String()`; разбирать «tomorrow», «next monday» незачем (принцип 4). См. §4, вопрос 2 |
| UB-10 | имя почасовой части сессии — `__('Бронь')` на языке запроса | на языке запроса (панель, приложение); при запуске фоновой задачей — по-русски | задача работает без запроса; имя части видно в агенте в составе сессии |
| UB-11 | агент узнаёт о сессии и балансе на следующем heartbeat | после коммита: `StartSession` на ПК (запуск брони сотрудником и задачей), `BalanceChanged` гостю (оплата, возврат); события панели `computer.state`, `reservation.changed`, `balance.changed` | push вместо опроса (CLAUDE.md) |
| UB-12 | гость после отмены сотрудником одного ПК группы не может отменить остаток (`can_cancel false`, 422) | то же | GAP-050: как в старом; остаток отменяет сотрудник (с возвратом) |
| UB-13 | — | возврат по брони, перенесённой из Laravel (операции оплаты в ledger нет): проводки те же, без ссылки `reverses_transaction_id` | выручка клуба за такую бронь вошла в начальные остатки переноса |
| UB-14 | счётчик лимита тарифа — журнал `tariff.purchased` + брони | `tariff_sales` (Q-007) + брони; экран «Тарифы» панели показывает продажи в агенте без броней (как показывал журнал в старом) | BR-190 |
| UB-15 | журнал `reservation.arrived` по коду — от имени гостя брони | то же, если гость ещё в клубе (запись журнала ссылается на членство); иначе запись не пишется | сессия по коду должна открыться и для брони гостя, вышедшего из клуба |

## 3. Контракт для темы Ж (агент: RPC кода брони, вход гостя, история)

Серверная часть готова и покрыта тестами; RPC, `proto` и экран агента — исполнитель П6.

```go
// Код брони на экране входа ПК (POST /api/agent/booking-code старого агента).
reservations.StartByCode(ctx, q, info club.Info, computerID int64, code string, env reservations.Env) (reservations.Started, error)
// Гость вошёл на ПК (Login, подтверждение QR): запустить его оплаченную бронь, если пора.
reservations.StartForSignedIn(ctx, q, info club.Info, computerID, userID int64, env reservations.Env) (reservations.Started, started bool, err error)
```

- Вызывать в тенантной транзакции клуба ПК (клуб и ПК — из сертификата mTLS, не из запроса); `info` — `club.Load`.
  `env.Now` — часы сервера; `env.Actor` — для `StartForSignedIn` гость (`Kind: "guest"`), для кода — `Kind: "agent"`
  (журнал пишется от имени гостя брони); `env.Notifier` не нужен, если RPC сам отдаёт сессию в ответе.
- `Started`: `SessionID`, `EndsAt`, `UserID` (0 — без аккаунта), `Guest` (true — по коду), `Minutes`, `Extends`.
  Ответ старого контроллера — `{message: «Бронь открыта: приятной игры!» (reservations.MsgCodeOpened), session:
  {id, ends_at, user_id: null, guest: true}}`.
- Ошибки `StartByCode` — `*reservations.Error` с `Field: "code"` и ключом перевода: `MsgCodeFormat` (не 6 цифр),
  `MsgCodeNotFound`, `MsgCodeOtherPC` (ответы различаются, как в старом — UQ-14), `MsgCodeEarly` (`:time` — местное
  время клуба, `:count` — интервал), `MsgCodeBusy`. `ErrNotFound` — ПК нет в клубе.
- Лимит попыток — правило `ratelimit.AgentBookingCode` (5/мин на ПК + 200/час на клуб): применить на RPC до вызова.
- Подписка клуба: gRPC агента проверяет её всегда (UQ-12) — вызовы выше её не проверяют.
- История в агенте (`agent/guestapi/history.go`): добавить события `reservation.paid` (вид `purchase`, заголовок
  «Бронь ПК :computer», примечание — `starts_at` из деталей, суммы — `main_balance_amount` / `bonus_balance_amount` со
  знаком минус) и `reservation.refunded` (вид `refund`, «Возврат за отменённую бронь»); списки событий — как в
  `legacy/guest.go` (`historyOwn`, `historyReceived`, фильтр `purchases`).
- Задача `app-bookings:start` уже подключена в `app.go` (присутствие — `presence.SignedInOrList`); после появления
  вызова `StartForSignedIn` при входе гостя она остаётся для гостей, вошедших раньше интервала.

## 4. Что не сделано и вопросы владельцу

1. **RPC кода брони, запуск брони при входе гостя, экран и история агента** — тема Ж (П6), контракт — §3.
2. **Формат `starts_at`** (UB-9): принят ISO 8601; остальные форматы `Carbon::parse` отклоняются. Подтвердить.
3. **Ограничение базы и перенос клубов** (UB-2): если в MySQL клуба окажутся две ожидающие брони одного ПК на
   пересекающееся время (старый код такого не допускал), перенос этого клуба остановится на вставке. Предложение:
   добавить такую проверку в сверку перед переносом (`tools/migrate-from-mysql/README.md`) — отдельной задачей.
4. **`docs/DECISIONS.md`**: записи по UQ-8 (UB-4) и UQ-9 (UB-5) — внести оркестратору (файл общий).
5. **Пропущенная бронь** (UQ-17): как в старом — деньги у клуба, записи в журнале нет. В отчётах смены оплата броней
   не участвует (оплата с баланса — не касса); в выручке клуба (`revenue_time`) она признаётся в момент оплаты брони.
6. **Экран «Тарифы»**: колонка продаж — без броней (UB-14). Если владельцу нужен общий счёт с бронями — отдельное решение.

## 5. Как обновить эталон старого REST

`server/internal/http/legacy/testdata/bookings.laravel.json` — ответы стенда старого проекта (Laravel `a680bab`, PHP
8.4.27) на последовательность из 34 запросов; данные — `testdata/bookings-seed.php`, «сейчас» — понедельник
16.11.2026 10:00 Алматы.

1. Копия старого проекта стенда `tools/visual` (`tools/visual/old.sh prepare`), своя база sqlite: `php artisan
   migrate:fresh --force`, затем `php artisan tinker --execute='require "bookings-seed.php";'` (печатает id и токены).
2. В `storage/framework/visual-now` копии — `2026-11-16T05:00:00+00:00`; `php artisan serve`.
3. Запросы — в порядке шагов эталона (поле `steps`: метод, путь, тело, гость); перед шагом `index-no-cancel` выключить
   `app_booking_cancel`, перед `index-disabled` — `app_booking_enabled`.
4. `TestBookingsMatchLaravel` повторяет шаги на новом сервере и сравнивает код HTTP, значения, типы, `null` и порядок
   ключей; к общему виду приводятся только id, случайный код брони и тексты правил валидатора (в тестовом сервере нет
   каталога переводов — ключом служит английский текст Laravel).
