# Реестр проблем — архитектура, качество кода, соответствие требованиям, зависимости, техдолг (CODE)

Ветка `audit/full-system-2026-11` (HEAD `e4e1156`), старый проект `/home/user/Zafiro` = `71db8d6` (совпадает с `origin/main`,
проверено `git ls-remote`). Только чтение; рабочие файлы — `/tmp/claude-0/-home-user-zafirogo/541f6760-cdd0-504b-ac95-cb07a87b9341/scratchpad/audit/code/`.
Уже найденное прошлыми аудитами (BC-01…BC-23, FE-01…FE-23, AC-*/AU-*/AP-*/AS-*) и решения «ревью 22.11.2026» не повторяются.
Регрессов прошлых находок не обнаружено (BC-03 `GetSeatMap`/`Results` — реализованы, все RPC из `proto/` имеют реализацию;
BC-20 линтеры — 0 находок; BC-22 — `TreatWarningsAsErrors=true`).

| ID | Приоритет | Категория | Файл:строка | Проблема | Сценарий/доказательство | Предлагаемое исправление | Меняет бизнес-логику? | Статус |
|---|---|---|---|---|---|---|---|---|
| CODE-1 | Medium | Соответствие (фоновые задачи) / рост данных | `server/internal/app/core.go:464-471`; `server/internal/worker/log_retention.go:18-27`; `db/queries/apiv1.sql:47`; `db/migrations/0001_init.sql:896` (`webhook_deliveries`) | Очистка `integrations:prune` перенесена только для `api_request_logs`. `api_idempotency_keys` (ответ API хранится целиком, Laravel — 24 ч) удаляется только при повторе того же ключа; `webhook_deliveries` в статусах delivered/failed/skipped (Laravel — 30 дней, `PruneIntegrationData.php:16-21`) не удаляется никогда. Golden GS-375 `TestIntegrationsPrune` («ключи [new]; доставки [pending, failed]») не реализован и не протестирован. | `grep -rniE 'delete from (webhook_deliveries\|api_idempotency_keys)'` по `db/` и `server/` — единственный `DELETE` по ключу конкретного запроса; в `LogRetention.Retention` только `api_request_logs` и две таблицы здоровья ПК; `stage4-h1.md:59-66` прямо говорит «webhook_deliveries … к этой задаче не относится», другой задачи нет. Каждый POST /api/v1 с уникальным `Idempotency-Key` и каждое событие вебхука = вечная строка (с данными клиентов в payload/response). | Добавить в воркер `logs:retention` (или отдельный под `leader.RunOnce`) построчную очистку пачками: `api_idempotency_keys` старше `integrations.idempotency_retention`, `webhook_deliveries` финальных статусов старше 30 дн. (конфиг); тест по GS-375. | нет (возврат к поведению Laravel) | CONFIRMED |
| CODE-2 | Medium | Соответствие (фоновые задачи) / рост данных | `server/internal/worker/burn_packages.go:19-24`; `server/internal/billing/service/expire.go:31-34`; `db/queries/sessions.sql:12-14`; `db/migrations/0001_init.sql:496` | Суточная задача Laravel `session-purchases:prune` (`routes/console.php:39-44`, BR-034) перенесена наполовину: сгорание пакетов есть, а удаления `session_purchases` старше 8 дней (кроме текущих сессий) нет. Комментарий в `expire.go:34` («части … чистит суточная задача; GAP-005») ссылается на несуществующую очистку. Части сессий, кончившихся по времени (основной путь), остаются навсегда; индекс `session_purchases_created_at_index` создан под очистку и не используется. | В `db/queries/*.sql` единственный `DELETE FROM session_purchases` — по `session_id` при досрочном завершении (`billing/service/end.go:184`). `BurnPackages.Tick` вызывает только `service.BurnExpiredPackages`. При 5 000–50 000 ПК — сотни тысяч/миллионы строк в месяц с `user_id` клиентов без срока хранения. | Добавить в суточную задачу `DELETE … WHERE created_at < now()-8d AND session_id NOT IN (текущие сессии)` пачками, ключ блокировки тот же; тест на GAP-005 + «8 дней» + «длинная сессия не теряет части». | нет | CONFIRMED |
| CODE-3 | Medium | Соответствие (экран «Сервер» суперадмина) | `server/internal/http/sections/superadmin/dashboard.go:111-116` | Перенос экрана не завершён: блок «Журнал ошибок» всегда пустой (`LogErrors = []`), сроки TLS-сертификатов не проверяются (`CertificateHost{Checked:false, Days:nil}` → вечное «Ещё не проверялся»). В Laravel — `ErrorLog::recent()` и `monitoring:certificates` (hourly, `Certificates::refresh()`), а решение PD-5 говорит «`monitoring:certificates` переносится как есть». Отложено «на Этап 4» (`stage3-superadmin.md:52`) и нигде не закрыто; хосты по умолчанию (app.url, superadmin/wiki/reseller domain) тоже не подставляются — только `superadmin.ssl_hosts`. | Код содержит комментарий «Этап 4 (Q-060, PD-5): пока пусто»; в `internal/monitoring` нет `tls.Dial`/`NotAfter` (grep). Метрика Prometheus из OBS-5 — для эксплуатации, экран продукта её не показывает. | Реализовать проверку сроков (часовой тикер под `leader.RunOnce`, результат в Valkey) с хостами по умолчанию как `Certificates::hosts()`; журнал ошибок — кольцевой буфер slog-обработчика уровня ERROR (решение Q-060 из backlog). | нет (перенос как есть) | CONFIRMED |
| CODE-4 | Medium | Соответствие решениям владельца | `db/migrations/0003_audit.sql:29,40`; `server/internal/store/models.go:208`; `docs/DECISIONS.md:62` | Решение Q-053 («хэш-цепочка аудита по клубу — на Этапе 4 вместе с экспортом») не выполнено: колонка `audit_log.hash` «зарезервирована, не заполняется», экспорта аудита нет. Ни отчёт Этапа 4, ни backlog не помечают пункт как отложенный — в `docs/backlog.md:46` он числится без статуса. | `grep -rn 'Q-053'` — только комментарии «зарезервировано»; поиска заполнения `hash` в `server/` нет. Без цепочки подмену/удаление строк ролью-владельцем БД (обход триггера `audit_log_append_only`) не обнаружить. | Реализовать цепочку (`hash = sha256(prev_hash ‖ канонические поля)` в той же транзакции, по клубу) и экспорт, либо явно перенести решение владельцем в DECISIONS. | да — нужно решение (срок) | CONFIRMED |
| CODE-5 | Medium | Соответствие решениям владельца / архитектура хранения | `db/migrations/0018_news_images.sql:13-22`, `0019_club_images.sql:1-25`, `0036_club_profile.sql:56-65`; `docs/open-questions/stage3-settings.md:9-15` (S-1) | Решение владельца Q-085: файлы — S3-совместимое хранилище (MinIO), имена сохранить. Реализация — `bytea` в Postgres (`news_images` ≤ 2 МБ, `club_images` ≤ 1/3 МБ, `club_photo_images` до 10 × 3 МБ на клуб). Отступление принято исполнителем (S-1, G4), в DECISIONS.md не отражено, владельцу не вынесено; `docs/backlog.md:87` по-прежнему обещает MinIO. | При 1 000 клубов с полным профилем — до ~30 ГБ картинок в основной базе: растут базовые копии WAL-G, время PITR и восстановления (RPO/RTO из stage-6), картинки едут в реплику и в каждый бэкап. | Вынести владельцу: подтвердить bytea (и записать в DECISIONS, убрать из backlog) или перенести байты в S3 (интерфейс хранилища + перенос данных). | да — нужно решение | CONFIRMED |
| CODE-6 | Medium | Прослеживаемость тестов (CLAUDE.md «Тесты»: ID сценария в имени подтеста) | `docs/golden-scenarios.md`; тесты `server/**/*_test.go`, `web/**` | Из 559 golden-сценариев 352 не упоминаются в тестах ни по ID, ни по предложенному имени теста; среди P1 — 35 из 126 (GS-025, 026, 037, 038, 057, 062, 069, 080, 082–085, 110, 116, 170, 182, 190, 193, 229, 231, 234, 235, 246, 255, 258, 262, 263, 379, 382, 384, 385, 415, 420, 450, 451). Выборочно 4 из 6 проверенных покрыты под другими именами (GS-080 — `TestShopOrdersScreen`, GS-116 — `app_integration_test.go:1217`), но GS-379 покрыт частично: нет проверки продления активной подписки «+10 к оплаченному периоду», ошибки `days = 0` и смены плана (`/subscription/plan` в тестах не вызывается). Пункт MIGRATION 70.1 «добавить тесты-подтверждения GS-379, GS-382…» открыт. | Скрипт: `gs-map.txt` (ID → имя теста) против `grep -rhoE 'GS-[0-9]+'` и диапазонов `GS-a…GS-b` в тестах + список `func Test*` (файлы `gs-*.txt` в папке аудита). `grep '"days": 0\|subscription/plan'` по тестам — пусто. | Подписать существующие подтесты ID сценариев (механически), дописать GS-379 (продление поверх периода, `days=0`, смена плана) и прогнать скрипт сверки в CI (список сценариев без теста — артефакт). | нет | CONFIRMED |
| CODE-7 | Medium | Документация / управление требованиями | `docs/MIGRATION.md` (собирается из `docs/parts/*.md`, `tools/build-migration-doc.sh`); `docs/OPEN_QUESTIONS.md:3`; `docs/backlog.md` | Чек-лист переноса не отражает состояние: 47 `[x]` / 457 `[ ]`, в разделах 10–60 (экраны, API, БД, настройки, задачи, интеграции) отмечено 2 пункта из 442, хотя большая часть сделана; таблица в шапке «Пунктов» считает только `[ ]`. В разделе 70 открыты уже сделанные А2 (GS-529…533 с тестами), А7 (Telegram о новом клубе, `telegram/alerts.go:150`), карточки GS-471. `OPEN_QUESTIONS.md` утверждает «все вопросы решены», а в `open-questions/*.md` 58 открытых пунктов, из них устаревшие F1-11 (колокольчик — сделан, `api/notifications.yaml`) и CM-11 (сделан `web/src/i18n/missing.ts`); `backlog.md` без статусов (сделанное и несделанное вперемешку, см. CODE-4, CODE-5). Невозможно ответить «что осталось до go-live» по документам. | Подсчёт `grep -c '^\s*- \[[ x]\]'` по файлам; регенерация `MIGRATION.md` в scratch совпала с файлом (`diff` пуст) — расхождение в источниках `docs/parts`. | Пройти чек-лист (отметить сделанное со ссылкой на тест/код или «не переносится: решение»), считать в шапке оба числа; в `OPEN_QUESTIONS.md` — сводная таблица открытых пунктов из `open-questions/*.md` с пометкой «блокирует прод»; в backlog — статусы. | нет | CONFIRMED |
| CODE-8 | Low | Техдолг (решения без исполнения) | `docs/open-questions/stage1-ledger.md:42-46` (L-6); `docs/backlog.md:89,91`; `db/migrations/0024_mobile_api_tokens.sql`; `db/queries/mobile_api.sql` | Решённые, но не сделанные очистки: ключи `ledger_idempotency`/`billing_requests` старше 13 месяцев (L-6); коды регистрации ПК старше 30 дней и сертификаты, истёкшие > 90 дней (A2 §8); `mobile_api_tokens` — Laravel `sanctum:prune-expired --hours=24` удалял истёкшие токены, здесь строки не удаляются никогда (отзыв = `revoked_at`, это нужно на время поклубного перехода, `mobile-compat.md:189`). | `grep` по `db/queries` — `DELETE` для этих таблиц нет. Не срочно (первые 13 месяцев), но задачи нигде не запланированы со сроком. | Завести задачи с датой: очистка токенов истёкших > N дней после переноса последнего клуба; L-6 и A2 §8 — в общий воркер очистки (тот же, что CODE-1/2). | нет | CONFIRMED |
| CODE-9 | Low | Документация в коде | 24 из 75 ссылок вида «`app/…php method()` (стр. N…)» в `server/**/*.go`, напр. `server/internal/billing/pricing/price.go:25,35,57,73,85`, `tariff.go:37,47,106,135`, `billing/balance.go:233` | После переноса изменений старого проекта (a680bab → 71db8d6) номера строк Laravel в комментариях-ссылках устарели: `discounted()` «стр. 371–376» — сейчас 463; `minutesPriceInCents()` «206–209» — 250; `CashRegisterService::refund()` «174» — 150. Требование CLAUDE.md «комментарий-ссылка на файл и метод» выполнено, но строки вводят в заблуждение при сверке формул; то же в `docs/business-rules.md`. | Скрипт сверки (функция PHP ищется в текущем файле, допуск ±5 строк): 75 ссылок, 51 верна, 24 — нет (список — `code/stale-laravel-lines.txt`). | Убрать номера строк из ссылок (метод + коммит базы `71db8d6`) или обновить их скриптом и проверять в CI. | нет | CONFIRMED |
| CODE-10 | Low | DRY (фронт, деньги) | `web/src/features/tariffs/api.ts:45-47` `priceInput`; `web/src/features/superadmin/PlansPage.tsx:15-18` `priceValue`; `web/src/features/settings/api.ts:75-82` `hundredthsToInput`; `web/src/features/reseller/percent.ts` (`trimHundredths`, `formatPercent` — двойной экспорт); `web/src/lib/money.ts` | Тиыны → строка реализованы 7 раз при наличии `lib/money.ts`; `priceInput` и `priceValue` побайтно одинаковы, обе неверны для отрицательных значений (`Math.floor(-150/100)` = −2, `-150 % 100` = −50 → «-2.-50»); knip: «Duplicate exports» в `reseller/percent.ts`. Сейчас цены неотрицательны (валидация), поэтому вреда нет. | `npx knip@6.44.0`; ручное чтение функций. | Свести в `lib/money.ts` (`tiynToFixed2`, `hundredthsToInput`), покрыть общими векторами; удалить двойной экспорт. | нет | CONFIRMED |
| CODE-11 | Low | Мёртвый сгенерированный код (фронт) | `web/src/api/gen/common.d.ts`, `i18n.d.ts`, `system.d.ts`, `v1.d.ts`; `web/scripts/gen-api.mjs` | Четыре файла типов генерируются и коммитятся, но нигде не импортируются (knip «Unused files»); `v1.d.ts` — типы интеграционного API, которым фронт не пользуется. Шум в `task gen:check` и ревью. | `npx knip@6.44.0 --reporter compact` → Unused files (7, из них 4 — `src/api/gen`); `grep` импортов — пусто. | Не генерировать TS-типы для разделов без вызовов из фронта (список в `gen-api.mjs`). | нет | CONFIRMED |
| CODE-12 | Low | Сложность нового денежного кода | `server/internal/reservations/paid.go:169` `startPaidSession` (когн. сложность 57), `reservations/app.go:596` `Book` (53), `shop/shop.go:254` `PlaceOrder` (56), `billing/service/guest.go:107` `SellGuestSession` (52) | BC-21 оставлен без изменений, но после него в денежном пути появились новые функции выше порога 40 (брони из приложения: `startPaidSession` — 4 ветки денег: код, своя сессия, урезание под чужую бронь с пакетом/возвратом). Это самые сложные функции прод-кода вместе с переносом данных. | `golangci-lint` с `gocognit` (min 40): 31 прод-функция; топ-15 — в `lint-extra.txt`. | При следующем изменении — выделить «урезание под чужую бронь» и «запись части сессии» в отдельные функции с табличными тестами. | нет | CONFIRMED |
| CODE-13 | Medium | Надёжность / тесты | `server/internal/agent/stream/rebalance_integration_test.go:270`, `stream/conn.go` (recordResult) | Базовый прогон: `TestRebalanceAfterInstanceReturns` упал 1 раз из 4 («timeout: all results recorded»); в журнале `agent command result not recorded … context canceled` — итог команды, пришедший во время перебалансировки (закрытие потока), не записывается: запись идёт в контексте потока, который уже отменён. | Базовый прогон `go-int.json` 22.11.2026; 3 повтора подряд прошли | Записывать итог в контексте, не зависящем от закрытия потока (`context.WithoutCancel` + таймаут), тест на закрытие потока сразу после итога | нет | CONFIRMED (флейк воспроизведён 1/4) |

