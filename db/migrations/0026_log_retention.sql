-- Этап 4: срок хранения журналов с месячными партициями (H1-Q1, Q-081 «отсоединение по сроку»).
--
-- В Laravel api_request_logs чистил integrations:prune (hourly) — строки старше
-- integrations.request_log_retention_days (90) дней (app/Console/Commands/PruneIntegrationData.php handle()).
-- Здесь журнал партиционирован по месяцам (0022), поэтому старые строки удаляются целыми партициями:
-- DETACH + DROP партиции, весь месяц которой старше срока (строки живут не меньше срока, до месяца дольше).
-- Удаляет фоновая задача logs:retention (server/internal/worker/log_retention.go) пулом zafiro_platform.
--
-- Белый список — только журналы, которые старый код удалял по сроку. ledger_* и audit_log не удаляются
-- никогда (журнал денег и аудит — только добавление), activity_logs старый код хранил вечно.

-- +goose Up
-- +goose StatementBegin
CREATE FUNCTION zafiro_drop_monthly_partitions(p_parent text, p_before timestamptz)
    RETURNS integer
    LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, public AS $$
DECLARE
    part record;
    month_start date;
    dropped integer := 0;
BEGIN
    IF p_parent NOT IN ('api_request_logs') THEN
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
REVOKE ALL ON FUNCTION zafiro_drop_monthly_partitions(text, timestamptz) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION zafiro_drop_monthly_partitions(text, timestamptz) TO zafiro_platform;

-- +goose Down
DROP FUNCTION IF EXISTS zafiro_drop_monthly_partitions(text, timestamptz);
