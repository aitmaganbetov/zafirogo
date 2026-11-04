package httpx

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"reflect"
	"sort"

	"github.com/aitmaganbetov/zafirogo/server/internal/activity"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/permissions"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/principal"
)

// HandlerFunc — тип strict-обработчика oapi-codegen (у каждого сгенерированного пакета свой именованный тип
// с тем же устройством; Middlewares приводит их).
type HandlerFunc func(ctx context.Context, w http.ResponseWriter, r *http.Request, request any) (any, error)

// Middleware — strict-middleware: обработчик операции operationID.
type Middleware func(f HandlerFunc, operationID string) HandlerFunc

// Access — правила доступа операций раздела.
type Access struct {
	// Routes — операция (имя метода StrictServerInterface) → имя маршрута Laravel (routes/web.php), чьё право
	// (permissions.RightFor, таблица RequireClubArea::ROUTES) открывает операцию. Операция в Routes требует
	// вход, выбранный клуб (запись — не в режиме просмотра) и право.
	Routes map[string]string
	// Messages — свой текст отказа в праве (иначе permissions.MsgNoRight).
	Messages map[string]string
	// Public — операции без проверки здесь (вход, health, публичные страницы): права проверяет сам обработчик.
	Public []string
}

// rights — операция → право; паника при сборке роутера, если у маршрута Laravel нет права.
func (a Access) rights() map[string]string {
	out := make(map[string]string, len(a.Routes))
	for op, route := range a.Routes {
		right := permissions.RightFor(route)
		if right == "" || !permissions.Exists(right) {
			panic("httpx: no right for route " + route)
		}
		out[op] = right
	}
	return out
}

// Uncovered — операции интерфейса iface (StrictServerInterface раздела), которых нет ни в Routes, ни в Public,
// и лишние имена в Routes/Public. Пусто — всё закрыто. Для теста раздела:
//
//	if bad := access.Uncovered(reflect.TypeFor[cashapi.StrictServerInterface]()); len(bad) > 0 { t.Fatal(bad) }
func (a Access) Uncovered(iface reflect.Type) []string {
	var bad []string
	public := map[string]bool{}
	for _, op := range a.Public {
		public[op] = true
	}
	for i := range iface.NumMethod() {
		name := iface.Method(i).Name
		if _, ok := a.Routes[name]; !ok && !public[name] {
			bad = append(bad, "no right: "+name)
		}
	}
	for op := range a.Routes {
		if _, ok := iface.MethodByName(op); !ok {
			bad = append(bad, "unknown operation in Routes: "+op)
		}
	}
	for op := range public {
		if _, ok := iface.MethodByName(op); !ok {
			bad = append(bad, "unknown operation in Public: "+op)
		}
	}
	sort.Strings(bad)
	return bad
}

// Middlewares — strict-middleware раздела: w/r в контексте (HTTPFrom) и права (Access). Использование:
//
//	mws := httpx.Middlewares[cashapi.StrictMiddlewareFunc](access)
//	si := cashapi.NewStrictHandlerWithOptions(h, mws, cashapi.StrictHTTPServerOptions{...})
func Middlewares[M ~func(H, string) H, H ~func(context.Context, http.ResponseWriter, *http.Request, any) (any, error)](a Access) []M {
	adapt := func(m Middleware) M {
		return M(func(f H, op string) H { return H(m(HandlerFunc(f), op)) })
	}
	return []M{adapt(withHTTP), adapt(adminAuth(a))}
}

// withHTTP — strict-middleware: кладёт w и r в контекст обработчика.
func withHTTP(f HandlerFunc, _ string) HandlerFunc {
	return func(ctx context.Context, w http.ResponseWriter, r *http.Request, req any) (any, error) {
		return f(WithHTTPContext(ctx, w, r), w, r, req)
	}
}

