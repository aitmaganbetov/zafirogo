package superadmin

// Форма новости — копия sections/news (NewsManager::validate(), storeImage()): разделы не импортируют друг друга.
// Новости платформы: club_id NULL, картинка — news/platform/… (как в старом storeImage()).

import (
	"bytes"
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
	"strings"
	"unicode/utf8"

	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
)

const (
	// maxImage — max:2048 (килобайт).
	maxImage = 2048 * 1024
	// maxField — предел текстового поля формы при чтении.
	maxField = 64 * 1024
)

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

// storeImage — картинка новости платформы: путь news/platform/<40 символов>.<png|jpg> и байты в news_images.
func (f *form) imagePath() (string, error) {
	name, err := randomName()
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("news/platform/%s.%s", name, f.imageExt), nil
}
