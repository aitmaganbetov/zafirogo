package httpserver

// REST админки (Этап 2, B1): общая часть — права по таблице маршрутов Laravel, тенантная транзакция с
// действиями после коммита, идемпотентность денежных POST, перевод ошибок сервисов в ответы как в Laravel.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/aitmaganbetov/zafirogo/server/internal/activity"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/presence"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/permissions"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/principal"
	"github.com/aitmaganbetov/zafirogo/server/internal/billing"
	"github.com/aitmaganbetov/zafirogo/server/internal/billing/pricing"
	billingsvc "github.com/aitmaganbetov/zafirogo/server/internal/billing/service"
	"github.com/aitmaganbetov/zafirogo/server/internal/billing/session"
	"github.com/aitmaganbetov/zafirogo/server/internal/cash"
	cashsvc "github.com/aitmaganbetov/zafirogo/server/internal/cash/service"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/api"
	"github.com/aitmaganbetov/zafirogo/server/internal/ledger"
	"github.com/aitmaganbetov/zafirogo/server/internal/stock"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// Presence — присутствие агентов клуба (агент B0 подставляет *presence.Store). nil — все ПК «offline».
type Presence interface {
	List(ctx context.Context, clubID int64) ([]presence.State, error)
}

// adminRoute — операция (имя метода strict-сервера) → маршрут Laravel, чьё право (permissions.RouteRights) открывает операцию REST.
// Laravel: app/Http/Middleware/RequireClubArea.php ROUTES.
var adminRoute = map[string]string{
	"GetCashShift":              "cash-register.index",
	"OpenCashShift":             "cash-register.shifts.open",
	"CloseCashShift":            "cash-register.shifts.close",
	"GetCashXReport":            "cash-register.x-report",
	"GetCashZReport":            "cash-register.z-report",
	"IssueZReportNonce":         "cash-register.z-report",
	"MarkZReportPrinted":        "cash-register.z-report.printed",
	"CreateCashSale":            "cash-register.sales.store",
	"CreateBalanceTopUp":        "cash-register.balance-top-ups.store",
	"CreateCashTransaction":     "cash-register.transactions.store",
	"ListCashOperations":        "cash-register.operations",
	"GetCashReceipt":            "cash-register.receipt",
	"RefundCashReceipt":         "cash-register.refund",
	"ListProducts":              "warehouse",
	"ListProductCategories":     "warehouse",
	"CreateProduct":             "warehouse.products.store",
	"UpdateProduct":             "warehouse.products.update",
	"CreateStockMovement":       "warehouse.stock-movements.store",
	"ListCustomers":             "users.index",
	"GetCustomer":               "users.index",
	"ListCustomerLedger":        "users.index",
	"AdjustCustomerBalance":     "users.update",
	"SetCustomerInitialBalance": "users.store",
	"ListTariffs":               "tariffs.index",
	"SellGuestSession":          "club-computers.guest-session",
	"EndComputerSession":        "club-computers.end-sessions",
	"GetClubMapState":           "club-map.state",
}

// rightMessage — текст отказа в праве, если он у маршрута свой (иначе permissions.MsgNoRight).
var rightMessage = map[string]string{
	// CashRegisterController::refund() — abort_unless(canRefund(), 403, …).
	"RefundCashReceipt": cash.MsgNoRefundRight,
}

// routeRights — операция → право (проверяется при сборке роутера: каждое право существует).
var routeRights = func() map[string]string {
	out := make(map[string]string, len(adminRoute))
	for op, route := range adminRoute {
		right := permissions.RightFor(route)
		if right == "" || !permissions.Exists(right) {
			panic("httpserver: no right for route " + route)
		}
		out[op] = right
	}
	return out
}()

// withAdminAuth — strict-middleware операций админки: Principal (401), выбранный клуб и запрет записи в режиме
// просмотра (RequireClub), право маршрута (RequireClubArea). Остальные операции (вход, health) — без изменений.
// Данные запроса журнала (IP, User-Agent) ставит RequestMeta роутера; здесь — страховка, если их нет.
func withAdminAuth(f api.StrictHandlerFunc, operationID string) api.StrictHandlerFunc {
	right, ok := routeRights[operationID]
	if !ok {
		return f
	}
	return func(ctx context.Context, w http.ResponseWriter, r *http.Request, req any) (any, error) {
		p, ok := principal.FromContext(ctx)
		if !ok {
			permissions.Unauthenticated(w)
			return nil, nil
		}
		if status, code, msg, ok := permissions.CheckClub(p, r.Method); !ok {
			permissions.WriteError(w, status, code, msg)
			return nil, nil
		}
		if !p.Can(right) {
			msg := permissions.MsgNoRight
			if m, ok := rightMessage[operationID]; ok {
				msg = m
			}
			permissions.Forbidden(w, msg)
			return nil, nil
		}
		if _, ok := activity.RequestMetaFromContext(ctx); !ok {
			ctx = activity.WithRequestMeta(ctx, activity.RequestMeta{IP: hostOnly(r.RemoteAddr), UserAgent: r.UserAgent()})
		}
		return f(ctx, w, r, req)
	}
}

