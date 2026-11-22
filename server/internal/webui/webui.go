// Package webui отдаёт собранный фронтенд (web/dist), вшитый в бинарник через embed.FS.
//
// Сборка (task build) копирует web/dist → server/internal/webui/dist. В git лежит только
// dist/.gitkeep, поэтому `go build` работает и без фронта: тогда на / отдаётся
// служебная страница-заглушка.
//
// Правила отдачи:
//   - существующий файл отдаётся как есть; /assets/* (имена с хешем от Vite) —
//     Cache-Control: public, max-age=31536000, immutable; index.html и sw.js — no-cache (перепроверка по ETag);
//   - текстовые файлы (JS, CSS, HTML, SVG, JSON …) от minCompress байт клиенту с Accept-Encoding: gzip отдаются
//     сжатыми: файл сжимается один раз при первом запросе и хранится в памяти процесса (PB-11); ответ несёт
//     Vary: Accept-Encoding и свой ETag;
//   - ETag — хеш содержимого (одинаков у всех инстансов и между перезапусками, PB-23), Last-Modified нет;
//   - /api/* сюда не доходит (роутер отвечает JSON 404), но на всякий случай — 404;
//   - путь с расширением, которого нет, — 404 (не прячем битые ссылки на ассеты);
//   - всё остальное — index.html (SPA fallback для клиентского роутера); для страниц публичного сайта — с
//     заголовком, описанием и Open Graph страницы (WithSiteMeta, site.go, решение L-1).
package webui

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed all:dist
var embedded embed.FS

// FS возвращает вшитую файловую систему с корнем в dist.
func FS() fs.FS {
	sub, err := fs.Sub(embedded, "dist")
	if err != nil {
		// fs.Sub падает только на некорректном имени каталога — это ошибка программиста.
		panic(err)
	}
	return sub
}

// placeholder отдаётся, если фронт не собран (в dist нет index.html).
const placeholder = `<!doctype html>
<html lang="ru"><head><meta charset="utf-8"><title>Zafiro</title></head>
<body><h1>Zafiro</h1><p>Фронтенд не собран. Выполните <code>task build</code>.</p></body></html>
`

// Option — настройка отдачи SPA.
type Option func(*options)

type options struct {
	cabinetDomain, resellerDomain string
	publicURL                     string
	site                          bool
	siteTexts                     SiteTexts
}

// WithSuperadminDomain — на хосте кабинета суперадмина (SUPERADMIN_DOMAIN, Q-069) index.html отдаётся с атрибутом
// <html data-superadmin-host>: SPA показывает кабинет с корня хоста (как Route::domain() старого кода).
func WithSuperadminDomain(domain string) Option { return func(o *options) { o.cabinetDomain = domain } }

// WithResellerDomain — на хосте кабинета реселлера (RESELLER_DOMAIN, UQ-21) index.html отдаётся с атрибутом
// <html data-reseller-host>: SPA показывает кабинет реселлера с корня хоста и не показывает регистрацию.
func WithResellerDomain(domain string) Option { return func(o *options) { o.resellerDomain = domain } }

// minCompress — файлы меньше этого не сжимаются (выигрыш меньше заголовков и работы).
const minCompress = 1024

// compressible — расширения текстовых файлов сборки; картинки, шрифты woff2 и архивы уже сжаты.
var compressible = map[string]bool{
	".js": true, ".mjs": true, ".css": true, ".html": true, ".svg": true, ".json": true, ".map": true,
	".txt": true, ".xml": true, ".webmanifest": true, ".wasm": true, ".md": true,
}

// variant — готовый к отдаче файл: ETag исходного содержимого и (если есть смысл) сжатая копия.
type variant struct {
	etag   string
	gz     []byte
	gzETag string
}

func newVariant(name string, data []byte) *variant {
	sum := sha256.Sum256(data)
	tag := base64.RawURLEncoding.EncodeToString(sum[:12])
	v := &variant{etag: `"` + tag + `"`}
	if len(data) < minCompress || !compressible[strings.ToLower(path.Ext(name))] {
		return v
	}
	var buf bytes.Buffer
	zw, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return v
	}
	if _, err := zw.Write(data); err != nil || zw.Close() != nil {
		return v
	}
	if buf.Len() < len(data)-len(data)/10 { // выигрыш меньше 10 % — не стоит распаковки на клиенте
		v.gz, v.gzETag = buf.Bytes(), `"`+tag+`-gz"`
	}
	return v
}

// acceptsGzip — клиент принимает gzip (Accept-Encoding: gzip без q=0).
func acceptsGzip(r *http.Request) bool {
	for _, part := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		enc, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		if !strings.EqualFold(strings.TrimSpace(enc), "gzip") {
			continue
		}
		q := strings.TrimSpace(params)
		if v, ok := strings.CutPrefix(q, "q="); ok {
			if f, err := strconv.ParseFloat(v, 64); err == nil && f == 0 {
				return false
			}
		}
		return true
	}
	return false
}

