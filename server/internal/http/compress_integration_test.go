//go:build integration

package httpserver

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aitmaganbetov/zafirogo/server/internal/i18n"
)

// PB-11 + FE-04 на собранном роутере: пакет переводов отдаётся gzip-ом со слабым ETag, Vary и no-cache; повтор с
// If-None-Match — 304 без тела; записи «ключ = значение» в пакет не попадают.
func TestI18nPackCompressedAndRevalidated(t *testing.T) {
	e := newFoundationEnv(t)
	ru, kk := map[string]string{}, map[string]string{}
	for i := range 200 {
		key := fmt.Sprintf("Строка интерфейса номер %d", i)
		ru[key] = key
		kk[key] = fmt.Sprintf("Интерфейс жолы %d", i)
	}
	kk["Без перевода"] = "Без перевода"
	if _, err := i18n.Import(context.Background(), e.db.Platform, map[string]i18n.Shipped{"ru": {"*": ru}, "kk": {"*": kk}}); err != nil {
		t.Fatal(err)
	}
	get := func(path string, hdr map[string]string) (*http.Response, []byte) {
		t.Helper()
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, e.url(path), nil)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		body, _ := io.ReadAll(res.Body)
		return res, body
	}

	res, body := get("/api/i18n/kk", map[string]string{"Accept-Encoding": "gzip"})
	if res.StatusCode != 200 || res.Header.Get("Content-Encoding") != "gzip" {
		t.Fatalf("kk: %d, Content-Encoding %q", res.StatusCode, res.Header.Get("Content-Encoding"))
	}
	if res.Header.Get("Cache-Control") != "no-cache" || !strings.Contains(res.Header.Get("Vary"), "Accept-Encoding") {
		t.Fatalf("kk: Cache-Control %q, Vary %q", res.Header.Get("Cache-Control"), res.Header.Get("Vary"))
	}
	etag := res.Header.Get("ETag")
	if !strings.HasPrefix(etag, `W/"`) {
		t.Fatalf("kk: ETag %q, want weak (the body on the wire is compressed)", etag)
	}
	zr, err := gzip.NewReader(strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	plain, _ := io.ReadAll(zr)
	var lines map[string]string
	if err := json.Unmarshal(plain, &lines); err != nil {
		t.Fatal(err)
	}
	if len(lines) != 200 || lines["Строка интерфейса номер 7"] != "Интерфейс жолы 7" {
		t.Fatalf("kk pack: %d lines", len(lines))
	}
	if _, ok := lines["Без перевода"]; ok {
		t.Fatal("kk pack has a key = value entry")
	}
	if len(body) >= len(plain)/2 {
		t.Fatalf("kk pack: %d bytes compressed of %d", len(body), len(plain))
	}

	for _, hdr := range []map[string]string{
		{"Accept-Encoding": "gzip", "If-None-Match": etag},
		{"If-None-Match": strings.TrimPrefix(etag, "W/")},
	} {
		if res, body := get("/api/i18n/kk", hdr); res.StatusCode != http.StatusNotModified || len(body) != 0 {
			t.Fatalf("If-None-Match %v: %d, %d bytes", hdr, res.StatusCode, len(body))
		}
	}

	// Язык-источник: все записи «ключ = значение» — пакет пуст и мал (не сжимается).
	res, body = get("/api/i18n/ru", map[string]string{"Accept-Encoding": "gzip"})
	if res.StatusCode != 200 || res.Header.Get("Content-Encoding") != "" || string(body) != "{}" {
		t.Fatalf("ru: %d, Content-Encoding %q, body %.80s", res.StatusCode, res.Header.Get("Content-Encoding"), body)
	}
}
