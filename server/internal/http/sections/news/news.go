// Package news — REST-раздел: новости клуба для агента ПК (api/news.yaml) и публичные картинки новостей.
//
// Laravel: app/Http/Controllers/NewsController.php, app/Services/NewsManager.php, app/Models/News.php.
// Новости платформы (club_id NULL) правит суперадминка (группа 6). Картинка хранится в news_images
// (0018_news_images.sql): у нового сервера нет общего диска между инстансами.
package news

import (
	"bytes"
	"context"
	"errors"
	_ "image/jpeg" // image.DecodeConfig: JPEG
	_ "image/png"  // image.DecodeConfig: PNG
	"net/http"
	"path"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/aitmaganbetov/zafirogo/server/internal/activity"
	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	newsapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/news"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/newsform"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

var access = httpx.Access{
	Routes: map[string]string{
		"ListNews":   "news.index",
		"CreateNews": "news.store",
		"UpdateNews": "news.update",
		"DeleteNews": "news.destroy",
	},
}

type handlers struct {
	d *httpx.Deps
}

var _ newsapi.StrictServerInterface = (*handlers)(nil)

// Section — новости клуба. Регистрация — server/internal/http/sections.go.
func Section() httpx.Section {
	return httpx.Section{Name: "news", Mount: mount}
}

func mount(r chi.Router, d *httpx.Deps) {
	h := &handlers{d: d}
	si := newsapi.NewStrictHandlerWithOptions(h,
		httpx.Middlewares[newsapi.StrictMiddlewareFunc](access),
		newsapi.StrictHTTPServerOptions{RequestErrorHandlerFunc: d.RequestError, ResponseErrorHandlerFunc: d.ResponseError()})
	newsapi.HandlerWithOptions(si, newsapi.ChiServerOptions{BaseRouter: r, ErrorHandlerFunc: d.ParamError})
	// NewsController::image(): публичная картинка по случайному имени файла (агент грузит её без токена).
	r.Get("/news-images/{news}/{file}", h.image)
}

// Тексты NewsController (ключи перевода).
const (
	msgAdded   = "Новость добавлена."
	msgSaved   = "Новость сохранена."
	msgDeleted = "Новость удалена."
	logCreated = "Добавлена новость: :title"
	logUpdated = "Изменена новость: :title"
	logDeleted = "Удалена новость: :title"

	subjectNews = `App\Models\News`
	// pageSize — News::PAGE.
	pageSize = 20
)

// ListNews — index(): новости клуба, новые сверху, по 20.
func (h *handlers) ListNews(ctx context.Context, req newsapi.ListNewsRequestObject) (newsapi.ListNewsResponseObject, error) {
	clubID := httpx.ClubID(ctx)
	page := 1
	if req.Params.Page != nil && *req.Params.Page > 1 {
		page = *req.Params.Page
	}
	out := newsapi.NewsPage{Items: []newsapi.NewsItem{}, Page: page, PerPage: pageSize}
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		var err error
		if out.Total, err = q.CountClubNews(ctx, clubID); err != nil {
			return err
		}
		rows, err := q.ListClubNews(ctx, store.ListClubNewsParams{ClubID: clubID, PageSize: pageSize, PageOffset: int64(page-1) * pageSize})
		for _, r := range rows {
			item := newsapi.NewsItem{Id: r.ID, Title: r.Title, Body: r.Body, IsPublished: r.IsPublished, CreatedAt: r.CreatedAt}
			if r.ImagePath != nil && *r.ImagePath != "" {
				item.HasImage = true
				u := newsform.ImageURL(r.ID, *r.ImagePath)
				item.ImageUrl = &u
			}
			out.Items = append(out.Items, item)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return newsapi.ListNews200JSONResponse(out), nil
}

// CreateNews — store(): новость клуба с картинкой, журнал news.created.
func (h *handlers) CreateNews(ctx context.Context, req newsapi.CreateNewsRequestObject) (newsapi.CreateNewsResponseObject, error) {
	f, err := newsform.Read(req.Body)
	if err != nil {
		return nil, err
	}
	if err := f.Validate(); err != nil {
		return nil, err
	}
	p := httpx.Principal(ctx)
	now := h.d.NowFunc()
	published := !f.HasPublished || f.Published // ($data['is_published'] ?? true)
	err = h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		var imgPath *string
		if f.Image != nil {
			pth, err := f.ImagePath("news/" + strconv.FormatInt(p.ClubID, 10))
			if err != nil {
				return err
			}
			imgPath = &pth
		}
		id, err := q.InsertClubNews(ctx, store.InsertClubNewsParams{
			ClubID: p.ClubID, Title: f.Title, Body: f.Body, ImagePath: imgPath, IsPublished: published, CreatedBy: &p.UserID, Now: &now,
		})
		if err != nil {
			return err
		}
		if f.Image != nil {
			if err := q.PutClubNewsImage(ctx, store.PutClubNewsImageParams{NewsID: id, ClubID: p.ClubID, ContentType: f.ImageType, Data: f.Image, Now: now}); err != nil {
				return err
			}
		}
		_, err = activity.Record(ctx, q, activity.Entry{
			ClubID: p.ClubID, Event: "news.created", UserID: p.UserID, SubjectType: subjectNews, SubjectID: id, At: now,
		}.Translated(logCreated, map[string]string{"title": f.Title}))
		return err
	})
	if err != nil {
		return nil, err
	}
	return newsapi.CreateNews201JSONResponse{Message: h.d.T(ctx, msgAdded, nil)}, nil
}

