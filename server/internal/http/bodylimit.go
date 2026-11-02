package httpserver

import (
	"mime"
	"net/http"
	"strings"
)

// Пределы тела запроса REST (SEC-002, docs/security-review.md). strict-обработчики oapi-codegen разбирают JSON
// (runtime.BindJSONBody → json.NewDecoder(r.Body)) ДО middleware прав: без предела любой анонимный клиент
// заставлял процесс держать в памяти тело любого размера (POST /api/auth/login с телом в гигабайт).
const (
	// MaxJSONBody — JSON и прочие тела. Самое большое законное тело — страница переводов суперадминки (≤ сотни КБ).
	MaxJSONBody = 1 << 20
	// MaxMultipartBody — загрузки (картинка входа ≤ 3 МБ, импорт переводов ≤ 2 МБ, новость ≤ 2 МБ + поля).
	MaxMultipartBody = 8 << 20
)

// bodyLimit ограничивает тело запроса (http.MaxBytesReader: чтение сверх предела — ошибка, разбор — 400/413).
// /api/v1 ограничивает тело сам (LimitApiRequestSize, 413 в конверте v1); старый REST мобильных — тоже (maxBody).
func bodyLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil && r.Body != http.NoBody && !isIntegrationAPI(r.URL.Path) {
			limit := int64(MaxJSONBody)
			if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err == nil && mt == "multipart/form-data" {
				limit = MaxMultipartBody
			}
			if r.ContentLength > limit {
				writeError(w, http.StatusRequestEntityTooLarge, "payload_too_large")
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, limit)
		}
		next.ServeHTTP(w, r)
	})
}

func isIntegrationAPI(p string) bool { return p == "/api/v1" || strings.HasPrefix(p, "/api/v1/") }
