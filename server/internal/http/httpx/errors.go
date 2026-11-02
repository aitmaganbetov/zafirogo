package httpx

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"

	"github.com/aitmaganbetov/zafirogo/server/internal/auth"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/permissions"
	"github.com/aitmaganbetov/zafirogo/server/internal/billing"
	"github.com/aitmaganbetov/zafirogo/server/internal/billing/pricing"
	billingsvc "github.com/aitmaganbetov/zafirogo/server/internal/billing/service"
	"github.com/aitmaganbetov/zafirogo/server/internal/billing/session"
	"github.com/aitmaganbetov/zafirogo/server/internal/cash"
	cashsvc "github.com/aitmaganbetov/zafirogo/server/internal/cash/service"
	apicommon "github.com/aitmaganbetov/zafirogo/server/internal/http/api/common"
	"github.com/aitmaganbetov/zafirogo/server/internal/ledger"
	"github.com/aitmaganbetov/zafirogo/server/internal/stock"
)

// ErrNotConfigured — у раздела нет нужного сервиса (режим без зависимостей): 503.
var ErrNotConfigured = errors.New("admin API is not configured")

// APIError — готовый ответ-ошибка: обработчик возвращает его как error, ResponseError пишет.
type APIError struct {
	Status int
	Body   any
	// msgs — тексты тела с подстановками → ключ и параметры (перевод на язык клуба, i18n.go).
	msgs map[string]Msg
}

func (e *APIError) Error() string { return http.StatusText(e.Status) }

// Write пишет ответ.
func (e *APIError) Write(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(e.Status)
	_ = json.NewEncoder(w).Encode(e.Body)
}

// ErrorBody — тело {error, message?} (apicommon.Error).
func ErrorBody(code, message string) apicommon.Error {
	e := apicommon.Error{Error: code}
	if message != "" {
		e.Message = &message
	}
	return e
}

// NewAPIError — ошибка со статусом, машинным кодом и текстом (русский текст = ключ перевода).
func NewAPIError(status int, code, message string) *APIError {
	return &APIError{Status: status, Body: ErrorBody(code, message)}
}

// NotFound — 404 not_found.
func NotFound() *APIError { return NewAPIError(http.StatusNotFound, "not_found", "") }

// Validation — 422 как ValidationException Laravel: {message: первая ошибка, errors: {поле: [текст]}}.
func Validation(field, message string) *APIError {
	return &APIError{Status: http.StatusUnprocessableEntity, Body: apicommon.ValidationError{
		Error: apicommon.ValidationErrorErrorValidationFailed, Message: message, Errors: map[string][]string{field: {message}},
	}}
}

// FormErrors — все ошибки формы сразу, как FormRequest Laravel (поля — в порядке правил).
type FormErrors struct {
	first  string
	fields map[string][]string
	msgs   map[string]Msg
}

// Add добавляет ошибку поля.
func (f *FormErrors) Add(field, message string) {
	if f.fields == nil {
		f.fields, f.first = map[string][]string{}, message
	}
	f.fields[field] = append(f.fields[field], message)
}

// Has — есть ли ошибка поля.
func (f *FormErrors) Has(field string) bool { return len(f.fields[field]) > 0 }

// AddErr — ошибка правила доменного пакета (MapError даёт 422) добавляется в форму; false — не ошибка формы.
func (f *FormErrors) AddErr(err error) bool {
	if ae := MapError(err); ae != nil {
		if v, ok := ae.Body.(apicommon.ValidationError); ok {
			for k, msgs := range v.Errors {
				for _, m := range msgs {
					f.Add(k, m)
				}
			}
			for text, m := range ae.msgs {
				if f.msgs == nil {
					f.msgs = map[string]Msg{}
				}
				f.msgs[text] = m
			}
			return true
		}
	}
	return false
}

// Err — 422 со всеми ошибками или nil.
func (f *FormErrors) Err() error {
	if f.fields == nil {
		return nil
	}
	return &APIError{Status: http.StatusUnprocessableEntity, Body: apicommon.ValidationError{
		Error: apicommon.ValidationErrorErrorValidationFailed, Message: f.first, Errors: f.fields,
	}, msgs: f.msgs}
}

// RuleError — ошибка правила формы с текстом валидатора Laravel (RuleMessage).
func RuleError(field, rule string) *APIError { return Validation(field, RuleMessage(field, rule, "")) }

