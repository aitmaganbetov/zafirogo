# Перенос данных MySQL (Laravel Zafiro) → PostgreSQL (zafirogo)

Статус: **ядро (M1) и производные данные (M2) готовы (Этап 5)** — перенос платформы и одного клуба со сверкой;
сквозная проверка нового сервера на перенесённых данных — `server/cmd/migrate-from-mysql/e2e_integration_test.go`.

- Команда: `server/cmd/migrate-from-mysql` (решение Q-077), логика — `server/internal/migrate`.
- Решения этого блока: `docs/open-questions/stage5-migration.md` (S5-M1-…, S5-M2-…).
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
export ZAFIRO_MIGRATE_LARAVEL_APP_KEY='base64:…'      # APP_KEY старого .env (секреты вебхуков, токены Telegram, Q-050)
export ZAFIRO_MIGRATE_LARAVEL_PREVIOUS_KEYS=''        # APP_PREVIOUS_KEYS, если APP_KEY ротировали
export ZAFIRO_SECURITY_DATA_KEYS='kid:секрет'          # ключи данных НОВОГО сервера — та же переменная, что у сервера
cd server
go run ./cmd/migrate-from-mysql platform apply --laravel-storage /srv/zafiro/storage/app/private --translations-seed ../db/seed
go run ./cmd/migrate-from-mysql plan   --club zafiro-arena --laravel-storage /srv/zafiro/storage/app/private --thaw-subscription active
go run ./cmd/migrate-from-mysql apply  --club zafiro-arena --laravel-storage … --thaw-subscription active --json apply-report.json
go run ./cmd/migrate-from-mysql verify --club zafiro-arena --laravel-storage …
```

`--club` — id или slug клуба в MySQL. Строки подключения и ключи — только из окружения (в аргументах их видно в списке
процессов); флаги `--mysql`/`--pg` — для локальной отладки. Секреты нигде не печатаются: в отчёте — только id строк.
Прочие флаги:

| Флаг | По умолчанию | Смысл |
|---|---|---|
| `--source-tz` | `UTC` | пояс строк времени в MySQL = `config('app.timezone')` Laravel (в старом проекте `UTC`, `config/app.php` ~74) |
| `--api-log-retention` | `2160h` | срок хранения `api_request_logs` (`integrations.request_log_retention`): старше — не переносятся |
| `--id-reserve` | `1000000000` | запас последовательностей id над максимальным id таблицы во всей MySQL (Q-029, S5-M1-3) |
| `--legacy-settings` | — | `storage/app/settings.json` для клуба `legacy` без `clubs.settings` (Q-022) |
| `--laravel-storage` | — | корень диска `local` старого Laravel: `storage/app/private` (`config/filesystems.php`; туда `storeAs(…, 'local')` писали логотипы, картинки входа и новостей). Не задан — картинки не переносятся (предупреждение) |
| `--thaw-subscription` | — | `active`/`trialing`: статус подписки до заморозки; приостановленная на время перевода подписка переносится в нём (S5-M2-1) |
| `--translations-seed` | — | `platform`: каталог `db/seed` (переводы новых строк zafirogo, как `import-translations -seed`) |
| `--core-only` | `false` | только ядро M1, без расширений M2 (отладка) |
| `--batch-size` | `2000` | строк в одной вставке (`INSERT … SELECT FROM unnest(…)`) |
| `--json` | — | отчёт в JSON (кроме текстового в stdout) |

Коды выхода: `0` — успех; `1` — ошибка; `2` — аргументы; `3` — блокеры в данных (ничего не записано; `plan` тоже
возвращает 3, если блокеры есть); `4` — расхождение сверки (`apply` откатан); `5` — клуб уже перенесён
(`apply` ничего не изменил). У `platform` — те же коды (кроме 5: повторный `platform apply` разрешён).

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
| `Q-050` | секрет (вебхук, токен Telegram) без `ZAFIRO_MIGRATE_LARAVEL_APP_KEY`/`ZAFIRO_SECURITY_DATA_KEYS` или не расшифровывается этим APP_KEY | блокер |
| `Q-003` | чек/заказ v1 не читается сервером (оставлен v1) — предупреждение; v2 читается иначе, чем v1, — блокер (ошибка конвертации) | см. слева |
| `Q-023n` | что изменила нормализация настроек; «экран показывал X, действовало Y» — предупреждение | сведения/предупреждение |
| `CQ-9` | строка старого чека без `product_id`, имя не однозначно — осталась без `product_id` | сведения |
| `CSQ-2` | приход и пополнение, и гостевая сессия по журналу; способ оплаты не распознан | предупреждение/сведения |
| `SV-3` | части идущей сессии не сопоставились с журналом — `zone_id/source` NULL (сервер читает по `kind`) | предупреждение |
| `Q-058` | токен мобильного приложения не переносится (владелец не перенесён, хэш не sha256); id занят — блокер | предупреждение |
| `Q-085` | файла нет, не PNG/JPEG, больше лимита; не задан `--laravel-storage` | предупреждение |
| `freeze` | подписка не приостановлена (клуб не заморожен?); приостановлена без `--thaw-subscription` | сведения/предупреждение |
| `platform` | платформенный шаг не выполнен (план клуба); при повторном `platform apply` — строки, отличающиеся от MySQL | предупреждение |

## Что переносится (ядро M1)

Порядок — по внешним ключам (`server/internal/migrate/spec.go`). Колонки не перечисляются вручную: переносятся все
колонки MySQL, у которых есть пара в Postgres (имена 0001 — как в Laravel); колонка MySQL без пары — блокер (схемы
разошлись). Новые колонки Postgres (`payment_method`, `income_kind`, `zone_id`, `source`, …) заполняет M2 (ниже).

| Таблицы | Как |
|---|---|
| `saas_plans` | только тариф подписки клуба; уже есть (по id, тот же slug) — не трогаем |
| `users` | участники клуба и все аккаунты, на которые ссылаются строки клуба (включая `api_keys.created_by/revoked_by`, `webhook_endpoints.created_by`); существующий id — не перезаписываем (S5-M1-4) |
| `clubs` | без `installation_key*` (Q-050: ключ установки выпускается заново); `settings` — как есть (v1) |
| `club_subscriptions`, `club_memberships` (Q-027), `club_invitations` | как есть |
| `member_balances` | строка с нулями (новый id: роль платформы вставляет только `club_id, user_id`, даты — 0004), остаток — `billing.MigrateOpeningBalance` |
| склад, касса, зоны, ПК, тарифы, сессии, пакеты, брони, друзья, чат, заказы, новости клуба, задачи | как есть (id сохраняются) |
| `activity_logs` | целиком (старый код не чистил), партиции под старые даты |
| `club_request_metrics`, `club_load_snapshots` | как есть (Q-083 «по желанию») |
| `platform_logs` | события с `club_id` клуба (без клуба — платформенный шаг) |
| `api_request_logs` | в пределах `--api-log-retention` |

Не переносятся: `agent_qr_logins` (одноразовые QR, живут минуты), `transfer_codes`, `password_recovery_codes` (Q-051),
`server_metrics`, `request_metrics` (Q-083), таблицы инфраструктуры Laravel (`sessions`, `cache`, `jobs`, …),
`personal_access_tokens` агентов ПК и других клубов. Ключи API, вебхуки, токены мобильных приложений — M2 (ниже);
платформенные `languages`, `translations`, `platform_settings`, новости и журнал платформы — платформенный шаг.

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

## M2: производные данные (`DefaultExtensions`, в той же транзакции и сверке)

| Расширение | Что | Как сверяется |
|---|---|---|
| `json-v2` (Q-003) | `cash_transactions.receipt_data`, `shop_orders.items` → v2 (тиыны, `"v": 2`) через читатели сервера v1 (`cashsvc.DecodeReceipt`, `shop.DecodeItems`); не читающийся сервером v1 — как есть с предупреждением | каждое значение: чтение сервером v2 в Postgres = чтение v1 в MySQL (до тиына) |
| `json-v2` (CQ-9) | `product_id` в строки старых чеков без него, если имя однозначно среди не удалённых товаров клуба (сравнение в MySQL — как `where('name')`) | то же |
| `json-v2` (Q-023, BQ-3) | `clubs.settings`: минуты-строки → число (действовавшее значение, см. S5-M2-3), флаги → boolean по правилу читателя (`filter_var` или `(bool)`), уровни → v2 (`bonus.EncodeLevels(bonus.Normalize(…))`), черновые ключи — как есть | настройки = нормализованные MySQL; `bonus.ParseSettings` до и после совпадает |
| `derived` (Q-007) | `tariff_sales.sold` = число `tariff.purchased` по тарифу клуба | по тарифу |
| `derived` (CSQ-2, Q-013) | `income_kind` приходов: запись `cash.income` с `details.balance_type` → `top_up`, `details.computers` → `guest_session` (как `ShiftReport::for()`); `payment_method` — подпись из `details.payment_method` → `cash/card/qr` по переводам старой базы | по каждому приходу |
| `derived` (SV-3) | `session_purchases.zone_id/source` частей идущих сессий: записи журнала сессии по порядку (`tariff.purchased` → `tariff`, зона ПК; `time.purchased` → `minutes`, зона-subject; `time.package_started` → `package`) | по каждой части |
| `integrations` | `api_keys` (хэш тот же, H1-1), `api_idempotency_keys` (+ `club_id`), `webhook_endpoints` (секрет: Crypt → `secretbox`, контекст `webhook.SecretContext`), `webhook_deliveries`; `personal_access_tokens` пользователей с ability `club:<id>` → `mobile_api_tokens` (тот же id, истёкшие не переносятся) | число строк; секрет из Postgres (ключ данных) = секрет MySQL (APP_KEY); токены по id и хэшу |
| `files` (Q-085) | `clubs.logo_path`, `signin_image_path` → `club_images`; `news.image_path` → `news_images`; PNG/JPEG, ≤ 1/3/2 МБ (как сервер) | байты = файл на диске |

Пути картинок в `clubs`/`news` переносятся как есть. Старые токены агентов (`club_computers.agent_token_hash`) — как
есть: новый сервер их не читает, агенты переустанавливаются с gRPC (S5-M2-8).

## Платформенный шаг (`platform plan|apply|verify`)

Один раз перед первым клубом (повторный запуск безопасен, S5-M2-11): `languages` (по коду), `translations` (по языку,
группе, `key_hash`; поверх `import-translations`: правки суперадмина из MySQL важнее файлов), строки `db/seed`
(`--translations-seed`, только недостающие), `platform_settings` (токены Telegram — расшифровка APP_KEY, шифрование
ключом данных без контекста, как читает `telegram.Store`), все `saas_plans` (по id и slug), новости платформы
(`club_id IS NULL`, с картинками `news/platform/…`) и журнал платформы без клуба (по id; автор, ещё не перенесённый, —
NULL). Существующие в zafirogo языки, переводы, настройки перезаписываются только при первом `platform apply` (нет записи
аудита `migration.platform_imported`), потом — предупреждение. Сверка — в той же транзакции до коммита.

## Порядок перевода клуба

Старый Laravel не меняется: заморозка — штатной функцией суперадминки, маршрутизация — балансировщиком.

1. [ ] **Подготовка** (за день): копия прод-MySQL → `platform plan` и `plan --club …` на копии; блокеры исправить в
       источнике (в старом Laravel), предупреждения согласовать с владельцем клуба. Клубы с активными мобилками — последними
       (Q-058). Сохранить: APP_KEY (из `.env` старого сервера), ключи данных нового сервера, путь `storage/app/private`
       (если zafirogo на другой машине — `rsync -a` каталогов `club-logos/<id>`, `club-signin-images/<id>`, `news/`).
2. [ ] **Платформа** (один раз, перед первым клубом): `platform apply` → `platform verify` (код 0).
3. [ ] **Заморозка клуба** в окно без игроков (ночь): в суперадминке старого Laravel «Приостановить подписку»
       (`Superadmin\ClubController::suspend()`: `status = suspended`, срок не меняется). С этого момента старый сервер
       отвечает клубу 402: панель, агенты ПК (экран блокировки), `/api/v1`, мобильные приложения — данные клуба больше не
       меняются. Запомнить статус подписки ДО приостановки (`active` или `trialing`, видно в `plan`). Перед заморозкой —
       закрыть или дождаться окончания платных сессий; открытая кассовая смена переносится (остаток сверяется).
4. [ ] **plan** с `--thaw-subscription <статус до заморозки>`: блокеров 0, находка `freeze` «заморозка снята».
5. [ ] **apply** (`--json apply-report.json`, тот же `--thaw-subscription`): одна транзакция со сверкой до коммита; код 0.
6. [ ] **verify** (код 0): число строк, балансы, касса, склад, JSON v2, секреты, токены, картинки. Сохранить отчёты.
7. [ ] **Переключение** клуба на zafirogo:
       - агенты ПК — переустановить новый агент (gRPC + mTLS, ключ образа/коды регистрации — Q-037; ключ установки клуба
         выпускается заново, Q-050);
       - сотрудники — адрес панели zafirogo (на время перехода — отдельный хост, например `app.zafiro.kz`: по общему
         адресу балансировщик не знает клуба сотрудника до входа);
       - мобильные приложения — ничего: `/api/(auth|guest|staff)/` уже идут в zafirogo, токены клуба теперь здесь
         (`mobile_api_tokens`), чужие запросы zafirogo проксирует в Laravel (`docs/mobile-compat.md` §3);
       - интеграции `/api/v1` — добавить префиксы ключей клуба (отчёт `apply`, итог «nginx: префиксы ключей /api/v1»)
         в карту балансировщика:
         ```nginx
         map "$http_x_api_key $http_authorization" $zafiro_v1 {
             default                 laravel;
             "~zfk_abcd1234_"        zafirogo;   # клуб zafiro-arena, перенесён 2026-11-…
         }
         location /api/v1/ { proxy_pass http://$zafiro_v1; proxy_set_header Host $host; }
         ```
         `nginx -t && nginx -s reload`. Вебхуки клуба отправляет zafirogo тем же секретом (подпись не меняется);
       - клуб в Laravel остаётся с приостановленной подпиской (только чтение для суперадмина).
8. [ ] **Проверка** после переключения: вход сотрудника, X-отчёт открытой смены = отчёт `verify`, продажа/пополнение,
       регистрация ПК и сессия гостя, вход мобильного приложения, запрос интегратора, доставка вебхука (журнал доставок).
9. [ ] **Откат** — до первой операции клуба в zafirogo (после неё деньги живут в новом ledger, откат — только вручную):
       убрать префиксы из карты nginx; в zafirogo приостановить подписку клуба (суперадминка); в MySQL вернуть подписку
       (`UPDATE club_subscriptions SET status = '<статус до заморозки>', canceled_at = NULL WHERE club_id = <id>` — суперадминка
       старого кода умеет только «продлить», что сдвинет срок); старые агенты ПК снова работают. Повторный перенос того же
       клуба требует удалить его из Postgres (S5-M2-О2).

После переноса всех клубов: `created_at NOT NULL DEFAULT now()` (Q-030), `CHECK (balance >= 0)` (Q-004), удаление полей
команд `club_computers` (Q-032), `VALIDATE CONSTRAINT webhook_endpoints_secret_sealed` (0025) — отдельными миграциями.

## Тесты

- Юнит: `go test ./internal/migrate/ ./cmd/migrate-from-mysql/` (расшифровка Laravel на эталоне PHP, нормализация
  настроек, чек v2 ↔ v1).
- Интеграционные (Docker): `TESTCONTAINERS_RYUK_DISABLED=true go test -tags integration ./internal/migrate/... ./cmd/migrate-from-mysql/`
  — MySQL `mysql:8.4.13` с `testdata/schema.sql` + `demo-data.sql`, Postgres с миграциями.
  - ядро (`migrate_integration_test.go`): данные-проблемы (старая роль, настройки «30 сек», журнал 2023 года,
    `created_at = NULL`, журнал API старше срока, пакет минут, общий аккаунт с отрицательным балансом во втором клубе, ПК со
    статусом вне CHECK, slug, занятый клубом zafirogo) → plan → apply → verify → балансы через ledger = MySQL × 100 →
    повторный apply ничего не меняет → второй клуб: блокеры откатывают перенос → RLS и работа сервера;
  - M2 (`m2_integration_test.go`, данные — `migratetest.InjectM2`: настройки v1, чек без `product_id`, продажи тарифа,
    идущая сессия, гостевой приход, ключи API, вебхук с секретом, зашифрованным настоящим PHP, токены Sanctum, логотип,
    переводы и токен Telegram): plan без APP_KEY — блокер → `platform apply` (дважды) → `apply` → `verify` → JSON v2, производные
    данные, секреты (HMAC тот же), токены, файлы → подмена чека и секрета ловится сверкой, секрет не попадает в отчёт;
  - сквозной (`cmd/migrate-from-mysql/e2e_integration_test.go`): заморозка (подписка suspended) → `platform apply/verify`
    → `plan/apply/verify --thaw-subscription active` (0 расхождений) → новый сервер (`app.Build` + `Run`, Valkey, NATS) на
    перенесённой базе: вход сотрудника по старому паролю (bcrypt → Argon2id), X-отчёт открытой смены = касса MySQL,
    мобильный токен в `/api/auth/me`, ключ интегратора в `/api/v1/me`, вебхук из очереди старого сервера доставлен с
    подписью, которая сходится с секретом старого сервера.
- `testdata` пересоздать: `tools/migrate-from-mysql/dump-schema.sh` (копия старого проекта + `php artisan migrate:fresh`
  в MySQL + `tools/visual/old/seed.php`; нужен Docker и кэш vendor `tools/php-oracle`). Перезапускать при изменении
  миграций старого проекта или `tools/visual/fixtures/demo.json`.
- Эталон шифрования Laravel (`server/internal/migrate/testdata/laravel_crypt.json`):
  `tools/migrate-from-mysql/laravel-crypt-fixture.sh` — настоящий `Illuminate\Encryption\Encrypter` из vendor старого
  проекта в `php:8.4.27-cli` (тестовые ключи).

## Остаётся

- [ ] Пересчёт балансов из истории старой БД (Q-005) — сверка сейчас сравнивает остатки MySQL × 100 с ledger
      (S5-M2-О1).
- [ ] Удаление перенесённого клуба из Postgres для повторного переноса после отката (S5-M2-О2).
- [ ] После переноса всех клубов — миграции из «Порядка перевода», п. «После переноса всех клубов».

## Открытые вопросы

`docs/open-questions/stage5-migration.md`; раньше — `docs/open-questions/infra.md` (раздел «Перенос данных»).
