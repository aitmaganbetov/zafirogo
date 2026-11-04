-- 0012_tenant_codes_tariff_names: club_id в transfer_codes и api_idempotency_keys; имя удалённого тарифа — снова свободно.
--
-- 1. Решение Q-028, L-1 (docs/DECISIONS.md): у transfer_codes и api_idempotency_keys появляется собственный club_id
--    (раньше клуб определялся через родителя — friendships / api_keys, и RLS-политика делала подзапрос к родителю).
--    Бэкфилл — из родителя; затем NOT NULL, составной FK (club_id, родитель) → родитель (club_id, id), чтобы клуб
--    строки не мог разойтись с клубом родителя; индексы с club_id впереди; RLS — как у обычных тенантных таблиц
--    (club_id = app.club_id). Лимиты кодов перевода «на аккаунт во всех клубах» (FriendTransferCodes::send(),
--    BR-092) будут считаться отдельным платформенным запросом, когда появится отправка кода (Этап 4).
-- 2. Решение Q-031: уникальность имени тарифа — только среди неудалённых (deleted_at IS NULL), как правило
--    Rule::unique(...)->whereNull('deleted_at') старого StoreTariffRequest/UpdateTariffRequest; в MySQL уникальный
--    индекс (club_id, name) не давал создать тариф с именем мягко удалённого. Сравнение без учёта регистра
--    (lower(name)) — как collation utf8mb4_unicode_ci в MySQL (решение Q-025).
-- 3. Решение Q-011: индекс time_packages (expires_at) для суточной задачи сгорания пакетов по всем клубам.

-- +goose Up

-- ---------------------------------------------------------------------------
-- Родители: (club_id, id) уникален — цель составных FK
-- ---------------------------------------------------------------------------

ALTER TABLE friendships ADD CONSTRAINT friendships_club_id_id_unique UNIQUE (club_id, id);
ALTER TABLE api_keys ADD CONSTRAINT api_keys_club_id_id_unique UNIQUE (club_id, id);

-- ---------------------------------------------------------------------------
-- transfer_codes.club_id
-- ---------------------------------------------------------------------------

ALTER TABLE transfer_codes ADD COLUMN club_id bigint;
UPDATE transfer_codes tc SET club_id = f.club_id FROM friendships f WHERE f.id = tc.friendship_id;
ALTER TABLE transfer_codes ALTER COLUMN club_id SET NOT NULL;
ALTER TABLE transfer_codes
    ADD CONSTRAINT transfer_codes_club_id_foreign FOREIGN KEY (club_id) REFERENCES clubs (id) ON DELETE CASCADE,
    ADD CONSTRAINT transfer_codes_club_id_friendship_id_foreign FOREIGN KEY (club_id, friendship_id)
        REFERENCES friendships (club_id, id) ON DELETE CASCADE,
    DROP CONSTRAINT transfer_codes_friendship_id_foreign;
DROP INDEX transfer_codes_friendship_id_foreign;
CREATE INDEX transfer_codes_club_id_friendship_id_index ON transfer_codes (club_id, friendship_id);
-- Последний код пользователя в клубе (LockLatestTransferCode: ORDER BY id DESC).
CREATE INDEX transfer_codes_club_id_user_id_id_index ON transfer_codes (club_id, user_id, id);

DROP POLICY tenant_isolation ON transfer_codes;
CREATE POLICY tenant_isolation ON transfer_codes USING (club_id = zafiro_current_club_id())
    WITH CHECK (club_id = zafiro_current_club_id());

-- ---------------------------------------------------------------------------
-- api_idempotency_keys.club_id
-- ---------------------------------------------------------------------------

ALTER TABLE api_idempotency_keys ADD COLUMN club_id bigint;
UPDATE api_idempotency_keys ik SET club_id = k.club_id FROM api_keys k WHERE k.id = ik.api_key_id;
ALTER TABLE api_idempotency_keys ALTER COLUMN club_id SET NOT NULL;
ALTER TABLE api_idempotency_keys
    ADD CONSTRAINT api_idempotency_keys_club_id_foreign FOREIGN KEY (club_id) REFERENCES clubs (id) ON DELETE CASCADE,
    ADD CONSTRAINT api_idempotency_keys_club_id_api_key_id_foreign FOREIGN KEY (club_id, api_key_id)
        REFERENCES api_keys (club_id, id) ON DELETE CASCADE,
    DROP CONSTRAINT api_idempotency_keys_api_key_id_foreign;
