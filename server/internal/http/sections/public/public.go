// Package public — публичные страницы без входа (api/public.yaml и не-JSON адреса). Laravel: LandingController,
// PwaController (manifest, offline), маршруты api-docs.spec (routes/web.php:119–129).
//
//   - GET /api/landing — данные лендинга (страница — SPA, web/src/features/public);
//   - GET /offline — страница «Нет подключения» (HTML с текстами на всех включённых языках; её кэширует service worker);
//   - GET /manifest.webmanifest — манифест PWA (решение Q-068);
//   - GET /docs/api/openapi.yaml — спецификация Integration API v1 (страница Swagger UI — SPA /docs/api).
package public

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"html/template"
	"io/fs"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	publicapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/public"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
	"github.com/aitmaganbetov/zafirogo/server/internal/webui"
)

var access = httpx.Access{Public: []string{"GetLanding"}}

// Константы старого кода.
const (
	// AgentDownload — LandingController::AGENT_DOWNLOAD: последний установщик, если суперадмин не задал ссылку.
	AgentDownload = "https://github.com/aitmaganbetov/Zafiro/releases/latest/download/ZafiroAgentSetup.exe"
	// defaultTrialDays — `max('trial_days') ?: 14`.
	defaultTrialDays = 14
	// ThemeColor — PwaController::THEME_COLOR.
	ThemeColor = "#101713"
	// appName — config('app.name') (в старом 'Laravel' заменялось на 'Zafiro').
	appName = "Zafiro"
	// defaultGuestLocale — LocaleResolver::DEFAULT_GUEST_LOCALE; fallbackLocale — DEFAULT_CLUB_LOCALE.
	defaultGuestLocale = "en"
	fallbackLocale     = "ru"
)

// openAPIv1 — resources/api/openapi-v1.yaml старого проекта (Integration API v1 переносится на Этапе 4; справка
// показывает его контракт как есть — docs/open-questions/stage3-public.md).
//
//go:embed openapi-v1.yaml
var openAPIv1 []byte

type handlers struct{ d *httpx.Deps }

var _ publicapi.StrictServerInterface = (*handlers)(nil)

// Section — публичные страницы.
func Section() httpx.Section { return httpx.Section{Name: "public", Mount: mount} }

func mount(r chi.Router, d *httpx.Deps) {
	h := &handlers{d: d}
	si := publicapi.NewStrictHandlerWithOptions(h,
		httpx.Middlewares[publicapi.StrictMiddlewareFunc](access),
		publicapi.StrictHTTPServerOptions{RequestErrorHandlerFunc: d.RequestError, ResponseErrorHandlerFunc: d.ResponseError()})
	publicapi.HandlerWithOptions(si, publicapi.ChiServerOptions{BaseRouter: r, ErrorHandlerFunc: d.ParamError})
	r.Get("/offline", h.offline)
	r.Get("/manifest.webmanifest", h.manifest)
	r.Get("/docs/api/openapi.yaml", openAPISpec)
	r.Get("/docs/api", apiDocsPage(webui.FS()))
}

// apiDocsPage — Route::view('/docs/api', 'api-docs'): страница Swagger UI — отдельная страница сборки
// (web/docs/api/index.html, без стилей SPA). Нет сборки — 404.
func apiDocsPage(fsys fs.FS) http.HandlerFunc {
	page, err := fs.ReadFile(fsys, "docs/api/index.html")
	return func(w http.ResponseWriter, r *http.Request) {
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(page)
	}
}

// GetLanding — LandingController::__invoke().
func (h *handlers) GetLanding(ctx context.Context, _ publicapi.GetLandingRequestObject) (publicapi.GetLandingResponseObject, error) {
	out := publicapi.Landing{TrialDays: defaultTrialDays, DownloadUrl: AgentDownload}
	if h.d.PlatformPool == nil {
		return publicapi.GetLanding200JSONResponse(out), nil
	}
	var row store.PublicLandingRow
	if err := db.InPlatformTx(ctx, h.d.PlatformPool, func(q *store.Queries) error {
		var err error
		row, err = q.PublicLanding(ctx)
		return err
	}); err != nil {
		return nil, err
	}
	if row.TrialDays > 0 {
		out.TrialDays = int64(row.TrialDays)
	}
	if u := downloadURL(row.Release); u != "" {
		out.DownloadUrl = u
	}
	return publicapi.GetLanding200JSONResponse(out), nil
}

// downloadURL — AgentUpdate::downloadUrl(): release()['download_url'], только https:// и корректный URL.
func downloadURL(release json.RawMessage) string {
	var rel struct {
		DownloadURL any `json:"download_url"`
	}
	if len(release) == 0 || json.Unmarshal(release, &rel) != nil {
		return ""
	}
	s, ok := rel.DownloadURL.(string)
	if !ok || !strings.HasPrefix(s, "https://") {
		return ""
	}
	if u, err := url.Parse(s); err != nil || u.Host == "" {
		return ""
	}
	return s
}