// RuleErrorWith — то же с явным значением :min/:max/:value.
func RuleErrorWith(field, rule, param string) *APIError {
	return Validation(field, RuleMessage(field, rule, param))
}

// MapError — ошибка домена → ответ; nil — неизвестная ошибка (500). Ошибки нового домена раздел переводит сам
// (ResponseError(extra...)) или добавляет сюда, если домен общий.
func MapError(err error) *APIError {
	var (
		ae  *APIError
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
			return RuleError(ce.Field, ce.Rule)
		}
		return ValidationMsg(ce.Field, ce.Message, ce.Params)
	case errors.As(err, &se):
		if se.Rule != "" {
			return RuleError(se.Field, se.Rule)
		}
		return Validation(se.Field, se.Message)
	case errors.As(err, &be):
		return ValidationMsg(be.Field, be.Message, be.Params)
	case errors.As(err, &pe):
		return ValidationMsg(pe.Field, pe.Message, pe.Params)
	case errors.As(err, &sse):
		return Validation(sse.Field, sse.Message)
	case errors.As(err, &bae):
		return ValidationMsg(bae.Field, bae.Message, bae.Params)
	case errors.As(err, &fe):
		return NewAPIError(http.StatusForbidden, permissions.CodeForbidden, fe.Message)
	case errors.Is(err, cashsvc.ErrNotFound), errors.Is(err, billingsvc.ErrNotFound), errors.Is(err, stock.ErrProductNotFound),
		errors.Is(err, ledger.ErrBalanceNotFound):
		return NotFound()
	case errors.Is(err, ledger.ErrIdempotencyConflict), errors.Is(err, billingsvc.ErrIdempotencyConflict):
		return NewAPIError(http.StatusConflict, "idempotency_conflict", "")
	case errors.Is(err, billingsvc.ErrInvalidInput):
		return NewAPIError(http.StatusBadRequest, "bad_request", "")
	case errors.Is(err, auth.ErrNoClub):
		return NewAPIError(http.StatusForbidden, permissions.CodeForbidden, permissions.MsgNoClubs)
	case errors.Is(err, ErrNotConfigured):
		return NewAPIError(http.StatusServiceUnavailable, "service_unavailable", "")
	}
	return nil
}

// Substitute — подстановки :name (как makeReplacements Laravel: длинные ключи раньше коротких).
func Substitute(msg string, params map[string]string) string {
	return (&cash.Error{Message: msg, Params: params}).Text()
}

// WriteError — {"error": code} со статусом (ответы вне strict-сервера: 404 /api/*, panic).
func WriteError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
}

// HostOnly — хост из "host:port" (r.RemoteAddr после RealIP).
func HostOnly(addr string) string {
	if h, _, err := net.SplitHostPort(addr); err == nil {
		return h
	}
	return addr
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
	"min_string":     "The :attribute field must be at least :min characters.",
	"string":         "The :attribute field must be a string.",
	"prohibited":     "The :attribute field is prohibited.",
	"confirmed":      "The :attribute field confirmation does not match.",
	"regex":          "The :attribute field format is invalid.",
	// API v1 (lang/en/validation.php): массивы, boolean, даты.
	"array":          "The :attribute field must be an array.",
	"max_array":      "The :attribute field must not have more than :max items.",
	"min_array":      "The :attribute field must have at least :min items.",
	"boolean":        "The :attribute field must be true or false.",
	"date":           "The :attribute field must be a valid date.",
	"after_or_equal": "The :attribute field must be a date after or equal to :date.",
}

// ruleParams — значения :min/:max/:value правил форм старого кода (FormRequest rules()). Разделы с другими
// значениями передают их явно (RuleErrorWith).
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

// RuleMessage — текст правила для поля (attribute — имя поля с пробелами вместо «_», как Laravel).
func RuleMessage(field, rule, param string) string {
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
	key := rule // правило для перевода (validation.<key>)
	if rule == cash.RuleMax {
		if p, ok := ruleParams[base+"|max_string"]; ok && param == "" {
			param, tpl, key = p, ruleTemplates["max_string"], "max_string"
		}
	}
	msg := strings.ReplaceAll(tpl, ":attribute", strings.ReplaceAll(field, "_", " "))
	msg = strings.ReplaceAll(msg, ":decimal", "0-2") // replaceDecimal(): 'decimal:0,2' → "0-2"
	for _, k := range []string{":min", ":max", ":value", ":date"} {
		msg = strings.ReplaceAll(msg, k, param)
	}
	rememberRule(msg, field, key, param)
	return msg
}
