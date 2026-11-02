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
// signin_image_path → club_images), картинки новостей клуба (news.image_path → news_images). В старом коде — файлы
// диска 'local' Laravel: config/filesystems.php root = storage_path('app/private') (ClubLogoController::update(),
// ClubSignInImageController::update(), NewsManager::storeImage() — storeAs(…, 'local')). Пути в clubs/news
// переносятся как есть (по ним сервер сверяет имя файла в URL).
//
// Проверки — как у сервера при загрузке (http/sections/settings/images.go readUpload(), 0018/0019): PNG или JPEG
// (image.DecodeConfig), размер ≤ 1 МБ (логотип), 3 МБ (вход), 2 МБ (новость). Нет файла, не картинка, больше
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
	// Ключ строки в Postgres: club_images (club, kind) или news_images (news id).
	Kind   string
	NewsID int64
	ClubID *int64
}

const (
	maxLogoBytes   = 1024 * 1024 // ClubLogoController: max:1024
	maxSigninBytes = 3072 * 1024 // ClubSignInImageController: max:3072
	maxNewsBytes   = 2048 * 1024 // NewsManager: max:2048
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
func loadImage(root, rel string, maxBytes int) (loadedImage, error) {
	clean := path.Clean("/" + strings.ReplaceAll(rel, `\`, "/"))
	if clean != "/"+rel || strings.Contains(rel, "..") {
		return loadedImage{}, errImage{"путь вне каталога storage — не переносится"}
	}
	fsys := os.DirFS(root)
	f, err := fsys.Open(strings.TrimPrefix(clean, "/"))
	if errors.Is(err, fs.ErrNotExist) {
		return loadedImage{}, errImage{"файл не найден на диске старого сервера — картинка не перенесена"}
	}
	if err != nil {
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
	_, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || (format != "png" && format != "jpeg") {
		return loadedImage{}, errImage{"файл не PNG/JPEG — картинка не перенесена"}
	}
	ct := "image/jpeg"
	if format == "png" {
		ct = "image/png"
	}
	return loadedImage{Data: data, ContentType: ct}, nil
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
		img, err := loadImage(root, f.Path, f.Max)
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
			if f.Kind != "" {
				_, err = pg.Exec(ctx, `INSERT INTO club_images (club_id, kind, content_type, data, created_at)
					VALUES ($1, $2, $3, $4, now())`, *f.ClubID, f.Kind, img.ContentType, img.Data)
			} else {
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
			if f.Kind != "" {
				qerr = pg.QueryRow(ctx, "SELECT data FROM club_images WHERE club_id = $1 AND kind = $2", *f.ClubID, f.Kind).Scan(&got)
			} else {
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
	rep.Totals["картинки (логотип, вход, новости)"] = fmt.Sprintf("%d из %d", moved, len(list))
	return nil
}
