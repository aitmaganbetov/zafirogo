package settings

// Логотип клуба и картинка экрана входа агента: ClubLogoController, ClubSignInImageController. Байты — в
// club_images (0019): у нового сервера нет общего диска между инстансами; путь прежнего формата — в clubs.

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
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/aitmaganbetov/zafirogo/server/internal/activity"
	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	settingsapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/settings"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// imageKind — вид картинки клуба: правила формы, каталог пути, маршрут, тексты.
type imageKind struct {
	kind       string // club_images.kind
	field      string // поле формы
	dir        string // каталог пути (storeAs('club-logos/<club>', …))
	route      string // публичный маршрут
	maxBytes   int    // max:<КБ>
	maxKB      string
	minW, minH int
	event      string
	logSaved   string
	logDeleted string
	msgSaved   string
	msgDeleted string
	// msgMax, msgDimensions — свои тексты правил (пусто — тексты валидатора).
	msgMax, msgDimensions string
}

var (
	kindLogo = imageKind{
		kind: "logo", field: "logo", dir: "club-logos", route: "/club-logos/", maxBytes: 1024 * 1024, maxKB: "1024",
		minW: 32, minH: 32, event: "club.logo_changed",
		logSaved: "Логотип клуба изменён", logDeleted: "Логотип клуба удалён",
		msgSaved: "Логотип сохранён.", msgDeleted: "Логотип удалён.",
	}
	kindSignin = imageKind{
		kind: "signin", field: "image", dir: "club-signin-images", route: "/club-signin-images/", maxBytes: 3072 * 1024,
		maxKB: "3072", minW: 640, minH: 360, event: "club.signin_image_changed",
		logSaved: "Картинка экрана входа изменена", logDeleted: "Картинка экрана входа удалена",
		msgSaved: "Картинка экрана входа сохранена.", msgDeleted: "Картинка экрана входа удалена: в агенте снова стандартная.",
		msgMax: "Картинка — не больше 3 МБ.", msgDimensions: "Картинка — от 640×360 до 4096×4096 пикселей.",
	}
)

// imageURL — Club::logoUrl() / signInImageUrl(): /<маршрут>/{club}/{basename(path)}.
func imageURL(k imageKind, clubID int64, p string) string {
	return k.route + strconv.FormatInt(clubID, 10) + "/" + path.Base(p)
}

// upload — файл формы (multipart) и его проверка: required|file|mimes:png,jpg,jpeg|max|dimensions.
type upload struct {
	data        []byte
	contentType string
	ext         string
}

func readUpload(mr *multipart.Reader, k imageKind) (upload, error) {
	var u upload
	found := false
	for mr != nil {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return u, httpx.NewAPIError(http.StatusBadRequest, "bad_request", "")
		}
		if part.FormName() != k.field || part.FileName() == "" {
			_, _ = io.Copy(io.Discard, io.LimitReader(part, 64*1024))
			continue
		}
		data, err := io.ReadAll(io.LimitReader(part, int64(k.maxBytes)+1))
		if err != nil {
			return u, httpx.NewAPIError(http.StatusBadRequest, "bad_request", "")
		}
		u.data, found = data, true
	}
	var fe httpx.FormErrors
	if !found || len(u.data) == 0 {
		fe.Add(k.field, httpx.RuleMessage(k.field, "required", ""))
		return u, fe.Err()
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(u.data))
	if err != nil || (format != "png" && format != "jpeg") {
		// validation.mimes — в ru.php нет: английский текст валидатора по умолчанию, как в старом.
		fe.Add(k.field, fmt.Sprintf("The %s field must be a file of type: png, jpg, jpeg.", k.field))
	}
	if len(u.data) > k.maxBytes {
		if k.msgMax != "" {
			fe.Add(k.field, k.msgMax)
		} else {
			fe.AddMsg(k.field, "validation.max.file", map[string]string{"attribute": k.field, "max": k.maxKB})
		}
	}
	if err == nil && (cfg.Width < k.minW || cfg.Height < k.minH || cfg.Width > 4096 || cfg.Height > 4096) {
		if k.msgDimensions != "" {
			fe.Add(k.field, k.msgDimensions)
		} else {
			fe.Add(k.field, fmt.Sprintf("The %s field has invalid image dimensions.", k.field))
		}
	}
	if err := fe.Err(); err != nil {
		return u, err
	}
	// guessExtension() === 'png' ? 'png' : 'jpg'.
	u.contentType, u.ext = "image/jpeg", "jpg"
	if format == "png" {
		u.contentType, u.ext = "image/png", "png"
	}
	return u, nil
}

// randomName — Str::random(40).
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

// UploadClubLogo — ClubLogoController::update().
func (h *handlers) UploadClubLogo(ctx context.Context, req settingsapi.UploadClubLogoRequestObject) (settingsapi.UploadClubLogoResponseObject, error) {
	msg, err := h.saveImage(ctx, req.Body, kindLogo)
	if err != nil {
		return nil, err
	}
	return settingsapi.UploadClubLogo200JSONResponse{Message: msg}, nil
}

