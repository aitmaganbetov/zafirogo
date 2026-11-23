package httpserver

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// Аудит безопасности A-05: токен приглашения и код QR-входа не попадают в журнал запросов.
func TestRequestLoggerHidesPathSecrets(t *testing.T) {
	const secret = "Zx9SecretTokenZx9SecretTokenZx9SecretTokenZx9SecretTokenZx9Secre"
	var buf bytes.Buffer
	r := chi.NewRouter()
	r.Use(RequestLogger(slog.New(slog.NewTextHandler(&buf, nil))))
	ok := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }
	r.Get("/api/invitations/{token}", ok)
	r.Post("/api/invitations/{token}/accept", ok)
	r.Get("/api/pc-login/{code}", ok)
	r.Get("/api/future/{token}/x", ok) // новый маршрут с параметром-секретом скрывается без правки списка путей
	r.Get("/api/customers/{userId}", ok)
	r.Get("/api/future/{token}/of/{name}", ok)

	cases := []struct{ method, path, want string }{
		{"GET", "/api/invitations/" + secret, "path=/api/invitations/***"},
		{"POST", "/api/invitations/" + secret + "/accept", "path=/api/invitations/***/accept"},
		{"GET", "/api/pc-login/" + secret, "path=/api/pc-login/***"},
		{"GET", "/api/future/" + secret + "/x", "path=/api/future/***/x"},
		// Скрывается только сегмент на месте параметра, а не все сегменты с тем же значением (ревью 22.11.2026).
		{"GET", "/api/pc-login/api", "path=/api/pc-login/*** "},
		{"GET", "/api/future/same/of/same", "path=/api/future/***/of/same "},
		// Маршрут не найден (не тот метод, лишний сегмент) и страницы SPA — тоже без секрета.
		{"DELETE", "/api/invitations/" + secret, "path=/api/invitations/***"},
		{"GET", "/api/pc-login/" + secret + "/unknown", "path=/api/pc-login/***/unknown"},
		{"GET", "/invitations/" + secret, "path=/invitations/***"},
		{"GET", "/pc-login/" + secret, "path=/pc-login/***"},
		// Обычные идентификаторы остаются (нужны при разборе инцидентов); query не пишется.
		{"GET", "/api/customers/42?ticket=" + secret, "path=/api/customers/42 "},
		{"GET", "/api/invitations/", "path=/api/invitations/ "},
	}
	for _, c := range cases {
		buf.Reset()
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(c.method, c.path, nil))
		line := buf.String()
		if strings.Contains(line, secret) {
			t.Errorf("%s %s: the secret is in the log: %s", c.method, c.path, line)
		}
		if !strings.Contains(line, c.want) {
			t.Errorf("%s %s: want %q in %s", c.method, c.path, c.want, line)
		}
	}
}

// Ответ 499 (клиент ушёл, permissions.StatusClientClosedRequest) — INFO, а не ERROR/WARN журнала запросов.
func TestRequestLoggerClientClosedIsInfo(t *testing.T) {
	var buf bytes.Buffer
	r := chi.NewRouter()
	r.Use(RequestLogger(slog.New(slog.NewTextHandler(&buf, nil))))
	r.Get("/gone", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(499) })
	r.Get("/fail", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) })
	for path, want := range map[string]string{"/gone": "level=INFO", "/fail": "level=ERROR"} {
		buf.Reset()
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
		if !strings.Contains(buf.String(), want) {
			t.Errorf("%s: want %s in %s", path, want, buf.String())
		}
	}
}
