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
- [ ] **AC-3. Сверки баланса с ledger в бою нет.** Запросы `ListMemberBalanceDrift` и `CashDrawerLedgerBalance`
  вызываются только интеграционными тестами. Рекомендация: суточная фоновая сверка с оповещением (отдельная задача).

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
  приложения и RLS; замена на строки SQL в тестах вывела бы их из-под `sqlc vet`. Рекомендация: оставить (первые два —
  основа для сверки из AC-3).

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
