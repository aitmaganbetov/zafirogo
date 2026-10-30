-- 0019_club_images: логотип клуба и картинка экрана входа агента в Postgres (Этап 3, группа 5 — настройки клуба).
--
-- В старом коде это файлы на локальном диске Laravel (`storage/app/club-logos/<club>/<40 символов>.png|jpg`,
-- `storage/app/club-signin-images/<club>/…`; app/Http/Controllers/ClubLogoController.php update(),
-- ClubSignInImageController.php update()), путь — в clubs.logo_path / clubs.signin_image_path, отдают их
-- публичные маршруты GET /club-logos/{club}/{file} и /club-signin-images/{club}/{file} (имя файла должно
-- совпасть с basename пути). Новый сервер — несколько инстансов без общего диска, поэтому байты лежат в
-- club_images (как news_images, 0018): одна картинка каждого вида на клуб, ≤ 1 МБ логотип и ≤ 3 МБ картинка входа
-- (правила max:1024 / max:3072 старых форм). Пути в clubs остаются прежнего формата: по ним сверяется имя файла
-- в URL и строится адрес для агента (ClubConfig.logo_url / signin_image_url). Решение — docs/open-questions/
-- stage3-settings.md S-1.
--
-- Плюс индекс журнала доставок вебхуков клуба по id (экран «API и интеграции»: keyset, новые сверху).

-- +goose Up

CREATE TABLE club_images (
    club_id      bigint NOT NULL,
    kind         varchar(16) NOT NULL,
    content_type varchar(32) NOT NULL,
    data         bytea NOT NULL,
    created_at   timestamptz NOT NULL,
    CONSTRAINT club_images_pkey PRIMARY KEY (club_id, kind),
    CONSTRAINT club_images_club_id_foreign FOREIGN KEY (club_id) REFERENCES clubs (id) ON DELETE CASCADE,
    CONSTRAINT club_images_kind_check CHECK (kind IN ('logo', 'signin')),
    CONSTRAINT club_images_content_type_check CHECK (content_type IN ('image/png', 'image/jpeg')),
    CONSTRAINT club_images_size_check CHECK (octet_length(data) BETWEEN 1 AND 3145728)
);
COMMENT ON TABLE club_images IS 'Логотип клуба (kind logo, ≤ 1 МБ) и картинка экрана входа агента (kind signin, ≤ 3 МБ); путь — clubs.logo_path / signin_image_path';

ALTER TABLE club_images ENABLE ROW LEVEL SECURITY;
ALTER TABLE club_images FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON club_images USING (club_id = zafiro_current_club_id())
    WITH CHECK (club_id = zafiro_current_club_id());
-- Публичная отдача картинки агенту (без токена, клуб из URL) — пул zafiro_platform.
CREATE POLICY platform_access ON club_images TO zafiro_platform USING (true) WITH CHECK (true);
GRANT SELECT, INSERT, UPDATE, DELETE ON club_images TO zafiro_app, zafiro_platform;

CREATE INDEX webhook_deliveries_club_id_id_index ON webhook_deliveries (club_id, id);

-- +goose Down

DROP INDEX IF EXISTS webhook_deliveries_club_id_id_index;
DROP TABLE IF EXISTS club_images;
