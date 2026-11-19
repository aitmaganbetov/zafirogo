-- Поиск клиентов на странице «Пользователи» по email (старый Zafiro 35c36a0, UserController::index()):
-- `email ILIKE '%…%'` по общей для всех клубов таблице users — без индекса полный проход по всем пользователям
-- платформы на каждый поиск. GIN pg_trgm, как users(name) и users(phone) в 0032 (расширение ставит 0032).
-- Шаблоны короче трёх символов индекс не ускоряет. Блокировка записи в users на время построения — как в 0032
-- (до переноса клубов таблица мала; на наполненной базе — в окно обслуживания).

-- +goose Up
CREATE INDEX users_email_trgm_index ON users USING gin (email gin_trgm_ops);

-- +goose Down
DROP INDEX users_email_trgm_index;
