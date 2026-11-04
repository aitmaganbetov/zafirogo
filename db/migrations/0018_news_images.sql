-- 0018_news_images: картинки новостей в Postgres (Этап 3, группа 4 — новости клуба).
--
-- В старом коде картинка новости — файл на локальном диске Laravel (`storage/app/news/<club|platform>/<40 символов>.png|jpg`,
-- app/Services/NewsManager.php storeImage()), путь — в news.image_path, отдаёт её публичный маршрут
-- GET /news-images/{news}/{file} (NewsController::image(): имя файла должно совпасть с basename(image_path)).
-- Новый сервер — несколько инстансов без общего диска, поэтому байты картинки лежат в таблице news_images
-- (одна картинка на новость, ≤ 2 МБ — правило max:2048 старой формы). news.image_path по-прежнему хранит путь
-- (тот же формат, что в старом): по нему проверяется имя файла в URL и строится адрес для агента.
-- Решение — docs/open-questions/stage3-news.md N-1.

-- +goose Up

CREATE TABLE news_images (
    news_id      bigint PRIMARY KEY,
    club_id      bigint,
    content_type varchar(32) NOT NULL,
    data         bytea NOT NULL,
    created_at   timestamptz NOT NULL,
    CONSTRAINT news_images_news_id_foreign FOREIGN KEY (news_id) REFERENCES news (id) ON DELETE CASCADE,
    CONSTRAINT news_images_club_id_foreign FOREIGN KEY (club_id) REFERENCES clubs (id) ON DELETE CASCADE,
    CONSTRAINT news_images_content_type_check CHECK (content_type IN ('image/png', 'image/jpeg')),
    CONSTRAINT news_images_size_check CHECK (octet_length(data) BETWEEN 1 AND 2097152)
);
CREATE INDEX news_images_club_id_index ON news_images (club_id);
COMMENT ON TABLE news_images IS 'Картинка новости (PNG/JPEG ≤ 2 МБ); путь — news.image_path. club_id NULL — новость платформы';

-- RLS как у news: картинку платформы (club_id NULL) читает любой клуб, пишет клуб только свою.
ALTER TABLE news_images ENABLE ROW LEVEL SECURITY;
ALTER TABLE news_images FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_read ON news_images FOR SELECT USING (club_id IS NULL OR club_id = zafiro_current_club_id());
CREATE POLICY tenant_insert ON news_images FOR INSERT WITH CHECK (club_id = zafiro_current_club_id());
CREATE POLICY tenant_update ON news_images FOR UPDATE USING (club_id = zafiro_current_club_id())
    WITH CHECK (club_id = zafiro_current_club_id());
CREATE POLICY tenant_delete ON news_images FOR DELETE USING (club_id = zafiro_current_club_id());
CREATE POLICY platform_access ON news_images TO zafiro_platform USING (true) WITH CHECK (true);
GRANT SELECT, INSERT, UPDATE, DELETE ON news_images TO zafiro_app, zafiro_platform;

-- +goose Down

DROP TABLE IF EXISTS news_images;