## Что проверено и как

**Соответствие требованиям.**
- `docs/MIGRATION.md`: 504 пункта, `[x]` 47 (45 в разделе 70, по 1 в API и настройках), `[ ]` 457. `MIGRATION.md` пересобран
  `tools/build-migration-doc.sh` в scratch — совпадает с файлом.
- Маршруты Laravel → аналоги: все 50 экранов `routes/web.php` имеют маршрут в `web/src/routes` или серверный обработчик
  (`/offline`, `/manifest.webmanifest`, `/docs/api`, `/wiki`, картинки — `public.go`, `settings/section.go`, `news.go`, `wiki.go`);
  `routes/api_staff.php` (24) — все в `http/legacy/routes.go`; `routes/api.php` — гостевые/каталог в `legacy`, агентские и
  гостевые маршруты агента — gRPC (`docs/mobile-compat.md` §1.4, осознанно), `/api/settings/{section}/{key}` — Q-059;
  `routes/api_v1.php` (17) — `sections/v1`. Все 48 именованных ключей `throttle:N,M,<имя>` из маршрутов Laravel есть в Go.
  Все 26 ключей `clubs.settings` (и новые `app_booking_*`, `agent_shell_theme`) используются в Go. Upstream: `71db8d6` = `origin/main`.
- Все `rpc` из `proto/` реализованы (поиск методов-получателей) — заглушек `Unimplemented` в бою нет; TODO в Go — один
  (`app/hooks.go:42`, известный W-3). Заглушки во фронте — только поле поиска в шапке (как в старом, `screens.md`).
