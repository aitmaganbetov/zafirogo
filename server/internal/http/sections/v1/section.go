// Package v1 — интеграционный API /api/v1 для внешних систем клуба (api/v1.yaml).
//
// Laravel: routes/api_v1.php (17 маршрутов), app/Http/Controllers/Api/V1/*, app/Http/Middleware/Api/*,
// app/Support/ApiError.php, config/integrations.php, docs/integration-api.md. Контракт внешний (решения Q-056,
// Q-057): пути, методы, параметры, формат ответов и ошибок `{"error": {"code", "message", …}}`, области ключей,
// идемпотентность и лимиты — как в старом проекте.
//
// Порядок обработки запроса — как приоритет middleware в bootstrap/app.php старого проекта:
//
//	маршрут (404/405 — без журнала) → журнал запроса (api.log) → размер тела (api.size) → API-ключ (api.key)
//	→ лимит ключа и клуба (api.limit) → область (api.scope) → Idempotency-Key (api.idempotent) → обработчик.
//
// Клуб — только из ключа: в контекст кладётся Principal создателя ключа в клубе ключа (изменения через ключ
// приписываются создателю, ApiV1Controller::actorId()), а в журнал действий — публичный префикс ключа.
package v1

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
)

// Section — интеграционный API v1.
func Section() httpx.Section { return httpx.Section{Name: "v1", Mount: mount} }

// idemMode — api.idempotent: нет, необязательный ключ, обязательный (api.idempotent:required).
type idemMode int

const (
	idemNone idemMode = iota
	idemOptional
	idemRequired
)

// scopeAny — api.scope:any (любой действующий ключ).
const scopeAny = "any"

// route — маршрут routes/api_v1.php.
type route struct {
	method, path string
	// name — имя маршрута Laravel (api.v1.…).
	name  string
	scope string
	idem  idemMode
	fn    func(h *handlers, c *call) response
}

// routes — routes/api_v1.php (17 маршрутов). Каждый объявляет область: маршрута без неё нет (default deny,
// AuthenticateApiKey::declaresScope()); тест сверяет список со спецификацией api/v1.yaml.
var routes = []route{
	{http.MethodGet, "/me", "api.v1.me", scopeAny, idemNone, (*handlers).me},
	{http.MethodGet, "/products", "api.v1.products.index", "products:read", idemNone, (*handlers).listProducts},
	{http.MethodGet, "/products/{product}", "api.v1.products.show", "products:read", idemNone, (*handlers).showProduct},
	{http.MethodGet, "/product-categories", "api.v1.product-categories.index", "products:read", idemNone, (*handlers).listCategories},
	{http.MethodGet, "/stock-movements", "api.v1.stock-movements.index", "products:read", idemNone, (*handlers).listStockMovements},
	{http.MethodPost, "/products", "api.v1.products.store", "products:write", idemOptional, (*handlers).createProduct},
	{http.MethodPatch, "/products/{product}", "api.v1.products.update", "products:write", idemOptional, (*handlers).updateProduct},
	{http.MethodPost, "/stock-movements", "api.v1.stock-movements.store", "stock:write", idemRequired, (*handlers).createStockMovement},
	{http.MethodGet, "/sales", "api.v1.sales.index", "sales:read", idemNone, (*handlers).listSales},
	{http.MethodGet, "/sales/{sale}", "api.v1.sales.show", "sales:read", idemNone, (*handlers).showSale},
	{http.MethodGet, "/zones", "api.v1.zones.index", "computers:read", idemNone, (*handlers).listZones},
	{http.MethodGet, "/computers", "api.v1.computers.index", "computers:read", idemNone, (*handlers).listComputers},
	{http.MethodGet, "/computers/{computer}", "api.v1.computers.show", "computers:read", idemNone, (*handlers).showComputer},
	{http.MethodPut, "/computers/{computer}/reservation", "api.v1.computers.reservation", "bookings:write", idemOptional, (*handlers).reservation},
	{http.MethodPost, "/computers/control", "api.v1.computers.control", "computers:control", idemRequired, (*handlers).control},
	{http.MethodGet, "/tariffs", "api.v1.tariffs.index", "tariffs:read", idemNone, (*handlers).listTariffs},
	{http.MethodGet, "/members", "api.v1.members.index", "members:read", idemNone, (*handlers).listMembers},
}

// Prefix — префикс маршрутов раздела (routes/api.php: Route::prefix('v1')).
const Prefix = "/api/v1"

type handlers struct{ d *httpx.Deps }

func mount(r chi.Router, d *httpx.Deps) {
	h := &handlers{d: d}
	r.Route(Prefix, func(r chi.Router) {
		// Маршрута нет — конверт ошибки v1 без проверки ключа и без журнала (как исключение маршрутизации Laravel).
		r.NotFound(func(w http.ResponseWriter, _ *http.Request) { errorResponse("not_found", http.StatusNotFound).write(w) })
		r.MethodNotAllowed(func(w http.ResponseWriter, _ *http.Request) {
			errorResponse("method_not_allowed", http.StatusMethodNotAllowed).write(w)
		})
		for _, rt := range routes {
			if rt.scope == "" {
				panic("v1: route without scope " + rt.path) // default deny
			}
			r.Method(rt.method, rt.path, h.chain(rt))
		}
	})
}

