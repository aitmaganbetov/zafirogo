-- 0013_agent_image_keys: регистрация бездисковых ПК по «ключу образа» клуба.
-- Решение Q-037 (docs/DECISIONS.md), протокол — docs/agent-protocol.md §12.8.
--
-- Бездисковый ПК (CCBoot, iCafe8 …) теряет ключ и сертификат при каждой перезагрузке, поэтому после загрузки
-- служба агента сама вызывает Enroll с многоразовым ключом образа (он лежит в образе):
--   * IP ПК (адрес TCP-соединения, который видит сервер) должен входить в одну из подсетей ключа;
--   * ПК с MAC, уже известным клубу (club_computers.mac_address), получает сертификат сразу — короткий, 24 ч;
--   * новый MAC попадает в agent_pending_computers и ждёт подтверждения сотрудником; подтверждение создаёт
--     карточку ПК, после чего следующий Enroll того же ПК — «известный MAC».
-- Это замена старого многоразового ключа установки клуба (clubs.installation_key_*, AgentEnrollController),
-- который добавлял неизвестные ПК в «Не назначена» без подтверждения.
--
-- Тенантность: обе таблицы с club_id, RLS + FORCE, политики tenant_isolation и platform_access (как 0004).
-- Поиск ключа при Enroll (клуб ещё не известен) — как у кодов 0009: транзакция ставит
-- app.enroll_code_hash = hex(HMAC-SHA256(ключ)) и видит ТОЛЬКО строку ключа с этим хешем (политика
-- image_key_lookup, только SELECT). Знать хеш = знать ключ (HMAC с секретом сервера).

-- +goose Up

-- Ключи образа клуба. Сам ключ не хранится — только HMAC-SHA256 (секрет — agent.enroll.code_pepper).
CREATE TABLE agent_image_keys (
    id                 bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    club_id            bigint NOT NULL,
    key_hash           bytea NOT NULL,
    -- Начало ключа для показа в панели («zik_AbCd…»), как clubs.installation_key_prefix в старом.
    key_prefix         varchar(12) NOT NULL,
    -- Подпись в панели («Образ CCBoot, зал 1»).
    name               varchar(100) NOT NULL,
    -- Подсети, из которых ключ действует (адрес, который видит сервер: обычно внешний IP клуба).
    subnets            cidr[] NOT NULL,
    created_by_user_id bigint,
    created_at         timestamptz NOT NULL DEFAULT now(),
    -- NULL — бессрочный (до отзыва).
    expires_at         timestamptz,
    revoked_at         timestamptz,
    revoked_by_user_id bigint,
    last_used_at       timestamptz,
    CONSTRAINT agent_image_keys_key_hash_unique UNIQUE (key_hash),
    CONSTRAINT agent_image_keys_key_hash_check CHECK (octet_length(key_hash) = 32),
    CONSTRAINT agent_image_keys_subnets_check CHECK (cardinality(subnets) BETWEEN 1 AND 16
        AND array_position(subnets, NULL) IS NULL),
    CONSTRAINT agent_image_keys_expires_check CHECK (expires_at IS NULL OR expires_at > created_at),
    CONSTRAINT agent_image_keys_club_id_foreign FOREIGN KEY (club_id) REFERENCES clubs (id) ON DELETE CASCADE,
    CONSTRAINT agent_image_keys_created_by_foreign FOREIGN KEY (created_by_user_id)
        REFERENCES users (id) ON DELETE SET NULL,
    CONSTRAINT agent_image_keys_revoked_by_foreign FOREIGN KEY (revoked_by_user_id)
        REFERENCES users (id) ON DELETE SET NULL,
    -- Ссылка из agent_pending_computers и agent_certificates (club_id, id).
    CONSTRAINT agent_image_keys_club_id_id_unique UNIQUE (club_id, id)
);
CREATE INDEX agent_image_keys_club_created_index ON agent_image_keys (club_id, created_at);
CREATE INDEX agent_image_keys_created_by_foreign ON agent_image_keys (created_by_user_id)
    WHERE created_by_user_id IS NOT NULL;
CREATE INDEX agent_image_keys_revoked_by_foreign ON agent_image_keys (revoked_by_user_id)
    WHERE revoked_by_user_id IS NOT NULL;
COMMENT ON TABLE agent_image_keys IS 'Ключи образа бездисковых ПК (Q-037). Ключ не хранится — только HMAC-SHA256';

