# Вопросы Этапа 1: касса поверх БД (`server/internal/cash/service`, `server/internal/stock/db.go`)

Сервисный слой кассы: смены, X/Z, продажи POS, пополнение с бонусом, приход/расход, возвраты, запись гостевой сессии в
смену; склад (приход/списание, создание/правка товара). Старый код: `app/Services/CashRegisterService.php`,
`ShiftReport.php`, `GuestSessions.php`, `app/Http/Controllers/CashRegisterController.php`,
`Api/Staff/CashController.php`, `app/Actions/{AdjustStock,CreateProduct,UpdateProduct}Action.php`.
Миграция `db/migrations/0007_cash_service.sql`, запросы `db/queries/cash.sql`, `db/queries/stock.sql`.
Код, который зависит от ответа, помечен `CSQ-N`.

- [x] **CSQ-1. Какой вид ledger-операции у «выравнивания» кассы при открытии смены?**
  - Контекст: в старом коде начальный остаток смены — просто число (`CashRegisterService::open()`), между сменами деньги
    вносят/изымают без записи. В ledger касса — один счёт `cash_drawer`; чтобы его остаток совпадал с остатком смены,
    при открытии проводится разница `opening_balance − остаток cash_drawer` со счётом `opening_balance`.
  - Сейчас: вид `cash_income` (разница > 0) / `cash_expense` (< 0), `meta.reason = "shift_opening"`, ключ
    `cash_shift_open:<id смены>`. Отдельного вида в `ledger_transactions_kind_check` нет, а пакет `ledger` не мой.
  - Варианты: (а) оставить; (б) добавить вид `cash_shift_opening` (миграция CHECK + константа в `ledger`).
  - Рекомендация: (б) — чтобы отчёты по видам операций не смешивали прочий приход и пересчёт кассы между сменами.
  - Решение (30.10.2026): принята рекомендация: (б) отдельный вид `cash_shift_opening` (миграция CHECK + константа в `ledger`).

- [x] **CSQ-2. Вид прихода (`cash_transactions.income_kind`) и перенос данных.**
  - Контекст: в старом X/Z «Пополнение балансов» и «Гостевые сессии» определялись по журналу действий
    (`ShiftReport::for()`, `$logged`, OQ-BR-04, допущение Q-007). В 0007 — колонка `income_kind` (`top_up`,
    `guest_session`, NULL), отчёт считает виды по ней (агрегат в SQL).
  - Нужно: в `tools/migrate-from-mysql` заполнить `income_kind` у старых приходов по `activity_logs`
    (`event = cash.income`, `subject = CashTransaction`, `details.balance_type` → `top_up`, `details.computers` → `guest_session`),
    иначе перенесённые смены покажут их как «Прочий приход».
  - «Продажа товаров» по-прежнему определяется по `description = 'Продажа POS'` (как старый код, см. CQ-8).
  - Решение (30.10.2026): принято: инструмент переноса заполняет `income_kind` старых приходов по `activity_logs`.

- [x] **CSQ-3. Печать Z-отчёта: флаг у смены, nonce в БД.**
  - Контекст: старый код держал nonce и флаг печати в сессии Laravel сотрудника (`CashRegisterController::zReport()`,
    `markZReportPrinted()`, `close()`); флаг «вытягивался» при закрытии. Q-015 рекомендует флаг в БД, nonce — в Valkey.
  - Сейчас (допущение задачи «пока в БД»): `cash_shifts.z_report_nonce_hash` (sha256 последнего выданного nonce),
    `z_report_printed_at/by`. Отличия: флаг — у смены, а не у сессии сотрудника (закрыть после печати может любой с правом);
    nonce одноразовый и гасится и при неверном значении (как `session()->pull()`), поэтому `MarkZReportPrinted` возвращает
    `false` без ошибки — HTTP-слой отвечает 419 и **коммитит** транзакцию.
  - Рекомендация: оставить флаг в БД; nonce перенести в Valkey, когда появится слой сессий (тогда колонка nonce не нужна).
  - Решение (30.10.2026): принята рекомендация (как Q-015): флаг печати — у смены в БД; nonce — в Valkey, колонка nonce после этого удаляется.

- [x] **CSQ-4. Идемпотентность кассовых документов.**
  - Контекст: CLAUDE.md требует ключ идемпотентности для всех движений денег; старая касса защиты от повтора не имела.
  - Сейчас: `cash_transactions.idempotency_key` (UNIQUE по клубу); ключ ledger-операции тот же (без ключа —
    `cash_transaction:<id>`). Повтор ищется после блокировки открытой смены (она сериализует кассу клуба), поэтому
    параллельные повторы получают одну операцию. Тело повтора сверяется по полям документа (строки чека, клиент/баланс/сумма/
    способ оплаты, тип/сумма/описание), для пополнения ещё и отпечатком ledger. Возвраты идемпотентны по `refund_of_id`
    (повтор → «Эта операция уже возвращена.», как в старом коде).
  - Вопрос: обязателен ли ключ для веб-форм (HTML-формы без JS его не пришлют) — или генерировать его в форме (скрытое поле)?
  - Рекомендация: скрытое поле с UUID в формах кассы; API сотрудника — заголовок `Idempotency-Key`.
  - Решение (30.10.2026): принята рекомендация: скрытое поле с UUID в формах кассы (SPA генерирует ключ), API сотрудника — заголовок `Idempotency-Key`.

