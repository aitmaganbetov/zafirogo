// Package webui отдаёт собранный фронтенд (web/dist), вшитый в бинарник через embed.FS.
//
// Сборка (task build) копирует web/dist → server/internal/webui/dist. В git лежит только
// dist/.gitkeep, поэтому `go build` работает и без фронта: тогда на / отдаётся
// служебная страница-заглушка.
//
// Правила отдачи:
//   - существующий файл отдаётся как есть; /assets/* (имена с хешем от Vite) —
//     Cache-Control: public, max-age=31536000, immutable; index.html — no-cache;
//   - /api/* сюда не доходит (роутер отвечает JSON 404), но на всякий случай — 404;
//   - путь с расширением, которого нет, — 404 (не прячем битые ссылки на ассеты);
//   - всё остальное — index.html (SPA fallback для клиентского роутера).
package webui

import (
	"bytes"
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
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

// Handler возвращает http.Handler, отдающий SPA из fsys.
func Handler(fsys fs.FS) http.Handler {
	index, err := fs.ReadFile(fsys, "index.html")
	if err != nil {
		index = []byte(placeholder)
	}
	fileServer := http.FileServerFS(fsys)
	modTime := time.Now()

	serveIndex := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		http.ServeContent(w, r, "index.html", modTime, bytes.NewReader(index))
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
			serveIndex(w, r)
			return
		}
		name := strings.TrimPrefix(p, "/")
		if path.Base(name) == ".gitkeep" {
			http.NotFound(w, r)
			return
		}
		if st, err := fs.Stat(fsys, name); err == nil && !st.IsDir() {
			if strings.HasPrefix(name, "assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			} else {
				w.Header().Set("Cache-Control", "public, max-age=3600")
			}
			fileServer.ServeHTTP(w, r)
			return
		}
		if path.Ext(name) != "" {
			http.NotFound(w, r)
			return
		}
		serveIndex(w, r)
	})
}
