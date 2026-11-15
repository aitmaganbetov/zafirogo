-- Аудит производительности (docs/open-questions/audit-perf.md): индексы под запросы панели крупного клуба,
-- удаление доказанных дублей и настройки хранения часто обновляемых таблиц. Данные не меняются.
--
-- 1. pg_trgm + GIN users(name), users(phone) (PB-05). Поиск клиента (касса, «Клиенты», брони, мобильный API,
--    суперадминка) — `ILIKE '%…%'` по общей для всех клубов таблице users: без индекса это полный проход по всем
--    пользователям ПЛАТФОРМЫ на каждый ввод в строке поиска. Расширение pg_trgm — «доверенное» (trusted) с
--    PostgreSQL 13: его ставит владелец базы без прав суперпользователя (роль миграций — владелец схемы и базы).
--    В управляемом Postgres, где создание расширений ограничено списком (RDS/Cloud SQL/Yandex MDB: pg_trgm в списке
--    есть), расширение включают в настройках кластера ДО миграции — CREATE EXTENSION IF NOT EXISTS тогда ничего не
--    делает. Шаблоны короче трёх символов индекс не ускоряет (планировщик читает таблицу, как раньше).
-- 2. club_memberships (club_id, role, user_id) (PB-09): сотрудники клуба, роли смены, счётчик клиентов — раньше
--    ради ~10 сотрудников читались все членства клуба (десятки тысяч клиентов).
-- 3. cash_transactions / stock_movements (club_id, id) вместо (club_id) (PB-04, PB-16): выборки «по id в клубе»
--    (API v1, мобильный API) и суммы дашборда; одноколоночный (club_id) уводил планировщик от (club_id, created_at).
-- 4. stock_movements (club_id, created_at DESC NULLS LAST, id DESC) вместо (club_id, created_at) (PB-07):
--    «последние движения» склада (latest()) — порядок запроса теперь совпадает с индексом; диапазоны по
--    created_at этот индекс обслуживает так же.
-- 5. Дубли по префиксу (PB-13): одноколоночные (club_id) там, где есть составной индекс с club_id первым, и
--    cash_transactions (club_id, cash_shift_id) при (club_id, cash_shift_id, type). Проверено запросом к pg_index
--    (те же колонки, классы операторов и порядок; индексы не обслуживают ограничений). Внешние ключи на clubs и
--    составные FK обслуживаются оставшимися индексами.
-- 6. club_computers (PB-12): индексы (status) и (last_seen_at) не использует ни один запрос (сверка «в сети» —
--    частичный club_computers_online_index, колокольчик — (club_id, …)), но из-за них каждое обновление состояния ПК
--    было не-HOT и писало во все индексы таблицы. fillfactor 70 оставляет на странице место под HOT-обновления
--    (действует на новые страницы; существующие таблицы пока пусты — клубы ещё не перенесены).
--    member_balances — fillfactor 85: баланс меняется при каждой покупке, индексируемые колонки — нет.
-- 7. Очереди outbox_events и agent_commands (PB-15, часть): строки живут минуты, таблицы малы при большом обороте —
--    autovacuum по умолчанию (20 % изменений) приходит к ним слишком поздно.
--
-- Блокировки: CREATE/DROP INDEX в транзакции миграции блокируют запись в таблицу на время построения. До первого
-- переноса клуба таблицы пусты или малы; на наполненной базе применять в окно обслуживания.

-- +goose Up
CREATE EXTENSION IF NOT EXISTS pg_trgm;

CREATE INDEX users_name_trgm_index ON users USING gin (name gin_trgm_ops);
CREATE INDEX users_phone_trgm_index ON users USING gin (phone gin_trgm_ops);

CREATE INDEX club_memberships_club_id_role_user_id_index ON club_memberships (club_id, role, user_id);

CREATE INDEX cash_transactions_club_id_id_index ON cash_transactions (club_id, id);
DROP INDEX cash_transactions_club_id_index;
DROP INDEX cash_transactions_club_shift_fk;

CREATE INDEX stock_movements_club_id_id_index ON stock_movements (club_id, id);
CREATE INDEX stock_movements_club_id_created_at_id_index ON stock_movements (club_id, created_at DESC NULLS LAST, id DESC);
DROP INDEX stock_movements_club_id_index;
DROP INDEX stock_movements_club_id_created_at_index;

DROP INDEX cash_shifts_club_id_index;
DROP INDEX club_zone_tariff_club_id_index;
DROP INDEX club_zones_club_id_index;
DROP INDEX product_categories_club_id_index;
DROP INDEX products_club_id_index;
DROP INDEX tariffs_club_id_index;

DROP INDEX club_computers_club_id_index;
DROP INDEX club_computers_status_index;
DROP INDEX club_computers_last_seen_at_index;
ALTER TABLE club_computers SET (fillfactor = 70);
ALTER TABLE member_balances SET (fillfactor = 85);

ALTER TABLE outbox_events SET (autovacuum_vacuum_scale_factor = 0.02, autovacuum_analyze_scale_factor = 0.02);
ALTER TABLE agent_commands SET (autovacuum_vacuum_scale_factor = 0.02, autovacuum_analyze_scale_factor = 0.02);

-- +goose Down
ALTER TABLE agent_commands RESET (autovacuum_vacuum_scale_factor, autovacuum_analyze_scale_factor);
ALTER TABLE outbox_events RESET (autovacuum_vacuum_scale_factor, autovacuum_analyze_scale_factor);

ALTER TABLE member_balances RESET (fillfactor);
ALTER TABLE club_computers RESET (fillfactor);
CREATE INDEX club_computers_last_seen_at_index ON club_computers (last_seen_at);
CREATE INDEX club_computers_status_index ON club_computers (status);
CREATE INDEX club_computers_club_id_index ON club_computers (club_id);

CREATE INDEX tariffs_club_id_index ON tariffs (club_id);
CREATE INDEX products_club_id_index ON products (club_id);
CREATE INDEX product_categories_club_id_index ON product_categories (club_id);
CREATE INDEX club_zones_club_id_index ON club_zones (club_id);
CREATE INDEX club_zone_tariff_club_id_index ON club_zone_tariff (club_id);
CREATE INDEX cash_shifts_club_id_index ON cash_shifts (club_id);

CREATE INDEX stock_movements_club_id_created_at_index ON stock_movements (club_id, created_at);
CREATE INDEX stock_movements_club_id_index ON stock_movements (club_id);
DROP INDEX stock_movements_club_id_created_at_id_index;
DROP INDEX stock_movements_club_id_id_index;

CREATE INDEX cash_transactions_club_shift_fk ON cash_transactions (club_id, cash_shift_id);
CREATE INDEX cash_transactions_club_id_index ON cash_transactions (club_id);
DROP INDEX cash_transactions_club_id_id_index;

DROP INDEX club_memberships_club_id_role_user_id_index;

DROP INDEX users_phone_trgm_index;
DROP INDEX users_name_trgm_index;
-- Расширение pg_trgm не удаляется: его могли включить до миграции (управляемый Postgres) или использовать вне схемы.