- Выборочная проверка `[x]` и `[ ]`-пунктов (≥ 15): А4, А9, А10, А11, А13, 70.2 скидка/счета/оплаты, 70.4 оплата/запуск/отмена
  брони, 70.5 выплата, 70.9 поиск/уровни зон — код и тесты есть; А2, А7, карточки GS-471 — сделаны, но `[ ]` (CODE-7);
  А1/А3/А5/А6/А12 — GS-081, GS-143, GS-549 есть, GS-379/GS-382 нет (CODE-6); 50.6 №6, №8, №9 — не сделаны (CODE-1, CODE-2, CODE-8);
  №3 `monitoring:certificates` — нет (CODE-3).
- Фоновые задачи `routes/console.php` (11) сверены с `internal/worker`, `internal/monitoring` и `app/core.go`.

**Бизнес-логика денег (сверка Laravel `71db8d6` ↔ Go ↔ `business-rules.md` ↔ тест):**
BR-012 `discounted` (`pricing.Discounted`, golden через BC-01), BR-014 цена минут `intdiv(h·m+30,60)` (`pricing.MinutesPrice`,
GS-041…045), BR-015 эффективные минуты, BR-032 `SessionSettlement::settle` (`session.Settle` — построчно), BR-041 бонус пополнения
(BC-01), BR-091 перевод другу (`billing.FriendTransfer`: проверки, блокировки, текст ошибок — совпадает, GS-075), BR-174/178
бронь: `PlanStart` = `min(d, intdiv(left+59,60))`, доли `intdiv(paid·m, d)` — совпадает; `DurationMinutes`, `BookedMinutes` (ceil);
BR-180 сумма счёта, BR-182 цена со скидкой, BR-183 `PeriodEnd`/`addMonthsNoOverflow`, BR-186 начисление реселлеру
(`subscription/calc.go`, фикстура PHP 8.4), продление подписки суперадмином (`SaExtendSubscription` = `extend()`, кроме теста —
CODE-6), запрет прямой записи балансов (триггер `member_balances_guard`, единственный `INSERT` — создание строки), аудит денег
(все `ledger.Post` денег идут через `billing.Charge/Credit/TopUp/…` с `auditMoney`; остальные `Post` — минутный ledger).
Расхождений в формулах без записи в DECISIONS не найдено.