// openAPISpec — api-docs.spec: response()->file(resource_path('api/openapi-v1.yaml'), application/yaml).
func openAPISpec(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	http.ServeContent(w, r, "openapi.yaml", time.Time{}, bytes.NewReader(openAPIv1))
}

// enabledCodes — включённые языки (TranslationCatalog::enabledCodes()); без каталога — ru.
func (h *handlers) enabledCodes(ctx context.Context) []string {
	if h.d.I18n == nil {
		return []string{fallbackLocale}
	}
	langs, _, err := h.d.I18n.Enabled(ctx)
	if err != nil || len(langs) == 0 {
		if err != nil {
			h.d.Log().WarnContext(ctx, "public: enabled languages", "err", err)
		}
		return []string{fallbackLocale}
	}
	out := make([]string, len(langs))
	for i, l := range langs {
		out[i] = l.Code
	}
	return out
}

func (h *handlers) t(ctx context.Context, locale, key string) string {
	if h.d.I18n == nil {
		return key
	}
	return h.d.I18n.Translate(ctx, locale, key, nil)
}

// GuestLocale — LocaleResolver::forGuest(): языки Accept-Language по убыванию q (Request::getLanguages()), сам код
// или его основа среди включённых; иначе en (если включён), иначе ru или первый включённый.
func GuestLocale(acceptLanguage string, enabled []string) string {
	has := func(c string) bool {
		for _, e := range enabled {
			if e == c {
				return true
			}
		}
		return false
	}
	for _, p := range preferredLanguages(acceptLanguage) {
		p = strings.ToLower(strings.ReplaceAll(p, "_", "-"))
		base, _, _ := strings.Cut(p, "-")
		for _, c := range []string{p, base} {
			if has(c) {
				return c
			}
		}
	}
	switch {
	case has(defaultGuestLocale):
		return defaultGuestLocale
	case has(fallbackLocale) || len(enabled) == 0:
		return fallbackLocale
	}
	return enabled[0]
}

// preferredLanguages — языки заголовка по убыванию q (порядок при равном q — как в заголовке; q=0 — не берём).
func preferredLanguages(header string) []string {
	type lang struct {
		tag string
		q   float64
		i   int
	}
	var list []lang
	for i, part := range strings.Split(header, ",") {
		tag, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		tag = strings.TrimSpace(tag)
		if tag == "" || tag == "*" {
			continue
		}
		q := 1.0
		if v, ok := strings.CutPrefix(strings.TrimSpace(params), "q="); ok {
			if f, err := strconv.ParseFloat(v, 64); err == nil {
				q = f
			}
		}
		if q <= 0 {
			continue
		}
		list = append(list, lang{tag: tag, q: q, i: i})
	}
	sort.SliceStable(list, func(a, b int) bool { return list[a].q > list[b].q })
	out := make([]string, len(list))
	for i, l := range list {
		out[i] = l.tag
	}
	return out
}

// Тексты страницы offline (PwaController::offline()).
const (
	keyTitle   = "Нет подключения"
	keyHeading = "Нет подключения к интернету"
	keyText    = "Zafiro работает с актуальными данными клуба, поэтому без сети страница недоступна. Проверьте подключение и повторите."
	keyRetry   = "Повторить"
)

// offlineScriptTemplate — inline-скрипт offline.blade.php: язык из localStorage['zafiro.locale'] (язык клуба, в котором
// работали последним), иначе navigator.languages. %s — JSON текстов.
const offlineScriptTemplate = `
        (() => {
            const texts = %s;
            let code = null;
            try { code = localStorage.getItem('zafiro.locale'); } catch (e) {}
            code = texts[code] ? code : (navigator.languages || []).map((l) => l.toLowerCase().split('-')[0]).find((l) => texts[l]);
            if (!code) return;
            document.documentElement.lang = code;
            document.addEventListener('DOMContentLoaded', () => {
                document.title = texts[code].title + ' · Zafiro';
                document.querySelectorAll('[data-text]').forEach((el) => { el.textContent = texts[code][el.dataset.text]; });
            });
        })();
    `

// offlineReload — `onclick="location.reload()"` и `addEventListener('online', …)` старой страницы: inline-обработчик
// атрибутом CSP без 'unsafe-inline' не пропустит, поэтому оба — в одном скрипте в конце страницы.
const offlineReload = `document.querySelector('[data-text="retry"]').addEventListener('click', () => location.reload());addEventListener('online', () => location.reload());`

