-- Этап 7: почасовая история здоровья ПК (docs/open-questions/stage7-health.md S7-H-13…S7-H-16). В старом Laravel этого
-- нет — новая функция по поручению владельца: в панели клуба по ПК видно состояние по неделям, чтобы решать, исправен
-- ли ПК (ремонт / замена / наблюдение).
--
-- Каждый Status агента в Postgres НЕ пишется (CLAUDE.md «Производительность»): агрегат часа копится в Valkey по ключу
-- «ПК + час» (server/internal/agent/health/hourly.go — общий для всех инстансов, переезд ПК на другой инстанс посреди
-- часа не теряет и не дублирует данные), а после конца часа задача agent:health-hourly (один исполнитель на кластер,
-- Q-086) пишет ОДНУ строку на ПК: INSERT … ON CONFLICT (club_id, computer_id, hour) DO UPDATE — повтор идемпотентен.
--
-- * Тенантность: club_id + RLS tenant_isolation (как 0004), запросы sqlc фильтруют по club_id.
-- * Партиции по месяцам hour (UTC), как computer_health_events (0030): создаёт db.EnsurePartitions, удаляет по сроку
--   agent.health.hourly_retention (180 дней) задача logs:retention (db.RetentionTables, белые списки функций ниже).
-- * Первичный ключ (club_id, computer_id, hour) — он же составной индекс выборок «ПК за период».
-- * Права — как у computer_health_events: пул клуба zafiro_app читает, вставляет и обновляет только измерения (UPSERT
--   повтора), но не ключ строки и не удаляет; zafiro_platform — только чтение.
-- * Температуры — десятые доли °C, загрузка — сотые доли процента (как Status агента), без float.

-- +goose Up
CREATE TABLE computer_health_hourly (
    club_id                 bigint      NOT NULL REFERENCES clubs (id) ON DELETE CASCADE,
    computer_id             bigint      NOT NULL REFERENCES club_computers (id) ON DELETE CASCADE,
    hour                    timestamptz NOT NULL CONSTRAINT computer_health_hourly_hour_check
        CHECK (date_trunc('hour', hour) = hour),
    online_minutes          smallint    NOT NULL CONSTRAINT computer_health_hourly_online_check
        CHECK (online_minutes BETWEEN 0 AND 60),
    session_minutes         smallint    NOT NULL CONSTRAINT computer_health_hourly_session_check
        CHECK (session_minutes BETWEEN 0 AND 60),
    cpu_load_min_bp         integer,
    cpu_load_avg_bp         integer,
    cpu_load_max_bp         integer,
    ram_min_bp              integer,
    ram_avg_bp              integer,
    ram_max_bp              integer,
    gpu_load_min_bp         integer,
    gpu_load_avg_bp         integer,
    gpu_load_max_bp         integer,
    cpu_temp_min_deci       integer,
    cpu_temp_avg_deci       integer,
    cpu_temp_max_deci       integer,
    gpu_temp_min_deci       integer,
    gpu_temp_avg_deci       integer,
    gpu_temp_max_deci       integer,
    system_disk_free_mb     bigint,
    system_disk_total_mb    bigint,
    smart_worst             varchar(8) CONSTRAINT computer_health_hourly_smart_check
        CHECK (smart_worst IN ('ok', 'warning', 'failing')),
    peripheral_disconnects  integer     NOT NULL DEFAULT 0 CHECK (peripheral_disconnects >= 0),
    unexpected_shutdowns    integer     NOT NULL DEFAULT 0 CHECK (unexpected_shutdowns >= 0),
    bugchecks               integer     NOT NULL DEFAULT 0 CHECK (bugchecks >= 0),
    CONSTRAINT computer_health_hourly_pkey PRIMARY KEY (club_id, computer_id, hour)
) PARTITION BY RANGE (hour);

COMMENT ON TABLE computer_health_hourly IS
    'Здоровье ПК за час (агрегат Status из Valkey, одна строка на ПК и час). Партиции по месяцам hour (UTC), 0031';
COMMENT ON COLUMN computer_health_hourly.system_disk_free_mb IS
    'Минимум свободного места на системном диске за час (МБ) и объём диска в тот момент (system_disk_total_mb)';
COMMENT ON COLUMN computer_health_hourly.unexpected_shutdowns IS
    'Прирост счётчика внезапных выключений агента (за 7 суток) за этот час; bugchecks — то же для «синих экранов»';

ALTER TABLE computer_health_hourly ENABLE ROW LEVEL SECURITY;
ALTER TABLE computer_health_hourly FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON computer_health_hourly USING (club_id = zafiro_current_club_id())
    WITH CHECK (club_id = zafiro_current_club_id());
CREATE POLICY platform_access ON computer_health_hourly TO zafiro_platform USING (true) WITH CHECK (true);