**Static analysis.**
- golangci-lint v2.15.0, временный конфиг `code/extra.golangci.yml` (копия `.golangci.yml` + gocritic, unparam, sqlclosecheck,
  contextcheck, prealloc, gocognit(40), durationcheck, makezero, nakedret, errchkjson, forcetypeassert, exhaustive, dupl(150),
  goconst, musttag, perfsprint, usestdlibvars, sloglint, fatcontext, intrange, mirror; nilerr/errorlint/bodyclose/noctx/
  rowserrcheck уже в проекте): 1 392 находки, в прод-коде без goconst — 135: contextcheck 32 (намеренно отвязанные контексты
  фона), sqlclosecheck 10 (только `internal/migrate`, известно BC-20), errchkjson 19 (Marshal фиксированных структур), unparam 6,
  exhaustive 24 (enum proto/PHP-JSON), gocognit 31, gocritic 1, prealloc 2. Реальных дефектов среди них не найдено (кроме CODE-12).
  Штатный прогон CI-конфигом (базовый прогон): 0 находок, go vet 0, gosec 0.
- `tsc --noEmit` (TS 6.0.3) поверх `strict` + `noUncheckedIndexedAccess` (уже включены): `noImplicitReturns` 0,
  `noImplicitOverride` 0, `exactOptionalPropertyTypes` 82, `noPropertyAccessFromIndexSignature` 80 — стилистика API-параметров.