// offlinePage — resources/views/offline.blade.php (разметка и стили без изменений).
var offlinePage = template.Must(template.New("offline").Parse(`<!DOCTYPE html>
<html lang="{{.Lang}}">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <meta name="theme-color" content="{{.ThemeColor}}">
    <title>{{.Title}} · Zafiro</title>
    <script>{{.Script}}</script>
    <style>
        :root { color-scheme: light dark; }
        body { margin: 0; min-height: 100vh; min-height: 100dvh; display: flex; align-items: center; justify-content: center; padding: 24px; box-sizing: border-box;
            font-family: Inter, system-ui, -apple-system, "Segoe UI", Roboto, sans-serif; background: #101713; color: #e5ece7; text-align: center; }
        main { max-width: 360px; }
        img { width: 96px; height: 96px; border-radius: 22px; }
        h1 { margin: 20px 0 8px; font-size: 24px; line-height: 1.3; }
        p { margin: 0 0 24px; color: #a7b5ab; font-size: 16px; line-height: 1.5; }
        button { border: 0; border-radius: 12px; padding: 12px 24px; background: #d90058; color: #fff; font: inherit; font-weight: 600; cursor: pointer; }
        button:focus-visible { outline: 3px solid #fff; outline-offset: 2px; }
    </style>
</head>
<body>
<main>
    <img src="/icons/icon-192.png" alt="">
    <h1 data-text="heading">{{.Heading}}</h1>
    <p data-text="text">{{.Text}}</p>
    <button type="button" data-text="retry">{{.Retry}}</button>
</main>
<script>{{.Reload}}</script>
</body>
</html>
`))

func scriptHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
}

// offline — PwaController::offline(): тексты на всех включённых языках встроены (страница кэшируется один раз и
// показывается позже на языке клуба), Cache-Control: no-cache. Язык разметки — язык гостя.
func (h *handlers) offline(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	enabled := h.enabledCodes(ctx)
	type texts struct {
		Title   string `json:"title"`
		Heading string `json:"heading"`
		Text    string `json:"text"`
		Retry   string `json:"retry"`
	}
	all := make(map[string]texts, len(enabled))
	for _, code := range enabled {
		all[code] = texts{h.t(ctx, code, keyTitle), h.t(ctx, code, keyHeading), h.t(ctx, code, keyText), h.t(ctx, code, keyRetry)}
	}
	// json.Marshal экранирует <, > и & — безопасно внутри <script> (как @json Blade).
	raw, err := json.Marshal(all)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	script := strings.Replace(offlineScriptTemplate, "%s", string(raw), 1)
	lang := GuestLocale(r.Header.Get("Accept-Language"), enabled)
	cur := texts{h.t(ctx, lang, keyTitle), h.t(ctx, lang, keyHeading), h.t(ctx, lang, keyText), h.t(ctx, lang, keyRetry)}
	var buf bytes.Buffer
	if err := offlinePage.Execute(&buf, map[string]any{
		"Lang": lang, "ThemeColor": ThemeColor, "Title": cur.Title, "Heading": cur.Heading, "Text": cur.Text, "Retry": cur.Retry,
		// Скрипты — свои строки (не данные запроса): template.JS вставляет их как есть, CSP пропускает их по sha256.
		"Script": template.JS(script), "Reload": template.JS(offlineReload), // #nosec G203 -- текст скрипта собран здесь, данные — json.Marshal
	}); err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	hd := w.Header()
	hd.Set("Content-Type", "text/html; charset=utf-8")
	hd.Set("Cache-Control", "no-cache")
	hd.Set("Content-Security-Policy", "default-src 'self'; script-src "+scriptHash(script)+" "+scriptHash(offlineReload)+
		"; style-src 'unsafe-inline'; img-src 'self'; object-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	_, _ = w.Write(buf.Bytes())
}

// manifest — PwaController::manifest(): язык гостя, ярлыки Касса / Склад / Список ПК.
func (h *handlers) manifest(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	lang := GuestLocale(r.Header.Get("Accept-Language"), h.enabledCodes(ctx))
	type icon struct {
		Src     string `json:"src"`
		Sizes   string `json:"sizes"`
		Type    string `json:"type"`
		Purpose string `json:"purpose"`
	}
	type shortcut struct {
		Name  string `json:"name"`
		URL   string `json:"url"`
		Icons []icon `json:"icons"`
	}
	mk := func(file string, size int, purpose string) icon {
		s := strconv.Itoa(size)
		return icon{Src: "/icons/" + file, Sizes: s + "x" + s, Type: "image/png", Purpose: purpose}
	}
	sc := func(name, u string) shortcut {
		return shortcut{Name: h.t(ctx, lang, name), URL: u, Icons: []icon{mk("icon-192.png", 192, "any")}}
	}
	body := map[string]any{
		"id": "/", "name": appName, "short_name": "Zafiro",
		"description": h.t(ctx, lang, "Система управления клубом"),
		"lang":        lang, "start_url": "/dashboard", "scope": "/", "display": "standalone",
		"background_color": ThemeColor, "theme_color": ThemeColor,
		"icons": []icon{mk("icon-192.png", 192, "any"), mk("icon-512.png", 512, "any"), mk("icon-maskable-512.png", 512, "maskable")},
		"shortcuts": []shortcut{
			sc("Касса", "/cash-register"), sc("Склад", "/warehouse"), sc("Список ПК", "/club-computers"),
		},
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false) // JSON_UNESCAPED_UNICODE | JSON_UNESCAPED_SLASHES
	if err := enc.Encode(body); err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/manifest+json")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write(bytes.TrimRight(buf.Bytes(), "\n"))
}
