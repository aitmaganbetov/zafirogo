// Package i18n — переводы интерфейса (api/i18n.yaml): языки и пакеты i18next. Laravel: TranslationCatalog.
package i18n

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	i18napi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/i18n"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
	"github.com/aitmaganbetov/zafirogo/server/internal/i18n"
)

// Переводы публичные: страницы входа и регистрации тоже переводятся.
var access = httpx.Access{Public: []string{"ListLanguages", "GetTranslations"}}

type handlers struct {
	d       *httpx.Deps
	catalog *i18n.Catalog
}

var _ i18napi.StrictServerInterface = (*handlers)(nil)

// Section — переводы интерфейса.
func Section() httpx.Section {
	return httpx.Section{Name: "i18n", Mount: mount}
}

func mount(r chi.Router, d *httpx.Deps) {
	h := &handlers{d: d}
	if d.Pool != nil {
		h.catalog = i18n.NewCatalog(d.Pool)
	}
	si := i18napi.NewStrictHandlerWithOptions(h,
		httpx.Middlewares[i18napi.StrictMiddlewareFunc](access),
		i18napi.StrictHTTPServerOptions{RequestErrorHandlerFunc: d.RequestError, ResponseErrorHandlerFunc: d.ResponseError()})
	i18napi.HandlerWithOptions(si, i18napi.ChiServerOptions{BaseRouter: r, ErrorHandlerFunc: d.ParamError})
}

// ListLanguages — включённые языки и версия переводов.
func (h *handlers) ListLanguages(ctx context.Context, _ i18napi.ListLanguagesRequestObject) (i18napi.ListLanguagesResponseObject, error) {
	if h.catalog == nil {
		return nil, httpx.ErrNotConfigured
	}
	langs, version, err := h.catalog.Enabled(ctx)
	if err != nil {
		return nil, err
	}
	out := i18napi.LanguageList{Languages: make([]i18napi.Language, 0, len(langs)), Version: version}
	for _, l := range langs {
		out.Languages = append(out.Languages, i18napi.Language{Code: l.Code, Name: l.Name})
	}
	return i18napi.ListLanguages200JSONResponse(out), nil
}

// GetTranslations — пакет языка; тело уже сериализовано каталогом (не перекодируется на каждый запрос).
func (h *handlers) GetTranslations(ctx context.Context, req i18napi.GetTranslationsRequestObject) (i18napi.GetTranslationsResponseObject, error) {
	if h.catalog == nil {
		return nil, httpx.ErrNotConfigured
	}
	pack, err := h.catalog.Pack(ctx, req.Locale)
	if errors.Is(err, i18n.ErrUnknownLocale) {
		return nil, httpx.NotFound()
	}
	if err != nil {
		return nil, err
	}
	if req.Params.IfNoneMatch != nil && etagMatch(*req.Params.IfNoneMatch, pack.ETag) {
		return notModified{etag: pack.ETag}, nil
	}
	return packResponse{pack: pack}, nil
}

// etagMatch — If-None-Match: список тегов через запятую или «*» (слабые W/ сравниваются по значению).
func etagMatch(header, etag string) bool {
	for _, t := range strings.Split(header, ",") {
		t = strings.TrimPrefix(strings.TrimSpace(t), "W/")
		if t == "*" || t == etag {
			return true
		}
	}
	return false
}

type packResponse struct{ pack i18n.Pack }

func (p packResponse) VisitGetTranslationsResponse(w http.ResponseWriter) error {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("ETag", p.pack.ETag)
	// Переводы меняются в суперадминке: браузер всегда перепроверяет (304 без тела).
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	_, err := w.Write(p.pack.Body)
	return err
}

type notModified struct{ etag string }

func (n notModified) VisitGetTranslationsResponse(w http.ResponseWriter) error {
	w.Header().Set("ETag", n.etag)
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusNotModified)
	return nil
}
