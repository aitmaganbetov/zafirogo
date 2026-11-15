package migrate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg" // image.DecodeConfig: JPEG
	_ "image/png"  // image.DecodeConfig: PNG
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

// files — расширение M2 «файлы» (Q-085): логотип клуба и картинка экрана входа (clubs.logo_path,
// signin_image_path → club_images), картинки новостей клуба (news.image_path → news_images), фото клуба для
// приложения (club_photos.path → club_photo_images, 0036, UQ-19). В старом коде — файлы
// диска 'local' Laravel: config/filesystems.php root = storage_path('app/private') (ClubLogoController::update(),
// ClubSignInImageController::update(), NewsManager::storeImage(), ClubProfileController::storePhotos() —
// storeAs(…, 'local')). Пути в clubs/news/club_photos
// переносятся как есть (по ним сервер сверяет имя файла в URL).
//
// Проверки — как у сервера при загрузке (http/sections/settings/images.go readUpload(), 0018/0019): PNG или JPEG
// (image.DecodeConfig), размер ≤ 1 МБ (логотип), 3 МБ (вход), 2 МБ (новость); фото клуба — PNG, JPEG или WebP
// ≤ 3 МБ (WebP — по сигнатуре контейнера RIFF/WEBP: декодера WebP в стандартной библиотеке нет). Нет файла, не картинка, больше
// лимита — предупреждение, картинка не переносится (старый сервер в таком случае тоже отдавал 404).
type files struct{}

// Files — расширение M2 «файлы».
func Files() Extension { return files{} }

func (files) Name() string { return "files" }

func (e files) Plan(ctx context.Context, env *Env) error  { return e.run(ctx, env, modePlan) }
func (e files) Apply(ctx context.Context, env *Env) error { return e.run(ctx, env, modeApply) }
func (e files) Verify(ctx context.Context, env *Env) error {
	return e.run(ctx, env, modeVerify)
}

// imageFile — картинка для переноса.
type imageFile struct {
	Table, Column, RowID string // откуда путь (для отчёта)
	Path                 string
	Max                  int
	// Ключ строки в Postgres: club_images (club, kind), news_images (news id) или club_photo_images (photo id).
	Kind    string
	NewsID  int64
	PhotoID int64
	ClubID  *int64
	// WebP — допустим и WebP (фото клуба: mimes:jpg,jpeg,png,webp).
	WebP bool
}

const (
	maxLogoBytes   = 1024 * 1024 // ClubLogoController: max:1024
	maxSigninBytes = 3072 * 1024 // ClubSignInImageController: max:3072
	maxNewsBytes   = 2048 * 1024 // NewsManager: max:2048
	maxPhotoBytes  = 3072 * 1024 // ClubProfileController::storePhotos(): max:3072
)

func (e files) run(ctx context.Context, env *Env, mode extMode) error {
	var list []imageFile
	clubID := env.ClubID
	var logo, signin sql.NullString
	if err := env.Source.QueryRowContext(ctx, "SELECT logo_path, signin_image_path FROM clubs WHERE id = ?", clubID).Scan(&logo, &signin); err != nil {
		return fmt.Errorf("mysql: club images: %w", err)
	}
	cid := strconv.FormatInt(clubID, 10)
	if logo.Valid && logo.String != "" {
		list = append(list, imageFile{Table: "clubs", Column: "logo_path", RowID: cid, Path: logo.String, Max: maxLogoBytes, Kind: "logo", ClubID: &clubID})
	}
	if signin.Valid && signin.String != "" {
		list = append(list, imageFile{Table: "clubs", Column: "signin_image_path", RowID: cid, Path: signin.String, Max: maxSigninBytes, Kind: "signin", ClubID: &clubID})
	}
	news, err := newsImages(ctx, env.Source, "club_id = ?", clubID)
	if err != nil {
		return err
	}
	for _, n := range news {
		n.ClubID = &clubID
		list = append(list, n)
	}
	photos, err := clubPhotos(ctx, env.Source, clubID)
	if err != nil {
		return err
	}
	list = append(list, photos...)
	return transferImages(ctx, env.Report, env.Target, env.Options.LaravelStorage, list, mode)
}

