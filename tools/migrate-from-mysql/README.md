# Перенос данных MySQL (Laravel Zafiro) → PostgreSQL (zafirogo)

Статус: **ядро готово (Этап 5, M1)** — перенос одного клуба со сверкой. Производные данные (M2) — список в конце.

- Команда: `server/cmd/migrate-from-mysql` (решение Q-077), логика — `server/internal/migrate`.
- Решения этого блока: `docs/open-questions/stage5-migration.md` (S5-M1-…).
- Источник для тестов: настоящая схема MySQL старого проекта и демо-данные, записанные старым кодом —
  `server/cmd/migrate-from-mysql/testdata/{schema,demo-data}.sql`, генерирует `tools/migrate-from-mysql/dump-schema.sh`.

## Принцип: поклубно

Клубы переходят по одному (решение владельца, `CLAUDE.md`). Пока клуб не перенесён, он работает на Laravel; после
переноса — только на zafirogo. Данные одного клуба переносятся целиком одной транзакцией, остальные клубы не
затрагиваются. Общие аккаунты (`users`) не дублируются: аккаунт, уже перенесённый с другим клубом, остаётся как есть.

## Запуск

```sh
export ZAFIRO_MIGRATE_MYSQL_DSN='zafiro_ro:…@tcp(mysql:3306)/zafiro'   # логин MySQL только для чтения
export ZAFIRO_MIGRATE_PG_DSN='postgres://zafiro_owner:…@pg:5432/zafiro' # владелец схемы, член zafiro_platform (PD-9)
cd server
go run ./cmd/migrate-from-mysql plan   --club zafiro-arena            # сухой прогон, отчёт
go run ./cmd/migrate-from-mysql apply  --club zafiro-arena --json apply-report.json
go run ./cmd/migrate-from-mysql verify --club zafiro-arena
```

`--club` — id или slug клуба в MySQL. Строки подключения — из окружения (в аргументах их видно в списке процессов);
флаги `--mysql`/`--pg` — для локальной отладки. Прочие флаги:

| Флаг | По умолчанию | Смысл |
|---|---|---|
| `--source-tz` | `UTC` | пояс строк времени в MySQL = `config('app.timezone')` Laravel (в старом проекте `UTC`, `config/app.php` ~74) |
| `--api-log-retention` | `2160h` | срок хранения `api_request_logs` (`integrations.request_log_retention`): старше — не переносятся |
| `--id-reserve` | `1000000000` | запас последовательностей id над максимальным id таблицы во всей MySQL (Q-029, S5-M1-3) |
| `--legacy-settings` | — | `storage/app/settings.json` для клуба `legacy` без `clubs.settings` (Q-022) |
| `--batch-size` | `2000` | строк в одной вставке (`INSERT … SELECT FROM unnest(…)`) |
| `--json` | — | отчёт в JSON (кроме текстового в stdout) |

Коды выхода: `0` — успех; `1` — ошибка; `2` — аргументы; `3` — блокеры в данных (ничего не записано; `plan` тоже
возвращает 3, если блокеры есть); `4` — расхождение сверки (`apply` откатан); `5` — клуб уже перенесён
(`apply` ничего не изменил).

## Что делает каждый режим

- **plan** — читает клуб в одном снимке MySQL (`REPEATABLE READ`, `CONSISTENT SNAPSHOT`), прогоняет **все** строки через
  те же преобразования, что `apply`, и проверяет их против схемы Postgres; ничего не пишет. Отчёт: строки по таблицам,
  что конвертируется (колонка → тиыны/timestamptz/boolean/jsonb), находки:
  блокеры (перенос невозможен), предупреждения (переносится с нормализацией/пометкой), сведения.
