package webui

import (
	"net/http"
	"net/http/httptest"
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
