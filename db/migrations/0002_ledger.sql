-- 0002_ledger: журнал денежных операций (ledger) с двойной записью.
-- Проект и обоснование — docs/schema-changes.md §4 и раздел «Как реализовано (Этап 1)».
--
-- Правила:
--  * все движения денег — операция (ledger_transactions) + проводки (ledger_entries), сумма проводок = 0;
--  * ключ идемпотентности уникален в клубе: ledger_idempotency (club_id, idempotency_key);
--  * журнал только дополняется: UPDATE/DELETE/TRUNCATE запрещены триггерами (и правами роли — 0004);
--  * member_balances.balance/bonus_balance — проекция: меняется ТОЛЬКО триггером при вставке проводки
--    по счёту клиента; прямое изменение запрещено триггером-сторожем (и правами роли — 0004);
--  * партиции по месяцам (UTC) создаёт zafiro_create_monthly_partitions() — миграция на 3 месяца вперёд,
--    дальше фоновая задача (Go-воркер, Q-081).
--  * знак суммы: amount > 0 — кредит, amount < 0 — дебет. Для счетов клиента (обязательства клуба) и
--    счетов выручки/фондов баланс = SUM(amount); для кассы (актив) баланс = −SUM(amount).

-- +goose Up

-- ---------------------------------------------------------------------------
-- Счета
-- ---------------------------------------------------------------------------

CREATE TABLE ledger_accounts (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    club_id    bigint NOT NULL,
    kind       varchar(32) NOT NULL,
    user_id    bigint,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT ledger_accounts_kind_check CHECK (kind IN (
        'member_main',      -- основной баланс клиента (member_balances.balance)
        'member_bonus',     -- бонусный баланс клиента (member_balances.bonus_balance)
        'cash_drawer',      -- касса клуба (наличные/карта/QR — один остаток, как в старом коде, BR-051)
        'revenue_time',     -- выручка за время (тарифы, минуты, гостевые сессии)
        'revenue_shop',     -- выручка магазина агента (заказы с ПК)
        'revenue_pos',      -- выручка POS (товары и услуги на кассе)
        'bonus_fund',       -- бонусный фонд: выпуск бонусов (дебет) и их расход/возврат (кредит)
        'cash_other',       -- прочие приходы/расходы кассы (BR-054)
        'owner_adjustment', -- корректировки владельцем без кассы (BR-058) и начальный баланс клиента (BR-059)
        'opening_balance'   -- входящие остатки при переносе клуба из MySQL
    )),
    CONSTRAINT ledger_accounts_member_user_check CHECK (
        (kind IN ('member_main', 'member_bonus')) = (user_id IS NOT NULL)
    ),
    CONSTRAINT ledger_accounts_club_kind_user_unique UNIQUE NULLS NOT DISTINCT (club_id, kind, user_id),
    CONSTRAINT ledger_accounts_club_id_id_unique UNIQUE (club_id, id),
    CONSTRAINT ledger_accounts_club_id_foreign FOREIGN KEY (club_id) REFERENCES clubs (id),
    CONSTRAINT ledger_accounts_membership_fk FOREIGN KEY (club_id, user_id)
        REFERENCES club_memberships (club_id, user_id)
);
CREATE INDEX ledger_accounts_membership_fk ON ledger_accounts (club_id, user_id) WHERE user_id IS NOT NULL;
COMMENT ON TABLE ledger_accounts IS 'Счета ledger: по два на клиента клуба (member_main/member_bonus) и по одному каждого вида на клуб';

-- ---------------------------------------------------------------------------
-- Операции и идемпотентность
-- ---------------------------------------------------------------------------

-- Отдельная последовательность: id операции выдаётся до вставки (в ledger_idempotency), чтобы
-- «захват» ключа и запись операции были одной транзакцией без UPDATE.
CREATE SEQUENCE ledger_transaction_id_seq AS bigint;

