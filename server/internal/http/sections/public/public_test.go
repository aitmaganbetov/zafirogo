package public

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
)

// LocaleResolver::forGuest().
func TestGuestLocale(t *testing.T) {
	all := []string{"ru", "kk", "en"}
	for _, c := range []struct {
		header  string
		enabled []string
		want    string
	}{
		{"ru-RU,ru;q=0.9,en;q=0.8", all, "ru"},
		{"kk-KZ", all, "kk"},
		{"de-DE,en;q=0.5,kk;q=0.7", all, "kk"}, // по убыванию q
		{"de-DE", all, "en"},                   // DEFAULT_GUEST_LOCALE
		{"de-DE", []string{"ru", "kk"}, "ru"},
		{"", []string{"kk"}, "kk"},
		{"ru;q=0", all, "en"},
	} {
		if got := GuestLocale(c.header, c.enabled); got != c.want {
			t.Errorf("%q %v: %s, want %s", c.header, c.enabled, got, c.want)
		}
	}
}

func TestDownloadURL(t *testing.T) {
	for raw, want := range map[string]string{
		`{"download_url":"https://cdn.zafiro.kz/a.exe"}`: "https://cdn.zafiro.kz/a.exe",
		`{"download_url":"http://cdn.zafiro.kz/a.exe"}`:  "",
		`{"download_url":"https://"}`:                    "",
		`{"download_url":5}`:                             "",
		`null`:                                           "",
		``:                                               "",
	} {
		if got := downloadURL(json.RawMessage(raw)); got != want {
			t.Errorf("%s: %q", raw, got)
		}
	}
}

// Без каталога переводов: страница offline с русскими текстами, CSP пропускает ровно её inline-скрипты.
func TestOfflineAndManifest(t *testing.T) {
	h := &handlers{d: &httpx.Deps{}}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/offline", nil)
	r.Header.Set("Accept-Language", "ru-RU")
	h.offline(w, r)
	body := w.Body.String()
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-cache" || !strings.Contains(body, `<html lang="ru">`) ||
		!strings.Contains(body, "<title>Нет подключения · Zafiro</title>") || !strings.Contains(body, `"retry":"Повторить"`) {
		t.Fatalf("offline: %d %s", w.Code, body)
	}
	csp := w.Header().Get("Content-Security-Policy")
	if strings.Count(csp, "'sha256-") != 2 || strings.Contains(csp, "unsafe-inline' 'sha") || !strings.Contains(csp, scriptHash(offlineReload)) {
		t.Fatalf("csp: %s", csp)
	}

	w = httptest.NewRecorder()
	h.manifest(w, httptest.NewRequest(http.MethodGet, "/manifest.webmanifest", nil))
	var m struct {
		Lang      string `json:"lang"`
		Shortcuts []struct{ Name, URL string }
	}
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil || w.Header().Get("Content-Type") != "application/manifest+json" ||
		len(m.Shortcuts) != 3 || m.Shortcuts[0].Name != "Касса" || m.Shortcuts[2].URL != "/club-computers" || m.Lang != "ru" {
		t.Fatalf("manifest: %s %+v", w.Body, m)
	}
}
