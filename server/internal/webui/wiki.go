package webui

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"io/fs"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// WikiPage — wiki Zafiro: одна статичная страница (resources/wiki/index.html старого проекта; в сборке —
// dist/wiki/index.html, шрифты из npm). Решение Q-069: свой хост сохраняется.
const WikiPage = "wiki/index.html"

// inlineScript — inline-скрипты страницы (без src): их sha256 разрешаются в CSP.
var inlineScript = regexp.MustCompile(`(?s)<script>(.*?)</script>`)

// Wiki оборачивает весь HTTP-обработчик сайта (routes/web.php:108–117, RestrictWikiHost):
//   - host пуст (WIKI_DOMAIN не задан) — страница на /wiki;
//   - host задан — страница на корне этого хоста, любой другой адрес хоста (кроме файлов сборки: стили, шрифты,
//     иконки) — 302 на корень (RestrictWikiHost); старый адрес /wiki основного сайта — 301 на хост wiki.
//
// Cache-Control: public, max-age=300, как response()->file(...) старого маршрута. Без собранной страницы — next.
func Wiki(fsys fs.FS, host string, next http.Handler) http.Handler {
	page, err := fs.ReadFile(fsys, WikiPage)
	if err != nil {
		return next
	}
	csp := wikiCSP(page)
	modTime := time.Now()
	serve := func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Type", "text/html; charset=utf-8")
		h.Set("Cache-Control", "public, max-age=300")
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		http.ServeContent(w, r, "index.html", modTime, bytes.NewReader(page))
	}
	isPage := func(p string) bool { return p == "/wiki" || p == "/wiki/" || p == "/wiki/index.html" }
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if host == "" {
			if isPage(r.URL.Path) && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
				serve(w, r)
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		if !strings.EqualFold(hostOnly(r.Host), host) {
			if isPage(r.URL.Path) {
				http.Redirect(w, r, "https://"+host+"/", http.StatusMovedPermanently)
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		switch {
		case r.URL.Path == "/" && (r.Method == http.MethodGet || r.Method == http.MethodHead):
			serve(w, r)
		case buildFile(fsys, r.URL.Path):
			next.ServeHTTP(w, r)
		default:
			http.Redirect(w, r, "/", http.StatusFound)
		}
	})
}

// buildFile — файл сборки, который нужен странице wiki на её хосте (стили и шрифты /assets, иконки /icons).
func buildFile(fsys fs.FS, p string) bool {
	name := strings.TrimPrefix(p, "/")
	if !strings.HasPrefix(name, "assets/") && !strings.HasPrefix(name, "icons/") {
		return false
	}
	st, err := fs.Stat(fsys, name)
	return err == nil && !st.IsDir()
}

func hostOnly(h string) string {
	if host, _, err := net.SplitHostPort(h); err == nil {
		return host
	}
	return h
}

// wikiCSP — CSP страницы wiki: свой origin и inline-скрипты страницы по sha256 (без внешних источников, Q-064).
func wikiCSP(page []byte) string {
	scripts := "'self'"
	for _, m := range inlineScript.FindAllSubmatch(page, -1) {
		sum := sha256.Sum256(m[1])
		scripts += " 'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
	}
	return "default-src 'self'; script-src " + scripts + "; style-src 'self' 'unsafe-inline'; img-src 'self' data:; " +
		"font-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'self'; form-action 'self'; frame-ancestors 'none'"
}
