package webui

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
)

func testFS() fstest.MapFS {
	return fstest.MapFS{
		"index.html":         {Data: []byte("<!doctype html><title>Zafiro SPA</title>")},
		"assets/app-abc.js":  {Data: []byte("console.log(1)")},
		"favicon.svg":        {Data: []byte("<svg/>")},
		".gitkeep":           {Data: nil},
		"assets/app-abc.css": {Data: []byte("body{}")},
	}
}

func get(t *testing.T, h http.Handler, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

func TestHandler(t *testing.T) {
	h := Handler(testFS())
	cases := []struct {
		path, wantBody, wantCache string
		wantStatus                int
	}{
		{"/", "Zafiro SPA", "no-cache", 200},
		{"/clubs/42/sessions", "Zafiro SPA", "no-cache", 200},
		{"/assets/app-abc.js", "console.log", "public, max-age=31536000, immutable", 200},
		{"/favicon.svg", "<svg/>", "public, max-age=3600", 200},
		{"/assets/missing.js", "", "", 404},
		{"/api/unknown", "", "", 404},
		{"/.gitkeep", "", "", 404},
	}
	for _, c := range cases {
		rec := get(t, h, c.path)
		if rec.Code != c.wantStatus {
			t.Errorf("%s: status %d, want %d", c.path, rec.Code, c.wantStatus)
			continue
		}
		if c.wantBody != "" && !strings.Contains(rec.Body.String(), c.wantBody) {
			t.Errorf("%s: body %q", c.path, rec.Body.String())
		}
		if c.wantCache != "" && rec.Header().Get("Cache-Control") != c.wantCache {
			t.Errorf("%s: Cache-Control %q, want %q", c.path, rec.Header().Get("Cache-Control"), c.wantCache)
		}
	}
}

func TestHandlerPlaceholderWithoutBuild(t *testing.T) {
	rec := get(t, Handler(fstest.MapFS{".gitkeep": {}}), "/")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "task build") {
		t.Fatalf("placeholder expected, got %d %q", rec.Code, rec.Body.String())
	}
}

func TestHandlerRejectsPost(t *testing.T) {
	rec := httptest.NewRecorder()
	Handler(testFS()).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status %d", rec.Code)
	}
}

func TestEmbeddedFSCompiles(t *testing.T) {
	// dist всегда содержит хотя бы .gitkeep — embed не должен падать.
	rec := get(t, Handler(FS()), "/")
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
}

func TestHandlerMarksSuperadminHost(t *testing.T) {
	h := Handler(fstest.MapFS{"index.html": {Data: []byte("<!doctype html><html lang=\"ru\"><title>SPA</title>")}}, WithSuperadminDomain("admin.example.kz"))
	req := httptest.NewRequest(http.MethodGet, "/clubs", nil)
	req.Host = "Admin.Example.kz:8443"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), "<html data-superadmin-host lang") {
		t.Fatalf("cabinet host: %q", rec.Body.String())
	}
	if body := get(t, h, "/clubs").Body.String(); strings.Contains(body, "data-superadmin-host") {
		t.Fatalf("other host marked: %q", body)
	}
}

// UQ-21: хост кабинета реселлера отмечается своим атрибутом; хост суперадмина и остальные — нет.
func TestHandlerMarksResellerHost(t *testing.T) {
	h := Handler(fstest.MapFS{"index.html": {Data: []byte("<!doctype html><html lang=\"ru\"><title>SPA</title>")}},
		WithSuperadminDomain("admin.example.kz"), WithResellerDomain("resseler.example.kz"))
	on := func(host, path string) string {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Host = host
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Header().Get("Vary") == "" {
			t.Fatalf("%s: нет Vary", host)
		}
		return rec.Body.String()
	}
	if body := on("Resseler.Example.kz:8443", "/"); !strings.Contains(body, "<html data-reseller-host lang") || strings.Contains(body, "data-superadmin-host") {
		t.Fatalf("reseller host: %q", body)
	}
	if body := on("admin.example.kz", "/"); strings.Contains(body, "data-reseller-host") || !strings.Contains(body, "data-superadmin-host") {
		t.Fatalf("cabinet host: %q", body)
	}
	if body := on("example.kz", "/login"); strings.Contains(body, "data-reseller-host") || strings.Contains(body, "data-superadmin-host") {
		t.Fatalf("other host marked: %q", body)
	}
}

