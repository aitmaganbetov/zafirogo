# Аудит (15.11.2026), дублирование и мёртвый код сервера: решения и вопросы владельцу

Исправления по отчёту аудита «качество кода бэкенда»: BC-01, BC-05…BC-08, BC-12…BC-18, BC-20. Денежные формулы и
округления не менялись: для каждой пары «модель под golden-тестами ↔ боевой код» сначала написан тест, который
прогоняет golden-сценарии и граничные значения через обе реализации, и только при совпадении до тиына и минуты
оставлена одна. Ниже — что именно сверено, что осталось как есть и что решает владелец.

## BC-01. Одна реализация каждого денежного и сессионного правила

Расхождений в расчётах между моделью и боевым путём **не найдено**. Golden-сценарии теперь проверяют боевой код.

| Правило | Было две реализации | Сверка | Осталась |
|---|---|---|---|
| Запуск пакета, `ensureComputerFor`, минуты тарифа (BR-015, BR-020, BR-024, GS-047, GAP-014) | `session.StartPackage`, `EnsureComputerFor`, `EffectiveMinutes` ↔ `pricing.QuoteStartPackage`, `Computer.CheckForCustomer`, `Tariff.EffectiveMinutes` | 24 192 запуска пакета (12 состояний ПК × 3 момента «сейчас» с долями секунды × 8 остатков × 14 сроков × 6 тарифов), все ошибки и их тексты, продление, конец сессии, срок части — совпало | `pricing.*` (боевой); сценарии модели перенесены в `billing/pricing/purchase_test.go`, `billing/service/load_test.go` |
| Истечение сессии (BR-033, GS-087, GAP-005) | `session.IsDue`, `ExpiryFor` ↔ SQL `service.ExpireDueSessions` | границы: ровно в конец, −1 нс, −1 с, +0,9 с, сессия без конца | SQL; тест `TestExpireSessions/GS-087/граница` |
| Возврат пополнения (BR-055, GS-005, GAP-024) | `cash.PlanTopUpRefund` ↔ расчёт внутри `billing.RefundTopUp` | 17 сценариев через Postgres и ledger (`TestRefundTopUpMatchesPlan`) — остатки в БД совпали до тиына | `cash.PlanTopUpRefund`; `billing.RefundTopUp` вызывает её под блокировкой баланса |
| Рост трат лояльности (BR-042, GS-034…GS-036, GAP-002, GAP-011) | `bonus.AddSpending` ↔ `service.addSpending` ↔ `shop.addSpending` (три копии) | 12 сценариев через Postgres | `billing.AddSpending` / `SubtractSpending` (`billing/spending.go`) — их зовут и покупка времени, и магазин; тест `billing/spending_integration_test.go` |
| Скидка клиента и флаг «накопительная» (BR-011) | `bonus.DiscountIsCumulative` ↔ сравнение на месте в `customers/views.go` и `legacy/staff.go` | для конечных чисел `max`, `math.Max` и `phpMax` совпадают | `bonus.LoyaltyDiscountFor`, `bonus.DiscountIsCumulative` — их вызывают оба экрана |
| «Половина вверх» в базисных пунктах (BR-012, BR-041) | `money.PercentOfHalfUp` ↔ формула на месте в `pricing.Discounted` и `bonus.TopUpForCents` | 230 023 пары «цена × базисные пункты» (0…10000) | `money.PercentOfHalfUp` |

Убрано вместе с моделью: второй набор ошибок (`session.ErrComputerReserved`, `ErrComputerBusy`, `ErrPackage*`) и
мёртвые ветки `guestapi.sessionStatus` — эти значения в бою никто не возвращал.

Что **не** дубль и оставлено:

- `bonus.TopUpFor`, `TopUpForTengeString` — формула одна (`TopUpForCents`), а это оболочка «сумма как float из запроса»
  в форме старого `TopUpBonus::for()`. Перенесены в тестовый файл (`bonus_test.go`): golden GS-002 и
  дифференциальный тест против PHP по-прежнему идут через них. Боевой путь получает центы от кассы
  (`cash.ParseTopUpAmount`, решение Q-002); что для ввода с ≤ 2 знаками это те же центы, проверяет
  `cash.TestTopUpBonusBaseEqualsAmount`.
- `cash.VATIncluded` — эталон: сервер НДС не считает, функция нужна общим векторам `testdata/vat.json`, по которым
  сверяется фронт (`web/src/features/cash/vat.test.ts`).
