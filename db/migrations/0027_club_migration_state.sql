-- Этап 5, перенос данных: состояние перенесённого клуба и откат переноса до открытия клуба
-- (docs/open-questions/stage5-migration.md S5-M2-О2, tools/migrate-from-mysql/README.md «Порядок перевода клуба»).
--
-- clubs.migration_state:
--  * imported — клуб перенесён инструментом migrate-from-mysql и ещё не открыт для работы (подписка закрыта до
--    go-live, S5-M3-2); до открытия его можно удалить и перенести заново (zafiro_purge_imported_club);
--  * live     — клуб работает в zafirogo (все клубы, созданные в zafirogo, и перенесённые после go-live).
--    Переход live → imported запрещён (триггер): после открытия деньги клуба живут в ledger, удалить их нельзя.
--
-- Журналы ledger_* и audit_log — только добавление (триггеры 0002/0003 и REVOKE 0004). Единственное исключение —
-- осознанная платформенная операция zafiro_purge_imported_club: SECURITY DEFINER, EXECUTE только у владельца схемы
-- (роль миграций, PD-9), только для клуба в состоянии imported без операций в zafirogo; удаляет строки клуба из всех
-- таблиц с club_id (вместе с партициями ledger и аудита) и пишет событие в platform_logs.

-- +goose Up
ALTER TABLE clubs ADD COLUMN migration_state varchar(16) NOT NULL DEFAULT 'live';
ALTER TABLE clubs ADD CONSTRAINT clubs_migration_state_check CHECK (migration_state IN ('imported', 'live'));
COMMENT ON COLUMN clubs.migration_state IS
    'imported — перенесён из MySQL и не открыт (можно удалить и перенести заново); live — работает (окончательно)';

-- +goose StatementBegin
CREATE FUNCTION zafiro_clubs_migration_state_guard() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.migration_state = 'live' AND NEW.migration_state <> 'live' THEN
        RAISE EXCEPTION 'clubs.migration_state: club % is live, % is forbidden', OLD.id, NEW.migration_state
            USING ERRCODE = 'insufficient_privilege';
    END IF;
    RETURN NEW;
END
$$;
-- +goose StatementEnd
CREATE TRIGGER clubs_migration_state_guard BEFORE UPDATE OF migration_state ON clubs
    FOR EACH ROW EXECUTE FUNCTION zafiro_clubs_migration_state_guard();

-- Только добавление (0002), кроме удаления строк клуба внутри zafiro_purge_imported_club: она ставит
-- zafiro.purge_club_id на время транзакции, а клуб должен быть в состоянии imported. Настройку может поставить
-- любой, но DELETE на журналах есть только у владельца схемы (REVOKE 0004), и клуб live этим не удалить.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION zafiro_forbid_change() RETURNS trigger
    LANGUAGE plpgsql AS $$
DECLARE
    purge_club text := NULLIF(current_setting('zafiro.purge_club_id', true), '');
BEGIN
    IF TG_OP = 'DELETE' AND TG_LEVEL = 'ROW' AND purge_club IS NOT NULL
       AND to_jsonb(OLD) ->> 'club_id' = purge_club
       AND EXISTS (SELECT 1 FROM public.clubs c WHERE c.id = purge_club::bigint AND c.migration_state = 'imported') THEN
        RETURN OLD;
    END IF;
    RAISE EXCEPTION '% is append-only: % is forbidden', TG_TABLE_NAME, TG_OP
        USING ERRCODE = 'insufficient_privilege';
END
$$;
-- +goose StatementEnd

-- p_users — аккаунты, которые удаляются вместе с клубом: их выбирает инструмент переноса под ролью zafiro_platform
-- (видит все клубы) — аккаунты, на которые ссылаются только строки этого клуба (S5-M3-1); суперадмины не удаляются.
-- Возвращает число удалённых строк по таблицам.
-- +goose StatementBegin
CREATE FUNCTION zafiro_purge_imported_club(p_club_id bigint, p_confirm_slug text, p_users bigint[])
    RETURNS jsonb
    LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, public AS $$
DECLARE
    v_slug     text;
    v_state    text;
    v_tables   text[];
    v_done     text[] := '{}';
    v_counts   jsonb := '{}';
    v_progress boolean;
    v_n        bigint;
    t          text;
