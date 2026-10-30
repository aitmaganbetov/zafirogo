-- Новости клуба (Этап 3, группа 4). Laravel: app/Http/Controllers/NewsController.php, app/Services/NewsManager.php,
-- app/Models/News.php. Новости платформы (club_id NULL) — суперадминка (группа 6). Показ гостю — guest.sql.

-- name: ListClubNews :many
-- News::ofClub()->latest()->latest('id')->paginate(News::PAGE).
SELECT id, title, body, image_path, is_published, created_at
FROM news
WHERE club_id = sqlc.arg(club_id)::bigint
ORDER BY created_at DESC NULLS LAST, id DESC
LIMIT sqlc.arg(page_size)::bigint OFFSET sqlc.arg(page_offset)::bigint;

-- name: CountClubNews :one
SELECT count(*)::bigint FROM news WHERE club_id = sqlc.arg(club_id)::bigint;

-- name: LockClubNews :one
-- ensureOwn(): новость своего клуба (новость платформы или чужая — 404), строка блокируется.
SELECT id, title, image_path
FROM news
WHERE club_id = sqlc.arg(club_id)::bigint AND id = sqlc.arg(id)
FOR UPDATE;

-- name: InsertClubNews :one
INSERT INTO news (club_id, title, body, image_path, is_published, created_by, created_at, updated_at)
VALUES (sqlc.arg(club_id)::bigint, sqlc.arg(title), sqlc.arg(body), sqlc.narg(image_path), sqlc.arg(is_published),
        sqlc.arg(created_by), sqlc.arg(now), sqlc.arg(now))
RETURNING id;

-- name: UpdateClubNews :exec
UPDATE news
SET title = sqlc.arg(title), body = sqlc.arg(body), is_published = sqlc.arg(is_published),
    image_path = sqlc.narg(image_path), updated_at = sqlc.arg(now)
WHERE club_id = sqlc.arg(club_id)::bigint AND id = sqlc.arg(id);

-- name: DeleteClubNews :exec
DELETE FROM news WHERE club_id = sqlc.arg(club_id)::bigint AND id = sqlc.arg(id);

-- name: PutClubNewsImage :exec
-- Картинка новости клуба (одна на новость; новая заменяет прежнюю).
INSERT INTO news_images (news_id, club_id, content_type, data, created_at)
VALUES (sqlc.arg(news_id), sqlc.arg(club_id)::bigint, sqlc.arg(content_type), sqlc.arg(data), sqlc.arg(now))
ON CONFLICT (news_id) DO UPDATE SET content_type = EXCLUDED.content_type, data = EXCLUDED.data,
                                    created_at = EXCLUDED.created_at;

-- name: DeleteClubNewsImage :exec
DELETE FROM news_images WHERE club_id = sqlc.arg(club_id)::bigint AND news_id = sqlc.arg(news_id);

-- name: PublicNewsImage :one
-- NewsController::image(): публичная картинка по id новости (агент грузит её без токена). Пул zafiro_platform:
-- клуб из запроса не известен; имя файла сверяется с basename(news.image_path) в обработчике.
SELECT n.club_id, n.image_path, i.content_type, i.data
FROM news n
JOIN news_images i ON i.news_id = n.id
WHERE n.id = sqlc.arg(news_id) AND (n.club_id IS NULL OR n.club_id = i.club_id);
