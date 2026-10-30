// Package integrations — REST-раздел: настройки → «API и интеграции» (api/integrations.yaml): ключи API, вебхуки,
// журнал доставок, последние запросы к API, доступ сотрудников к интеграциям.
//
// Laravel: app/Http/Controllers/IntegrationController.php, app/Services/Integrations/ApiKeyManager.php,
// config/integrations.php. Доступ — @can('manage-integrations') (владелец или club_memberships.can_manage_integrations),
// проверяется в обработчиках: маршрутов settings.integrations* нет в RequireClubArea::ROUTES. Конвенции — api/README.md.
package integrations

import (
	"github.com/go-chi/chi/v5"

	integrationsapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/integrations"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
)

// access — все операции проверяют доступ сами (allow()): Gate manage-integrations, не право матрицы.
var access = httpx.Access{
	Public: []string{
		"GetIntegrations", "ListWebhookDeliveries", "CreateApiKey", "RotateApiKey", "RevokeApiKey", "CreateWebhook",
		"UpdateWebhook", "DeleteWebhook", "RotateWebhookSecret", "RetryWebhookDelivery", "UpdateIntegrationsPermission",
	},
}

type handlers struct {
	d *httpx.Deps
}

var _ integrationsapi.StrictServerInterface = (*handlers)(nil)

// Section — «API и интеграции». Регистрация — server/internal/http/sections.go.
func Section() httpx.Section {
	return httpx.Section{Name: "integrations", Mount: mount}
}

func mount(r chi.Router, d *httpx.Deps) {
	si := integrationsapi.NewStrictHandlerWithOptions(&handlers{d: d},
		httpx.Middlewares[integrationsapi.StrictMiddlewareFunc](access),
		integrationsapi.StrictHTTPServerOptions{RequestErrorHandlerFunc: d.RequestError, ResponseErrorHandlerFunc: d.ResponseError()})
	integrationsapi.HandlerWithOptions(si, integrationsapi.ChiServerOptions{BaseRouter: r, ErrorHandlerFunc: d.ParamError})
}