BEGIN
    SELECT slug, migration_state INTO v_slug, v_state FROM public.clubs WHERE id = p_club_id FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'zafiro_purge_imported_club: club % not found', p_club_id USING ERRCODE = 'no_data_found';
    END IF;
    IF p_confirm_slug IS DISTINCT FROM v_slug THEN
        RAISE EXCEPTION 'zafiro_purge_imported_club: confirmation does not match the slug of club %', p_club_id
            USING ERRCODE = 'invalid_parameter_value';
    END IF;
    IF v_state <> 'imported' THEN
        RAISE EXCEPTION 'zafiro_purge_imported_club: club % is %, purge is allowed only before go-live', p_club_id, v_state
            USING ERRCODE = 'object_not_in_prerequisite_state';
    END IF;

    -- Строки клуба видны владельцу под FORCE ROW LEVEL SECURITY (0004) только с app.club_id клуба.
    PERFORM set_config('app.club_id', p_club_id::text, true);
    -- Клуб не открывали: в ledger только входящие остатки переноса (и сгорание пакетов фоновой задачей — без
    -- участия людей, Q-011), в аудите — только записи переноса.
    IF EXISTS (SELECT 1 FROM public.ledger_transactions
               WHERE club_id = p_club_id AND kind NOT IN ('migration_opening_balance', 'package_expired')) THEN
        RAISE EXCEPTION 'zafiro_purge_imported_club: club % has money operations in zafirogo', p_club_id
            USING ERRCODE = 'object_not_in_prerequisite_state';
    END IF;
    IF EXISTS (SELECT 1 FROM public.audit_log WHERE club_id = p_club_id AND action NOT LIKE 'migration.%') THEN
        RAISE EXCEPTION 'zafiro_purge_imported_club: club % has audited actions in zafirogo', p_club_id
            USING ERRCODE = 'object_not_in_prerequisite_state';
    END IF;

    PERFORM set_config('zafiro.purge_club_id', p_club_id::text, true);

    -- Все таблицы с club_id (родители партиций — вместе с партициями), кроме самих clubs.
    SELECT coalesce(array_agg(c.oid::regclass::text ORDER BY c.relname), '{}') INTO v_tables
    FROM pg_class c
    JOIN pg_namespace n ON n.oid = c.relnamespace AND n.nspname = 'public'
    WHERE c.relkind IN ('r', 'p') AND NOT c.relispartition AND c.relname <> 'clubs'
      AND EXISTS (SELECT 1 FROM pg_attribute a
                  WHERE a.attrelid = c.oid AND a.attname = 'club_id' AND a.attnum > 0 AND NOT a.attisdropped);

    -- Порядок по внешним ключам: таблица удаляется, когда на неё не ссылается ни одна ещё не очищенная
    -- (ссылки NO ACTION/RESTRICT — activity_logs → club_memberships, products → product_categories, ledger → …).
    WHILE cardinality(v_done) < cardinality(v_tables) LOOP
        v_progress := false;
        FOREACH t IN ARRAY v_tables LOOP
            CONTINUE WHEN t = ANY (v_done);
            CONTINUE WHEN EXISTS (
                SELECT 1 FROM pg_constraint fk
                WHERE fk.contype = 'f' AND fk.conparentid = 0
                  AND fk.confrelid = t::regclass AND fk.conrelid <> t::regclass
                  AND fk.conrelid::regclass::text = ANY (v_tables)
                  AND NOT (fk.conrelid::regclass::text = ANY (v_done)));
            EXECUTE format('DELETE FROM %s WHERE club_id = $1', t) USING p_club_id;
            GET DIAGNOSTICS v_n = ROW_COUNT;
            IF v_n > 0 THEN
                v_counts := v_counts || jsonb_build_object(t, v_n);
            END IF;
            v_done := v_done || t;
            v_progress := true;
        END LOOP;
        IF NOT v_progress THEN
            RAISE EXCEPTION 'zafiro_purge_imported_club: cyclic foreign keys among %', v_tables;
        END IF;
    END LOOP;

    DELETE FROM public.clubs WHERE id = p_club_id;
    DELETE FROM public.users u WHERE u.id = ANY (coalesce(p_users, '{}')) AND NOT u.is_superadmin;
    GET DIAGNOSTICS v_n = ROW_COUNT;
    v_counts := v_counts || jsonb_build_object('clubs', 1, 'users', v_n);

    INSERT INTO public.platform_logs (user_id, club_id, event, description, subject_type, subject_id, details,
                                      ip_address, created_at, updated_at)
    VALUES (NULL, NULL, 'migration.club_purged',
            'Перенесённый клуб ' || v_slug || ' удалён для повторного переноса',
            'App\Models\Club', p_club_id,
            jsonb_build_object('club_id', p_club_id, 'slug', v_slug, 'deleted', v_counts,
                               '_t', jsonb_build_object('key', 'Перенесённый клуб :club удалён для повторного переноса',
                                                        'params', jsonb_build_object('club', v_slug))),
            '127.0.0.1', now(), now());

    PERFORM set_config('zafiro.purge_club_id', '', true);
    RETURN v_counts;
END
$$;
-- +goose StatementEnd
-- Только владелец схемы (роль миграций): ни сервер (zafiro_app), ни суперадминка (zafiro_platform).
REVOKE ALL ON FUNCTION zafiro_purge_imported_club(bigint, text, bigint[]) FROM PUBLIC;

-- +goose Down
DROP FUNCTION IF EXISTS zafiro_purge_imported_club(bigint, text, bigint[]);

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION zafiro_forbid_change() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION '% is append-only: % is forbidden', TG_TABLE_NAME, TG_OP
        USING ERRCODE = 'insufficient_privilege';
END
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS clubs_migration_state_guard ON clubs;
DROP FUNCTION IF EXISTS zafiro_clubs_migration_state_guard();
ALTER TABLE clubs DROP COLUMN IF EXISTS migration_state;
