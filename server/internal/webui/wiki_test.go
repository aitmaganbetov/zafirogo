package webui

import (
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func wikiFS() fstest.MapFS {
	return fstest.MapFS{
		"index.html":         {Data: []byte("<!doctype html><title>SPA</title>")},
		"wiki/index.html":    {Data: []byte("<!doctype html><title>Zafiro Wiki</title><script>go()</script>")},
		"assets/wiki-1.css":  {Data: []byte("body{}")},
		"icons/icon-192.png": {Data: []byte("png")},
	}
}

func wikiGet(h http.Handler, host, target string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, target, nil)
	r.Host = host
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestWikiOnSite(t *testing.T) {
	fsys := wikiFS()
	h := Wiki(fsys, "", Handler(fsys))
	w := wikiGet(h, "zafiro.test", "/wiki")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Zafiro Wiki") {
		t.Fatalf("/wiki: %d %s", w.Code, w.Body)
	}
	if got := w.Header().Get("Cache-Control"); got != "public, max-age=300" {
		t.Errorf("cache: %q", got)
	}
	sum := sha256.Sum256([]byte("go()"))
	if csp := w.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "'sha256-"+base64.StdEncoding.EncodeToString(sum[:])+"'") ||
		strings.Contains(csp, "unsafe-inline' 'sha") || strings.Contains(csp, "googleapis") {
		t.Errorf("csp: %q", csp)
	}
	if w := wikiGet(h, "zafiro.test", "/dashboard"); !strings.Contains(w.Body.String(), "SPA") {
		t.Errorf("SPA: %s", w.Body)
	}
}

func TestWikiOwnHost(t *testing.T) {
	fsys := wikiFS()
	h := Wiki(fsys, "wiki.zafiro.test", Handler(fsys))
	if w := wikiGet(h, "wiki.zafiro.test:443", "/"); w.Code != 200 || !strings.Contains(w.Body.String(), "Zafiro Wiki") {
		t.Fatalf("wiki root: %d %s", w.Code, w.Body)
	}
	for _, p := range []string{"/assets/wiki-1.css", "/icons/icon-192.png"} {
		if w := wikiGet(h, "wiki.zafiro.test", p); w.Code != 200 {
			t.Errorf("%s: %d", p, w.Code)
		}
	}
	// RestrictWikiHost: любой другой адрес хоста wiki — на wiki.
	for _, p := range []string{"/dashboard", "/api/auth/me", "/wiki"} {
		if w := wikiGet(h, "WIKI.zafiro.test", p); w.Code != http.StatusFound || w.Header().Get("Location") != "/" {
			t.Errorf("%s: %d %q", p, w.Code, w.Header().Get("Location"))
		}
	}
	// Старый адрес /wiki основного сайта — 301 на хост wiki.
	if w := wikiGet(h, "zafiro.test", "/wiki"); w.Code != http.StatusMovedPermanently || w.Header().Get("Location") != "https://wiki.zafiro.test/" {
		t.Errorf("/wiki on site: %d %q", w.Code, w.Header().Get("Location"))
	}
	if w := wikiGet(h, "zafiro.test", "/"); !strings.Contains(w.Body.String(), "SPA") {
		t.Errorf("site root: %s", w.Body)
	}
}