// ---------------------------------------------------------------------------
// Контекст запроса
// ---------------------------------------------------------------------------

var errAdminNotConfigured = errors.New("admin API is not configured")

func (h *handlers) principal(ctx context.Context) principal.Principal {
	p, _ := principal.FromContext(ctx)
	return p
}

// actor — кто выполняет денежную операцию (ledger, audit_log).
func (h *handlers) actor(ctx context.Context) billing.BalanceActor {
	p := h.principal(ctx)
	a := billing.BalanceActor{UserID: p.UserID, Kind: "staff", RequestID: middleware.GetReqID(ctx)}
	if p.ReadOnly {
		a.Kind = "superadmin"
	}
	if _, r := httpFrom(ctx); r != nil {
		if ip, err := netip.ParseAddr(hostOnly(r.RemoteAddr)); err == nil {
			a.IP = ip.Unmap()
		}
	}
	return a
}

func (h *handlers) now() time.Time {
	if h.opts.Now != nil {
		return h.opts.Now()
	}
	return time.Now()
}

// tx — тенантная транзакция клуба из Principal (auth.TenantTx). Действия после коммита (уведомления агента
// из cash/service) копятся в CommitHooks и выполняются только после успешного COMMIT; при повторе транзакции
// (deadlock) очередь сбрасывается.
func (h *handlers) tx(ctx context.Context, fn func(ctx context.Context, q *store.Queries) error) error {
	if h.opts.Pool == nil {
		return errAdminNotConfigured
	}
	ctx, hooks := billingsvc.WithCommitHooks(ctx)
	err := auth.TenantTx(ctx, h.opts.Pool, func(q *store.Queries) error {
		hooks.Reset()
		return fn(ctx, q)
	})
	if err == nil {
		hooks.Run(context.WithoutCancel(ctx))
	}
	return err
}

// idempotent — денежная операция в транзакции q под ключом Idempotency-Key: повтор с тем же телом возвращает
// сохранённый ответ первого выполнения, с другим — 409 (billing_requests, как покупки времени).
func idempotent[T any](ctx context.Context, q *store.Queries, clubID int64, key, op string, request any, now time.Time, run func() (T, error)) (T, error) {
	var out T
	replayed, err := billingsvc.ClaimRequest(ctx, q, clubID, key, "http:"+op, request, &out, now)
	if err != nil || replayed {
		return out, err
	}
	if out, err = run(); err != nil {
		return out, err
	}
	return out, billingsvc.RememberRequest(ctx, q, clubID, key, out)
}

// ---------------------------------------------------------------------------
// Ошибки
// ---------------------------------------------------------------------------

// apiError — готовый ответ-ошибка: обработчик возвращает его как error, ResponseErrorHandlerFunc пишет.
type apiError struct {
	status int
	body   any
}

func (e *apiError) Error() string { return http.StatusText(e.status) }

func errorBody(code, message string) api.Error {
	e := api.Error{Error: code}
	if message != "" {
		e.Message = &message
	}
	return e
}

func newAPIError(status int, code, message string) *apiError {
	return &apiError{status: status, body: errorBody(code, message)}
}

func notFoundError() *apiError { return newAPIError(http.StatusNotFound, "not_found", "") }

// validation — 422 как ValidationException Laravel: {message: первая ошибка, errors: {поле: [текст]}}.
func validation(field, message string) *apiError {
	return &apiError{status: http.StatusUnprocessableEntity, body: api.ValidationError{
		Error: api.ValidationErrorErrorValidationFailed, Message: message, Errors: map[string][]string{field: {message}},
	}}
}

// formErrors — все ошибки формы сразу, как FormRequest Laravel (поля — в порядке правил).
type formErrors struct {
	first  string
	fields map[string][]string
}

func (f *formErrors) add(field, message string) {
	if f.fields == nil {
		f.fields, f.first = map[string][]string{}, message
	}
	f.fields[field] = append(f.fields[field], message)
}

