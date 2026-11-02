-- Проверка восстановленной базы (учения восстановления, docs/runbooks/restore.md §5).
-- Запуск под суперпользователем восстановленного кластера (видит все клубы — RLS не действует):
--
--   psql -v ON_ERROR_STOP=1 -v expected_version=28 -f verify.sql
--
-- Печатает таблицу проверок; если хоть одна не прошла — ошибка (код psql ≠ 0).
-- Денежные инварианты — те же, что держат триггеры ledger (0002_ledger.sql) и ночная сверка
-- (db/queries/balances.sql ListMemberBalanceDrift), но по всем клубам сразу; агрегаты — в SQL.

\set QUIET on
\pset footer off
SET client_min_messages = warning;
SET statement_timeout = 0;
BEGIN;

CREATE TEMP TABLE drill_checks (
    ord    int,
    name   text,
    ok     boolean,
    detail text
) ON COMMIT DROP;

-- 1. Миграции: последняя применённая версия goose = последняя миграция в репозитории.
INSERT INTO drill_checks
SELECT 1, 'goose: версия схемы', max(version_id) = :expected_version,
       format('в базе %s, ожидается %s', max(version_id), :expected_version)
FROM goose_db_version WHERE is_applied;

-- 2. Проекция балансов = сумма проводок ledger по счетам клиента (ListMemberBalanceDrift по всем клубам).
INSERT INTO drill_checks
WITH sums AS (
    SELECT a.club_id, a.user_id,
           COALESCE(sum(e.amount) FILTER (WHERE a.kind = 'member_main'), 0)::bigint  AS ledger_main,
           COALESCE(sum(e.amount) FILTER (WHERE a.kind = 'member_bonus'), 0)::bigint AS ledger_bonus
    FROM ledger_accounts a
    JOIN ledger_entries e ON e.club_id = a.club_id AND e.account_id = a.id
    WHERE a.kind IN ('member_main', 'member_bonus')
    GROUP BY a.club_id, a.user_id
), drift AS (
    SELECT mb.club_id, mb.user_id
    FROM member_balances mb
    LEFT JOIN sums s ON s.club_id = mb.club_id AND s.user_id = mb.user_id
    WHERE mb.balance <> COALESCE(s.ledger_main, 0) OR mb.bonus_balance <> COALESCE(s.ledger_bonus, 0)
)
SELECT 2, 'деньги: member_balances = Σ проводок', count(*) = 0,
       CASE WHEN count(*) = 0 THEN format('клиентов: %s', (SELECT count(*) FROM member_balances))
            ELSE format('расхождений: %s (первые: %s)', count(*),
                        (SELECT string_agg(format('клуб %s клиент %s', club_id, user_id), ', ')
                         FROM (SELECT * FROM drift ORDER BY club_id, user_id LIMIT 5) d)) END
FROM drift;

-- 3. Двойная запись: сумма проводок каждой операции = 0.
INSERT INTO drill_checks
WITH unbalanced AS (
    SELECT club_id, transaction_id
    FROM ledger_entries
    GROUP BY club_id, transaction_id, created_at
    HAVING sum(amount) <> 0
)
SELECT 3, 'деньги: Σ проводок операции = 0', count(*) = 0,
       CASE WHEN count(*) = 0 THEN format('операций: %s, проводок: %s',
                                          (SELECT count(*) FROM ledger_transactions), (SELECT count(*) FROM ledger_entries))
            ELSE format('несбалансированных операций: %s', count(*)) END
FROM unbalanced;

-- 4. У каждой операции есть проводки (денежные или минутные) и ключ идемпотентности.
INSERT INTO drill_checks
SELECT 4, 'деньги: операции без проводок', count(*) = 0, format('%s', count(*))
FROM ledger_transactions t
WHERE NOT EXISTS (SELECT 1 FROM ledger_entries e
                  WHERE e.club_id = t.club_id AND e.transaction_id = t.id AND e.created_at = t.created_at)
  AND NOT EXISTS (SELECT 1 FROM ledger_minute_entries m
                  WHERE m.club_id = t.club_id AND m.transaction_id = t.id AND m.created_at = t.created_at);