-- ПК, пришедшие по ключу образа с MAC, которого клуб не знает. Одна открытая заявка на MAC клуба:
-- повторные Enroll того же ПК обновляют её (last_seen_at, железо, IP).
CREATE TABLE agent_pending_computers (
    id                  bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    club_id             bigint NOT NULL,
    image_key_id        bigint,
    mac_address         varchar(17) NOT NULL,
    hardware_uuid       varchar(255),
    hostname            varchar(255),
    -- Сведения о ПК (DeviceInfo в JSON: версия агента, ОС, CPU, ОЗУ, GPU, локальный IP).
    device              jsonb NOT NULL DEFAULT '{}'::jsonb,
    ip                  inet,
    created_at          timestamptz NOT NULL DEFAULT now(),
    last_seen_at        timestamptz NOT NULL DEFAULT now(),
    attempts            integer NOT NULL DEFAULT 1,
    approved_at         timestamptz,
    approved_by_user_id bigint,
    -- Карточка, созданная (или найденная по MAC) при подтверждении.
    club_computer_id    bigint,
    rejected_at         timestamptz,
    rejected_by_user_id bigint,
    CONSTRAINT agent_pending_computers_mac_check CHECK (mac_address ~ '^([0-9A-F]{2}:){5}[0-9A-F]{2}$'),
    CONSTRAINT agent_pending_computers_decision_check CHECK (approved_at IS NULL OR rejected_at IS NULL),
    CONSTRAINT agent_pending_computers_club_id_foreign FOREIGN KEY (club_id) REFERENCES clubs (id) ON DELETE CASCADE,
    CONSTRAINT agent_pending_computers_image_key_fk FOREIGN KEY (club_id, image_key_id)
        REFERENCES agent_image_keys (club_id, id) ON DELETE SET NULL (image_key_id),
    CONSTRAINT agent_pending_computers_computer_fk FOREIGN KEY (club_id, club_computer_id)
        REFERENCES club_computers (club_id, id) ON DELETE SET NULL (club_computer_id),
    CONSTRAINT agent_pending_computers_approved_by_foreign FOREIGN KEY (approved_by_user_id)
        REFERENCES users (id) ON DELETE SET NULL,
    CONSTRAINT agent_pending_computers_rejected_by_foreign FOREIGN KEY (rejected_by_user_id)
        REFERENCES users (id) ON DELETE SET NULL
);
-- Одна нерешённая или отклонённая заявка на MAC клуба (подтверждённые остаются историей).
CREATE UNIQUE INDEX agent_pending_computers_open_mac_unique ON agent_pending_computers (club_id, mac_address)
    WHERE approved_at IS NULL;
-- Список ожидающих в панели (новые сверху).
CREATE INDEX agent_pending_computers_waiting_index ON agent_pending_computers (club_id, last_seen_at)
    WHERE approved_at IS NULL AND rejected_at IS NULL;
CREATE INDEX agent_pending_computers_image_key_fk ON agent_pending_computers (club_id, image_key_id)
    WHERE image_key_id IS NOT NULL;
CREATE INDEX agent_pending_computers_computer_fk ON agent_pending_computers (club_id, club_computer_id)
    WHERE club_computer_id IS NOT NULL;
CREATE INDEX agent_pending_computers_approved_by_foreign ON agent_pending_computers (approved_by_user_id)
    WHERE approved_by_user_id IS NOT NULL;
CREATE INDEX agent_pending_computers_rejected_by_foreign ON agent_pending_computers (rejected_by_user_id)
    WHERE rejected_by_user_id IS NOT NULL;
COMMENT ON TABLE agent_pending_computers IS 'Бездисковые ПК с новым MAC, ожидающие подтверждения сотрудником (Q-037)';

-- Сертификаты по ключу образа: короткие (24 ч) и продлеваются тоже на 24 ч (флаг наследуется при продлении).
ALTER TABLE agent_certificates ADD COLUMN diskless boolean NOT NULL DEFAULT false;
ALTER TABLE agent_certificates ADD COLUMN image_key_id bigint;
ALTER TABLE agent_certificates DROP CONSTRAINT agent_certificates_issued_via_check;
ALTER TABLE agent_certificates ADD CONSTRAINT agent_certificates_issued_via_check
    CHECK (issued_via IN ('enroll', 'renew', 'image_key'));
ALTER TABLE agent_certificates ADD CONSTRAINT agent_certificates_image_key_fk FOREIGN KEY (club_id, image_key_id)
    REFERENCES agent_image_keys (club_id, id) ON DELETE SET NULL (image_key_id);
CREATE INDEX agent_certificates_image_key_fk ON agent_certificates (club_id, image_key_id) WHERE image_key_id IS NOT NULL;
COMMENT ON COLUMN agent_certificates.diskless IS 'Выпущен по ключу образа (бездисковый ПК): срок 24 ч, продление — тоже 24 ч';

ALTER TABLE agent_image_keys ENABLE ROW LEVEL SECURITY;
ALTER TABLE agent_image_keys FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON agent_image_keys USING (club_id = zafiro_current_club_id())
    WITH CHECK (club_id = zafiro_current_club_id());
CREATE POLICY platform_access ON agent_image_keys TO zafiro_platform USING (true) WITH CHECK (true);
CREATE POLICY image_key_lookup ON agent_image_keys FOR SELECT
    USING (key_hash = zafiro_current_enroll_code_hash());

ALTER TABLE agent_pending_computers ENABLE ROW LEVEL SECURITY;
ALTER TABLE agent_pending_computers FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON agent_pending_computers USING (club_id = zafiro_current_club_id())
    WITH CHECK (club_id = zafiro_current_club_id());
CREATE POLICY platform_access ON agent_pending_computers TO zafiro_platform USING (true) WITH CHECK (true);

-- +goose Down
DROP INDEX IF EXISTS agent_certificates_image_key_fk;
ALTER TABLE agent_certificates DROP CONSTRAINT IF EXISTS agent_certificates_image_key_fk;
ALTER TABLE agent_certificates DROP CONSTRAINT IF EXISTS agent_certificates_issued_via_check;
-- NOT VALID: строки 'image_key' (история выпуска) остаются, новые проверяются.
ALTER TABLE agent_certificates ADD CONSTRAINT agent_certificates_issued_via_check
    CHECK (issued_via IN ('enroll', 'renew')) NOT VALID;
ALTER TABLE agent_certificates DROP COLUMN IF EXISTS image_key_id;
ALTER TABLE agent_certificates DROP COLUMN IF EXISTS diskless;
DROP TABLE IF EXISTS agent_pending_computers;
DROP TABLE IF EXISTS agent_image_keys;
