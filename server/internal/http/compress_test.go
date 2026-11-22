package httpserver

import (
	"bytes"
	"compress/gzip"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func gunzipBody(t *testing.T, b []byte) string {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("not gzip: %v", err)
	}
	out, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// PB-11: JSON от compressMin байт — gzip клиентам, которые его принимают; маленькое, уже сжатое, потоки, WebSocket,
// 204/304, HEAD и /api/auth/* — как есть.
func TestCompress(t *testing.T) {
	big := `{"items":[` + strings.Repeat(`{"id":1,"name":"Компьютер","status":"online"},`, 200) + `{}]}`
	small := `{"ok":true}`
	mux := http.NewServeMux()
	json := func(body string) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("ETag", `"v1"`)
			w.WriteHeader(http.StatusOK)
			half := len(body) / 2
			_, _ = w.Write([]byte(body[:half])) // несколько Write, как json.Encoder
			_, _ = w.Write([]byte(body[half:]))
		}
	}
	mux.HandleFunc("/api/big", json(big))
	mux.HandleFunc("/api/small", json(small))
	mux.HandleFunc("/api/auth/login", json(big))
	mux.HandleFunc("/api/png", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(bytes.Repeat([]byte("png"), 2000))
	})
	mux.HandleFunc("/api/encoded", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write(bytes.Repeat([]byte("x"), 5000))
	})
	mux.HandleFunc("/api/sse", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(strings.Repeat("data: x\n\n", 500)))
	})
	mux.HandleFunc("/api/stream", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("tick\n"))
		w.(http.Flusher).Flush()
		_, _ = w.Write([]byte(strings.Repeat("tock\n", 1000)))
	})
	mux.HandleFunc("/api/304", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("ETag", `"v1"`)
		w.WriteHeader(http.StatusNotModified)
	})
	mux.HandleFunc("/api/204", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("/api/error", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"error":"validation_failed"}`))
	})
	mux.HandleFunc("/ws", func(w http.ResponseWriter, _ *http.Request) {
		if _, ok := w.(*compressWriter); ok {
			http.Error(w, "wrapped", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusSwitchingProtocols)
	})
	h := compress(mux)
	do := func(method, target string, hdr map[string]string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, target, nil)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		h.ServeHTTP(rec, req)
		return rec
	}
	gz := map[string]string{"Accept-Encoding": "gzip, deflate, br"}

	rec := do("GET", "/api/big", gz)
	if rec.Code != 200 || rec.Header().Get("Content-Encoding") != "gzip" || rec.Header().Get("Vary") != "Accept-Encoding" {
		t.Fatalf("big: %d %v", rec.Code, rec.Header())
	}
	if gunzipBody(t, rec.Body.Bytes()) != big {
		t.Fatal("big: gunzip != source")
	}
	if rec.Body.Len() > len(big)/5 {
		t.Fatalf("big: %d of %d bytes", rec.Body.Len(), len(big))
	}
	if rec.Header().Get("ETag") != `W/"v1"` || rec.Header().Get("Content-Length") != "" {
		t.Fatalf("big: ETag %q, Content-Length %q", rec.Header().Get("ETag"), rec.Header().Get("Content-Length"))
	}

	plain := []struct {
		name, method, target string
		hdr                  map[string]string
		status               int
		body                 string
	}{
		{"без Accept-Encoding", "GET", "/api/big", nil, 200, big},
		{"gzip;q=0", "GET", "/api/big", map[string]string{"Accept-Encoding": "gzip;q=0"}, 200, big},
		{"маленький", "GET", "/api/small", gz, 200, small},
		{"ошибка", "GET", "/api/error", gz, 422, `{"error":"validation_failed"}`},
		{"токены", "GET", "/api/auth/login", gz, 200, big},
		{"картинка", "GET", "/api/png", gz, 200, strings.Repeat("png", 2000)},
		{"уже сжато", "GET", "/api/encoded", gz, 200, strings.Repeat("x", 5000)},
		{"SSE", "GET", "/api/sse", gz, 200, strings.Repeat("data: x\n\n", 500)},
		{"поток с Flush", "GET", "/api/stream", gz, 200, "tick\n" + strings.Repeat("tock\n", 1000)},
		{"304", "GET", "/api/304", gz, 304, ""},
		{"204", "GET", "/api/204", gz, 204, ""},
		{"WebSocket", "GET", "/ws", map[string]string{"Accept-Encoding": "gzip", "Upgrade": "websocket"}, 101, ""},
	}
	for _, c := range plain {
		rec := do(c.method, c.target, c.hdr)
		if rec.Code != c.status {
			t.Errorf("%s: status %d, want %d", c.name, rec.Code, c.status)
		}
		if enc := rec.Header().Get("Content-Encoding"); enc != "" && c.name != "уже сжато" {
			t.Errorf("%s: Content-Encoding %q", c.name, enc)
		}
		if rec.Body.String() != c.body {
			t.Errorf("%s: body changed (%d bytes, want %d)", c.name, rec.Body.Len(), len(c.body))
		}
	}
	if rec := do("GET", "/api/small", gz); rec.Header().Get("ETag") != `"v1"` || rec.Header().Get("Vary") != "Accept-Encoding" {
		t.Errorf("small: ETag %q, Vary %q", rec.Header().Get("ETag"), rec.Header().Get("Vary"))
	}
	if rec := do("HEAD", "/api/big", gz); rec.Header().Get("Content-Encoding") != "" {
		t.Errorf("HEAD: Content-Encoding %q", rec.Header().Get("Content-Encoding"))
	}
}

// Паника обработчика: накопленное начало ответа не уходит клиенту — Recoverer отвечает 500 сам.
func TestCompressPanic(t *testing.T) {
	h := Recoverer(slog.New(slog.DiscardHandler))(compress(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"partial":`))
		panic("boom")
	})))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/x", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "partial") {
		t.Fatalf("panic: %d %q", rec.Code, rec.Body.String())
	}
}