- ESLint 10.14 с типами (`projectService`) и правилами `no-floating-promises` 0, `react-hooks/exhaustive-deps` 0,
  `no-misused-promises` 12 (8 — `onSubmit={handleSubmit(...)}`, 4 — тесты), `switch-exhaustiveness-check` 4,
  `no-unnecessary-condition` 19.
- Агент: сборка копии (`git archive`, контейнер `audit-code-dotnet`, образ `mcr.microsoft.com/dotnet/sdk:10.0.403`) с
  `AnalysisMode=All`, `TreatWarningsAsErrors=false`: 202 предупреждения (164 в `src`): CA1848 94, CA1515 60, CA1032 58, CA2000 34,
  CA1051 32 … Проверены CA2213 `AgentRuntime._streamCts` (освобождается владельцем в `RunStreamAsync`, комментарий стр. 1020),
  CA5394 (Random для джиттера — не безопасность), CA5398 (TLS 1.2/1.3 — осознанно). Штатная сборка — без предупреждений
  (`TreatWarningsAsErrors=true`). Копия удалена.

**Зависимости.**
- Go: все прямые зависимости используются; `testcontainers`, `goleak`, `yaml/v3` в бинарники не попадают
  (`go list -deps ./cmd/zafiro` — 0), `go-sql-driver/mysql` — только `cmd/migrate-from-mysql`. Отставания — только минорные/патчи
  (pgx 5.11→5.13, grpc 1.84→1.85, x/crypto 0.57→0.59 и т. п.; Dependabot). Лицензии 57 модулей прод-бинарников: Apache 23,
  BSD 12, MIT 19, ISC 1, MPL-2.0 1 (mysql-драйвер, только инструмент переноса, без изменений — совместимо); GPL/AGPL нет.
