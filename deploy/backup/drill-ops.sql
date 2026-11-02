-- Денежная операция для сквозных учений (drill-e2e.sh): проводки ledger по правилам 0002_ledger.sql —
-- операция + ключ идемпотентности + две проводки с суммой 0; баланс клиента меняет триггер, не UPDATE.
-- Только для одноразовой базы учений (под суперпользователем; на боевой базе не запускать).
--
--   psql -v kind=owner_top_up -v amount=12345 -v key=drill-marker-1 -f drill-ops.sql
--     owner_top_up    — клиенту с наибольшим балансом клуба 1 зачисляется amount тиынов («метка» до T);
--     owner_write_off — у того же клиента списывается amount тиынов (amount=all — весь баланс; «ошибка» после T).
\set ON_ERROR_STOP 1
\set QUIET on
SELECT set_config('drill.kind', :'kind', false), set_config('drill.amount', :'amount', false),
       set_config('drill.key', :'key', false) \g /dev/null

BEGIN;
DO $$
DECLARE
    v_kind   text := current_setting('drill.kind');
    v_key    text := current_setting('drill.key');
    v_amount bigint;
    v_club   bigint;
    v_user   bigint;
    v_member bigint;
    v_owner  bigint;
    v_tx     bigint;
    v_at     timestamptz := clock_timestamp();
BEGIN
    IF v_kind NOT IN ('owner_top_up', 'owner_write_off') THEN
        RAISE EXCEPTION 'drill-ops: kind = owner_top_up | owner_write_off';
    END IF;
    SELECT mb.club_id, mb.user_id INTO v_club, v_user
    FROM member_balances mb WHERE mb.club_id = 1 ORDER BY mb.balance DESC, mb.user_id LIMIT 1;
    IF v_club IS NULL THEN
        RAISE EXCEPTION 'drill-ops: в клубе 1 нет клиентов (нужны демо-данные)';
    END IF;
    v_amount := CASE WHEN current_setting('drill.amount') = 'all'
                     THEN (SELECT balance FROM member_balances WHERE club_id = v_club AND user_id = v_user)
                     ELSE current_setting('drill.amount')::bigint END;
    IF v_amount <= 0 THEN
        RAISE EXCEPTION 'drill-ops: сумма должна быть > 0';
    END IF;

    INSERT INTO ledger_accounts (club_id, kind, user_id) VALUES (v_club, 'member_main', v_user)
        ON CONFLICT DO NOTHING;
    INSERT INTO ledger_accounts (club_id, kind, user_id) VALUES (v_club, 'owner_adjustment', NULL)
        ON CONFLICT DO NOTHING;
    SELECT id INTO v_member FROM ledger_accounts WHERE club_id = v_club AND kind = 'member_main' AND user_id = v_user;
    SELECT id INTO v_owner FROM ledger_accounts WHERE club_id = v_club AND kind = 'owner_adjustment' AND user_id IS NULL;

    v_tx := nextval('ledger_transaction_id_seq');
    INSERT INTO ledger_idempotency (club_id, idempotency_key, fingerprint, transaction_id, transaction_created_at)
    VALUES (v_club, v_key, sha256(convert_to(v_key, 'UTF8')), v_tx, v_at);
    INSERT INTO ledger_transactions (id, club_id, kind, idempotency_key, meta, created_at)
    VALUES (v_tx, v_club, v_kind, v_key, '{"drill": true}', v_at);
    INSERT INTO ledger_entries (club_id, transaction_id, account_id, amount, created_at) VALUES
        (v_club, v_tx, v_member, CASE WHEN v_kind = 'owner_top_up' THEN v_amount ELSE -v_amount END, v_at),
        (v_club, v_tx, v_owner,  CASE WHEN v_kind = 'owner_top_up' THEN -v_amount ELSE v_amount END, v_at);
    RAISE NOTICE 'drill-ops: % % тиын, клуб %, клиент %, операция %', v_kind, v_amount, v_club, v_user, v_tx;
END
$$;
COMMIT;