CREATE TABLE ledger_transactions (
    id                      bigint NOT NULL DEFAULT nextval('ledger_transaction_id_seq'),
    club_id                 bigint NOT NULL,
    kind                    varchar(32) NOT NULL,
    idempotency_key         varchar(200) NOT NULL,
    actor_user_id           bigint,
    cash_shift_id           bigint,
    cash_transaction_id     bigint,
    session_id              varchar(36),
    shop_order_id           bigint,
    reverses_transaction_id bigint,
    meta                    jsonb,
    created_at              timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT ledger_transactions_pkey PRIMARY KEY (club_id, id, created_at),
    CONSTRAINT ledger_transactions_kind_check CHECK (kind IN (
        'top_up',                    -- пополнение на кассе (BR-053), вкл. бонус за пополнение (BR-041)
        'top_up_refund',             -- возврат пополнения по чеку (BR-055)
        'owner_top_up',              -- пополнение владельцем без кассы (BR-058)
        'owner_write_off',           -- списание владельцем без кассы (BR-058)
        'initial_balance',           -- начальный баланс нового клиента (BR-059)
        'friend_transfer',           -- перевод другу (BR-091, BR-092)
        'tariff_purchase',           -- покупка тарифа (BR-020)
        'minutes_purchase',          -- покупка минут (BR-021)
        'package_start',             -- запуск сохранённого пакета (BR-024), только минуты
        'session_refund',            -- возврат при завершении сессии (BR-032)
        'package_kept',              -- сохранение пакета при завершении (BR-032), только минуты
        'package_expired',           -- сгорание пакета (BR-034, допущение Q-011), только минуты
        'shop_order',                -- заказ из агента (BR-075)
        'shop_order_cancel',         -- отмена заказа (BR-077)
        'pos_sale',                  -- продажа POS (BR-052)
        'pos_refund',                -- возврат продажи POS (BR-055)
        'cash_income',               -- прочий приход кассы (BR-054)
        'cash_expense',              -- расход кассы (BR-054)
        'guest_session',             -- гостевая сессия с кассы (BR-038)
        'migration_opening_balance'  -- входящий остаток при переносе из MySQL
    )),
    CONSTRAINT ledger_transactions_key_check CHECK (length(idempotency_key) > 0),
    CONSTRAINT ledger_transactions_club_id_foreign FOREIGN KEY (club_id) REFERENCES clubs (id)
) PARTITION BY RANGE (created_at);
CREATE INDEX ledger_transactions_club_id_created_at_index ON ledger_transactions (club_id, created_at);
CREATE INDEX ledger_transactions_club_id_kind_created_at_index ON ledger_transactions (club_id, kind, created_at);
CREATE INDEX ledger_transactions_club_id_cash_shift_id_index ON ledger_transactions (club_id, cash_shift_id)
    WHERE cash_shift_id IS NOT NULL;
CREATE INDEX ledger_transactions_club_id_session_id_index ON ledger_transactions (club_id, session_id)
    WHERE session_id IS NOT NULL;
COMMENT ON TABLE ledger_transactions IS 'Денежная операция (только добавление). Ссылки на документы — без FK: документы чистятся (session_purchases, transfer_codes)';
COMMENT ON COLUMN ledger_transactions.idempotency_key IS 'Уникальность в клубе — ledger_idempotency (PK club_id, idempotency_key)';

CREATE TABLE ledger_idempotency (
    club_id                bigint NOT NULL,
    idempotency_key        varchar(200) NOT NULL,
    fingerprint            bytea NOT NULL,
    transaction_id         bigint NOT NULL,
    transaction_created_at timestamptz NOT NULL,
    CONSTRAINT ledger_idempotency_pkey PRIMARY KEY (club_id, idempotency_key),
    CONSTRAINT ledger_idempotency_fingerprint_check CHECK (octet_length(fingerprint) = 32),
    CONSTRAINT ledger_idempotency_club_id_foreign FOREIGN KEY (club_id) REFERENCES clubs (id)
);
COMMENT ON TABLE ledger_idempotency IS 'Ключи идемпотентности операций: повтор с тем же ключом и тем же отпечатком (sha256 запроса) возвращает прежнюю операцию, с другим — ошибка';

-- ---------------------------------------------------------------------------
-- Проводки (деньги) и движения минут
-- ---------------------------------------------------------------------------

CREATE TABLE ledger_entries (
    id             bigint GENERATED ALWAYS AS IDENTITY,
    club_id        bigint NOT NULL,
    transaction_id bigint NOT NULL,
    account_id     bigint NOT NULL,
    amount         bigint NOT NULL,
    balance_after  bigint,
    created_at     timestamptz NOT NULL,
    CONSTRAINT ledger_entries_pkey PRIMARY KEY (club_id, id, created_at),
    CONSTRAINT ledger_entries_amount_check CHECK (amount <> 0),
    CONSTRAINT ledger_entries_transaction_fk FOREIGN KEY (club_id, transaction_id, created_at)
        REFERENCES ledger_transactions (club_id, id, created_at),
    CONSTRAINT ledger_entries_account_fk FOREIGN KEY (club_id, account_id)
        REFERENCES ledger_accounts (club_id, id)
) PARTITION BY RANGE (created_at);
CREATE INDEX ledger_entries_transaction_fk ON ledger_entries (club_id, transaction_id, created_at);
CREATE INDEX ledger_entries_club_account_created_at_index ON ledger_entries (club_id, account_id, created_at, id);
COMMENT ON COLUMN ledger_entries.amount IS 'Тиыны со знаком: > 0 кредит, < 0 дебет. Для счетов клиента: + баланс растёт';
COMMENT ON COLUMN ledger_entries.balance_after IS 'Баланс счёта клиента после проводки (тиыны); NULL у счетов клуба';