- `session.Extend` и `pricing.Computer.SessionEnd`, `session.TariffPartExpiresAt` и `pricing.Tariff.UsageExpiresAt` —
  обе пары работают в бою намеренно: `pricing` даёт неусечённый конец для проверки брони (как `$sessionEndsAt` в
  памяти Laravel), `session` — усечённое до секунды значение для записи; сервис сверяет их между собой.

### Наблюдения при сверке (не расчёт; решает владелец)

- [ ] **AC-1. `RefundTopUpResult.NewBonusBalance` равен 0, если возврат не тронул бонусный счёт.** Пример: возврат
  пополнения основного баланса 1,00 ₸ без бонуса в чеке, у клиента 7,00 ₸ бонусов. Правило `cash.PlanTopUpRefund`
  отвечает `NewBonusBalance = 7.00`, `billing.RefundTopUp` — `0` (поле берётся из проводок ledger, а бонусной проводки
  нет). В БД бонусы целы (7,00), поле сейчас никто не читает; так же устроено `TopUpResult.NewBonusBalance`.
  Laravel: `CashRegisterService::refund()` (~стр. 174–186) это значение не возвращал вовсе. Зафиксировано тестом
  `TestRefundTopUpMatchesPlan`. Рекомендация: оставить, а если поле понадобится экрану — заполнять из плана.
- [ ] **AC-2. Модель запуска пакета не знала про баланс.** `pricing.QuoteStartPackage` (боевой) при отрицательном
  основном балансе отвечает «Недостаточно средств для покупки тарифа.» — как `sell(0, 'main_balance_only', 'main')`
  в Laravel (`AgentTariffPurchaseService::sell()`, стр. 259–261: `$mainBalance < $mainPayment`). Удалённая модель
  `session.StartPackage` баланс не принимала. Боевое поведение верное; добавлен тест
  `TestPackageStart_CappedByExpiry/BR-024/отрицательный_баланс`.