- [x] **CSQ-5. Журнал интерфейса (`activity_logs`) и вебхуки кассы и склада.**
  - Контекст: старый код в тех же транзакциях писал `cash.income`, `user.balance_topped_up`, `cash.refund`,
    `user.balance_refunded`, `product.purchased`, `stock.received/written_off`, `product.created/updated`,
    `cash.shift_opened/closed`, `cash.z_report_printed` и слал вебхуки `stock.changed`, `sale.completed`, `product.*`.
    История клиента (`GuestHistory`) и экран журнала читают `activity_logs`.
  - Сейчас: сервис пишет только `audit_log` (категория money; пополнение и его возврат — `billing`), а результаты методов
    содержат всё для журнала и вебхуков (суммы, движения склада, бонус и процент). Писать их должен слой, где есть IP,
    user-agent и очередь вебхуков (HTTP + NATS outbox).
  - Вопрос: подтвердить такое разделение (или журнал интерфейса писать прямо в сервисе — тогда ему нужен IP/UA в Actor).
  - Решение (30.10.2026): принята рекомендация из `stage2-events.md` (SV-8/CSQ-5): журнал и outbox пишет сервис в той же транзакции; IP/UA — из контекста запроса (`activity.WithRequestMeta`).

- [x] **CSQ-6. Возврат продажи не ссылается на исходную ledger-операцию.**
  - Контекст: `ledger_transactions.reverses_transaction_id` у `pos_refund` пуст — нет запроса «ledger-операция по
    cash_transaction_id» (индекс есть только частичный по `cash_shift_id`). Связь восстанавливается через
    `cash_transactions.refund_of_id` → `ledger_transactions.cash_transaction_id`.
  - Рекомендация: добавить запрос/индекс в `ledger.sql` (владелец пакета ledger) и заполнять ссылку.
  - Решение (30.10.2026): принята рекомендация: запрос/индекс «ledger-операция по `cash_transaction_id`» и заполнение `reverses_transaction_id` у `pos_refund`.

- [x] **CSQ-7. Остаток кассы по журналу при открытии смены — SUM по всем проводкам счёта.**
  - Контекст: `CashDrawerLedgerBalance` суммирует все проводки `cash_drawer` клуба (индекс `(club_id, account_id, created_at)`),
    это растёт с годами (открытие смены — раз-два в сутки, поэтому терпимо).
  - Рекомендация: позже — снимок остатка (например, id последней проводки + остаток в `cash_shifts` при закрытии) и сумма
    только «после снимка».
  - Решение (30.10.2026): принята рекомендация: позже — снимок остатка при закрытии смены и сумма только после снимка.

- [x] **CSQ-8. Порядок проверок пополнения и 404.**
  - Контекст: Laravel сначала валидирует форму (`user_id` — член клуба, `amount`, `balance_type`, `payment_method`), затем
    сервис проверяет смену, затем `MemberBalance::firstOrFail()` (404).
  - Сейчас так же: не член клуба → правило `in` поля `user_id`; нет строки `member_balances` → `service.ErrNotFound`.
    Laravel возвращал все ошибки формы сразу, здесь — первую (как во всём пакете `cash`).
  - Решение (30.10.2026): принято как сделано: порядок проверок как в Laravel; все ошибки формы сразу — общим валидатором при переносе экранов (RQ-1).

- [x] **CSQ-9. Описание гостевой сессии длиннее 255 символов.**
  - Контекст: `cash_transactions.description varchar(255)`; длинный список ПК в старом коде — ошибка MySQL (500).
  - Сейчас: ошибка правила `max` поля `computer_ids`. Рекомендация: обрезать список ПК в описании или расширить колонку до `text`.
  - Решение (30.10.2026): по принципам (PD-6) — `cash_transactions.description` расширить до `text`, список ПК не обрезать.

## Замечания к другим пакетам и документам

- `billing.TopUp` (CQ-4 из `stage1-cash.md`): добавлено поле `TopUpParams.BonusBase` (0 — как раньше, от `Amount`);
  в отпечаток запроса оно попадает, только если отличается от `Amount`, поэтому прежние ключи не меняются.
  Тест — `server/internal/billing/topup_bonusbase_integration_test.go`.
- Время ledger-операций и `audit_log.created_at` ставит БД (`now()` транзакции), а не внедряемые часы сервиса:
  `cash_shifts.opened_at/closed_at`, `cash_transactions.created_at`, движения склада — по часам сервиса (`Service.Now`).
- Интерфейс `CashRecorder` (`server/internal/billing/service/guest.go`) реализует `*cash/service.Service` напрямую
  (методы `LockOpenShift`, `RecordGuestSessionIncome`; проверка `var _ billingsvc.CashRecorder = (*Service)(nil)`):
  `cash/service` импортирует `billing/service`, цикла нет. Связка: `billingsvc.New(pool, clock, &cashservice.Service{…})`.
  Сквозной тест — `TestSellGuestSessionWithCash` (GS-091, GS-092).