-- Ledger минут: отдельная таблица в той же операции (минуты — не деньги: свои единицы, нет второй
-- стороны, сумма не обязана быть 0). Проекция — time_packages.minutes_left.
CREATE TABLE ledger_minute_entries (
    id              bigint GENERATED ALWAYS AS IDENTITY,
    club_id         bigint NOT NULL,
    transaction_id  bigint NOT NULL,
    user_id         bigint NOT NULL,
    time_package_id bigint,
    tariff_id       bigint,
    minutes         bigint NOT NULL,
    created_at      timestamptz NOT NULL,
    CONSTRAINT ledger_minute_entries_pkey PRIMARY KEY (club_id, id, created_at),
    CONSTRAINT ledger_minute_entries_minutes_check CHECK (minutes <> 0),
    CONSTRAINT ledger_minute_entries_transaction_fk FOREIGN KEY (club_id, transaction_id, created_at)
        REFERENCES ledger_transactions (club_id, id, created_at),
    CONSTRAINT ledger_minute_entries_membership_fk FOREIGN KEY (club_id, user_id)
        REFERENCES club_memberships (club_id, user_id)
) PARTITION BY RANGE (created_at);
CREATE INDEX ledger_minute_entries_transaction_fk ON ledger_minute_entries (club_id, transaction_id, created_at);
CREATE INDEX ledger_minute_entries_club_user_created_at_index ON ledger_minute_entries (club_id, user_id, created_at);
COMMENT ON COLUMN ledger_minute_entries.minutes IS 'Минуты пакета со знаком: + сохранено/начислено, − запущено/сгорело';

-- ---------------------------------------------------------------------------
-- Только добавление
-- ---------------------------------------------------------------------------

-- +goose StatementBegin
CREATE FUNCTION zafiro_forbid_change() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION '% is append-only: % is forbidden', TG_TABLE_NAME, TG_OP
        USING ERRCODE = 'insufficient_privilege';
END
$$;
-- +goose StatementEnd

CREATE TRIGGER ledger_accounts_append_only BEFORE UPDATE OR DELETE ON ledger_accounts
    FOR EACH ROW EXECUTE FUNCTION zafiro_forbid_change();
CREATE TRIGGER ledger_accounts_no_truncate BEFORE TRUNCATE ON ledger_accounts
    FOR EACH STATEMENT EXECUTE FUNCTION zafiro_forbid_change();
CREATE TRIGGER ledger_transactions_append_only BEFORE UPDATE OR DELETE ON ledger_transactions
    FOR EACH ROW EXECUTE FUNCTION zafiro_forbid_change();
CREATE TRIGGER ledger_transactions_no_truncate BEFORE TRUNCATE ON ledger_transactions
    FOR EACH STATEMENT EXECUTE FUNCTION zafiro_forbid_change();
CREATE TRIGGER ledger_idempotency_append_only BEFORE UPDATE OR DELETE ON ledger_idempotency
    FOR EACH ROW EXECUTE FUNCTION zafiro_forbid_change();
CREATE TRIGGER ledger_idempotency_no_truncate BEFORE TRUNCATE ON ledger_idempotency
    FOR EACH STATEMENT EXECUTE FUNCTION zafiro_forbid_change();
CREATE TRIGGER ledger_entries_append_only BEFORE UPDATE OR DELETE ON ledger_entries
    FOR EACH ROW EXECUTE FUNCTION zafiro_forbid_change();
CREATE TRIGGER ledger_entries_no_truncate BEFORE TRUNCATE ON ledger_entries
    FOR EACH STATEMENT EXECUTE FUNCTION zafiro_forbid_change();
CREATE TRIGGER ledger_minute_entries_append_only BEFORE UPDATE OR DELETE ON ledger_minute_entries
    FOR EACH ROW EXECUTE FUNCTION zafiro_forbid_change();
