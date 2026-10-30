-- 0010_agent_stream: стрим агента ПК (AgentService.Connect) — присутствие и очередь команд.
-- Протокол — docs/agent-protocol.md (разделы 2, «Heartbeat и запись в БД», «Маршрутизация команд»),
-- реализация — server/internal/agent/{presence,commands,stream}.
--
--  1. club_computers.agent_boot_id — UUID загрузки Windows из Hello.boot_id. Новый boot_id = «свежая загрузка»
--     (старый fresh_boot в heartbeat, app/Services/ClubComputerSessions.php heartbeat()/resetGuestSession()).
--     Пишется один раз за загрузку ПК; нужен в Postgres, чтобы «свежая загрузка» не срабатывала повторно
--     после потери Valkey или переподключения к другому инстансу.
--  2. club_computers.agent_state += 'service' — состояние «техрежим» нового агента (ComputerState.SERVICE,
--     proto/agent/v1/common.proto). В Laravel техрежим виден только по staff_* (staff_kind = 'pin'|'panel'),
--     agent_state его не знал (ClubComputer::AGENT_STATES).
--  3. Частичный индекс по status = 'online' — сверка «ПК online в Postgres, но нет в Valkey» (падение инстанса
--     или потеря Valkey) фоновой проверкой presence.Sweeper без полного прохода по таблице.
--     Онлайн больше не определяется по last_seen_at моложе 90 с (ClubComputer::HEARTBEAT_TIMEOUT_SECONDS):
--     last_seen_at пишется только при смене состояния, признак — status = 'online'.
--  4. agent_command_counters — последний выданный seq долговременной команды на ПК (UPDATE … RETURNING,
--     пропуски допустимы). Отдельная таблица, а не колонка club_computers: выдача seq не должна ждать
--     блокировку строки ПК, которую держат покупки/завершение сессии (SELECT … FOR UPDATE в billing).
--  5. agent_commands — неподтверждённые долговременные команды (seq > 0) до expires_at и итог выполнения.
--     Мгновенные команды (seq = 0) не хранятся. Это очередь, а не журнал истории: строки удаляются
--     фоновой задачей через 7 дней (presence.Sweeper), поэтому без месячных партиций
--     (docs/open-questions/stage2-agent-stream.md, AS-4).

-- +goose Up

ALTER TABLE club_computers ADD COLUMN agent_boot_id varchar(64);
COMMENT ON COLUMN club_computers.agent_boot_id IS 'Hello.boot_id последней загрузки Windows (новый — свежая загрузка ПК)';

ALTER TABLE club_computers DROP CONSTRAINT club_computers_agent_state_check;
ALTER TABLE club_computers ADD CONSTRAINT club_computers_agent_state_check
    CHECK (agent_state IN ('idle', 'in_session', 'locked', 'shutting_down', 'service'));

CREATE INDEX club_computers_online_index ON club_computers (club_id, id) WHERE status = 'online';

CREATE TABLE agent_command_counters (
    club_id     bigint NOT NULL,
    computer_id bigint NOT NULL,
    last_seq    bigint NOT NULL DEFAULT 0 CHECK (last_seq >= 0),
    CONSTRAINT agent_command_counters_pkey PRIMARY KEY (club_id, computer_id),
    CONSTRAINT agent_command_counters_computer_fk FOREIGN KEY (club_id, computer_id)
        REFERENCES club_computers (club_id, id) ON DELETE CASCADE
);
COMMENT ON TABLE agent_command_counters IS 'Последний seq долговременной команды агента на ПК (Command.seq)';

CREATE TABLE agent_commands (
    club_id           bigint NOT NULL,
    computer_id       bigint NOT NULL,
    seq               bigint NOT NULL CHECK (seq > 0),
    command_id        uuid NOT NULL,
    kind              varchar(32) NOT NULL,
    payload           bytea NOT NULL,
    issued_by_user_id bigint,
    created_at        timestamptz NOT NULL,
    expires_at        timestamptz,
    status            varchar(16) NOT NULL DEFAULT 'pending',
    error_code        integer,
    result_message    varchar(500),
    accepted_at       timestamptz,
    completed_at      timestamptz,
    CONSTRAINT agent_commands_pkey PRIMARY KEY (club_id, computer_id, seq),
    CONSTRAINT agent_commands_command_id_unique UNIQUE (command_id),
    CONSTRAINT agent_commands_status_check
        CHECK (status IN ('pending', 'accepted', 'succeeded', 'failed', 'duplicate', 'expired')),
    CONSTRAINT agent_commands_computer_fk FOREIGN KEY (club_id, computer_id)
        REFERENCES club_computers (club_id, id) ON DELETE CASCADE
);
COMMENT ON TABLE agent_commands IS 'Долговременные команды агенту (seq > 0): хранятся до expires_at, досылаются после reconnect по Hello.last_command_seq';
COMMENT ON COLUMN agent_commands.payload IS 'Command в двоичном protobuf (zafiro.agent.v1.Command)';
-- Истечение невыполненных команд (фоновая задача).
CREATE INDEX agent_commands_open_expires_at_index ON agent_commands (expires_at)
    WHERE completed_at IS NULL;
-- Удаление старых строк (фоновая задача).
CREATE INDEX agent_commands_created_at_index ON agent_commands (created_at);

-- RLS как у остальных тенантных таблиц (0004_rls.sql); права роли получают через ALTER DEFAULT PRIVILEGES.
ALTER TABLE agent_command_counters ENABLE ROW LEVEL SECURITY;
ALTER TABLE agent_command_counters FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON agent_command_counters USING (club_id = zafiro_current_club_id())
    WITH CHECK (club_id = zafiro_current_club_id());
CREATE POLICY platform_access ON agent_command_counters TO zafiro_platform USING (true) WITH CHECK (true);

ALTER TABLE agent_commands ENABLE ROW LEVEL SECURITY;
ALTER TABLE agent_commands FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON agent_commands USING (club_id = zafiro_current_club_id())
    WITH CHECK (club_id = zafiro_current_club_id());
CREATE POLICY platform_access ON agent_commands TO zafiro_platform USING (true) WITH CHECK (true);

-- +goose Down

DROP TABLE IF EXISTS agent_commands;
DROP TABLE IF EXISTS agent_command_counters;
DROP INDEX IF EXISTS club_computers_online_index;
UPDATE club_computers SET agent_state = NULL WHERE agent_state = 'service';
ALTER TABLE club_computers DROP CONSTRAINT club_computers_agent_state_check;
ALTER TABLE club_computers ADD CONSTRAINT club_computers_agent_state_check
    CHECK (agent_state IN ('idle', 'in_session', 'locked', 'shutting_down'));
ALTER TABLE club_computers DROP COLUMN IF EXISTS agent_boot_id;
