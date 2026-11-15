package httpserver

import (
	"mime"
	"net/http"
	"strings"
)

// requireJSONBody — REST панели и кабинета суперадмина (/api/*, strict-обработчики oapi-codegen) принимает тело
// только как application/json или multipart/form-data (загрузки); иначе — 415 unsupported_media_type
// (аудит A-12). strict-обработчик разбирает JSON, не глядя на Content-Type, поэтому без этой проверки тело
// text/plain («простой» запрос чужой страницы, без preflight CORS) дошло бы до обработчика.
//
// Не действует на внешние входы, чью совместимость менять нельзя: /api/v1 (интеграции — свой конверт ошибок) и
// старый REST мобильных приложений (слой legacy стоит в цепочке раньше и такие запросы сюда не пропускает).
// Запрос без тела (POST выхода, refresh, действия без параметров) Content-Type не обязан иметь.
func requireJSONBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hasBody(r) && isPanelAPI(r.URL.Path) && !acceptedBodyType(r.Header.Get("Content-Type")) {
			writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// hasBody — у запроса есть тело: известная ненулевая длина или длина неизвестна (chunked).
func hasBody(r *http.Request) bool {
	return r.Body != nil && r.Body != http.NoBody && r.ContentLength != 0
}

func isPanelAPI(p string) bool { return strings.HasPrefix(p, "/api/") && !isIntegrationAPI(p) }

func acceptedBodyType(contentType string) bool {
	mt, _, err := mime.ParseMediaType(contentType)
	return err == nil && (mt == "application/json" || mt == "multipart/form-data")
}