func gunzip(t *testing.T, b []byte) string {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func getWith(t *testing.T, h http.Handler, target string, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	h.ServeHTTP(rec, req)
	return rec
}

// PB-11 / PB-23: текстовые файлы сборки — gzip по Accept-Encoding (один раз сжатые, в памяти), Vary, ETag по
// содержимому, 304 по If-None-Match; маленькое и уже сжатое — как есть; sw.js и index.html — no-cache.
func TestHandlerCompression(t *testing.T) {
	js := strings.Repeat("export const translation = 'zafiro';\n", 200)
	html := "<!doctype html><html><title>Zafiro SPA</title>" + strings.Repeat("<link rel=modulepreload href=/assets/x.js>", 60) + "</html>"
	png := bytes.Repeat([]byte{0x89, 'P', 'N', 'G', 1, 2, 3, 4}, 400)
	fsys := fstest.MapFS{
		"index.html":           {Data: []byte(html)},
		"assets/app-abc.js":    {Data: []byte(js)},
		"assets/small-abc.js":  {Data: []byte("console.log(1)")},
		"assets/logo-abc.png":  {Data: png},
		"sw.js":                {Data: []byte(strings.Repeat("self.addEventListener('fetch', () => {});\n", 50))},
		"assets/font-ab.woff2": {Data: bytes.Repeat([]byte("font"), 1000)},
	}
	h := Handler(fsys, WithSuperadminDomain("admin.zafiro.kz"))
	gz := map[string]string{"Accept-Encoding": "gzip, deflate, br"}

	rec := getWith(t, h, "/assets/app-abc.js", gz)
	if rec.Code != 200 || rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("js: %d, Content-Encoding %q", rec.Code, rec.Header().Get("Content-Encoding"))
	}
	if gunzip(t, rec.Body.Bytes()) != js {
		t.Fatal("js: gunzip != source")
	}
	if rec.Body.Len() >= len(js)/2 {
		t.Fatalf("js: %d bytes of %d", rec.Body.Len(), len(js))
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Fatalf("js: Content-Type %q", ct)
	}
	if rec.Header().Get("Vary") != "Accept-Encoding" || rec.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Fatalf("js: Vary %q, Cache-Control %q", rec.Header().Get("Vary"), rec.Header().Get("Cache-Control"))
	}
	gzTag := rec.Header().Get("ETag")
	if gzTag == "" {
		t.Fatal("js: no ETag")
	}
	if rec.Header().Get("Content-Length") != strconv.Itoa(rec.Body.Len()) {
		t.Fatalf("js: Content-Length %q for %d bytes", rec.Header().Get("Content-Length"), rec.Body.Len())
	}
	if rec = getWith(t, h, "/assets/app-abc.js", map[string]string{"Accept-Encoding": "gzip", "If-None-Match": gzTag}); rec.Code != 304 || rec.Body.Len() != 0 {
		t.Fatalf("js If-None-Match: %d, %d bytes", rec.Code, rec.Body.Len())
	}

	// Без Accept-Encoding (и с gzip;q=0) — как есть, другой ETag, тот же Vary.
	for _, hdr := range []map[string]string{nil, {"Accept-Encoding": "gzip;q=0, identity"}} {
		rec = getWith(t, h, "/assets/app-abc.js", hdr)
		if rec.Code != 200 || rec.Header().Get("Content-Encoding") != "" || rec.Body.String() != js {
			t.Fatalf("js identity %v: %d, encoding %q", hdr, rec.Code, rec.Header().Get("Content-Encoding"))
		}
		if tag := rec.Header().Get("ETag"); tag == "" || tag == gzTag || rec.Header().Get("Vary") != "Accept-Encoding" {
			t.Fatalf("js identity: ETag %q (gzip %q), Vary %q", tag, gzTag, rec.Header().Get("Vary"))
		}
	}

	// Маленькое и уже сжатое — не сжимаются.
	for _, p := range []string{"/assets/small-abc.js", "/assets/logo-abc.png", "/assets/font-ab.woff2"} {
		rec = getWith(t, h, p, gz)
		if rec.Code != 200 || rec.Header().Get("Content-Encoding") != "" {
			t.Fatalf("%s: %d, Content-Encoding %q", p, rec.Code, rec.Header().Get("Content-Encoding"))
		}
	}

	// index.html: no-cache, gzip, ETag по содержимому (у кабинета суперадмина — свой), 304.
	rec = getWith(t, h, "/clubs/1", gz)
	if rec.Code != 200 || rec.Header().Get("Content-Encoding") != "gzip" || rec.Header().Get("Cache-Control") != "no-cache" ||
		gunzip(t, rec.Body.Bytes()) != html || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("index: %d %v", rec.Code, rec.Header())
	}
	if rec.Header().Get("Last-Modified") != "" {
		t.Fatalf("index: Last-Modified %q (differs between instances)", rec.Header().Get("Last-Modified"))
	}
	indexTag := rec.Header().Get("ETag")
	if again := getWith(t, Handler(fsys, WithSuperadminDomain("admin.zafiro.kz")), "/", gz); again.Header().Get("ETag") != indexTag {
		t.Fatalf("index ETag differs between instances: %q vs %q", again.Header().Get("ETag"), indexTag)
	}
	if rec = getWith(t, h, "/", map[string]string{"Accept-Encoding": "gzip", "If-None-Match": indexTag}); rec.Code != 304 {
		t.Fatalf("index If-None-Match: %d", rec.Code)
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "admin.zafiro.kz"
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("If-None-Match", indexTag)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(gunzip(t, rec.Body.Bytes()), "data-superadmin-host") {
		t.Fatalf("cabinet index with the club ETag: %d", rec.Code)
	}

	// sw.js — no-cache.
	if rec = getWith(t, h, "/sw.js", gz); rec.Header().Get("Cache-Control") != "no-cache" || rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("sw.js: Cache-Control %q, Content-Encoding %q", rec.Header().Get("Cache-Control"), rec.Header().Get("Content-Encoding"))
	}
}