- npm: прод-зависимости — MIT 26, OFL 4, Apache 2, CC-BY-4.0+OFL+MIT 1 (Font Awesome, заголовок лицензии в `fa-subset.css`
  сохранён), Unlicense 1; GPL/AGPL нет. knip 6.44.0: `zod` не импортируется (известно, решение владельца AF-2), 4 неиспользуемых
  сгенерированных файла (CODE-11). Мажорные отставания: `i18next` 26→27, `react-i18next` 17→18, `typescript` 6→7,
  `@testing-library/jest-dom` 7→8; `swagger-ui-dist` 5.17.14 (2024) и `@fortawesome` 6.4.0 закреплены как в старом (Q-064).
- Агент (NuGet, центральные версии + lock-файлы): все 14 пакетов используются (BouncyCastle — Ed25519 обновлений, QRCoder,
  Grpc.StatusProto, ProtectedData, WindowsServices, WebView2); лицензии MIT/Apache. `xunit` 2.9.3 — линия v2 (есть v3), не срочно.

**Архитектура.**
- Граф импортов 132 пакетов (`go list`): циклов нет, доменные пакеты не импортируют `http`/`app`; fan-in: `store` 64,
  `db` 35, `httpx` 34, `activity` 32; fan-out: `http` 49, `app` 42 (сборка). 30 из 66 HTTP-пакетов читают `store` напрямую;
  запись из обработчиков — только CRUD настроек/новостей/задач (54 вызова), деньги — через сервисы (`legacy` вызывает
  `cash/service`, `reservations`, `billing`). Интерфейсы маленькие (≤ 5 методов). Самые большие файлы — `http/legacy/staff.go`
  1 678 строк, `migrate/migrate.go` 1 230.