- **apply** — партиции журналов под старые даты (`db.EnsurePartitions`, ≤ 36 месяцев за вызов), затем **одна транзакция**:
  блокировка переносов (advisory lock), `app.club_id` клуба, `SET LOCAL ROLE zafiro_platform` (PD-9); таблицы клуба;
  входящие остатки в ledger; запись аудита `migration.club_imported`; расширения (M2); `RESET ROLE` и сдвиг
  последовательностей; **сверка до коммита**. Любая ошибка, блокер или расхождение — откат, в Postgres ничего нет.
  Клуб уже в Postgres — отказ (код 5) без изменений (S5-M1-1, S5-M1-2).
- **verify** — сверка перенесённого клуба с MySQL (только чтение): число строк по каждой таблице, баланс каждого клиента
  (основной и бонусный, MySQL × 100 = `member_balances` = сумма проводок ledger), суммы балансов, `total_spent`,
  остатки склада (каждый товар и сумма), приходы/расходы кассы, остаток открытой смены, остаток счёта `cash_drawer`,
  число ПК. Сверяет состояние на момент переноса: после начала работы клуба в zafirogo расхождения ожидаемы.

## Проверки plan (находки)

| Код | Что | Важность |
|---|---|---|
| `Q-026` | значение вне `CHECK (col IN (…))` Postgres (списки читаются из `pg_constraint`) | блокер: исправить в MySQL |
| `Q-025` | slug клуба совпадает без учёта регистра со slug клуба zafirogo | блокер |
| `user` | id аккаунта занят в zafirogo аккаунтом с другим телефоном; телефон занят другим id | блокер: ручное слияние |
| `user_ref` | строки клуба ссылаются на несуществующий аккаунт | блокер |
| `not_null`, `value`, `column` | NULL в NOT NULL колонке Postgres; значение не преобразуется (дата, сумма, JSON); колонка MySQL без пары | блокер |
| `saas_plan` | тариф платформы: id занят другим slug или slug — другим id | блокер |
| `migrated` | клуб уже перенесён | блокер (`apply` — код 5) |
| `Q-004` | отрицательный баланс → входящий остаток с пометкой | предупреждение |
| `Q-027` | не-сотрудническая роль (`Пользователь` и т. п.) → `client` | предупреждение |
| `Q-030` | `created_at = NULL`: в `activity_logs` — `updated_at` или момент переноса, в прочих — как есть | предупреждение |
| `Q-022` | клуб `legacy` без `settings`: файл `--legacy-settings` или настройки по умолчанию | предупреждение |
| `Q-023` | настройки: минуты строкой («30 сек»), черновые ключи (`tax_rate`, `loyalty_rules`), флаги не boolean, уровни не списком или с равными порогами (BQ-3) | предупреждение |
| `user_kept` | общий аккаунт уже в zafirogo (перенесён с другим клубом) — оставлен как есть | сведения/предупреждение |
| `skipped`, `retention`, `partitions`, `source` | таблицы вне ядра, журнал API старше срока, диапазон партиций, пояса MySQL | сведения |

## Что переносится (ядро M1)

Порядок — по внешним ключам (`server/internal/migrate/spec.go`). Колонки не перечисляются вручную: переносятся все
колонки MySQL, у которых есть пара в Postgres (имена 0001 — как в Laravel); колонка MySQL без пары — блокер (схемы
разошлись). Новые колонки Postgres (`payment_method`, `income_kind`, `zone_id`, `source`, …) — по умолчанию, их заполняет M2.

| Таблицы | Как |
|---|---|
| `saas_plans` | только тариф подписки клуба; уже есть (по id, тот же slug) — не трогаем |
| `users` | участники клуба и все аккаунты, на которые ссылаются строки клуба; существующий id — не перезаписываем (S5-M1-4) |
| `clubs` | без `installation_key*` (Q-050: ключ установки выпускается заново); `settings` — как есть (v1) |
| `club_subscriptions`, `club_memberships` (Q-027), `club_invitations` | как есть |
| `member_balances` | строка с нулями (новый id: роль платформы вставляет только `club_id, user_id`, даты — 0004), остаток — `billing.MigrateOpeningBalance` |
| склад, касса, зоны, ПК, тарифы, сессии, пакеты, брони, друзья, чат, заказы, новости клуба, задачи | как есть (id сохраняются) |
| `activity_logs` | целиком (старый код не чистил), партиции под старые даты |
| `club_request_metrics`, `club_load_snapshots` | как есть (Q-083 «по желанию») |
| `platform_logs` | события с `club_id` клуба (без клуба — платформенный шаг) |
| `api_request_logs` | в пределах `--api-log-retention` |

