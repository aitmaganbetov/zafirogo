-- 0041_club_zones_level: уровень зоны, который задаёт клуб (Zafiro 79d014f, 55e3878): клиент с тарифом может сам
-- пересесть в зону того же или более низкого уровня, чем его текущая (BR-196).
-- Laravel: database/migrations/2026_11_19_100000_add_level_to_club_zones.php (unsignedTinyInteger, nullable,
-- after price_per_hour).
--
-- Отличия от Laravel:
--  * unsignedTinyInteger → smallint (как rotation в 0001) с CHECK 1…99 — правило формы
--    StoreClubZoneRequest ('level' => nullable|integer|min:1|max:99); в MySQL колонка принимала 0…255.
--    Значение вне 1…99 при переносе — ошибка вставки (блокер с id строки), старый код такое не записывал.
--  * Индекса нет: выборка «зоны клуба с level ≤ N» идёт по зонам одного клуба (единицы–десятки строк,
--    индекс club_zones_club_id_id_unique).

-- +goose Up
ALTER TABLE club_zones ADD COLUMN level smallint;
ALTER TABLE club_zones ADD CONSTRAINT club_zones_level_check CHECK (level BETWEEN 1 AND 99);
COMMENT ON COLUMN club_zones.level IS
    'Уровень зоны 1…99, задаёт клуб; NULL — без уровня. Тариф пересаживается в зоны своего тарифа и в зоны с уровнем ≤ уровня текущей зоны клиента (Laravel: SeatTransfers::allowedZoneIds(), 2026_11_19_100000_add_level_to_club_zones)';

-- +goose Down
ALTER TABLE club_zones DROP COLUMN level;