// Частичный ответ (Range → 206, Content-Range по несжатым смещениям) не сжимается: иначе клиент получил бы gzip-поток
// с диапазоном, который к нему не относится (ревью 22.11.2026).
func TestCompressLeavesPartialContent(t *testing.T) {
	text := strings.Repeat("строка текста для частичной отдачи\n", 400)
	h := compress(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		http.ServeContent(w, r, "", time.Time{}, strings.NewReader(text))
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/file", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("Range", "bytes=100-4099")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206", rec.Code)
	}
	if enc := rec.Header().Get("Content-Encoding"); enc != "" {
		t.Fatalf("Content-Encoding = %q у частичного ответа", enc)
	}
	if got, want := rec.Header().Get("Content-Range"), "bytes 100-4099/"+strconv.Itoa(len(text)); got != want {
		t.Fatalf("Content-Range = %q, want %q", got, want)
	}
	if rec.Header().Get("Content-Length") != "4000" || rec.Body.String() != text[100:4100] {
		t.Fatalf("тело частичного ответа изменено: Content-Length = %q, %d байт", rec.Header().Get("Content-Length"), rec.Body.Len())
	}
	// Диапазон вне файла: 416 с Content-Range — тоже как есть.
	req = httptest.NewRequest(http.MethodGet, "/api/file", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("Range", "bytes=999999-")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestedRangeNotSatisfiable || rec.Header().Get("Content-Encoding") != "" {
		t.Fatalf("416: status = %d, Content-Encoding = %q", rec.Code, rec.Header().Get("Content-Encoding"))
	}
	// Тот же ресурс целиком — сжимается.
	req = httptest.NewRequest(http.MethodGet, "/api/file", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Encoding") != "gzip" || gunzipBody(t, rec.Body.Bytes()) != text {
		t.Fatalf("полный ответ: status = %d, Content-Encoding = %q", rec.Code, rec.Header().Get("Content-Encoding"))
	}
}
