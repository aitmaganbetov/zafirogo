// Package news — REST-раздел: новости клуба для агента ПК (api/news.yaml) и публичные картинки новостей.
//
// Laravel: app/Http/Controllers/NewsController.php, app/Services/NewsManager.php, app/Models/News.php.
// Новости платформы (club_id NULL) правит суперадминка (группа 6). Картинка хранится в news_images
// (0018_news_images.sql): у нового сервера нет общего диска между инстансами.
package news

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg" // image.DecodeConfig: JPEG
	_ "image/png"  // image.DecodeConfig: PNG
	"io"
	"math/big"
	"mime/multipart"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/aitmaganbetov/zafirogo/server/internal/activity"
	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	newsapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/news"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
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
	// maxImage — max:2048 (килобайт).
	maxImage = 2048 * 1024
	// maxField — предел текстового поля формы при чтении (body max:5000 символов — до 20 000 байт UTF-8).
	maxField = 64 * 1024
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
				u := ImageURL(r.ID, *r.ImagePath)
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

// ImageURL — News::imageUrl(): /news-images/{id}/{basename(image_path)}.
func ImageURL(newsID int64, imagePath string) string {
	return "/news-images/" + strconv.FormatInt(newsID, 10) + "/" + path.Base(imagePath)
}

// form — поля NewsManager::validate().
type form struct {
	title, body         string
	published, hasPub   bool
	removeImage         bool
	image               []byte
	imageType, imageExt string
	hasTitle, hasBody   bool
}

// readForm — multipart: title, body, is_published, remove_image, image. Повтор поля (hidden 0 + checkbox 1) —
// последнее значение, как разбор формы PHP.
func readForm(mr *multipart.Reader) (form, error) {
	var f form
	if mr == nil {
		return f, nil
	}
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return f, httpx.NewAPIError(http.StatusBadRequest, "bad_request", "")
		}
		name := part.FormName()
		if name == "image" && part.FileName() != "" {
			data, err := io.ReadAll(io.LimitReader(part, maxImage+1))
			if err != nil {
				return f, httpx.NewAPIError(http.StatusBadRequest, "bad_request", "")
			}
			f.image = data
			continue
		}
		data, err := io.ReadAll(io.LimitReader(part, maxField))
		if err != nil {
			return f, httpx.NewAPIError(http.StatusBadRequest, "bad_request", "")
		}
		v := string(data)
		switch name {
		case "title":
			f.title, f.hasTitle = strings.TrimSpace(v), true
		case "body":
			f.body, f.hasBody = strings.TrimSpace(v), true
		case "is_published":
			f.published, f.hasPub = boolField(v), true
		case "remove_image":
			f.removeImage = boolField(v)
		}
	}
	return f, nil
}

// boolField — правило boolean Laravel: true/false, 1/0, "1"/"0" (иное — false).
func boolField(v string) bool { return v == "1" || v == "true" }

// validate — title required|max:150, body required|max:5000, image png/jpeg ≤ 2 МБ, 64…4096 пикселей.
func (f *form) validate() error {
	var fe httpx.FormErrors
	switch n := utf8.RuneCountInString(f.title); {
	case n == 0:
		fe.Add("title", httpx.RuleMessage("title", "required", ""))
	case n > 150:
		fe.Add("title", httpx.RuleMessage("title", "max_string", "150"))
	}
	switch n := utf8.RuneCountInString(f.body); {
	case n == 0:
		fe.Add("body", httpx.RuleMessage("body", "required", ""))
	case n > 5000:
		fe.Add("body", httpx.RuleMessage("body", "max_string", "5000"))
	}
	if f.image != nil {
		cfg, format, err := image.DecodeConfig(bytes.NewReader(f.image))
		switch {
		case err != nil || (format != "png" && format != "jpeg"):
			fe.Add("image", httpx.RuleMessage("image", "mimes", ""))
		case len(f.image) > maxImage:
			fe.Add("image", httpx.RuleMessage("image", "max_file", ""))
		case cfg.Width < 64 || cfg.Height < 64 || cfg.Width > 4096 || cfg.Height > 4096:
			fe.Add("image", httpx.RuleMessage("image", "dimensions", ""))
		default:
			// storeImage(): guessExtension() === 'png' ? 'png' : 'jpg'.
			f.imageType, f.imageExt = "image/jpeg", "jpg"
			if format == "png" {
				f.imageType, f.imageExt = "image/png", "png"
			}
		}
	}
	return fe.Err()
}

