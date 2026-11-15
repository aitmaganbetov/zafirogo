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

	cases := []struct{ method, path, want string }{
		{"GET", "/api/invitations/" + secret, "path=/api/invitations/***"},
		{"POST", "/api/invitations/" + secret + "/accept", "path=/api/invitations/***/accept"},
		{"GET", "/api/pc-login/" + secret, "path=/api/pc-login/***"},
		{"GET", "/api/future/" + secret + "/x", "path=/api/future/***/x"},
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