// response — ответ слоя обработки (журнал, идемпотентность и лимиты видят статус и тело до записи).
type response struct {
	status  int
	body    []byte
	headers map[string]string
}

func (r response) write(w http.ResponseWriter) {
	for k, v := range r.headers {
		w.Header().Set(k, v)
	}
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "application/json")
	}
	w.Header().Set("Cache-Control", "no-store, private")
	w.WriteHeader(r.status)
	_, _ = w.Write(r.body)
}

func (r *response) setHeader(k, v string) {
	if r.headers == nil {
		r.headers = map[string]string{}
	}
	r.headers[k] = v
}

// jsonBody — тело ответа JSON без экранирования <, >, & (json_encode Laravel тоже не портит текст: экранирование
// там — <-последовательности, клиенты разбирают одинаково).
func jsonBody(v any) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return []byte(`{"error":{"code":"server_error","message":"Internal server error."}}`)
	}
	return bytes.TrimRight(buf.Bytes(), "\n")
}

func jsonResponse(status int, v any) response { return response{status: status, body: jsonBody(v)} }

// data — {"data": …} (response()->json(['data' => …])).
func data(status int, v any) response { return jsonResponse(status, map[string]any{"data": v}) }

// ---------------------------------------------------------------------------
// Ошибки (App\Support\ApiError)
// ---------------------------------------------------------------------------

// errorMessages — ApiError::MESSAGES (короткий английский текст; код стабилен).
var errorMessages = map[string]string{
	"invalid_api_key":           "The API key is missing or invalid.",
	"api_key_revoked":           "The API key has been revoked.",
	"api_key_expired":           "The API key has expired.",
	"api_key_owner_lost_access": "The user who created this API key no longer manages integrations of the club.",
	"subscription_inactive":     "The club subscription is not active.",
	"ip_not_allowed":            "Requests from this IP address are not allowed for this API key.",
	"too_many_failed_attempts":  "Too many failed authentication attempts. Try again later.",
	"insufficient_scope":        "The API key does not have the scope required for this operation.",
	"tenant_override_forbidden": "The club is determined by the API key and cannot be set in the request.",
	"payload_too_large":         "The request body is too large.",
	"idempotency_key_required":  "This operation requires an Idempotency-Key header.",
	"idempotency_key_invalid":   `The Idempotency-Key header must be 1-100 characters: letters, digits, "-", "_" or ":".`,
	"idempotency_key_reused":    "This Idempotency-Key was already used with a different request.",
	"idempotency_in_progress":   "A request with this Idempotency-Key is still being processed.",
	"rate_limited":              "Too many requests. Slow down and retry after the delay in the Retry-After header.",
	"validation_failed":         "The request data is invalid.",
	"not_found":                 "The resource does not exist.",
	"method_not_allowed":        "The HTTP method is not supported for this endpoint.",
	"forbidden":                 "This operation is not allowed.",
	"bad_request":               "The request is malformed.",
	"conflict":                  "The operation conflicts with the current state of the resource.",
	"server_error":              "Internal server error.",
}

// errorEnvelope — {"error": {"code", "message", …extra}} (ApiError::response()). Порядок ключей: code, message,
// затем дополнительные поля (fields, required_scopes, computer_ids).
type errorEnvelope struct {
	Code           string              `json:"code"`
	Message        string              `json:"message"`
	Fields         map[string][]string `json:"fields,omitempty"`
	RequiredScopes []string            `json:"required_scopes,omitempty"`
	ComputerIDs    []int64             `json:"computer_ids,omitempty"`
}

// apiError — ошибка обработчика (throw new ApiError(...)).
type apiError struct {
	status int
	env    errorEnvelope
}

func (e *apiError) Error() string { return e.env.Code }

func newError(code string, status int, message string) *apiError {
	if message == "" {
		message = errorMessages[code]
	}
	if message == "" {
		message = errorMessages["server_error"]
	}
	return &apiError{status: status, env: errorEnvelope{Code: code, Message: message}}
}

func (e *apiError) response() response {
	return jsonResponse(e.status, map[string]errorEnvelope{"error": e.env})
}

func errorResponse(code string, status int) response { return newError(code, status, "").response() }

// pathOf — '/'.ltrim($request->path(), '/') без query (журнал), и $request->path() без ведущего «/» (хэш
// идемпотентности).
func pathOf(r *http.Request) string {
	p := "/" + strings.TrimLeft(r.URL.Path, "/")
	if p != "/" {
		p = strings.TrimRight(p, "/")
	}
	return p
}
