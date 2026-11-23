-- 0046_products_image_path: фото товара (Zafiro 745fd21 «Show product photos … in the agent shop»): путь файла на
-- диске public Laravel (storage/app/public/products/<hash>.jpg), его отдаёт ProductResource::imageUrl() как
-- {app.url}/storage/<path> (GS-145, GS-146). Без колонки перенос MySQL → Postgres упирался в блокер `column`
-- («колонка MySQL без пары») у каждого клуба; в GG-11 (docs/open-questions/stage2-guest-grpc.md) поле было пустым.
-- Laravel: миграции в database/migrations нет — колонка есть в рабочей MySQL (varchar(255) NULL), тип — как там.
--
-- Отличия от Laravel: нет. Сами файлы и маршрут /storage/products/… в Go ещё не перенесены (как картинки GG-11):
-- колонка хранит путь, чтобы фото не потерялись при переносе.

-- +goose Up
ALTER TABLE products ADD COLUMN image_path varchar(255);
COMMENT ON COLUMN products.image_path IS
    'Путь фото товара на диске public Laravel (products/<hash>.jpg); NULL — заглушка /images/product-placeholder.svg (Laravel: ProductResource::imageUrl())';

-- +goose Down
ALTER TABLE products DROP COLUMN image_path;