REVOKE ALL ON computer_health_hourly FROM zafiro_app, zafiro_platform;
GRANT SELECT, INSERT ON computer_health_hourly TO zafiro_app;
GRANT UPDATE (online_minutes, session_minutes, cpu_load_min_bp, cpu_load_avg_bp, cpu_load_max_bp, ram_min_bp, ram_avg_bp,
              ram_max_bp, gpu_load_min_bp, gpu_load_avg_bp, gpu_load_max_bp, cpu_temp_min_deci, cpu_temp_avg_deci,
              cpu_temp_max_deci, gpu_temp_min_deci, gpu_temp_avg_deci, gpu_temp_max_deci, system_disk_free_mb,
              system_disk_total_mb, smart_worst, peripheral_disconnects, unexpected_shutdowns, bugchecks)
    ON computer_health_hourly TO zafiro_app;
GRANT SELECT ON computer_health_hourly TO zafiro_platform;

-- Белые списки функций партиций (CREATE OR REPLACE сохраняет владельца и права: EXECUTE — только zafiro_platform).
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION zafiro_create_monthly_partitions(p_parent text, p_from date, p_months integer)
    RETURNS integer
    LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, public AS $$
DECLARE
    month_start date;
    part_name text;
    created integer := 0;
    grantee text;
BEGIN
    IF p_parent NOT IN ('ledger_transactions', 'ledger_entries', 'ledger_minute_entries', 'audit_log',
                        'activity_logs', 'api_request_logs', 'computer_health_events', 'computer_health_hourly') THEN
        RAISE EXCEPTION 'zafiro_create_monthly_partitions: % is not a monthly partitioned table', p_parent;
    END IF;
    IF p_months < 1 OR p_months > 36 THEN
        RAISE EXCEPTION 'zafiro_create_monthly_partitions: months must be 1..36, got %', p_months;
    END IF;
    month_start := date_trunc('month', p_from)::date;
    FOR i IN 1..p_months LOOP
        part_name := p_parent || '_' || to_char(month_start, 'YYYY_MM');
        IF to_regclass('public.' || quote_ident(part_name)) IS NULL THEN
            EXECUTE format('CREATE TABLE public.%I PARTITION OF public.%I FOR VALUES FROM (%L) TO (%L)',
                           part_name, p_parent,
                           month_start::timestamp AT TIME ZONE 'UTC',
                           (month_start + 1 * interval '1 month')::timestamp AT TIME ZONE 'UTC');
            FOR grantee IN SELECT r.rolname FROM pg_roles r WHERE r.rolname IN ('zafiro_app', 'zafiro_platform') LOOP
                EXECUTE format('REVOKE ALL ON public.%I FROM %I', part_name, grantee);
            END LOOP;
            EXECUTE format('REVOKE ALL ON public.%I FROM PUBLIC', part_name);
            created := created + 1;
        END IF;
        month_start := (month_start + interval '1 month')::date;
    END LOOP;
    RETURN created;
END
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION zafiro_drop_monthly_partitions(p_parent text, p_before timestamptz)
    RETURNS integer
    LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, public AS $$
DECLARE
    part record;
    month_start date;
    dropped integer := 0;
BEGIN
    IF p_parent NOT IN ('api_request_logs', 'computer_health_events', 'computer_health_hourly') THEN
        RAISE EXCEPTION 'zafiro_drop_monthly_partitions: % has no retention period', p_parent;
    END IF;
    FOR part IN
        SELECT c.relname
        FROM pg_inherits i
        JOIN pg_class c ON c.oid = i.inhrelid
        WHERE i.inhparent = ('public.' || quote_ident(p_parent))::regclass
          AND c.relname ~ ('^' || p_parent || '_[0-9]{4}_[0-9]{2}$')
        ORDER BY c.relname
    LOOP
        month_start := to_date(right(part.relname, 7), 'YYYY_MM');
        -- Партиция [month_start, month_start + 1 месяц) в UTC (zafiro_create_monthly_partitions).
        IF ((month_start + interval '1 month')::timestamp AT TIME ZONE 'UTC') <= p_before THEN
            EXECUTE format('ALTER TABLE public.%I DETACH PARTITION public.%I', p_parent, part.relname);
            EXECUTE format('DROP TABLE public.%I', part.relname);
            dropped := dropped + 1;
        END IF;
    END LOOP;
    RETURN dropped;
END
$$;
-- +goose StatementEnd