CREATE TRIGGER ledger_minute_entries_no_truncate BEFORE TRUNCATE ON ledger_minute_entries
    FOR EACH STATEMENT EXECUTE FUNCTION zafiro_forbid_change();

-- ---------------------------------------------------------------------------
-- Инварианты операции (проверяются при COMMIT)
-- ---------------------------------------------------------------------------

-- Сумма денежных проводок операции = 0.
-- +goose StatementBegin
CREATE FUNCTION ledger_check_balanced() RETURNS trigger
    LANGUAGE plpgsql AS $$
DECLARE
    total numeric;
BEGIN
    SELECT COALESCE(sum(e.amount), 0) INTO total
    FROM ledger_entries e
    WHERE e.club_id = NEW.club_id AND e.transaction_id = NEW.transaction_id AND e.created_at = NEW.created_at;
    IF total <> 0 THEN
        RAISE EXCEPTION 'ledger transaction % is unbalanced: sum of entries = %', NEW.transaction_id, total
            USING ERRCODE = 'check_violation', CONSTRAINT = 'ledger_entries_balanced';
    END IF;
    RETURN NULL;
END
$$;
-- +goose StatementEnd

CREATE CONSTRAINT TRIGGER ledger_entries_balanced AFTER INSERT ON ledger_entries
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION ledger_check_balanced();

-- У операции есть хотя бы одна проводка (денежная или минутная).
-- +goose StatementBegin
CREATE FUNCTION ledger_check_not_empty() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM ledger_entries e
                   WHERE e.club_id = NEW.club_id AND e.transaction_id = NEW.id AND e.created_at = NEW.created_at)
       AND NOT EXISTS (SELECT 1 FROM ledger_minute_entries m
                       WHERE m.club_id = NEW.club_id AND m.transaction_id = NEW.id AND m.created_at = NEW.created_at) THEN
        RAISE EXCEPTION 'ledger transaction % has no entries', NEW.id
            USING ERRCODE = 'check_violation', CONSTRAINT = 'ledger_transactions_not_empty';
    END IF;
    RETURN NULL;
END
$$;
-- +goose StatementEnd

CREATE CONSTRAINT TRIGGER ledger_transactions_not_empty AFTER INSERT ON ledger_transactions
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION ledger_check_not_empty();

-- ---------------------------------------------------------------------------
-- Проекция member_balances: меняется только проводкой
-- ---------------------------------------------------------------------------

-- Проводка по счёту клиента сдвигает member_balances в той же транзакции и запоминает balance_after.
-- Дебет, после которого баланс < 0, запрещён (старый код всегда проверял достаточность перед списанием:
-- BR-053/055/058/075/091, BR-018); исключение — входящий остаток переноса (Q-004: в старых данных возможен минус).
-- SECURITY DEFINER: у роли приложения нет права UPDATE на колонки баланса (0004).
-- +goose StatementBegin
CREATE FUNCTION ledger_apply_member_entry() RETURNS trigger
    LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, public AS $$
DECLARE
    acc_kind varchar(32);
    acc_user bigint;
    new_balance bigint;
    tx_kind varchar(32);
BEGIN
    SELECT a.kind, a.user_id INTO acc_kind, acc_user
    FROM ledger_accounts a
    WHERE a.club_id = NEW.club_id AND a.id = NEW.account_id;
    IF acc_kind IS NULL THEN
        RAISE EXCEPTION 'ledger account % not found in club %', NEW.account_id, NEW.club_id
            USING ERRCODE = 'foreign_key_violation';
    END IF;

    IF acc_kind = 'member_main' THEN
        UPDATE member_balances SET balance = balance + NEW.amount, updated_at = now()
        WHERE club_id = NEW.club_id AND user_id = acc_user
        RETURNING balance INTO new_balance;
    ELSIF acc_kind = 'member_bonus' THEN
        UPDATE member_balances SET bonus_balance = bonus_balance + NEW.amount, updated_at = now()
        WHERE club_id = NEW.club_id AND user_id = acc_user
        RETURNING bonus_balance INTO new_balance;
    ELSE
        NEW.balance_after := NULL;
        RETURN NEW;
    END IF;

    IF new_balance IS NULL THEN
        RAISE EXCEPTION 'member_balances row for user % in club % not found', acc_user, NEW.club_id
            USING ERRCODE = 'foreign_key_violation', CONSTRAINT = 'ledger_member_balance_exists';
    END IF;
    IF NEW.amount < 0 AND new_balance < 0 THEN
        SELECT t.kind INTO tx_kind FROM ledger_transactions t
        WHERE t.club_id = NEW.club_id AND t.id = NEW.transaction_id AND t.created_at = NEW.created_at;
        IF tx_kind IS DISTINCT FROM 'migration_opening_balance' THEN
            RAISE EXCEPTION 'insufficient funds on % of user %: balance would be %', acc_kind, acc_user, new_balance
                USING ERRCODE = 'check_violation', CONSTRAINT = 'ledger_member_balance_non_negative';
        END IF;
    END IF;
    NEW.balance_after := new_balance;
    RETURN NEW;