// randomName — Str::random(40): буквы и цифры.
func randomName() (string, error) {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 40)
	for i := range b {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			return "", err
		}
		b[i] = alphabet[n.Int64()]
	}
	return string(b), nil
}

// storeImage — картинка новости: путь news/<club>/<40 символов>.<png|jpg> и байты в news_images.
func (f *form) imagePath(clubID int64) (string, error) {
	name, err := randomName()
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("news/%d/%s.%s", clubID, name, f.imageExt), nil
}

// CreateNews — store(): новость клуба с картинкой, журнал news.created.
func (h *handlers) CreateNews(ctx context.Context, req newsapi.CreateNewsRequestObject) (newsapi.CreateNewsResponseObject, error) {
	f, err := readForm(req.Body)
	if err != nil {
		return nil, err
	}
	if err := f.validate(); err != nil {
		return nil, err
	}
	p := httpx.Principal(ctx)
	now := h.d.NowFunc()
	published := !f.hasPub || f.published // ($data['is_published'] ?? true)
	err = h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		var imgPath *string
		if f.image != nil {
			pth, err := f.imagePath(p.ClubID)
			if err != nil {
				return err
			}
			imgPath = &pth
		}
		id, err := q.InsertClubNews(ctx, store.InsertClubNewsParams{
			ClubID: p.ClubID, Title: f.title, Body: f.body, ImagePath: imgPath, IsPublished: published, CreatedBy: &p.UserID, Now: &now,
		})
		if err != nil {
			return err
		}
		if f.image != nil {
			if err := q.PutClubNewsImage(ctx, store.PutClubNewsImageParams{NewsID: id, ClubID: p.ClubID, ContentType: f.imageType, Data: f.image, Now: now}); err != nil {
				return err
			}
		}
		_, err = activity.Record(ctx, q, activity.Entry{
			ClubID: p.ClubID, Event: "news.created", UserID: p.UserID, SubjectType: subjectNews, SubjectID: id, At: now,
		}.Translated(logCreated, map[string]string{"title": f.title}))
		return err
	})
	if err != nil {
		return nil, err
	}
	return newsapi.CreateNews201JSONResponse{Message: h.d.T(ctx, msgAdded, nil)}, nil
}

// UpdateNews — update(): новая картинка или «убрать картинку» удаляют прежнюю; журнал news.updated.
func (h *handlers) UpdateNews(ctx context.Context, req newsapi.UpdateNewsRequestObject) (newsapi.UpdateNewsResponseObject, error) {
	f, err := readForm(req.Body)
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
		if err := f.validate(); err != nil {
			return err
		}
		imgPath := cur.ImagePath
		switch {
		case f.image != nil:
			pth, err := f.imagePath(p.ClubID)
			if err != nil {
				return err
			}
			imgPath = &pth
			if err := q.PutClubNewsImage(ctx, store.PutClubNewsImageParams{NewsID: cur.ID, ClubID: p.ClubID, ContentType: f.imageType, Data: f.image, Now: now}); err != nil {
				return err
			}
		case f.removeImage:
			imgPath = nil
			if err := q.DeleteClubNewsImage(ctx, store.DeleteClubNewsImageParams{ClubID: p.ClubID, NewsID: cur.ID}); err != nil {
				return err
			}
		}
		if err := q.UpdateClubNews(ctx, store.UpdateClubNewsParams{
			ClubID: p.ClubID, ID: cur.ID, Title: f.title, Body: f.body, IsPublished: f.hasPub && f.published, // ?? false
			ImagePath: imgPath, Now: &now,
		}); err != nil {
			return err
		}
		_, err = activity.Record(ctx, q, activity.Entry{
			ClubID: p.ClubID, Event: "news.updated", UserID: p.UserID, SubjectType: subjectNews, SubjectID: cur.ID, At: now,
		}.Translated(logUpdated, map[string]string{"title": f.title}))
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