// adminAuth — Principal (401), выбранный клуб и запрет записи в режиме просмотра (RequireClub), право маршрута
// (RequireClubArea). Операции не из Routes — без изменений. Данные журнала (IP, User-Agent) ставит RequestMeta
// роутера; здесь — страховка.
func adminAuth(a Access) Middleware {
	rights := a.rights()
	return func(f HandlerFunc, operationID string) HandlerFunc {
		right, ok := rights[operationID]
		if !ok {
			return f
		}
		msg := permissions.MsgNoRight
		if m, ok := a.Messages[operationID]; ok {
			msg = m
		}
		return func(ctx context.Context, w http.ResponseWriter, r *http.Request, req any) (any, error) {
			p, ok := principal.FromContext(ctx)
			if !ok {
				permissions.Unauthenticated(w)
				return nil, nil
			}
			if status, code, m, ok := permissions.CheckClub(p, r.Method); !ok {
				permissions.WriteError(w, status, code, m)
				return nil, nil
			}
			if !p.Can(right) {
				permissions.Forbidden(w, msg)
				return nil, nil
			}
			if _, ok := activity.RequestMetaFromContext(ctx); !ok {
				ctx = activity.WithRequestMeta(ctx, activity.RequestMeta{IP: HostOnly(r.RemoteAddr), UserAgent: r.UserAgent()})
			}
			return f(ctx, w, r, req)
		}
	}
}

// RequestError — RequestErrorHandlerFunc strict-сервера: тело не разобралось — 400 bad_request.
func (d *Deps) RequestError(w http.ResponseWriter, r *http.Request, err error) {
	d.Log().WarnContext(r.Context(), "bad request", slog.String("err", err.Error()))
	WriteError(w, http.StatusBadRequest, "bad_request")
}

// ParamError — ErrorHandlerFunc chi-обёртки: ошибки параметров (нет Idempotency-Key, неверный id в пути) — 400
// до прав и тела.
func (d *Deps) ParamError(w http.ResponseWriter, r *http.Request, err error) {
	code := "bad_request"
	if missingHeader(err, "Idempotency-Key") {
		code = "idempotency_key_required"
	}
	d.Log().WarnContext(r.Context(), "bad request", slog.String("err", err.Error()))
	WriteError(w, http.StatusBadRequest, code)
}

// missingHeader — err (или обёрнутая) — *RequiredHeaderError сгенерированного пакета с ParamName == name.
// Тип свой в каждом пакете раздела, поэтому по имени типа и полю.
func missingHeader(err error, name string) bool {
	for e := err; e != nil; e = errors.Unwrap(e) {
		v := reflect.ValueOf(e)
		if v.Kind() != reflect.Pointer || v.IsNil() || v.Elem().Kind() != reflect.Struct {
			continue
		}
		if v.Elem().Type().Name() != "RequiredHeaderError" {
			continue
		}
		if f := v.Elem().FieldByName("ParamName"); f.IsValid() && f.Kind() == reflect.String && f.String() == name {
			return true
		}
	}
	return false
}

// ResponseError — ResponseErrorHandlerFunc strict-сервера: ошибки сервисов → статус и тело как в старом коде
// (MapError и мапперы раздела extra; неизвестная ошибка — 500 без подробностей).
func (d *Deps) ResponseError(extra ...func(error) *APIError) func(w http.ResponseWriter, r *http.Request, err error) {
	return func(w http.ResponseWriter, r *http.Request, err error) {
		var ae *APIError
		for _, m := range extra {
			if ae = m(err); ae != nil {
				break
			}
		}
		if ae == nil {
			ae = MapError(err)
		}
		if ae == nil {
			d.Log().ErrorContext(r.Context(), "handler error", slog.String("err", err.Error()))
			ae = NewAPIError(http.StatusInternalServerError, "internal_error", "")
		}
		if d.I18n != nil && ClubID(r.Context()) > 0 {
			ae = ae.localized(r.Context(), d.I18n, d.Locale(r.Context()))
		}
		ae.Write(w)
	}
}