Не переносятся ядром: `api_keys`, `api_idempotency_keys`, `webhook_endpoints`, `webhook_deliveries` (M2),
`agent_qr_logins` (одноразовые QR, живут минуты), `transfer_codes`, `password_recovery_codes` (Q-051),
`server_metrics`, `request_metrics` (Q-083), таблицы инфраструктуры Laravel (`sessions`, `cache`, `jobs`,
`personal_access_tokens` → M2 для мобильных токенов, …). Платформенные `languages`, `translations`,
`platform_settings`, новости платформы — один раз при первом переходе (платформенный шаг, не поклубный).

## Преобразования

- **Деньги**: `decimal(…,2)` тенге → `bigint` тиыны × 100 **строкой** (`money.ParseTenge`, без float). Правило
  определяется по паре типов (decimal в MySQL → bigint в Postgres); масштаб не 2 — ошибка схемы.
  `*_cents` уже в тиынах — как есть. Суммы внутри JSON — v1 (тенге), v2 — M2.
- **Время**: строки `DATETIME`/`TIMESTAMP` читаются без смены пояса сессии MySQL (как их читал Laravel: в
  `config/database.php` нет `timezone`) и трактуются в `--source-tz` → `timestamptz`. В отчёте — пояса сервера.
- **boolean**: `tinyint(1)` → `false` только для `0`. **JSON** → `jsonb` (проверка валидности и `\u0000`).
- **id** сохраняются (Q-029); после переноса последовательность каждой таблицы ставится не ниже
  `max(id во всей MySQL) + --id-reserve` (Laravel продолжает выдавать id остальным клубам — S5-M1-3); не уменьшается.

## Деньги в ledger

- Балансы клиентов: `migration_opening_balance`, ключ `opening_balance:<user_id>` (счета `member_main`/`member_bonus` ±,
  `opening_balance` ∓). Отрицательные остатки — тем же путём (Q-004), в отчёте — `Q-004`.
- Касса: `migration_opening_balance`, ключ `opening_cash_drawer` — остаток открытой смены (opening + приходы − расходы,
  `CashRegisterService::totals()`), а без открытой — `closing_balance` последней закрытой. Тогда снимок кассы при
  закрытии первой смены в zafirogo (CSQ-7) сразу верный.
- Пакеты минут: ledger минут, ключ `opening_minutes:<time_package_id>` (если владелец пакета — участник клуба).
- Аудит: одна запись `money` / `migration.club_imported` с суммами.

## Порядок перевода клуба

- [ ] `plan` на копии продовой MySQL: блокеры исправить в источнике, предупреждения согласовать с владельцем клуба.
- [ ] Заморозка клуба в Laravel (только чтение по `club_id`), уведомление персонала; открытые сессии на ПК завершить.
- [ ] `plan` → `apply` → `verify` (код 0); сохранить JSON-отчёты.
- [ ] Расширения M2 (ниже) — в том же `apply`, когда будут готовы.
- [ ] Переключение клуба: DNS/конфиг, переустановка агентов ПК (gRPC + mTLS), мобильные — Q-058 (клубы с мобилками последними).
- [ ] Клуб в Laravel оставить только для чтения.

## Тесты