// newsImages — новости с картинкой по условию (клуб или платформа).
func newsImages(ctx context.Context, src querierMy, where string, args ...any) ([]imageFile, error) {
	rows, err := src.QueryContext(ctx, "SELECT id, image_path FROM news WHERE "+where+" AND image_path IS NOT NULL AND image_path <> '' ORDER BY id", args...) // #nosec G202 -- условие — константа кода
	if err != nil {
		return nil, fmt.Errorf("mysql: news images: %w", err)
	}
	defer rows.Close()
	var out []imageFile
	for rows.Next() {
		var id int64
		var p string
		if err := rows.Scan(&id, &p); err != nil {
			return nil, err
		}
		out = append(out, imageFile{Table: "news", Column: "image_path", RowID: strconv.FormatInt(id, 10), Path: p, Max: maxNewsBytes, NewsID: id})
	}
	return out, rows.Err()
}

// clubPhotos — фото клуба для приложения (club_photos; строки переносит ядро, здесь — файлы).
func clubPhotos(ctx context.Context, src querierMy, clubID int64) ([]imageFile, error) {
	rows, err := src.QueryContext(ctx, "SELECT id, path FROM club_photos WHERE club_id = ? ORDER BY id", clubID)
	if err != nil {
		return nil, fmt.Errorf("mysql: club photos: %w", err)
	}
	defer rows.Close()
	var out []imageFile
	for rows.Next() {
		var id int64
		var p string
		if err := rows.Scan(&id, &p); err != nil {
			return nil, err
		}
		out = append(out, imageFile{Table: "club_photos", Column: "path", RowID: strconv.FormatInt(id, 10), Path: p,
			Max: maxPhotoBytes, PhotoID: id, ClubID: &clubID, WebP: true})
	}
	return out, rows.Err()
}

// querierMy — соединение MySQL (снимок клуба или пул платформенного шага).
type querierMy interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// loadedImage — проверенная картинка.
type loadedImage struct {
	Data        []byte
	ContentType string
}

// errImage — файл не переносится (причина — для отчёта).
type errImage struct{ reason string }

func (e errImage) Error() string { return e.reason }