- Покрытие юнит-тестами (`go test -cover`, без integration): money 100 %, cash 95,9 %, auth/permissions 94,1 %,
  billing/session 91,7 %, billing/pricing 91,3 %, billing/bonus 86,1 %; сервисы с БД — 4–30 % (покрываются интеграционными:
  115 из 288 тестовых файлов с тегом `integration`). Без тестов вообще (ни юнит, ни integration): `agent/identity`, `auth/principal`,
  `friends`, `http/newsform`, `http/sections/reseller`, `http/wsticket`, `cmd/import-translations` (584 строки; кроме последнего —
  косвенно через интеграционные тесты `internal/http`).

**Техдолг.** `docs/open-questions/*.md`: 58 открытых `[ ]` (audit-perf 9, audit-code 8, audit-security 6, audit-ci 6,
stage6-backup 5, stage6-observability 4, stage5-migration 4, stage5-agent 3, stage4-h1 3, …). Блокируют прод (по `stage-6-report.md`
и `stage5-*`): проверка агента на Windows 10/11 (S7-HA-1, 70.7, 70.9), ключ подписи обновлений и сертификат Authenticode,
часовой пояс MySQL прода (S5-M1-О1), прод-инфраструктура (S3 копий в другом регионе, TLS внутри, NATS ×3, реплика),
пилот на копии прода. Плюс несделанные решения CODE-3, CODE-4, CODE-5 и очистки CODE-1, CODE-2, CODE-8.

## Что не удалось проверить и почему

- `govulncheck` (базовый прогон): `vuln.go.dev` закрыт прокси — уязвимости Go-зависимостей не проверены (остаточный риск:
  известные CVE в pgx/grpc/x/crypto). `npm audit` — в базовом прогоне (область безопасности).
- Покрытие интеграционными тестами не измерено: запуск `-tags=integration -cover` поднимает testcontainers с произвольными
  именами (правило имён `audit-code-*`) и конкурирует с базовым прогоном; оценка — по числу интеграционных файлов на пакет.
- Агент на Windows (analyzers для `net10.0-windows`-специфики, WebView2) — только сборка в Linux-контейнере.
- Покрытие golden-сценариев сверено по ID/именам; содержательная сверка каждого из 352 непомеченных сценариев не делалась
  (выборка 6, из них 4 полностью покрыты, 1 частично, 1 — GS-375 — не реализован).

## Измерения

| Что | Значение |
|---|---|
| MIGRATION.md | 504 пункта: 47 `[x]`, 457 `[ ]` (разделы 10–60: 2 из 442 отмечены) |
| Golden-сценарии | 559; с тестом по ID/имени 207; без — 352 (P1 35 / P2 158 / P3 159); GAP — 36 из 36 с тестом |
| Ссылки «метод Laravel (стр. N)» в Go | 75, устарели 24 (32 %) |
| golangci-lint (доп. линтеры) | 1 392 всего; прод без goconst — 135; gocognit > 40: 31 прод-функция (макс. 57) |
| tsc доп. флаги | exactOptionalPropertyTypes 82, noPropertyAccessFromIndexSignature 80, остальные 0 |
| ESLint с типами | misused-promises 12, switch-exhaustiveness 4, unnecessary-condition 19, floating-promises 0 |
| Агент AnalysisMode=All | 202 предупреждения (src 164), штатно 0 |
| Go: LOC прод (без gen/store) / тесты | 93 302 / 58 330; 288 тестовых файлов, 115 integration |
| Пакеты Go без тестов | 7 (вне сгенерированных) |
| Открытые пункты open-questions | 58 в 15 файлах; OPEN_QUESTIONS.md — 0 открытых из 86 |
| Диск | 1,7–2,4 ГБ свободно во время работы; копия агента удалена |