// DeleteClubLogo — ClubLogoController::destroy().
func (h *handlers) DeleteClubLogo(ctx context.Context, _ settingsapi.DeleteClubLogoRequestObject) (settingsapi.DeleteClubLogoResponseObject, error) {
	msg, err := h.deleteImage(ctx, kindLogo)
	if err != nil {
		return nil, err
	}
	return settingsapi.DeleteClubLogo200JSONResponse{Message: msg}, nil
}

// UploadSigninImage — ClubSignInImageController::update().
func (h *handlers) UploadSigninImage(ctx context.Context, req settingsapi.UploadSigninImageRequestObject) (settingsapi.UploadSigninImageResponseObject, error) {
	msg, err := h.saveImage(ctx, req.Body, kindSignin)
	if err != nil {
		return nil, err
	}
	return settingsapi.UploadSigninImage200JSONResponse{Message: msg}, nil
}

// DeleteSigninImage — ClubSignInImageController::destroy().
func (h *handlers) DeleteSigninImage(ctx context.Context, _ settingsapi.DeleteSigninImageRequestObject) (settingsapi.DeleteSigninImageResponseObject, error) {
	msg, err := h.deleteImage(ctx, kindSignin)
	if err != nil {
		return nil, err
	}
	return settingsapi.DeleteSigninImage200JSONResponse{Message: msg}, nil
}

func (h *handlers) setPath(ctx context.Context, q *store.Queries, k imageKind, clubID int64, p *string, now time.Time) error {
	if k.kind == kindLogo.kind {
		return q.SettingsSetLogoPath(ctx, store.SettingsSetLogoPathParams{ClubID: clubID, Path: p, Now: &now})
	}
	return q.SettingsSetSigninImagePath(ctx, store.SettingsSetSigninImagePathParams{ClubID: clubID, Path: p, Now: &now})
}

func (h *handlers) saveImage(ctx context.Context, body *multipart.Reader, k imageKind) (string, error) {
	u, err := readUpload(body, k)
	if err != nil {
		return "", err
	}
	p := httpx.Principal(ctx)
	name, err := randomName()
	if err != nil {
		return "", err
	}
	imgPath := fmt.Sprintf("%s/%d/%s.%s", k.dir, p.ClubID, name, u.ext)
	now := h.d.NowFunc()
	err = h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		if _, err := q.SettingsLockClub(ctx, p.ClubID); err != nil {
			return err
		}
		if err := q.SettingsPutImage(ctx, store.SettingsPutImageParams{ClubID: p.ClubID, Kind: k.kind, ContentType: u.contentType, Data: u.data, Now: now}); err != nil {
			return err
		}
		if err := h.setPath(ctx, q, k, p.ClubID, &imgPath, now); err != nil {
			return err
		}
		_, err := activity.Record(ctx, q, activity.Entry{
			ClubID: p.ClubID, Event: k.event, UserID: p.UserID, SubjectType: subjectClub, SubjectID: p.ClubID, At: now,
		}.Translated(k.logSaved, nil))
		return err
	})
	if err != nil {
		return "", err
	}
	h.configChanged(ctx, p.ClubID)
	return h.d.T(ctx, k.msgSaved, nil), nil
}

func (h *handlers) deleteImage(ctx context.Context, k imageKind) (string, error) {
	p := httpx.Principal(ctx)
	now := h.d.NowFunc()
	changed := false
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		row, err := q.SettingsLockClub(ctx, p.ClubID)
		if err != nil {
			return err
		}
		prev := row.LogoPath
		if k.kind == kindSignin.kind {
			prev = row.SigninImagePath
		}
		if prev == nil {
			return nil
		}
		changed = true
		if err := q.SettingsDeleteImage(ctx, store.SettingsDeleteImageParams{ClubID: p.ClubID, Kind: k.kind}); err != nil {
			return err
		}
		if err := h.setPath(ctx, q, k, p.ClubID, nil, now); err != nil {
			return err
		}
		_, err = activity.Record(ctx, q, activity.Entry{
			ClubID: p.ClubID, Event: k.event, UserID: p.UserID, SubjectType: subjectClub, SubjectID: p.ClubID, At: now,
		}.Translated(k.logDeleted, nil))
		return err
	})
	if err != nil {
		return "", err
	}
	if changed {
		h.configChanged(ctx, p.ClubID)
	}
	return h.d.T(ctx, k.msgDeleted, nil), nil
}

// image — show(): имя файла должно совпасть с basename пути клуба; иначе 404. Заголовки — как в старом.
func (h *handlers) image(k imageKind) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		clubID, err := strconv.ParseInt(chi.URLParam(r, "club"), 10, 64)
		file := chi.URLParam(r, "file")
		if err != nil || clubID <= 0 || file == "" || h.d.PlatformPool == nil {
			httpx.WriteError(w, http.StatusNotFound, "not_found")
			return
		}
		var row store.SettingsPublicImageRow
		err = db.InPlatformTx(r.Context(), h.d.PlatformPool, func(q *store.Queries) error {
			var err error
			row, err = q.SettingsPublicImage(r.Context(), store.SettingsPublicImageParams{ClubID: clubID, Kind: k.kind})
			return err
		})
		p := row.LogoPath
		if k.kind == kindSignin.kind {
			p = row.SigninImagePath
		}
		if err != nil || p == nil || path.Base(*p) != file {
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				h.d.Log().ErrorContext(r.Context(), "club image", "club_id", clubID, "kind", k.kind, "err", err)
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
}