-- Бездисковый ПК (0013, Q-037; S7-H-18) — одно правило для стрима (agent_presence.sql GetAgentComputer) и панели ПК
-- (computer_health.sql ComputerHealthComputer): действующий сертификат ПК (не отозван, не истёк; у ПК в сети это
-- сертификат подключения — все действующие сертификаты ПК одного вида: выпуск по коду или ключу образа отзывает прежние,
-- продление сохраняет признак), иначе последний выпущенный (ПК давно не в сети: сертификат по ключу образа живёт сутки),
-- иначе false. SECURITY INVOKER (по умолчанию): agent_certificates читается с RLS вызывающего — чужой клуб не виден.
-- Индекс club_computer_index: диапазон not_after > now() — одна-две строки. Права — как у zafiro_current_club_id (0004).
-- +goose StatementBegin
CREATE FUNCTION zafiro_computer_diskless(p_club_id bigint, p_computer_id bigint) RETURNS boolean
    LANGUAGE sql STABLE PARALLEL SAFE AS $$
    SELECT COALESCE((SELECT ac.diskless FROM agent_certificates ac
                     WHERE ac.club_id = p_club_id AND ac.club_computer_id = p_computer_id AND ac.not_after > now()
                       AND ac.revoked_at IS NULL
                     ORDER BY ac.issued_at DESC LIMIT 1),
                    (SELECT ac.diskless FROM agent_certificates ac
                     WHERE ac.club_id = p_club_id AND ac.club_computer_id = p_computer_id
                     ORDER BY ac.issued_at DESC LIMIT 1), false)
$$;
-- +goose StatementEnd

-- Партиции: текущий месяц и 3 вперёд (дальше — db.EnsurePartitions раз в сутки).
-- +goose StatementBegin
DO $$
BEGIN
    PERFORM zafiro_create_monthly_partitions('computer_health_hourly', (now() AT TIME ZONE 'UTC')::date, 4);
END
$$;
-- +goose StatementEnd

-- +goose Down
DROP FUNCTION IF EXISTS zafiro_computer_diskless(bigint, bigint);
DROP TABLE computer_health_hourly;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION zafiro_drop_monthly_partitions(p_parent text, p_before timestamptz)
    RETURNS integer
    LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, public AS $$
DECLARE
    part record;
    month_start date;
    dropped integer := 0;
BEGIN
    IF p_parent NOT IN ('api_request_logs', 'computer_health_events') THEN
        RAISE EXCEPTION 'zafiro_drop_monthly_partitions: % has no retention period', p_parent;
    END IF;
    FOR part IN
        SELECT c.relname
        FROM pg_inherits i
        JOIN pg_class c ON c.oid = i.inhrelid
        WHERE i.inhparent = ('public.' || quote_ident(p_parent))::regclass
          AND c.relname ~ ('^' || p_parent || '_[0-9]{4}_[0-9]{2}$')
        ORDER BY c.relname
    LOOP
        month_start := to_date(right(part.relname, 7), 'YYYY_MM');
        IF ((month_start + interval '1 month')::timestamp AT TIME ZONE 'UTC') <= p_before THEN
            EXECUTE format('ALTER TABLE public.%I DETACH PARTITION public.%I', p_parent, part.relname);
            EXECUTE format('DROP TABLE public.%I', part.relname);
            dropped := dropped + 1;
        END IF;
    END LOOP;
    RETURN dropped;
END
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION zafiro_create_monthly_partitions(p_parent text, p_from date, p_months integer)
    RETURNS integer
    LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, public AS $$
DECLARE
    month_start date;
    part_name text;
    created integer := 0;
    grantee text;
BEGIN
    IF p_parent NOT IN ('ledger_transactions', 'ledger_entries', 'ledger_minute_entries', 'audit_log',
                        'activity_logs', 'api_request_logs', 'computer_health_events') THEN
        RAISE EXCEPTION 'zafiro_create_monthly_partitions: % is not a monthly partitioned table', p_parent;
    END IF;
    IF p_months < 1 OR p_months > 36 THEN
        RAISE EXCEPTION 'zafiro_create_monthly_partitions: months must be 1..36, got %', p_months;
    END IF;
    month_start := date_trunc('month', p_from)::date;
    FOR i IN 1..p_months LOOP
        part_name := p_parent || '_' || to_char(month_start, 'YYYY_MM');
        IF to_regclass('public.' || quote_ident(part_name)) IS NULL THEN
            EXECUTE format('CREATE TABLE public.%I PARTITION OF public.%I FOR VALUES FROM (%L) TO (%L)',
                           part_name, p_parent,
                           month_start::timestamp AT TIME ZONE 'UTC',
                           (month_start + 1 * interval '1 month')::timestamp AT TIME ZONE 'UTC');
            FOR grantee IN SELECT r.rolname FROM pg_roles r WHERE r.rolname IN ('zafiro_app', 'zafiro_platform') LOOP
                EXECUTE format('REVOKE ALL ON public.%I FROM %I', part_name, grantee);
            END LOOP;
            EXECUTE format('REVOKE ALL ON public.%I FROM PUBLIC', part_name);
            created := created + 1;
        END IF;
        month_start := (month_start + interval '1 month')::date;
    END LOOP;
    RETURN created;
END
$$;
-- +goose StatementEnd