-- 5. Партиции журналов (server/internal/db PartitionedTables): текущий и следующий месяц (UTC) на месте —
--    иначе вставки в ledger/журналы упадут до первого прохода воркера партиций.
INSERT INTO drill_checks
WITH parents(name) AS (
    VALUES ('ledger_transactions'), ('ledger_entries'), ('ledger_minute_entries'), ('audit_log'),
           ('activity_logs'), ('api_request_logs')
), months(m) AS (
    VALUES (date_trunc('month', now() AT TIME ZONE 'UTC')::date),
           ((date_trunc('month', now() AT TIME ZONE 'UTC') + interval '1 month')::date)
), missing AS (
    SELECT p.name || '_' || to_char(m.m, 'YYYY_MM') AS part
    FROM parents p CROSS JOIN months m
    WHERE NOT EXISTS (
        SELECT 1 FROM pg_inherits i
        JOIN pg_class c ON c.oid = i.inhrelid
        WHERE i.inhparent = to_regclass('public.' || p.name)
          AND c.relname = p.name || '_' || to_char(m.m, 'YYYY_MM'))
)
SELECT 5, 'партиции: текущий и следующий месяц', count(*) = 0,
       CASE WHEN count(*) = 0 THEN format('партиций всего: %s',
                (SELECT count(*) FROM pg_inherits i JOIN parents p ON i.inhparent = to_regclass('public.' || p.name)))
            ELSE 'нет: ' || string_agg(part, ', ') END
FROM missing;

-- 6. RLS: включена и принудительна (FORCE) на каждой таблице с club_id, кроме платформенных
--    (0004_rls.sql: platform_logs — журнал платформы без политик клуба), и на transfer_codes/api_idempotency_keys.
INSERT INTO drill_checks
WITH t AS (
    SELECT c.relname, c.relrowsecurity, c.relforcerowsecurity
    FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
    WHERE n.nspname = 'public' AND c.relkind IN ('r', 'p') AND NOT c.relispartition
      AND (EXISTS (SELECT 1 FROM pg_attribute a
                   WHERE a.attrelid = c.oid AND a.attname = 'club_id' AND NOT a.attisdropped)
           OR c.relname IN ('transfer_codes', 'api_idempotency_keys'))
      AND c.relname NOT IN ('platform_logs')
)
SELECT 6, 'RLS: включена на тенантных таблицах', bool_and(relrowsecurity AND relforcerowsecurity),
       CASE WHEN bool_and(relrowsecurity AND relforcerowsecurity) THEN format('таблиц: %s', count(*))
            ELSE 'без RLS: ' || string_agg(relname, ', ') FILTER (WHERE NOT (relrowsecurity AND relforcerowsecurity)) END
FROM t;

-- 7. Роли сервера и их логины (прямые члены) не обходят RLS (как проверка сервера при старте).
INSERT INTO drill_checks
WITH bad AS (
    SELECT r.rolname FROM pg_roles r
    WHERE (r.rolname IN ('zafiro_app', 'zafiro_platform')
           OR EXISTS (SELECT 1 FROM pg_auth_members m JOIN pg_roles g ON g.oid = m.roleid
                      WHERE m.member = r.oid AND g.rolname IN ('zafiro_app', 'zafiro_platform')))
      AND (r.rolsuper OR r.rolbypassrls)
)
SELECT 7, 'RLS: роли zafiro_app/zafiro_platform без BYPASSRLS',
       (SELECT count(*) FROM pg_roles WHERE rolname IN ('zafiro_app', 'zafiro_platform')) = 2
       AND NOT EXISTS (SELECT 1 FROM bad),
       COALESCE('обходят RLS: ' || (SELECT string_agg(rolname, ', ') FROM bad), 'ок');

-- Итог по деньгам — для отчёта учений (сверка с исходной базой на тот же момент).
SELECT club_id AS "клуб", count(*) AS "клиентов", sum(balance) AS "Σ баланс, тиын",
       sum(bonus_balance) AS "Σ бонусы, тиын"
FROM member_balances GROUP BY club_id ORDER BY club_id;

SELECT name AS "проверка", CASE WHEN ok THEN 'ОК' ELSE 'ОШИБКА' END AS "итог", detail AS "подробности"
FROM drill_checks ORDER BY ord;

-- Провал любой проверки — ошибка psql (код выхода 3 при ON_ERROR_STOP).
DO $$
DECLARE
    failed text;
BEGIN
    SELECT string_agg(name, '; ') INTO failed FROM drill_checks WHERE ok IS NOT TRUE;
    IF failed IS NOT NULL THEN
        RAISE EXCEPTION 'проверка восстановленной базы не прошла: %', failed;
    END IF;
END
$$;

COMMIT;