- [x] **AC-3. Фоновая сверка балансов с журналом операций — сделано (подтверждено владельцем 15.11.2026).**
  Было: запросы `ListMemberBalanceDrift` и `CashDrawerLedgerBalance` вызывались только интеграционными тестами.
  Стало: воркер `ledger:reconcile` (`server/internal/worker/ledger_reconcile.go`; общий цикл `worker.loop`, один
  исполнитель на кластер — `leader.RunOnce`, Q-086). **Ничего не исправляет** — находит, записывает, оповещает.
  - **Что сверяется** в каждом клубе: `member_balances.balance/bonus_balance` = Σ проводок по счетам клиента; Σ проводок
    каждой операции = 0; снимок остатка кассы последней закрытой смены (CSQ-7) = остатку по журналу на момент снимка.
  - **Когда:** раз в час (`workers.ledger_reconcile.interval`) — частый проход: только клиенты, чей баланс менялся с
    прошлой сверки (`member_balances.updated_at` ставит триггер каждой проводки; запас назад 10 минут), клиенты с
    открытым расхождением и операции за это время; раз в сутки (`workers.ledger_reconcile.full_interval`) — полный:
    все проводки клуба и снимок кассы. Запись в обход журнала, не тронувшая `updated_at`, находится полным проходом.
    Инстанс проверяет «пора ли» каждые 15 минут по общей строке `ledger_reconcile_state` — несколько инстансов не
    повторяют сверку, а после сбоя её подхватывают без ожидания часа.
  - **Как:** клубы — пачками по 200 (keyset по id), каждый запрос — одна команда в своей короткой транзакции клуба
    пулом `zafiro_app` (RLS, предел команды 2 минуты, `db.InTenantTx`); общей транзакции нет; находки и журнал
    платформы — пулом `zafiro_platform`. Агрегаты — в SQL.
  - **При расхождении:** строка `ledger_reconcile_findings` (миграция 0033) и запись `platform_logs`
    `ledger.drift_found` — клуб, клиент (или операция, смена), разница в тиынах («проекция − журнал») в тексте и в
    `details`. **Один раз:** следующие проходы молчат, пока разница не изменилась (отпечаток — сами разницы:
    клиент продолжает платить — запись не повторяется); изменилась — запись снова; исчезла — `ledger.drift_resolved`.
    Не больше 200 новых записей одного вида на клуб за проход (испорченная целиком проекция не зальёт журнал).
  - **Оповещение:** метрики `zafiro_ledger_reconcile_open_findings` и
    `zafiro_ledger_reconcile_last_success_timestamp_seconds`, алерты `ZafiroLedgerDrift` (critical) и
    `ZafiroLedgerReconcileStale` (warning, 3 часа без успешной сверки), задача добавлена в `ZafiroWorkerNotRunning`;
    первые шаги — `docs/observability.md` (`rb-ledger-drift`). Экран «Сервер» кабинета — красная строка «Сверка
    балансов с журналом» только при расхождениях или остановке сверки (`audit-ui.md` AU-15); сами записи видны в
    журнале платформы (фильтр «Расхождение с журналом операций»).
  - **Замеры** (Postgres 18.6 на машине разработки, тёплый кэш; клуб с 50 000 клиентов, 500 000 операций и 1 000 000
    проводок за 24 месяца — 28 месячных партиций; роль `zafiro_app`, RLS; `EXPLAIN (ANALYZE, BUFFERS)`):

    | Запрос | Время | Буферы | Примечание |
    |---|---|---|---|
    | Полная сверка балансов (`ListMemberBalanceDrift`) | 0,51–0,56 с | 14 372 shared (112 МБ), temp ≈ 25–30 МБ | параллельное чтение всех партиций + hash-агрегат |
    | Частая сверка балансов (`ReconcileMemberBalancesTouched`, 628 клиентов за 70 минут) | 117 мс | ≈ 100 500 обращений (индексные пробы 628 × 2 счёта × 28 партиций) | чтения с диска — 3 693 страницы на холодном индексе |
    | Операции за час (`ReconcileUnbalancedTransactions`, 630 операций) | 10 мс | 266 | без условия по `e.created_at` было 77 мс и 9 360 — добавлено для отсечения партиций |
    | Операции за всё время (полный проход) | 0,87 с | 15 053 shared, temp ≈ 130 МБ | раз в сутки |
    | Весь тик воркера, два клуба (большой и на 1 000 клиентов) | полный 1,2–1,6 с; частый 0,09–0,15 с | — | вместе с записью состояния |

    Вывод: частый проход на крупном клубе — десятая доля секунды; полный — около 1,5 с на 1 млн проводок раз в
    сутки, по одному клубу за раз (нагрузка растёт с историей линейно — поэтому он суточный, а не часовой).
    В бою партиции общие для всех клубов: полный проход клуба пойдёт по индексу `(club_id, account_id, …)`, а не
    чтением всей партиции.
  - **Тесты** (`ledger_reconcile_integration_test.go`): чистая база — тишина (ни находок, ни записей журнала; тик
    раньше периода пропускается); расхождение, созданное в обход суперпользователем с выключенными триггерами,
    находится и журналируется один раз за шесть проходов, честная операция клиента запись не повторяет, изменение
    разницы — пишет снова, исчезновение — закрывает; частый проход против полного; операция с несбалансированными
    проводками; снимок кассы; предел записей; один исполнитель (тик под чужим локом ничего не делает, из шести
    одновременных тиков проход один и запись одна); пулу клуба таблицы сверки недоступны. `TestLedgerReconcileOf`,
    тесты promtool обоих алертов.
  - **Владельцу (не блокирует):** оповещение в Telegram-бот оповещений (как «диск заполнен») не добавлено — сейчас
    Prometheus/Alertmanager и экран «Сервер». Рекомендация: добавить одной строкой через `monitoring.Alerts`
    (ключ `ledger-drift`), если Alertmanager в бою не будет подключён к Telegram.

## BC-05. Часовой пояс клуба — `internal/clubtz`

Девять копий «LoadLocation с откатом на UTC» заменены двумя функциями; поведение каждого места сохранено:

- `clubtz.Location(tz)` — пусто или неизвестный пояс → UTC: панель, карта, дашборд, интеграции, агент, мониторинг,
  старый мобильный API (8 мест);
- `clubtz.Strict(tz)` — пусто → UTC, неизвестный пояс → ошибка: покупка времени (`billing/service`), потому что
  периоды продажи и действия тарифа считаются во времени клуба.

- [ ] **AC-4. Поведение расходится между разделами по-прежнему** (это не менялось): клуб с неизвестным поясом не
  может продавать время, но панель у него работает в UTC. В Laravel `CurrentClub::timezone()` всегда откатывался на
  UTC. Форма настроек неизвестный пояс не сохраняет (`validTimezone`), так что случай возможен только после переноса
  данных. Рекомендация: оставить ошибку в денежном пути и добавить проверку пояса в `migrate-from-mysql`.

## BC-06. Валюта клуба — `billingsvc.ClubCurrency` / `CurrencyOf`

Пять реализаций `data_get($settings, 'finances.currency', 'KZT')` сведены в одну с явным параметром:

- обычный режим (панель, агент, журнал, события): непустая строка, иначе `KZT`;
- `literal` (только старый мобильный API, `http/legacy`): буквально как `data_get` — умолчание лишь для
  отсутствующего ключа и `null`; пустая строка остаётся пустой, не-строка отдаётся своей JSON-записью (`5`, `true`).

Таблица из 23 входов снята со всех пяти прежних реализаций до объединения (`TestClubCurrency`,
`TestClubSettingCurrencyLiteral`).

- [ ] **AC-5.** Нужен ли мобильным приложениям буквальный режим? Форма настроек сохраняет только `KZT`, `RUB`, `$`,
  поэтому пустая строка и число возможны лишь в перенесённых данных. Рекомендация: оставить до переноса мобильных.

## BC-07. `number_format` — `money.NumberFormat`, `money.GroupThousands`

Шесть копий группировки тысяч (`cash`, `httpx`, `billing`, `http/legacy`, `superadmin`, `clubmap`) заменены одной.
До замены вывод каждой копии сверен побайтно на 260 значениях (границы групп, отрицательные, `MaxInt64`,
`MinInt64 + 1`) со всеми разделителями, которыми они вызывались. Единственное отличие — `legacy.moneyStr(MinInt64)`:
старая копия переполнялась, новая печатает число верно; в данных такого значения быть не может.

## BC-08. Циклы воркеров — `worker.loop`

Шесть одинаковых `Run` заменены общим циклом; периоды, ключи блокировок и тексты журнала не менялись:

- суточные и часовые задачи — «период после окончания тика», повтор через 5 минут после ошибки только у сгорания
  пакетов (как было);
- истечение сессий и автобиллинг — по сетке `time.Ticker` (как было);
- `ExpireSessions.Tick` берёт блокировку через `leader.RunOnce` вместо собственной копии advisory-lock (ключ тот же:
  `hashtext('job:club-computers:expire-sessions')`).

Панику тика цикл не перехватывает: это делает `safego.Run` в `app.Run` (журнал со стеком, перезапуск цикла с растущей
задержкой, первый тик снова сразу) — проверено тестом `TestLoop` на виртуальном времени.

## BC-12, BC-13. Мёртвый код

`deadcode ./...`: 67 → 20 недостижимых из `cmd/*` функций; недостижимых даже из тестов: 7 → 0.

Удалено: `HourlyFlusher.Rows` (со счётчиком), `bonus.Object`, `grpc.IsCode`, `Server.ServeContext`,
`stream.Register`, `phpNumber.String`, `ledger.Pending.TransactionID`, `ledger.Load`, `password.IsLegacy`,
`outbox.ClubSubjects`, `stock.ParsePrice`, `stock.AsError`, `App.Close`, `money.Sub`, `WeeklyPeriods.IsAlways`,
`cash.CheckXReport`, `cash.CanShowRefund`.

Перенесено в тестовые файлы (эталоны и документация старого поведения, в бинарник не входят): `money.PHPRound83`
с помощниками (исторический эталон PHP 8.3, BR-001), `LegacyAmountToCents`, `LegacyFormatCents`, `StoredCents`,
`ParseTengeViaFloat`, `FormatFloat2`, `PHPMod`; `bonus.Value.Keys`, `migrate.LooksEncrypted`, `App.MetricsAddr`,
`Alerts.IsOpen`, `safego.Panics`.

Оставлено намеренно (20): пакет `telegram/telegramtest` (тестовый двойник Telegram, 11 функций); `Access.Uncovered`
(тест покрытия прав в каждом разделе); `grpc.ReasonOf`, `pki.ParseCertificatePEM`, `pki.NewDevIssuingCA`,
`secretbox.New`, `ledger.Kinds` — помощники тестов других пакетов и `cmd/loadgen`; `webhook.Verify` — образец проверки
подписи для получателя; `money.FromTengeFloat` и `cash.VATIncluded` — эталоны (см. выше).

- [ ] **AC-6. `stock.ParsePrice` удалена как неиспользуемая.** Панель передаёт цену товара целыми тиынами
  (`price_tiyn`), API интеграций разбирает цену своей функцией (`v1.form.price`: без обрезки пробелов, пустая строка —
  `numeric`, а не `required`). Правило Q-002 («больше двух знаков — ошибка») для товаров проверяется тестами `v1`.
- [ ] **AC-7. `WeeklyPeriods.IsAlways` удалена:** сервер её не вызывал, подпись «всегда» строит фронт
  (`web/src/features/tariffs/api.ts`). Вызов `WeeklyPeriods.isAlways` в `tools/php-oracle/eval.php` остался без
  потребителя.