- Юнит: `go test ./internal/migrate/ ./cmd/migrate-from-mysql/`.
- Интеграционные (Docker): `TESTCONTAINERS_RYUK_DISABLED=true go test -tags integration ./internal/migrate/... ./cmd/migrate-from-mysql/`
  — MySQL `mysql:8.4.13` с `testdata/schema.sql` + `demo-data.sql`, Postgres с миграциями. Сценарий: данные-проблемы
  (старая роль, настройки «30 сек», журнал 2023 года, `created_at = NULL`, журнал API старше срока, пакет минут,
  общий аккаунт с отрицательным балансом во втором клубе, ПК со статусом вне CHECK, slug, занятый клубом zafirogo) →
  plan → apply → verify → балансы через ledger = MySQL × 100 → повторный apply ничего не меняет → второй клуб:
  блокеры откатывают перенос, после исправления переносится, первый клуб не задет → RLS и работа сервера
  (`billing.Charge`, журнал) на перенесённых данных. Команда целиком — `cmd/migrate-from-mysql/main_integration_test.go`.
- `testdata` пересоздать: `tools/migrate-from-mysql/dump-schema.sh` (копия старого проекта + `php artisan migrate:fresh`
  в MySQL + `tools/visual/old/seed.php`; нужен Docker и кэш vendor `tools/php-oracle`). Перезапускать при изменении
  миграций старого проекта или `tools/visual/fixtures/demo.json`.

## M2: производные данные (точки расширения)

Расширение — `migrate.Extension` (`Plan`/`Apply`/`Verify`, окружение `migrate.Env`: снимок MySQL, транзакция Postgres
клуба, отчёт). `Apply` выполняется в транзакции ядра после входящих остатков и до сверки — ошибка откатывает всё.
Подключение — `Options.Extensions` (в `cmd/migrate-from-mysql/main.go`).

- [ ] JSON → v2 (Q-003, `docs/schema-changes.md` §10.11): `cash_transactions.receipt_data`, `shop_orders.items`, уровни
      в `clubs.settings` (BQ-3); вместе — нормализация настроек Q-023 (сейчас только отчёт).
- [ ] `tariff_sales.sold` = число `activity_logs` `tariff.purchased` по тарифу (Q-007, `docs/open-questions/stage1-service.md`).
- [ ] `cash_transactions.income_kind` старых приходов по `activity_logs` (`top_up`, `guest_session`; CSQ-2,
      `docs/open-questions/stage1-cash-service.md`); `payment_method` — из `activity_logs.details.payment_method`.
- [ ] `session_purchases.zone_id/source` частей текущих сессий из `activity_logs` (SV-3); `product_id` в старые чеки (CQ-9).
- [ ] Токены мобильных приложений `personal_access_tokens` → `mobile_api_tokens` с тем же id (`docs/mobile-compat.md` §2.3).
- [ ] Ключи API (`api_keys`, `api_idempotency_keys`), вебхуки (`webhook_endpoints`, `webhook_deliveries`) — секреты
      `APP_KEY` расшифровать старым ключом и зашифровать новым (Q-050, EV-2; ключ — секрет окружения переноса).
- [ ] Старые токены агентов (`club_computers.agent_token_hash`) — погасить или оставить (агенты переустанавливаются).
- [ ] Файлы: логотипы, картинки входа (`clubs.logo_path`, `signin_image_path` → `club_images`), новости
      (`storage/app/news/**` → `news_images`), Q-085.
- [ ] Пересчёт балансов из истории старой БД и отчёт расхождений (Q-005).
- [ ] Платформенный шаг (один раз): `languages`, `translations` (`docs/open-questions/stage3-foundation.md`),
      `platform_settings` (секреты — с перешифровкой), новости и журнал платформы без клуба.
- [ ] После переноса всех клубов: `created_at NOT NULL DEFAULT now()` (Q-030), `CHECK (balance >= 0)` (Q-004),
      удаление полей команд `club_computers` (Q-032) — отдельными миграциями.

## Открытые вопросы

`docs/open-questions/stage5-migration.md`; раньше — `docs/open-questions/infra.md` (раздел «Перенос данных»).