// addErr — ошибка правила пакета cash (Rule — текст валидатора, иначе текст правила).
func (f *formErrors) addErr(err error) bool {
	if ae := mapError(err); ae != nil {
		if v, ok := ae.body.(api.ValidationError); ok {
			for k, msgs := range v.Errors {
				for _, m := range msgs {
					f.add(k, m)
				}
			}
			return true
		}
	}
	return false
}

func (f *formErrors) err() error {
	if f.fields == nil {
		return nil
	}
	return &apiError{status: http.StatusUnprocessableEntity, body: api.ValidationError{
		Error: api.ValidationErrorErrorValidationFailed, Message: f.first, Errors: f.fields,
	}}
}

// ruleError — ошибка правила формы с текстом валидатора Laravel.
func ruleError(field, rule string) *apiError { return validation(field, ruleMessage(field, rule, "")) }

// ruleErrorWith — то же с явным значением :min/:max/:value (правило формы, отличное от общего ruleParams).
func ruleErrorWith(field, rule, param string) *apiError {
	return validation(field, ruleMessage(field, rule, param))
}

// writeAPIError — ResponseErrorHandlerFunc: ошибки сервисов → статус и тело как в старом коде.
func writeAPIError(log *slog.Logger) func(w http.ResponseWriter, r *http.Request, err error) {
	return func(w http.ResponseWriter, r *http.Request, err error) {
		ae := mapError(err)
		if ae == nil {
			log.ErrorContext(r.Context(), "handler error", slog.String("err", err.Error()))
			ae = newAPIError(http.StatusInternalServerError, "internal_error", "")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(ae.status)
		_ = json.NewEncoder(w).Encode(ae.body)
	}
}

// mapError — ошибка домена → ответ; nil — неизвестная ошибка (500).
func mapError(err error) *apiError {
	var (
		ae  *apiError
		ce  *cash.Error
		se  *stock.Error
		be  *billingsvc.Error
		pe  *pricing.Error
		sse *session.Error
		bae *billing.BalanceError
		fe  *billingsvc.ForbiddenError
	)
	switch {
	case errors.As(err, &ae):
		return ae
	case errors.As(err, &ce):
		if ce.Rule != "" {
			return ruleError(ce.Field, ce.Rule)
		}
		return validation(ce.Field, ce.Text())
	case errors.As(err, &se):
		if se.Rule != "" {
			return ruleError(se.Field, se.Rule)
		}
		return validation(se.Field, se.Message)
	case errors.As(err, &be):
		return validation(be.Field, substitute(be.Message, be.Params))
	case errors.As(err, &pe):
		return validation(pe.Field, substitute(pe.Message, pe.Params))
	case errors.As(err, &sse):
		return validation(sse.Field, sse.Message)
	case errors.As(err, &bae):
		return validation(bae.Field, substitute(bae.Message, bae.Params))
	case errors.As(err, &fe):
		return newAPIError(http.StatusForbidden, permissions.CodeForbidden, fe.Message)
	case errors.Is(err, cashsvc.ErrNotFound), errors.Is(err, billingsvc.ErrNotFound), errors.Is(err, stock.ErrProductNotFound),
		errors.Is(err, ledger.ErrBalanceNotFound):
		return notFoundError()
	case errors.Is(err, ledger.ErrIdempotencyConflict), errors.Is(err, billingsvc.ErrIdempotencyConflict):
		return newAPIError(http.StatusConflict, "idempotency_conflict", "")
	case errors.Is(err, billingsvc.ErrInvalidInput):
		return newAPIError(http.StatusBadRequest, "bad_request", "")
	case errors.Is(err, auth.ErrNoClub):
		return newAPIError(http.StatusForbidden, permissions.CodeForbidden, permissions.MsgNoClubs)
	case errors.Is(err, errAdminNotConfigured):
		return newAPIError(http.StatusServiceUnavailable, "service_unavailable", "")
	}
	return nil
}

// substitute — подстановки :name (как makeReplacements Laravel: длинные ключи раньше коротких).
func substitute(msg string, params map[string]string) string {
	return (&cash.Error{Message: msg, Params: params}).Text()
}

// ---------------------------------------------------------------------------
// Тексты валидатора Laravel (lang/en/validation.php; русский перевод — в каталоге переводов по этим ключам)
// ---------------------------------------------------------------------------

var ruleTemplates = map[string]string{
	cash.RuleRequired: "The :attribute field is required.",
	cash.RuleNumeric:  "The :attribute field must be a number.",
	cash.RuleInteger:  "The :attribute field must be an integer.",
	cash.RuleMin:      "The :attribute field must be at least :min.",
	cash.RuleMax:      "The :attribute field must not be greater than :max.",
	cash.RuleGt:       "The :attribute field must be greater than :value.",
	cash.RuleIn:       "The selected :attribute is invalid.",
	cash.RuleDistinct: "The :attribute field has a duplicate value.",
	// Решение Q-002 (docs/DECISIONS.md): > 2 знаков после точки — 'decimal:0,2' Laravel (:decimal = "0-2").
	cash.RuleDecimal: "The :attribute field must have :decimal decimal places.",
	"exists":         "The selected :attribute is invalid.",
	"unique":         "The :attribute has already been taken.",
	"max_string":     "The :attribute field must not be greater than :max characters.",
	"string":         "The :attribute field must be a string.",
	"prohibited":     "The :attribute field is prohibited.",
}

// ruleParams — значения :min/:max/:value правил форм старого кода (FormRequest rules()).
var ruleParams = map[string]string{
	"opening_balance|min":    "0",
	"opening_balance|max":    "99999999999999.99", // decimal(16,2) (CQ-2): в старом — ошибка MySQL
	"amount|gt":              "0",
	"amount|max":             "9999999999.99", // StoreBalanceTopUpRequest
	"quantity|min":           "1",
	"quantity|max":           "1000000",
	"price|min":              "0",
	"price|max":              "9999999999.99",
	"sku|max_string":         "100",
	"name|max_string":        "150",
	"top_up_amount|min":      "0.01",
	"top_up_amount|max":      "10000000",
	"minutes|min":            "1",
	"minutes|max":            "720",
	"computer_ids|max":       "50",
	"description|max_string": "255",
	"note|max_string":        "255",
	"balance|min":            "0",
	"balance|max":            "9999999999.99",
	"bonus_balance|min":      "0",
	"bonus_balance|max":      "9999999999.99",
}

// ruleMessage — текст правила для поля (attribute — имя поля с пробелами вместо «_», как Laravel).
func ruleMessage(field, rule, param string) string {
	tpl, ok := ruleTemplates[rule]
	if !ok {
		tpl = "The :attribute field is invalid."
	}
	base := field
	if i := strings.LastIndex(field, "."); i >= 0 {
		base = field[i+1:]
	}
	if param == "" {
		param = ruleParams[base+"|"+rule]
	}
	if rule == cash.RuleMax {
		if p, ok := ruleParams[base+"|max_string"]; ok && param == "" {
			param, tpl = p, ruleTemplates["max_string"]
		}
	}
	msg := strings.ReplaceAll(tpl, ":attribute", strings.ReplaceAll(field, "_", " "))
	msg = strings.ReplaceAll(msg, ":decimal", "0-2") // replaceDecimal(): 'decimal:0,2' → "0-2"
	for _, k := range []string{":min", ":max", ":value"} {
		msg = strings.ReplaceAll(msg, k, param)
	}
	return msg
}

// ---------------------------------------------------------------------------
// Пагинация
// ---------------------------------------------------------------------------

const (
	defaultPageSize = 25 // paginate(25) старого кода
	maxPageSize     = 100
)

func pageSize(limit *int) int32 {
	if limit == nil || *limit <= 0 {
		return defaultPageSize
	}
	if *limit > maxPageSize {
		return maxPageSize
	}
	return int32(*limit) //nolint:gosec // ограничено maxPageSize
}

// encodeCursor / decodeCursor — непрозрачный курсор keyset-пагинации (JSON в base64url).
func encodeCursor(v any) *string {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	s := base64.RawURLEncoding.EncodeToString(b)
	return &s
}

func decodeCursor(s *string, v any) error {
	if s == nil || *s == "" {
		return nil
	}
	b, err := base64.RawURLEncoding.DecodeString(*s)
	if err != nil || json.Unmarshal(b, v) != nil {
		return newAPIError(http.StatusBadRequest, "bad_cursor", "")
	}
	return nil
}

// likePattern — шаблон ILIKE для подстроки (экранирование %, _ и \).
func likePattern(q *string) string {
	if q == nil {
		return ""
	}
	s := strings.TrimSpace(*q)
	if s == "" {
		return ""
	}
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + r.Replace(s) + "%"
}

// ---------------------------------------------------------------------------
// Разное
// ---------------------------------------------------------------------------

func ptr[T any](v T) *T { return &v }
