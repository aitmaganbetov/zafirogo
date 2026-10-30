-- Экран кассы (Этап 3, G2): витрина POS, поиск клиента для пополнения, операции смены по страницам.
-- Laravel: app/Http/Controllers/CashRegisterController.php index(), operations(). Решение Q-070: вместо «все
-- товары и все клиенты в HTML» — страницы витрины и поиск клиента.

-- name: CashCatalogCategories :many
-- index(): $products->pluck('category')->filter()->unique()->take(5) — категории товаров в наличии в порядке
-- первого появления в списке по имени (min(name)), без пустой; с числом товаров (title и счётчик кнопки).
SELECT category, count(*)::bigint AS products
FROM products
WHERE club_id = sqlc.arg(club_id) AND deleted_at IS NULL AND quantity > 0 AND category <> ''
GROUP BY category
ORDER BY min(name), category
LIMIT sqlc.arg(max_categories);

-- name: CashCatalogProductCount :one
-- index(): $products->count() — товаров в наличии (кнопка «Все»).
SELECT count(*)::bigint
FROM products
WHERE club_id = sqlc.arg(club_id) AND deleted_at IS NULL AND quantity > 0;

-- name: CashListProducts :many
-- index(): Product::where('quantity', '>', 0)->orderBy('name') — страница витрины; keyset (name, id);
-- category = '' — все. Индекс products_club_id_name_index.
SELECT id, name, category, price, quantity
FROM products
WHERE club_id = sqlc.arg(club_id) AND deleted_at IS NULL AND quantity > 0
  AND (sqlc.arg(category)::text = '' OR category = sqlc.arg(category)::text)
  AND (name, id) > (sqlc.arg(after_name)::text, sqlc.arg(after_id)::bigint)
ORDER BY name, id
LIMIT sqlc.arg(page_size);

-- name: CashActiveServices :many
-- index(): ClubService::where('is_active', true)->orderBy('name')->get(['id', 'name', 'price', 'unit']).
-- Индекс club_services_club_id_is_active_name_index.
SELECT id, name, price, unit
FROM club_services
WHERE club_id = sqlc.arg(club_id) AND is_active
ORDER BY name, id
LIMIT sqlc.arg(max_services);

-- name: CashSearchCustomers :many
-- index(): пользователи клуба (join club_memberships, left join member_balances) по имени — поиск по имени или
-- телефону (search — экранированный шаблон ILIKE, пусто — первые по имени).
SELECT u.id, u.name, u.phone,
       COALESCE(b.balance, 0)::bigint AS balance, COALESCE(b.bonus_balance, 0)::bigint AS bonus_balance
FROM club_memberships m
JOIN users u ON u.id = m.user_id
LEFT JOIN member_balances b ON b.club_id = m.club_id AND b.user_id = m.user_id
WHERE m.club_id = sqlc.arg(club_id)
  AND (sqlc.arg(search)::text = '' OR u.name ILIKE sqlc.arg(search)::text OR u.phone ILIKE sqlc.arg(search)::text)
ORDER BY u.name, u.id
LIMIT sqlc.arg(page_size);

-- name: CashListShiftIncomesPage :many
-- operations(): приходы смены, новые сверху, с возвратом — paginate(25) (номер страницы, как ->links()).
SELECT t.id, t.cash_shift_id, t.user_id, t.type, t.amount, t.description, t.receipt_data, t.payment_method,
       t.income_kind, t.created_at,
       r.id AS refund_id, r.created_at AS refunded_at
FROM cash_transactions t
LEFT JOIN cash_transactions r ON r.club_id = t.club_id AND r.refund_of_id = t.id
WHERE t.club_id = sqlc.arg(club_id)
  AND t.cash_shift_id = sqlc.arg(cash_shift_id)
  AND t.type = 'income'
ORDER BY t.id DESC
LIMIT sqlc.arg(page_size) OFFSET sqlc.arg(page_offset);

-- name: CashCountShiftIncomes :one
-- operations(): всего приходов смены (->links()). Индекс cash_transactions_club_shift_type_index.
SELECT count(*)::bigint
FROM cash_transactions
WHERE club_id = sqlc.arg(club_id) AND cash_shift_id = sqlc.arg(cash_shift_id) AND type = 'income';