END
$$;
-- +goose StatementEnd

CREATE TRIGGER ledger_entries_apply_member BEFORE INSERT ON ledger_entries
    FOR EACH ROW EXECUTE FUNCTION ledger_apply_member_entry();

-- Сторож: balance/bonus_balance меняет только ledger_apply_member_entry() (вложенный триггер, глубина > 1).
-- Новая строка — только с нулями: начальный баланс — операция initial_balance / migration_opening_balance.
-- +goose StatementBegin
CREATE FUNCTION member_balances_guard() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
    IF pg_trigger_depth() > 1 THEN
        RETURN NEW;
    END IF;
    IF TG_OP = 'INSERT' AND (NEW.balance <> 0 OR NEW.bonus_balance <> 0) THEN
        RAISE EXCEPTION 'member_balances: a new row starts at zero; post a ledger operation instead'
            USING ERRCODE = 'insufficient_privilege';
    END IF;
    IF TG_OP = 'UPDATE' AND (NEW.balance IS DISTINCT FROM OLD.balance OR NEW.bonus_balance IS DISTINCT FROM OLD.bonus_balance) THEN
        RAISE EXCEPTION 'member_balances: balance changes only through the ledger'
            USING ERRCODE = 'insufficient_privilege';
    END IF;
    RETURN NEW;
END
$$;
-- +goose StatementEnd

CREATE TRIGGER member_balances_guard BEFORE INSERT OR UPDATE OF balance, bonus_balance ON member_balances
    FOR EACH ROW EXECUTE FUNCTION member_balances_guard();

-- ---------------------------------------------------------------------------
-- Партиции по месяцам
-- ---------------------------------------------------------------------------

-- Создаёт месячные партиции [месяц, месяц + 1) в UTC начиная с месяца p_from, p_months штук; возвращает
-- число созданных. Вызывается миграцией и фоновой задачей (раз в сутки, 3 месяца вперёд).
-- Партиция наследует индексы, ограничения и триггеры родителя. Права на партиции не выдаются:
-- доступ — только через родительскую таблицу, где действуют RLS-политики (0004).
-- +goose StatementBegin
CREATE FUNCTION zafiro_create_monthly_partitions(p_parent text, p_from date, p_months integer)
    RETURNS integer
    LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, public AS $$
DECLARE
    month_start date;
    part_name text;
    created integer := 0;
    grantee text;
BEGIN
    IF p_parent NOT IN ('ledger_transactions', 'ledger_entries', 'ledger_minute_entries', 'audit_log') THEN
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
REVOKE ALL ON FUNCTION zafiro_create_monthly_partitions(text, date, integer) FROM PUBLIC;

-- Текущий месяц (UTC) и ещё 3 вперёд.
SELECT zafiro_create_monthly_partitions('ledger_transactions', (now() AT TIME ZONE 'UTC')::date, 4);
SELECT zafiro_create_monthly_partitions('ledger_entries', (now() AT TIME ZONE 'UTC')::date, 4);
SELECT zafiro_create_monthly_partitions('ledger_minute_entries', (now() AT TIME ZONE 'UTC')::date, 4);

-- +goose Down
DROP TRIGGER IF EXISTS member_balances_guard ON member_balances;
DROP FUNCTION IF EXISTS member_balances_guard();
DROP TABLE IF EXISTS ledger_minute_entries;
DROP TABLE IF EXISTS ledger_entries;
DROP TABLE IF EXISTS ledger_idempotency;
DROP TABLE IF EXISTS ledger_transactions;
DROP TABLE IF EXISTS ledger_accounts;
DROP SEQUENCE IF EXISTS ledger_transaction_id_seq;
DROP FUNCTION IF EXISTS zafiro_create_monthly_partitions(text, date, integer);
DROP FUNCTION IF EXISTS ledger_apply_member_entry();
DROP FUNCTION IF EXISTS ledger_check_not_empty();
DROP FUNCTION IF EXISTS ledger_check_balanced();
DROP FUNCTION IF EXISTS zafiro_forbid_change();
