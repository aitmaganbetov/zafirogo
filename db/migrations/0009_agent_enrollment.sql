-- 0009_agent_enrollment: регистрация ПК по одноразовому коду и выпущенные клиентские сертификаты агента (mTLS).
-- Заменяет ключ установки клуба (clubs.installation_key*) и токен устройства (club_computers.agent_token_hash):
-- docs/agent-protocol.md §3, proto/agent/v1/enrollment.proto. Старые колонки не трогаем — они нужны переносу данных.
--
-- Тенантность: обе таблицы с club_id, RLS + FORCE, политики tenant_isolation и platform_access (как 0004).
-- Особый случай — поиск кода при Enroll: клуб ещё не известен (ПК без сертификата). Транзакция ставит
-- app.enroll_code_hash = hex(HMAC-SHA256(код)) и видит ТОЛЬКО строку с этим хешем (политика enroll_code_lookup,
-- только SELECT); затем ставит app.club_id найденного клуба и работает как обычная тенантная транзакция.
-- Знать хеш = знать код (HMAC с секретом сервера), поэтому политика не раскрывает чужие клубы.

-- +goose Up

-- Одноразовые коды регистрации ПК. Хранится только HMAC-SHA256 кода (секрет — конфигурация сервера).
CREATE TABLE agent_enrollment_codes (
    id                 bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    club_id            bigint NOT NULL,
    code_hash          bytea NOT NULL,
    -- Зона для новой карточки ПК (NULL — «Не назначена», как ClubComputerRegistrar::defaultZone()).
    club_zone_id       bigint,
    -- Карточка ПК, которую регистрирует код («Перерегистрировать ПК»); NULL — «новый ПК» (карточка ищется по MAC/UUID).
    club_computer_id   bigint,
    created_by_user_id bigint,
    created_at         timestamptz NOT NULL DEFAULT now(),
    expires_at         timestamptz NOT NULL,
    used_at            timestamptz,
    used_computer_id   bigint,
    used_ip            inet,
    canceled_at        timestamptz,
    CONSTRAINT agent_enrollment_codes_code_hash_unique UNIQUE (code_hash),
    CONSTRAINT agent_enrollment_codes_code_hash_check CHECK (octet_length(code_hash) = 32),
    CONSTRAINT agent_enrollment_codes_expires_check CHECK (expires_at > created_at),
    CONSTRAINT agent_enrollment_codes_club_id_foreign FOREIGN KEY (club_id) REFERENCES clubs (id) ON DELETE CASCADE,
    CONSTRAINT agent_enrollment_codes_zone_fk FOREIGN KEY (club_id, club_zone_id)
        REFERENCES club_zones (club_id, id) ON DELETE SET NULL (club_zone_id),
    CONSTRAINT agent_enrollment_codes_computer_fk FOREIGN KEY (club_id, club_computer_id)
        REFERENCES club_computers (club_id, id) ON DELETE CASCADE,
    CONSTRAINT agent_enrollment_codes_created_by_foreign FOREIGN KEY (created_by_user_id)
        REFERENCES users (id) ON DELETE SET NULL
);
-- Список действующих кодов клуба в панели (новые сверху).
CREATE INDEX agent_enrollment_codes_club_created_index ON agent_enrollment_codes (club_id, created_at);
CREATE INDEX agent_enrollment_codes_zone_fk ON agent_enrollment_codes (club_id, club_zone_id) WHERE club_zone_id IS NOT NULL;
CREATE INDEX agent_enrollment_codes_computer_fk ON agent_enrollment_codes (club_id, club_computer_id)
    WHERE club_computer_id IS NOT NULL;
CREATE INDEX agent_enrollment_codes_created_by_foreign ON agent_enrollment_codes (created_by_user_id)
    WHERE created_by_user_id IS NOT NULL;
COMMENT ON TABLE agent_enrollment_codes IS 'Одноразовые коды «Подключить ПК» (15 минут). Код не хранится — только HMAC-SHA256';

