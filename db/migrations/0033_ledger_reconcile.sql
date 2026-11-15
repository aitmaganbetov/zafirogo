-- Аудит (15.11.2026), AC-3: фоновая сверка проекций денег с журналом операций (ledger). Новая функция, в старом
-- Laravel её нет (там не было и журнала). Воркер ledger:reconcile (server/internal/worker/ledger_reconcile.go)
-- раз в час сверяет member_balances с суммой проводок, баланс каждой операции (Σ проводок = 0) и снимок остатка
-- кассы; НИЧЕГО не исправляет — только записывает расхождение и оповещает (platform_logs, метрика, алерт).
--
-- * ledger_reconcile_findings — открытые и закрытые расхождения. Нужна для дедупликации: о расхождении пишется
--   в журнал платформы один раз, пока оно не изменилось (fingerprint) или не исчезло (resolved_at), а не каждый час.
--   Одно открытое расхождение на (клуб, вид, предмет) — частичный уникальный индекс.
-- * ledger_reconcile_state — одна строка: когда сверка в последний раз прошла успешно (метрика и алерт «сверка
--   не идёт»; общий для инстансов ответ на вопрос «пора ли»: тик на втором инстансе не повторяет сверку).
--
-- Тенантность: у findings есть club_id → RLS включён и принудителен, как у всех тенантных таблиц (0004), но политика
-- одна — platform_access: пулу клуба zafiro_app таблица не нужна и не видна (права отозваны). Читает и пишет только
-- zafiro_platform (воркер, кабинет суперадмина, сбор метрик). Суммы — тиыны (bigint) внутри details.

-- +goose Up
CREATE TABLE ledger_reconcile_findings (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    club_id       bigint      NOT NULL REFERENCES clubs (id) ON DELETE CASCADE,
    kind          varchar(32) NOT NULL CONSTRAINT ledger_reconcile_findings_kind_check
        CHECK (kind IN ('member_balance', 'unbalanced_transaction', 'cash_drawer_snapshot')),
    subject_id    bigint      NOT NULL,
    fingerprint   text        NOT NULL,
    details       jsonb       NOT NULL,
    first_seen_at timestamptz NOT NULL,
    last_seen_at  timestamptz NOT NULL,
    resolved_at   timestamptz
);
CREATE UNIQUE INDEX ledger_reconcile_findings_open_unique ON ledger_reconcile_findings (club_id, kind, subject_id)
    WHERE resolved_at IS NULL;

COMMENT ON TABLE ledger_reconcile_findings IS
    'Расхождения проекций денег с журналом операций, найденные фоновой сверкой (аудит AC-3). Только запись и оповещение, без исправления';
COMMENT ON COLUMN ledger_reconcile_findings.kind IS
    'member_balance: баланс клиента ≠ Σ проводок; unbalanced_transaction: Σ проводок операции ≠ 0; cash_drawer_snapshot: снимок остатка кассы ≠ журналу';
COMMENT ON COLUMN ledger_reconcile_findings.subject_id IS
    'member_balance — users.id; unbalanced_transaction — ledger_transactions.id; cash_drawer_snapshot — cash_shifts.id смены со снимком';
COMMENT ON COLUMN ledger_reconcile_findings.fingerprint IS
    'Значения расхождения одной строкой: пока не изменилась — в журнал платформы повторно не пишется';
COMMENT ON COLUMN ledger_reconcile_findings.details IS 'Числа расхождения в тиынах (целые)';

ALTER TABLE ledger_reconcile_findings ENABLE ROW LEVEL SECURITY;
ALTER TABLE ledger_reconcile_findings FORCE ROW LEVEL SECURITY;
CREATE POLICY platform_access ON ledger_reconcile_findings TO zafiro_platform USING (true) WITH CHECK (true);

CREATE TABLE ledger_reconcile_state (
    id                 boolean PRIMARY KEY DEFAULT true CONSTRAINT ledger_reconcile_state_single_row CHECK (id),
    last_started_at    timestamptz,
    last_success_at    timestamptz,
    last_full_at       timestamptz,
    last_duration_ms   bigint  NOT NULL DEFAULT 0,
    last_clubs         integer NOT NULL DEFAULT 0,
    last_error         text    NOT NULL DEFAULT ''
);
INSERT INTO ledger_reconcile_state (id) VALUES (true);
COMMENT ON TABLE ledger_reconcile_state IS
    'Одна строка: последний запуск фоновой сверки ledger (успех, полный проход, длительность, ошибка) — аудит AC-3';

REVOKE ALL ON ledger_reconcile_findings, ledger_reconcile_state FROM zafiro_app, zafiro_platform;
GRANT SELECT, INSERT, UPDATE ON ledger_reconcile_findings TO zafiro_platform;
GRANT SELECT, UPDATE ON ledger_reconcile_state TO zafiro_platform;
REVOKE ALL ON SEQUENCE ledger_reconcile_findings_id_seq FROM zafiro_app;

-- +goose Down
DROP TABLE ledger_reconcile_state;
DROP TABLE ledger_reconcile_findings;