// serveGzip отдаёт сжатую копию целиком, с Content-Length (http.ServeContent для ответа с Content-Encoding длину не
// ставит — ответ шёл бы кусками) и условным запросом по ETag сжатого варианта. Range не поддерживается (не объявлен):
// файлы сборки — сотни килобайт.
func serveGzip(w http.ResponseWriter, r *http.Request, name string, v *variant) {
	h := w.Header()
	if h.Get("Content-Type") == "" {
		ct := mime.TypeByExtension(path.Ext(name))
		if ct == "" {
			ct = "application/octet-stream"
		}
		h.Set("Content-Type", ct)
	}
	h.Set("Content-Encoding", "gzip")
	h.Set("ETag", v.gzETag)
	if etagMatch(r.Header.Get("If-None-Match"), v.gzETag) {
		h.Del("Content-Type")
		w.WriteHeader(http.StatusNotModified)
		return
	}
	h.Set("Content-Length", strconv.Itoa(len(v.gz)))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(v.gz)
	}
}

// etagMatch — If-None-Match: список тегов через запятую или «*» (слабое сравнение: W/ не учитывается).
func etagMatch(header, etag string) bool {
	if header == "" {
		return false
	}
	for _, t := range strings.Split(header, ",") {
		t = strings.TrimPrefix(strings.TrimSpace(t), "W/")
		if t == "*" || t == etag {
			return true
		}
	}
	return false
}

// Handler возвращает http.Handler, отдающий SPA из fsys.
func Handler(fsys fs.FS, opts ...Option) http.Handler {
	var o options
	for _, fn := range opts {
		fn(&o)
	}
	index, err := fs.ReadFile(fsys, "index.html")
	if err != nil {
		index = []byte(placeholder)
	}
	cabinetIndex := bytes.Replace(index, []byte("<html"), []byte("<html data-superadmin-host"), 1)
	indexVariant, cabinetVariant := newVariant("index.html", index), newVariant("index.html", cabinetIndex)
	resellerIndex := bytes.Replace(index, []byte("<html"), []byte("<html data-reseller-host"), 1)
	resellerVariant := newVariant("index.html", resellerIndex)
	fileServer := http.FileServerFS(fsys)
	var site *siteIndex
	if o.site {
		site = newSiteIndex(index, o.siteTexts, o.publicURL)
	}

	// variants — файлы сборки по имени: набор конечен (вшитая ФС), сжатие — один раз на файл.
	var (
		mu       sync.RWMutex
		variants = map[string]*variant{}
	)
	variantOf := func(name string) *variant {
		mu.RLock()
		v, ok := variants[name]
		mu.RUnlock()
		if ok {
			return v
		}
		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil
		}
		v = newVariant(name, data) // вне блокировки: сжатие большого файла — десятки миллисекунд
		mu.Lock()
		defer mu.Unlock()
		if prev, ok := variants[name]; ok {
			return prev
		}
		variants[name] = v
		return v
	}

	// serveIndex — index.html для пути p ("" — общий, без заголовков сайта).
	serveIndex := func(w http.ResponseWriter, r *http.Request, p string) {
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Add("Vary", "Accept-Encoding")
		body, v := index, indexVariant
		siteHost := true // на хостах кабинетов суперадмина и реселлера сайт не показывается (L-1)
		if o.cabinetDomain != "" || o.resellerDomain != "" {
			w.Header().Add("Vary", "Host")
			switch {
			case o.cabinetDomain != "" && sameHost(r.Host, o.cabinetDomain):
				body, v, siteHost = cabinetIndex, cabinetVariant, false
			case o.resellerDomain != "" && sameHost(r.Host, o.resellerDomain):
				body, v, siteHost = resellerIndex, resellerVariant, false
			}
		}
		if site != nil && siteHost {
			if b, sv, ok := site.page(r, p); ok {
				// Заголовки страницы зависят от языка гостя и адреса сайта (og:image).
				w.Header().Add("Vary", "Accept-Language")
				if o.cabinetDomain == "" && o.resellerDomain == "" {
					w.Header().Add("Vary", "Host")
				}
				body, v = b, sv
			}
		}
		if v.gz != nil && acceptsGzip(r) {
			serveGzip(w, r, "index.html", v)
			return
		}
		w.Header().Set("ETag", v.etag)
		http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(body))
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
			return
		}
		p := path.Clean("/" + r.URL.Path)
		if p == "/api" || strings.HasPrefix(p, "/api/") {
			http.NotFound(w, r)
			return
		}
		if p == "/" || p == "/index.html" {
			serveIndex(w, r, "/")
			return
		}
		name := strings.TrimPrefix(p, "/")
		if path.Base(name) == ".gitkeep" {
			http.NotFound(w, r)
			return
		}
		if st, err := fs.Stat(fsys, name); err == nil && !st.IsDir() {
			switch {
			case strings.HasPrefix(name, "assets/"):
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			case name == "sw.js":
				// Сервис-воркер: новая версия должна подхватываться сразу (перепроверка по ETag — 304 без тела).
				w.Header().Set("Cache-Control", "no-cache")
			default:
				w.Header().Set("Cache-Control", "public, max-age=3600")
			}
			if v := variantOf(name); v != nil {
				if v.gz != nil {
					w.Header().Add("Vary", "Accept-Encoding")
					if acceptsGzip(r) {
						serveGzip(w, r, name, v)
						return
					}
				}
				w.Header().Set("ETag", v.etag)
			}
			fileServer.ServeHTTP(w, r)
			return
		}
		if path.Ext(name) != "" {
			http.NotFound(w, r)
			return
		}
		serveIndex(w, r, p)
	})
}

// sameHost — имя хоста запроса (без порта) совпадает с domain без учёта регистра.
func sameHost(host, domain string) bool {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.EqualFold(host, domain)
}
