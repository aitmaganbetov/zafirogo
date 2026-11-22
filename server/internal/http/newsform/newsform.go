// Package newsform — форма новости (NewsManager::validate(), storeImage()): общая для новостей клуба (раздел news)
// и новостей платформы (раздел superadmin). Laravel: app/Services/NewsManager.php, app/Models/News.php.
package newsform

import (
	"bytes"
	"errors"
	"image"
	_ "image/jpeg" // image.DecodeConfig: JPEG
	_ "image/png"  // image.DecodeConfig: PNG
	"io"
	"mime/multipart"
	"net/http"
	"path"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
)

const (
	// maxImage — max:2048 (килобайт).
	maxImage = 2048 * 1024
	// maxField — предел текстового поля формы при чтении (body max:5000 символов — до 20 000 байт UTF-8).
	maxField = 64 * 1024
)

// Form — поля NewsManager::validate().
type Form struct {
	Title, Body string
	// Published — is_published; HasPublished — поле было в форме (создание: ?? true, изменение: ?? false).
	Published, HasPublished bool
	RemoveImage             bool
	// Image — байты картинки; ImageType — её Content-Type после Validate.
	Image     []byte
	ImageType string
	imageExt  string
}

// Read — multipart: title, body, is_published, remove_image, image. Повтор поля (hidden 0 + checkbox 1) —
// последнее значение, как разбор формы PHP.
func Read(mr *multipart.Reader) (Form, error) {
	var f Form
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
			f.Image = data
			continue
		}
		data, err := io.ReadAll(io.LimitReader(part, maxField))
		if err != nil {
			return f, httpx.NewAPIError(http.StatusBadRequest, "bad_request", "")
		}
		v := string(data)
		switch name {
		case "title":
			f.Title = strings.TrimSpace(v)
		case "body":
			f.Body = strings.TrimSpace(v)
		case "is_published":
			f.Published, f.HasPublished = boolField(v), true
		case "remove_image":
			f.RemoveImage = boolField(v)
		}
	}
	return f, nil
}

// boolField — правило boolean Laravel: true/false, 1/0, "1"/"0" (иное — false).
func boolField(v string) bool { return v == "1" || v == "true" }

// Validate — title required|max:150, body required|max:5000, image png/jpeg ≤ 2 МБ, 64…4096 пикселей.
func (f *Form) Validate() error {
	var fe httpx.FormErrors
	switch n := utf8.RuneCountInString(f.Title); {
	case n == 0:
		fe.Rule("title", "required", "")
	case n > 150:
		fe.Rule("title", "max_string", "150")
	}
	switch n := utf8.RuneCountInString(f.Body); {
	case n == 0:
		fe.Rule("body", "required", "")
	case n > 5000:
		fe.Rule("body", "max_string", "5000")
	}
	if f.Image != nil {
		cfg, format, err := image.DecodeConfig(bytes.NewReader(f.Image))
		switch {
		case err != nil || (format != "png" && format != "jpeg"):
			fe.Rule("image", "mimes", "")
		case len(f.Image) > maxImage:
			fe.Rule("image", "max_file", "")
		case cfg.Width < 64 || cfg.Height < 64 || cfg.Width > 4096 || cfg.Height > 4096:
			fe.Rule("image", "dimensions", "")
		default:
			// storeImage(): guessExtension() === 'png' ? 'png' : 'jpg'.
			f.ImageType, f.imageExt = "image/jpeg", "jpg"
			if format == "png" {
				f.ImageType, f.imageExt = "image/png", "png"
			}
		}
	}
	return fe.Err()
}

// ImagePath — storeImage(): путь картинки <dir>/<40 символов>.<png|jpg> (dir — news/<club> или news/platform).
func (f *Form) ImagePath(dir string) (string, error) {
	name, err := httpx.RandomName()
	if err != nil {
		return "", err
	}
	return dir + "/" + name + "." + f.imageExt, nil
}

// ImageURL — News::imageUrl(): /news-images/{id}/{basename(image_path)}.
func ImageURL(newsID int64, imagePath string) string {
	return "/news-images/" + strconv.FormatInt(newsID, 10) + "/" + path.Base(imagePath)
}