-- Выпущенные клиентские сертификаты агентов. Строка не удаляется при удалении карточки ПК: триггер ниже
-- отзывает сертификаты удаляемой карточки (computer_deleted), история выпуска сохраняется.
CREATE TABLE agent_certificates (
    -- Серийный номер (128 случайных бит) в hex нижним регистром.
    serial              varchar(40) PRIMARY KEY,
    club_id             bigint NOT NULL,
    club_computer_id    bigint NOT NULL,
    -- SHA-256 SubjectPublicKeyInfo: продление требует новую пару ключей.
    spki_sha256         bytea NOT NULL,
    not_before          timestamptz NOT NULL,
    not_after           timestamptz NOT NULL,
    issued_at           timestamptz NOT NULL DEFAULT now(),
    issued_via          varchar(16) NOT NULL,
    enrollment_code_id  bigint,
    -- Продление: сертификат, который заменяет этот. Прежний отзывается (superseded) при первом
    -- использовании нового или истекает сам.
    replaces_serial     varchar(40),
    first_used_at       timestamptz,
    -- Железо на момент выпуска (сверка при продлении — DEVICE_MISMATCH).
    mac_address         varchar(17),
    hardware_uuid       varchar(255),
    revoked_at          timestamptz,
    revoke_reason       varchar(32),
    revoked_by_user_id  bigint,
    -- Время последнего изменения строки (выпуск, первое использование, отзыв): инкрементальное
    -- обновление кэша сертификатов на инстансах.
    changed_at          timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT agent_certificates_serial_check CHECK (serial ~ '^[0-9a-f]{1,40}$'),
    CONSTRAINT agent_certificates_spki_check CHECK (octet_length(spki_sha256) = 32),
    CONSTRAINT agent_certificates_validity_check CHECK (not_after > not_before),
    CONSTRAINT agent_certificates_issued_via_check CHECK (issued_via IN ('enroll', 'renew')),
    CONSTRAINT agent_certificates_revoke_check CHECK ((revoked_at IS NULL) = (revoke_reason IS NULL)),
    CONSTRAINT agent_certificates_revoke_reason_check CHECK (revoke_reason IN (
        'reenrolled',       -- новый Enroll той же карточки
        'superseded',       -- заменён продлённым сертификатом
        'staff',            -- «Отключить ПК» / «Перерегистрировать» в панели
        'computer_deleted', -- карточка ПК удалена
        'compromised',      -- подозрение на компрометацию ключа
        'club_terminated'   -- отзыв всех ПК клуба
    )),
    CONSTRAINT agent_certificates_club_id_foreign FOREIGN KEY (club_id) REFERENCES clubs (id) ON DELETE CASCADE,
    CONSTRAINT agent_certificates_code_foreign FOREIGN KEY (enrollment_code_id)
        REFERENCES agent_enrollment_codes (id) ON DELETE SET NULL,
    CONSTRAINT agent_certificates_revoked_by_foreign FOREIGN KEY (revoked_by_user_id)
        REFERENCES users (id) ON DELETE SET NULL
);
-- Сертификаты ПК (отзыв при повторной регистрации, удалении, «Отключить ПК»).
CREATE INDEX agent_certificates_club_computer_index ON agent_certificates (club_id, club_computer_id, not_after);
-- Кэш инстансов: полная загрузка действующих и инкрементальная по changed_at (платформенные запросы).
CREATE INDEX agent_certificates_valid_index ON agent_certificates (not_after) WHERE revoked_at IS NULL;
CREATE INDEX agent_certificates_changed_at_index ON agent_certificates (changed_at);
CREATE INDEX agent_certificates_code_foreign ON agent_certificates (enrollment_code_id) WHERE enrollment_code_id IS NOT NULL;
CREATE INDEX agent_certificates_revoked_by_foreign ON agent_certificates (revoked_by_user_id)
    WHERE revoked_by_user_id IS NOT NULL;
COMMENT ON TABLE agent_certificates IS 'Клиентские сертификаты агентов ПК (mTLS). Действующий = revoked_at IS NULL и now() < not_after';

-- Удаление карточки ПК отзывает её сертификаты (кто бы ни удалял: панель, каскад зоны или клуба).
-- +goose StatementBegin
CREATE FUNCTION zafiro_revoke_deleted_computer_certificates() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
    UPDATE agent_certificates
    SET revoked_at = now(), revoke_reason = 'computer_deleted', changed_at = now()
    WHERE club_id = OLD.club_id AND club_computer_id = OLD.id AND revoked_at IS NULL;
    RETURN OLD;
END
$$;
-- +goose StatementEnd
CREATE TRIGGER club_computers_revoke_agent_certificates
    AFTER DELETE ON club_computers
    FOR EACH ROW EXECUTE FUNCTION zafiro_revoke_deleted_computer_certificates();

-- Хеш кода, который ищет текущая транзакция Enroll (hex в app.enroll_code_hash).
-- +goose StatementBegin
CREATE FUNCTION zafiro_current_enroll_code_hash() RETURNS bytea
    LANGUAGE sql STABLE PARALLEL SAFE AS $$
    SELECT decode(NULLIF(current_setting('app.enroll_code_hash', true), ''), 'hex')
$$;
-- +goose StatementEnd

ALTER TABLE agent_enrollment_codes ENABLE ROW LEVEL SECURITY;
ALTER TABLE agent_enrollment_codes FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON agent_enrollment_codes USING (club_id = zafiro_current_club_id())
    WITH CHECK (club_id = zafiro_current_club_id());
CREATE POLICY platform_access ON agent_enrollment_codes TO zafiro_platform USING (true) WITH CHECK (true);
CREATE POLICY enroll_code_lookup ON agent_enrollment_codes FOR SELECT
    USING (code_hash = zafiro_current_enroll_code_hash());

ALTER TABLE agent_certificates ENABLE ROW LEVEL SECURITY;
ALTER TABLE agent_certificates FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON agent_certificates USING (club_id = zafiro_current_club_id())
    WITH CHECK (club_id = zafiro_current_club_id());
CREATE POLICY platform_access ON agent_certificates TO zafiro_platform USING (true) WITH CHECK (true);

-- +goose Down
DROP TRIGGER IF EXISTS club_computers_revoke_agent_certificates ON club_computers;
DROP FUNCTION IF EXISTS zafiro_revoke_deleted_computer_certificates();
DROP TABLE IF EXISTS agent_certificates;
DROP TABLE IF EXISTS agent_enrollment_codes;
DROP FUNCTION IF EXISTS zafiro_current_enroll_code_hash();
