package superadmin

import (
	"context"
	"errors"
	"path"
	"strconv"

	"github.com/jackc/pgx/v5"

	"github.com/aitmaganbetov/zafirogo/server/internal/activity"
	superadminapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/superadmin"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// Новости платформы — первая карточка главного экрана агента во всех клубах. Laravel:
// app/Http/Controllers/Superadmin/NewsController.php, app/Services/NewsManager.php. Картинки отдаёт публичный
// маршрут /news-images/{news}/{file} раздела news (одна таблица news_images, club_id NULL).
const (
	newsPage    = 20 // News::PAGE
	subjectNews = `App\Models\News`
)

func newsImageURL(id int64, imagePath string) string {
	return "/news-images/" + strconv.FormatInt(id, 10) + "/" + path.Base(imagePath)
}

// SaListPlatformNews — Superadmin\NewsController::index().
func (h *handlers) SaListPlatformNews(ctx context.Context, req superadminapi.SaListPlatformNewsRequestObject) (superadminapi.SaListPlatformNewsResponseObject, error) {
	pg := page(req.Params.Page)
	out := superadminapi.PlatformNewsPage{Items: []superadminapi.PlatformNewsItem{}, Page: pg, PerPage: newsPage}
	err := h.tx(ctx, func(q *store.Queries) error {
		var err error
		if out.Total, err = q.SuperadminCountPlatformNews(ctx); err != nil {
			return err
		}
		rows, err := q.SuperadminPlatformNews(ctx, store.SuperadminPlatformNewsParams{PageSize: newsPage, PageOffset: int64(pg-1) * newsPage})
		for _, r := range rows {
			item := superadminapi.PlatformNewsItem{Id: r.ID, Title: r.Title, Body: r.Body, IsPublished: r.IsPublished, CreatedAt: r.CreatedAt}
			if r.ImagePath != nil && *r.ImagePath != "" {
				item.HasImage = true
				item.ImageUrl = ptr(newsImageURL(r.ID, *r.ImagePath))
			}
			out.Items = append(out.Items, item)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return superadminapi.SaListPlatformNews200JSONResponse(out), nil
}

// SaCreatePlatformNews — store(): журнал платформы superadmin.news_created.
func (h *handlers) SaCreatePlatformNews(ctx context.Context, req superadminapi.SaCreatePlatformNewsRequestObject) (superadminapi.SaCreatePlatformNewsResponseObject, error) {
	f, err := readForm(req.Body)
	if err != nil {
		return nil, err
	}
	if err := f.validate(); err != nil {
		return nil, err
	}
	now := h.d.NowFunc()
	uid := httpx.Principal(ctx).UserID
	published := !f.hasPub || f.published
	err = h.tx(ctx, func(q *store.Queries) error {
		var img *string
		if f.image != nil {
			p, err := f.imagePath()
			if err != nil {
				return err
			}
			img = &p
		}
		id, err := q.SuperadminInsertPlatformNews(ctx, store.SuperadminInsertPlatformNewsParams{Title: f.title, Body: f.body, ImagePath: img,
			IsPublished: published, CreatedBy: &uid, Now: &now})
		if err != nil {
			return err
		}
		if f.image != nil {
			if err := q.SuperadminPutPlatformNewsImage(ctx, store.SuperadminPutPlatformNewsImageParams{NewsID: id, ContentType: f.imageType, Data: f.image, Now: now}); err != nil {
				return err
			}
		}
		if err := h.log(ctx, q, activity.PlatformEntry{Event: "superadmin.news_created", Key: "Добавлена новость: :title",
			Params: map[string]string{"title": f.title}, SubjectType: subjectNews, SubjectID: id}); err != nil {
			return err
		}
		return h.audit(ctx, q, 0, "settings", "platform_news.created", subjectNews, id, nil, map[string]any{"title": f.title, "is_published": published})
	})
	if err != nil {
		return nil, err
	}
	return superadminapi.SaCreatePlatformNews201JSONResponse(msg(h.d.T(ctx, "Новость добавлена.", nil))), nil
}

// SaUpdatePlatformNews — update(): новость клуба — 404 (abort_unless($news->club_id === null, 404)).
func (h *handlers) SaUpdatePlatformNews(ctx context.Context, req superadminapi.SaUpdatePlatformNewsRequestObject) (superadminapi.SaUpdatePlatformNewsResponseObject, error) {
	f, err := readForm(req.Body)
	if err != nil {
		return nil, err
	}
	now := h.d.NowFunc()
	err = h.tx(ctx, func(q *store.Queries) error {
		cur, err := q.SuperadminLockPlatformNews(ctx, req.NewsId)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NotFound()
		}
		if err != nil {
			return err
		}
		if err := f.validate(); err != nil {
			return err
		}
		img := cur.ImagePath
		switch {
		case f.image != nil:
			p, err := f.imagePath()
			if err != nil {
				return err
			}
			img = &p
			if err := q.SuperadminPutPlatformNewsImage(ctx, store.SuperadminPutPlatformNewsImageParams{NewsID: cur.ID, ContentType: f.imageType, Data: f.image, Now: now}); err != nil {
				return err
			}
		case f.removeImage:
			img = nil
			if err := q.SuperadminDeletePlatformNewsImage(ctx, cur.ID); err != nil {
				return err
			}
		}
		published := f.hasPub && f.published
		if err := q.SuperadminUpdatePlatformNews(ctx, store.SuperadminUpdatePlatformNewsParams{Title: f.title, Body: f.body,
			IsPublished: published, ImagePath: img, Now: &now, ID: cur.ID}); err != nil {
			return err
		}
		if err := h.log(ctx, q, activity.PlatformEntry{Event: "superadmin.news_updated", Key: "Изменена новость: :title",
			Params: map[string]string{"title": f.title}, SubjectType: subjectNews, SubjectID: cur.ID}); err != nil {
			return err
		}
		return h.audit(ctx, q, 0, "settings", "platform_news.updated", subjectNews, cur.ID, map[string]any{"title": cur.Title},
			map[string]any{"title": f.title, "is_published": published})
	})
	if err != nil {
		return nil, err
	}
	return superadminapi.SaUpdatePlatformNews200JSONResponse(msg(h.d.T(ctx, "Новость сохранена.", nil))), nil
}

// SaDeletePlatformNews — destroy(): журнал без предмета.
func (h *handlers) SaDeletePlatformNews(ctx context.Context, req superadminapi.SaDeletePlatformNewsRequestObject) (superadminapi.SaDeletePlatformNewsResponseObject, error) {
	err := h.tx(ctx, func(q *store.Queries) error {
		cur, err := q.SuperadminLockPlatformNews(ctx, req.NewsId)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NotFound()
		}
		if err != nil {
			return err
		}
		if err := q.SuperadminDeletePlatformNews(ctx, cur.ID); err != nil {
			return err
		}
		if err := h.log(ctx, q, activity.PlatformEntry{Event: "superadmin.news_deleted", Key: "Удалена новость: :title",
			Params: map[string]string{"title": cur.Title}}); err != nil {
			return err
		}
		return h.audit(ctx, q, 0, "settings", "platform_news.deleted", subjectNews, cur.ID, map[string]any{"title": cur.Title}, nil)
	})
	if err != nil {
		return nil, err
	}
	return superadminapi.SaDeletePlatformNews200JSONResponse(msg(h.d.T(ctx, "Новость удалена.", nil))), nil
}