// UpdateNews — update(): новая картинка или «убрать картинку» удаляют прежнюю; журнал news.updated.
func (h *handlers) UpdateNews(ctx context.Context, req newsapi.UpdateNewsRequestObject) (newsapi.UpdateNewsResponseObject, error) {
	f, err := newsform.Read(req.Body)
	if err != nil {
		return nil, err
	}
	p := httpx.Principal(ctx)
	now := h.d.NowFunc()
	err = h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		cur, err := q.LockClubNews(ctx, store.LockClubNewsParams{ClubID: p.ClubID, ID: req.NewsId})
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NotFound() // ensureOwn()
		}
		if err != nil {
			return err
		}
		if err := f.Validate(); err != nil {
			return err
		}
		imgPath := cur.ImagePath
		switch {
		case f.Image != nil:
			pth, err := f.ImagePath("news/" + strconv.FormatInt(p.ClubID, 10))
			if err != nil {
				return err
			}
			imgPath = &pth
			if err := q.PutClubNewsImage(ctx, store.PutClubNewsImageParams{NewsID: cur.ID, ClubID: p.ClubID, ContentType: f.ImageType, Data: f.Image, Now: now}); err != nil {
				return err
			}
		case f.RemoveImage:
			imgPath = nil
			if err := q.DeleteClubNewsImage(ctx, store.DeleteClubNewsImageParams{ClubID: p.ClubID, NewsID: cur.ID}); err != nil {
				return err
			}
		}
		if err := q.UpdateClubNews(ctx, store.UpdateClubNewsParams{
			ClubID: p.ClubID, ID: cur.ID, Title: f.Title, Body: f.Body, IsPublished: f.HasPublished && f.Published, // ?? false
			ImagePath: imgPath, Now: &now,
		}); err != nil {
			return err
		}
		_, err = activity.Record(ctx, q, activity.Entry{
			ClubID: p.ClubID, Event: "news.updated", UserID: p.UserID, SubjectType: subjectNews, SubjectID: cur.ID, At: now,
		}.Translated(logUpdated, map[string]string{"title": f.Title}))
		return err
	})
	if err != nil {
		return nil, err
	}
	return newsapi.UpdateNews200JSONResponse{Message: h.d.T(ctx, msgSaved, nil)}, nil
}

// DeleteNews — destroy(): новость и картинка (каскад), журнал news.deleted без предмета.
func (h *handlers) DeleteNews(ctx context.Context, req newsapi.DeleteNewsRequestObject) (newsapi.DeleteNewsResponseObject, error) {
	p := httpx.Principal(ctx)
	now := h.d.NowFunc()
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		cur, err := q.LockClubNews(ctx, store.LockClubNewsParams{ClubID: p.ClubID, ID: req.NewsId})
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NotFound()
		}
		if err != nil {
			return err
		}
		if err := q.DeleteClubNews(ctx, store.DeleteClubNewsParams{ClubID: p.ClubID, ID: cur.ID}); err != nil {
			return err
		}
		_, err = activity.Record(ctx, q, activity.Entry{
			ClubID: p.ClubID, Event: "news.deleted", UserID: p.UserID, At: now,
		}.Translated(logDeleted, map[string]string{"title": cur.Title}))
		return err
	})
	if err != nil {
		return nil, err
	}
	return newsapi.DeleteNews200JSONResponse{Message: h.d.T(ctx, msgDeleted, nil)}, nil
}

// image — NewsController::image(): имя файла должно совпасть с basename(image_path); иначе 404.
func (h *handlers) image(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "news"), 10, 64)
	file := chi.URLParam(r, "file")
	if err != nil || id <= 0 || file == "" || h.d.PlatformPool == nil {
		httpx.WriteError(w, http.StatusNotFound, "not_found")
		return
	}
	var row store.PublicNewsImageRow
	err = db.InPlatformTx(r.Context(), h.d.PlatformPool, func(q *store.Queries) error {
		var err error
		row, err = q.PublicNewsImage(r.Context(), id)
		return err
	})
	if err != nil || row.ImagePath == nil || path.Base(*row.ImagePath) != file {
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			h.d.Log().ErrorContext(r.Context(), "news image", "news_id", id, "err", err)
		}
		httpx.WriteError(w, http.StatusNotFound, "not_found")
		return
	}
	hd := w.Header()
	hd.Set("Content-Type", row.ContentType)
	hd.Set("Cache-Control", "public, max-age=31536000, immutable")
	hd.Set("X-Content-Type-Options", "nosniff")
	hd.Set("Content-Security-Policy", "default-src 'none'")
	http.ServeContent(w, r, file, time.Time{}, bytes.NewReader(row.Data))
}
