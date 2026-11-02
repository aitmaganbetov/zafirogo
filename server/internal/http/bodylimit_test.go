package httpserver

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// countingReader — сколько байт тела прочитал сервер.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// SEC-002: анонимный POST с огромным JSON не читается целиком (strict-обработчик разбирает тело до прав).
func TestBodyLimitAnonymousJSON(t *testing.T) {
	router := NewRouter(Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	huge := `{"login":"` + strings.Repeat("a", 4*MaxJSONBody) + `","password":"x"}`
	for _, withLength := range []bool{true, false} {
		body := &countingReader{r: strings.NewReader(huge)}
		req := httptest.NewRequest(http.MethodPost, "/api/auth/login", body)
		req.Header.Set("Content-Type", "application/json")
		if withLength {
			req.ContentLength = int64(len(huge))
		} else {
			req.ContentLength = -1
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if body.n > MaxJSONBody+64*1024 {
			t.Fatalf("length=%v: server read %d bytes of body (limit %d)", withLength, body.n, MaxJSONBody)
		}
		if rec.Code != http.StatusRequestEntityTooLarge && rec.Code != http.StatusBadRequest {
			t.Fatalf("length=%v: status %d", withLength, rec.Code)
		}
	}
}

// Загрузки (multipart) — свой, больший предел; /api/v1 ограничивает тело сам.
func TestBodyLimitMultipartAndV1(t *testing.T) {
	var got int64
	h := bodyLimit(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got, _ = io.Copy(io.Discard, r.Body)
	}))
	for _, tc := range []struct {
		path, ct string
		size     int
		want     int64
	}{
		{"/api/settings/signin-image", "multipart/form-data; boundary=x", 4 << 20, 4 << 20},
		{"/api/settings/signin-image", "multipart/form-data; boundary=x", 9 << 20, MaxMultipartBody},
		{"/api/cash-register/sales", "application/json", 2 << 20, MaxJSONBody},
		{"/api/v1/customers", "application/json", 2 << 20, 2 << 20},
	} {
		req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(strings.Repeat("a", tc.size)))
		req.ContentLength = -1
		req.Header.Set("Content-Type", tc.ct)
		got = 0
		h.ServeHTTP(httptest.NewRecorder(), req)
		if got != tc.want {
			t.Errorf("%s %s: read %d, want %d", tc.path, tc.ct, got, tc.want)
		}
	}
}