// loadImage читает файл диска 'local' и проверяет его как сервер. Путь — только внутри корня (без «..» и
// абсолютных путей: значение из базы — недоверенное).
func loadImage(root, rel string, maxBytes int, webp bool) (loadedImage, error) {
	clean := path.Clean("/" + strings.ReplaceAll(rel, `\`, "/"))
	if clean != "/"+rel || strings.Contains(rel, "..") {
		return loadedImage{}, errImage{"путь вне каталога storage — не переносится"}
	}
	// os.Root, а не os.DirFS: символическая ссылка в storage, ведущая наружу (../../.env, /etc/…), не читается (SEC-017).
	dir, err := os.OpenRoot(root)
	if err != nil {
		return loadedImage{}, fmt.Errorf("open storage: %w", err)
	}
	defer dir.Close()
	f, err := dir.Open(strings.TrimPrefix(clean, "/"))
	if errors.Is(err, fs.ErrNotExist) {
		return loadedImage{}, errImage{"файл не найден на диске старого сервера — картинка не перенесена"}
	}
	if err != nil {
		if strings.Contains(err.Error(), "path escapes from parent") {
			return loadedImage{}, errImage{"путь вне каталога storage — не переносится"}
		}
		return loadedImage{}, fmt.Errorf("open %s: %w", filepath.Base(rel), err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, int64(maxBytes)+1))
	if err != nil {
		return loadedImage{}, fmt.Errorf("read %s: %w", filepath.Base(rel), err)
	}
	if len(data) == 0 {
		return loadedImage{}, errImage{"пустой файл — картинка не перенесена"}
	}
	if len(data) > maxBytes {
		return loadedImage{}, errImage{fmt.Sprintf("файл больше %d КБ — картинка не перенесена", maxBytes/1024)}
	}
	if webp && isWebP(data) {
		return loadedImage{Data: data, ContentType: "image/webp"}, nil
	}
	_, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || (format != "png" && format != "jpeg") {
		if webp {
			return loadedImage{}, errImage{"файл не PNG/JPEG/WebP — картинка не перенесена"}
		}
		return loadedImage{}, errImage{"файл не PNG/JPEG — картинка не перенесена"}
	}
	ct := "image/jpeg"
	if format == "png" {
		ct = "image/png"
	}
	return loadedImage{Data: data, ContentType: ct}, nil
}

// isWebP — контейнер WebP: «RIFF» + размер + «WEBP» и блок VP8/VP8L/VP8X.
func isWebP(data []byte) bool {
	return len(data) >= 16 && string(data[0:4]) == "RIFF" && string(data[8:12]) == "WEBP" && string(data[12:15]) == "VP8"
}

// transferImages — запись (apply), проверка (plan) или сверка (verify) картинок.
func transferImages(ctx context.Context, rep *Report, pg PGQuerier, root string, list []imageFile, mode extMode) error {
	if len(list) == 0 {
		return nil
	}
	if root == "" {
		rep.add(Warning, CodeFile, "", "", "", "", fmt.Sprintf(
			"не задан --laravel-storage: картинки (%d) не переносятся и не сверяются", len(list)))
		return nil
	}
	moved := 0
	for _, f := range list {
		img, err := loadImage(root, f.Path, f.Max, f.WebP)
		var bad errImage
		if errors.As(err, &bad) {
			if mode != modeVerify {
				rep.add(Warning, CodeFile, f.Table, f.Column, f.Path, f.RowID, bad.reason)
			}
		} else if err != nil {
			return err
		}
		switch mode {
		case modeApply:
			if err != nil {
				continue
			}
			switch {
			case f.Kind != "":
				_, err = pg.Exec(ctx, `INSERT INTO club_images (club_id, kind, content_type, data, created_at)
					VALUES ($1, $2, $3, $4, now())`, *f.ClubID, f.Kind, img.ContentType, img.Data)
			case f.PhotoID != 0:
				_, err = pg.Exec(ctx, `INSERT INTO club_photo_images (photo_id, club_id, content_type, data, created_at)
					VALUES ($1, $2, $3, $4, now())`, f.PhotoID, *f.ClubID, img.ContentType, img.Data)
			default:
				_, err = pg.Exec(ctx, `INSERT INTO news_images (news_id, club_id, content_type, data, created_at)
					VALUES ($1, $2, $3, $4, now()) ON CONFLICT (news_id) DO NOTHING`, f.NewsID, f.ClubID, img.ContentType, img.Data)
			}
			if err != nil {
				return fmt.Errorf("pg: image %s %s: %w", f.Table, f.RowID, err)
			}
			moved++
		case modeVerify:
			var got []byte
			var qerr error
			switch {
			case f.Kind != "":
				qerr = pg.QueryRow(ctx, "SELECT data FROM club_images WHERE club_id = $1 AND kind = $2", *f.ClubID, f.Kind).Scan(&got)
			case f.PhotoID != 0:
				qerr = pg.QueryRow(ctx, "SELECT data FROM club_photo_images WHERE club_id = $1 AND photo_id = $2", *f.ClubID, f.PhotoID).Scan(&got)
			default:
				qerr = pg.QueryRow(ctx, "SELECT data FROM news_images WHERE news_id = $1", f.NewsID).Scan(&got)
			}
			found := qerr == nil
			if qerr != nil && !isNoRows(qerr) {
				return fmt.Errorf("pg: image %s %s: %w", f.Table, f.RowID, qerr)
			}
			switch {
			case err != nil && found:
				rep.add(Blocker, CodeVerifyMismatch, f.Table, f.Column, "", f.RowID, "картинка в Postgres есть, файла на диске нет")
			case err == nil && !found:
				rep.add(Blocker, CodeVerifyMismatch, f.Table, f.Column, "", f.RowID, "картинка не перенесена")
			case err == nil && sha256.Sum256(got) != sha256.Sum256(img.Data):
				rep.add(Blocker, CodeVerifyMismatch, f.Table, f.Column, "", f.RowID, "картинка в Postgres отличается от файла")
			case err == nil:
				moved++
			}
		default:
			if err == nil {
				moved++
			}
		}
	}
	rep.Totals["картинки (логотип, вход, новости, фото клуба)"] = fmt.Sprintf("%d из %d", moved, len(list))
	return nil
}