CREATE INDEX api_idempotency_keys_club_id_api_key_id_index ON api_idempotency_keys (club_id, api_key_id);

DROP POLICY tenant_isolation ON api_idempotency_keys;
CREATE POLICY tenant_isolation ON api_idempotency_keys USING (club_id = zafiro_current_club_id())
    WITH CHECK (club_id = zafiro_current_club_id());

-- ---------------------------------------------------------------------------
-- Имя тарифа: уникально среди неудалённых (Q-031)
-- ---------------------------------------------------------------------------

ALTER TABLE tariffs DROP CONSTRAINT tariffs_club_id_name_unique;
CREATE UNIQUE INDEX tariffs_club_id_name_unique ON tariffs (club_id, lower(name)) WHERE deleted_at IS NULL;

-- ---------------------------------------------------------------------------
-- Суточное сгорание пакетов (Q-011): поиск просроченных по всем клубам
-- ---------------------------------------------------------------------------

CREATE INDEX time_packages_expires_at_index ON time_packages (expires_at);

-- +goose Down

DROP INDEX time_packages_expires_at_index;

-- Не сработает, если после Up появились тарифы с именем удалённого (или отличающиеся только регистром):
-- такие данные старая схема не допускает.
DROP INDEX tariffs_club_id_name_unique;
ALTER TABLE tariffs ADD CONSTRAINT tariffs_club_id_name_unique UNIQUE (club_id, name);

DROP POLICY tenant_isolation ON api_idempotency_keys;
CREATE POLICY tenant_isolation ON api_idempotency_keys
    USING (EXISTS (SELECT 1 FROM api_keys k
                   WHERE k.id = api_idempotency_keys.api_key_id AND k.club_id = zafiro_current_club_id()))
    WITH CHECK (EXISTS (SELECT 1 FROM api_keys k
                        WHERE k.id = api_idempotency_keys.api_key_id AND k.club_id = zafiro_current_club_id()));
DROP INDEX api_idempotency_keys_club_id_api_key_id_index;
ALTER TABLE api_idempotency_keys
    ADD CONSTRAINT api_idempotency_keys_api_key_id_foreign FOREIGN KEY (api_key_id) REFERENCES api_keys (id) ON DELETE CASCADE,
    DROP CONSTRAINT api_idempotency_keys_club_id_api_key_id_foreign,
    DROP CONSTRAINT api_idempotency_keys_club_id_foreign;
ALTER TABLE api_idempotency_keys DROP COLUMN club_id;

DROP POLICY tenant_isolation ON transfer_codes;
CREATE POLICY tenant_isolation ON transfer_codes
    USING (user_id = zafiro_current_user_id()
           OR EXISTS (SELECT 1 FROM friendships f
                      WHERE f.id = transfer_codes.friendship_id AND f.club_id = zafiro_current_club_id()))
    WITH CHECK (EXISTS (SELECT 1 FROM friendships f
                        WHERE f.id = transfer_codes.friendship_id AND f.club_id = zafiro_current_club_id()));
DROP INDEX transfer_codes_club_id_user_id_id_index;
DROP INDEX transfer_codes_club_id_friendship_id_index;
CREATE INDEX transfer_codes_friendship_id_foreign ON transfer_codes (friendship_id);
ALTER TABLE transfer_codes
    ADD CONSTRAINT transfer_codes_friendship_id_foreign FOREIGN KEY (friendship_id) REFERENCES friendships (id) ON DELETE CASCADE,
    DROP CONSTRAINT transfer_codes_club_id_friendship_id_foreign,
    DROP CONSTRAINT transfer_codes_club_id_foreign;
ALTER TABLE transfer_codes DROP COLUMN club_id;

ALTER TABLE api_keys DROP CONSTRAINT api_keys_club_id_id_unique;
ALTER TABLE friendships DROP CONSTRAINT friendships_club_id_id_unique;