## BC-14. Запросы sqlc

Удалено 19 запросов без единого вызова (641 → 622), файл `db/queries/admin_map.sql` целиком и запись `GetClubBySlug`
из исключений `tenant-filter`: `AdminListComputerStates`, `AdminGetComputerSession`, `AdminGetProduct`,
`ApiV1CountComputers`, `CountRecentComputerCertificates`, `CountUnpublishedOutboxEvents`, `GetClubBySlug`,
`ListComputersByClub`, `GetComputer`, `ListActiveTariffsByClub`, `GetProduct`, `ListProductStockMovements`,
`GuestComputersByIDs`, `SuperadminUserHasMembership`, а также оставшиеся без вызовов после оптимизации
`MarkAgentOnline`, `MarkAgentOffline`, `MonitoringAddClubRequests`, `MonitoringInsertLoadSnapshot`,
`SuperadminUpsertTranslation`.

- [ ] **AC-8. Шесть запросов «только для тестов» не удалены, а помечены комментарием:** `ListMemberBalanceDrift`,
  `CashDrawerLedgerBalance`, `SumClubAccountByPeriod`, `ListCashShiftTransactions`, `ListClubActivityLogs`,
  `ListClubAuditLog`. Ими интеграционные тесты четырёх пакетов сверяют балансы с ledger и читают журналы под ролью
  приложения и RLS; замена на строки SQL в тестах вывела бы их из-под `sqlc vet`. Рекомендация: оставить. `ListMemberBalanceDrift` теперь вызывает и фоновая сверка
  (AC-3, полный проход); `CashDrawerLedgerBalance` остаётся тестовым (сверка кассы — `ReconcileCashDrawerSnapshot`).

## BC-15. REST-операции без вызова из фронта — не удалены

`POST /api/auth/logout-all`, `GET /api/customers/{userId}/ledger`, `POST /api/customers/{userId}/balance-adjustments`,
`POST /api/customers/{userId}/initial-balance`. В старом Laravel таких маршрутов нет (`routes/web.php`,
`routes/api.php`), мобильные приложения ходят через `http/legacy`, во `web/src` вызовов нет. Два денежных эндпоинта
оставлены прежними решениями (RQ-13, `stage3-customers-staff.md`), `logout-all` описан в `stage6-security.md`.

- [ ] **AC-9.** Убрать их (меньше поверхность атаки на денежные операции) или подключить экраны («Выйти на всех
  устройствах», история баланса клиента)? Рекомендация: `logout-all` и `ledger` подключить, два денежных — убрать,
  если начальный баланс и корректировка останутся только в форме клиента.

## BC-16…BC-18, BC-20

- **BC-17.** `billing.BalanceError` получил поле `Code`; агентский API определяет «недостаточно средств» по
  `billing.CodeInsufficientFunds`, а не по подстроке «Недостаточно» в русском тексте. Поиск подстроки «бонус» в
  `guestapi/history.go` оставлен: это разбор данных старого журнала (`balance_type` хранился подписью), а не ошибки.
- **BC-18.** Проглоченные ошибки чтения БД теперь пишутся в журнал (WARN), ответы клиенту прежние: гость при вводе
  PIN сотрудника, имя ПК в «вошли на другом ПК», имя клуба и пользователя в журнале платформы, блок «крупнейшие
  таблицы», постановка ПК на пересмотр автобиллинга.
- **BC-16.** Подстановка `:name` — одна функция `i18n.Substitute` (длинные ключи раньше коротких); в агентском API
  копия подставляла в порядке обхода `map`, то есть при ключах-префиксах результат был случайным. Копия в
  `app/hooks.go` оставлена: она однопроходная (`strings.NewReplacer`) и не подставляет повторно в уже вставленный
  текст. Не тронуты: помощники `ptr`/`deref` (6 + 5 однострочных копий), одинаковые тексты ошибок в `cash` и `shop`,
  похожие обработчики (`computers.go`/`reregister.go`, `superadmin/telegram.go`, `superadmin/logs.go`) — выгода мала.
- **BC-20.** В `.golangci.yml` включены `nolintlint`, `nilerr`, `wastedassign`, `copyloopvar`, `rowserrcheck`;
  убрана 21 устаревшая директива `//nolint`, 11 намеренных «ошибка → nil» помечены причиной. `unparam` (шумит на
  тестах) и `sqlclosecheck` (только `internal/migrate`) не включены.
- **BC-21** (сложность функций) и **BC-23** (`go.yaml.in/yaml/v3` ради одного теста) — без изменений.
