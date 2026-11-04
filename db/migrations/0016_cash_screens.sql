-- 0016_cash_screens: схема экранов кассы и склада (Этап 3, G2).
--
-- 1. Q-015, CSQ-3, RQ-7 — одноразовый nonce печати Z-отчёта хранится в Valkey (cash/service.ZNonceStore, TTL 1 ч,
--    ключ — клуб, смена, сотрудник, как session() Laravel); колонка с его хешем в cash_shifts больше не нужна.
--    Флаг печати (z_report_printed_at/by) остаётся у смены.
-- 2. Остатки для кассира (StockController::index(): orderBy('category')->orderBy('name')->paginate(50)) —
--    составной индекс клуба по категории и имени среди не удалённых товаров.

-- +goose Up
ALTER TABLE cash_shifts DROP COLUMN IF EXISTS z_report_nonce_hash;

CREATE INDEX products_club_id_category_name_index
    ON products (club_id, category, name, id) WHERE deleted_at IS NULL;

-- +goose Down
DROP INDEX IF EXISTS products_club_id_category_name_index;

-- Выданные до отката nonce живут в Valkey и после отката не действуют: смену надо напечатать заново.
ALTER TABLE cash_shifts ADD COLUMN z_report_nonce_hash bytea;
COMMENT ON COLUMN cash_shifts.z_report_nonce_hash IS 'sha256 одноразового nonce последнего показа Z-отчёта (Q-015); сам nonce не хранится';
