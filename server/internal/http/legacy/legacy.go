// Package legacy — слой совместимости со старым REST Laravel для мобильных приложений Flutter, которые не меняются
// (решение Q-058, docs/DECISIONS.md): mobileclient (гостевые маршруты routes/api.php) и mobileemployee
// (routes/api_staff.php). Те же пути (/api/...), методы, параметры, JSON ответов и ошибок ({message, errors}),
// коды HTTP и лимиты throttle:*; аутентификация — токены Sanctum «<id>|<токен>» (таблица mobile_api_tokens, 0024).
//
// Всё — поверх новых сервисов (cash/service, billing/service, clubmap, friends, shop, stock, chat): здесь только
// разбор запроса в старом виде и ответ в старом формате (суммы — строки "1500.00" или float тенге, как отдавал
// старый API). Карта «вызов приложения → маршрут → сервис» и порядок перехода клубов — docs/mobile-compat.md.
//
// Маршрутизация по клубу при поклубном переходе (docs/mobile-compat.md §3): приложение знает один адрес. Если задан
// Upstream (старый Laravel), запрос, который не относится к клубу этого сервера (токен не найден в
// mobile_api_tokens; телефон входа без членства в клубах этого сервера; вход в клуб каталога, которого здесь нет),
// уходит туда без изменений (обратный прокси); иначе обслуживается здесь. Каталог клубов (/api/catalog/*, до
// входа) объединяет клубы обоих серверов (catalog.go, решение UQ-2).
package legacy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aitmaganbetov/zafirogo/server/internal/auth/password"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/ratelimit"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
	"github.com/aitmaganbetov/zafirogo/server/internal/money"
	"github.com/aitmaganbetov/zafirogo/server/internal/verifycode"
)

// DefaultTokenTTL — срок токена: AuthController::issueUserLoginResponse() / switchClub(): now()->addDays(30).
const DefaultTokenTTL = 30 * 24 * time.Hour

// maxBody — тело запроса приложения (JSON); больше — 413 (старый nginx/PHP отклонял бы раньше).
const maxBody = 1 << 20

// PasswordRecovery — восстановление пароля по коду в Telegram (PasswordRecovery::sendCode()/reset(),
// verifycode.PasswordRecovery). Ошибки правил — *verifycode.Error (422), остальные — 500. Reset возвращает id аккаунта: его токены отзываются.
type PasswordRecovery interface {
	SendCode(ctx context.Context, phone string) error
	Reset(ctx context.Context, phone, code, newPassword string) (userID int64, err error)
}

// TransferCodes — код подтверждения перевода другу в Telegram (FriendTransferCodes::send(), verifycode.TransferCodes).
// amount — Friends::amountToCents() ввода (money.ParseTransferAmount). Ошибки правил — *verifycode.Error (422).
type TransferCodes interface {
	Send(ctx context.Context, clubID, userID, friendshipID int64, amount money.Tiyn) error
}

// FieldError — ошибка правила (ValidationException::withMessages([Field => Message])) от внешних сервисов.
type FieldError struct {
	Field   string
	Message string
	Params  map[string]string
}

func (e *FieldError) Error() string { return e.Field + ": " + e.Message }

// Options — зависимости слоя (кроме общих httpx.Deps).
type Options struct {
	// Limiter — лимиты throttle:* (customer-login, password-recovery, staff-app, api.guest.*, …). nil — без лимитов.
	Limiter *ratelimit.Limiter
	// Passwords — проверка пароля (Argon2id и старые bcrypt) при входе и переводе.
	Passwords *password.Hasher
	// Upstream — старый Laravel для клубов, ещё не переехавших (http://laravel.internal). nil — все клубы здесь.
	Upstream *url.URL
	// UpstreamHost — заголовок Host для Upstream (пусто — Host исходного запроса).
	UpstreamHost string
	// PublicBaseURL — адрес сайта для картинок (новости, товары): https://zafiro.kz.
	PublicBaseURL string
	// TokenTTL — срок токена (0 — DefaultTokenTTL).
	TokenTTL time.Duration
	// Recovery и TransferCodes — Telegram (H2). nil — ответ старого кода «через Telegram пока не подключено».
	Recovery      PasswordRecovery
	TransferCodes TransferCodes
	Log           *slog.Logger
}

// API — обработчик старых маршрутов мобильных приложений.
type API struct {
	o     Options
	d     *httpx.Deps
	inner http.Handler
	mux   chi.Router
	proxy *httputil.ReverseProxy
	// catalog — кэш ответов каталога клубов (catalog.go); upstreamClient — запрос каталога старого сервера.
	catalog        catalogCache
	upstreamClient *http.Client
}

// New собирает слой. d — общие зависимости разделов REST; inner — корневой роутер сервера (для вызова нового
// REST изнутри, как у уведомлений, чей список собирает раздел notifications).
func New(o Options, d *httpx.Deps, inner http.Handler) *API {
	if o.TokenTTL <= 0 {
		o.TokenTTL = DefaultTokenTTL
	}
	if o.Log == nil {
		o.Log = d.Log()
	}
	if o.Recovery == nil && d.PasswordRecovery != nil {
		o.Recovery = d.PasswordRecovery
	}
	a := &API{o: o, d: d, inner: inner, upstreamClient: &http.Client{Timeout: catalogUpstreamTimeout}}
	if o.Upstream != nil {
		up, host := o.Upstream, o.UpstreamHost
		a.proxy = &httputil.ReverseProxy{
			Rewrite: func(pr *httputil.ProxyRequest) {
				pr.SetURL(up)
				pr.SetXForwarded()
				if host != "" {
					pr.Out.Host = host
				} else {
					pr.Out.Host = pr.In.Host
				}
			},
			ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
				o.Log.WarnContext(r.Context(), "legacy upstream", slog.String("err", err.Error()))
				writeMessage(w, http.StatusBadGateway, "Server Error")
			},
		}
	}
	a.mux = a.routes()
	return a
}

type internalKey struct{}

// Internal помечает запрос, который слой сам отправляет в новый REST (его Match не перехватывает).
func Internal(ctx context.Context) context.Context {
	return context.WithValue(ctx, internalKey{}, true)
}

var sanctumRe = regexp.MustCompile(`^([0-9]{1,19})\|([A-Za-z0-9]{1,128})$`)

// bearer — «<id>|<токен>» из Authorization: Bearer (только формат Sanctum).
func bearer(r *http.Request) (id int64, plain string, ok bool) {
	h := r.Header.Get("Authorization")
	if len(h) < 7 || !strings.EqualFold(h[:7], "bearer ") {
		return 0, "", false
	}
	m := sanctumRe.FindStringSubmatch(strings.TrimSpace(h[7:]))
	if m == nil {
		return 0, "", false
	}
	id, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil || id <= 0 {
		return 0, "", false
	}
	return id, m[2], true
}

// Пути, которых нет у нового REST: только старый контракт.
var legacyOnly = map[string]bool{
	"/api/auth/password/code":  true,
	"/api/auth/password/reset": true,
	"/api/auth/clubs":          true,
	"/api/auth/switch-club":    true,
}

// Match — запрос старого контракта мобильных приложений. Пути /api/auth/login, /api/auth/me и /api/auth/logout
// есть и у панели: вход приложения отличается полем device_name (у формы панели его нет —
// additionalProperties: false), me/logout — токеном формата Sanctum.
func (a *API) Match(r *http.Request) bool {
	if v, _ := r.Context().Value(internalKey{}).(bool); v {
		return false
	}
	p := r.URL.Path
	switch {
	case strings.HasPrefix(p, "/api/staff/"), strings.HasPrefix(p, "/api/guest/"), strings.HasPrefix(p, "/api/catalog/"), legacyOnly[p]:
		return true
	case p == "/api/auth/me" || p == "/api/auth/logout":
		_, _, ok := bearer(r)
		return ok
	case p == "/api/auth/login" && r.Method == http.MethodPost:
		body := peekBody(r)
		var v map[string]json.RawMessage
		if json.Unmarshal(body, &v) != nil {
			return false
		}
		_, ok := v["device_name"]
		return ok
	}
	return false
}

// readBodyKeep — readBody, но тело остаётся на месте (запрос может уйти на старый сервер как есть).
func readBodyKeep(r *http.Request) body {
	raw := peekBody(r)
	b := readBody(r)
	r.Body = io.NopCloser(bytes.NewReader(raw))
	return b
}

// peekBody читает тело (до maxBody) и возвращает его на место.
func peekBody(r *http.Request) []byte {
	if r.Body == nil {
		return nil
	}
	b, _ := io.ReadAll(io.LimitReader(r.Body, maxBody))
	r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(b), r.Body))
	return b
}

// ServeHTTP — маршруты старого контракта (Match уже проверен).
func (a *API) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.mux.ServeHTTP(w, r)
}

// Middleware — перехват запросов старого контракта до аутентификации панели (httpserver.Options.Legacy).
func (a *API) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.Match(r) {
			a.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ---------------------------------------------------------------------------
// Ответы в формате Laravel
// ---------------------------------------------------------------------------

// marshalJSON — json_encode ответа Laravel: без экранирования HTML, с переводом строки в конце (json.Encoder).
func marshalJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// writeRawJSON — готовое JSON-тело с заголовками ответа Laravel.
func writeRawJSON(w http.ResponseWriter, status int, body []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache, private")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	body, _ := marshalJSON(v)
	writeRawJSON(w, status, body)
}

// writeMessage — {"message": "..."} (HttpException, abort()).
func writeMessage(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"message": msg})
}

// writeCode — {"message": "...", "code": "..."} (ответы контроллеров с машинным кодом).
func writeCode(w http.ResponseWriter, status int, msg, code string) {
	writeJSON(w, status, map[string]string{"message": msg, "code": code})
}

// validationBody — тело ValidationException: message = первая ошибка (+ «(and N more errors)», как
// ValidationException::summarize()), errors — поле → тексты.
type validationBody struct {
	Message string              `json:"message"`
	Errors  map[string][]string `json:"errors"`
}

func summarize(first string, errs map[string][]string) string {
	n := -1
	for _, v := range errs {
		n += len(v)
	}
	switch {
	case n <= 0:
		return first
	case n == 1:
		return first + " (and 1 more error)"
	}
	return first + " (and " + strconv.Itoa(n) + " more errors)"
}

func writeValidation(w http.ResponseWriter, field, msg string) {
	errs := map[string][]string{field: {msg}}
	writeJSON(w, http.StatusUnprocessableEntity, validationBody{Message: msg, Errors: errs})
}

// msgUnauthenticated — AuthenticationException в JSON.
const msgUnauthenticated = "Unauthenticated."

// modelNotFound — ModelNotFoundException → NotFoundHttpException("No query results for model [X] id").
func modelNotFound(w http.ResponseWriter, model string, id string) {
	writeMessage(w, http.StatusNotFound, "No query results for model [App\\Models\\"+model+"] "+id)
}

// pathID — ключ модели {param} из пути: не положительное целое — 404 модели (route model binding); ok=false — ответ
// записан. s — ключ как в пути (для текста 404).
func pathID(w http.ResponseWriter, r *http.Request, param, model string) (id int64, s string, ok bool) {
	s = chi.URLParam(r, param)
	id, err := strconv.ParseInt(s, 10, 64)
	if err != nil || id <= 0 {
		modelNotFound(w, model, s)
		return 0, s, false
	}
	return id, s, true
}

// writeError — ошибка сервиса в формате Laravel. Ответ строит общий httpx.ResponseError (тексты старого кода,
// перевод на язык клуба), затем тело {error, message, errors} приводится к {message, errors}.
func (a *API) writeError(w http.ResponseWriter, r *http.Request, err error) {
	var fe *FieldError
	if errors.As(err, &fe) {
		writeValidation(w, fe.Field, a.t(r, fe.Message, fe.Params))
		return
	}
	var ve *verifycode.Error
	if errors.As(err, &ve) {
		writeValidation(w, ve.Field, a.t(r, ve.Key, ve.Params))
		return
	}
	status, body := a.renderError(r, err)
	switch {
	case status == http.StatusUnprocessableEntity && len(body.Errors) > 0:
		writeJSON(w, status, validationBody{Message: summarize(body.Message, body.Errors), Errors: body.Errors})
	case status == http.StatusNotFound:
		writeMessage(w, status, "Not Found")
	case status >= 500:
		writeMessage(w, status, "Server Error")
	default:
		writeMessage(w, status, body.Message)
	}
}

// httpxErrorBody — нужное из тела ошибки общего слоя ({error, message, errors}); нет message — "".
type httpxErrorBody struct {
	Message string              `json:"message"`
	Errors  map[string][]string `json:"errors"`
}

// renderError — ответ общего httpx.ResponseError на ошибку (тексты старого кода на языке клуба): статус и тело.
func (a *API) renderError(r *http.Request, err error) (int, httpxErrorBody) {
	rec := &bufferWriter{h: http.Header{}}
	a.d.ResponseError()(rec, r, err)
	var body httpxErrorBody
	_ = json.Unmarshal(rec.buf.Bytes(), &body)
	return rec.status, body
}

// bufferWriter — ответ в памяти (ошибки httpx и вызовы нового REST изнутри).
type bufferWriter struct {
	h      http.Header
	buf    bytes.Buffer
	status int
}

func (b *bufferWriter) Header() http.Header { return b.h }
func (b *bufferWriter) Write(p []byte) (int, error) {
	if b.status == 0 {
		b.status = http.StatusOK
	}
	return b.buf.Write(p)
}
func (b *bufferWriter) WriteHeader(s int) {
	if b.status == 0 {
		b.status = s
	}
}

// ---------------------------------------------------------------------------
// Разбор тела
// ---------------------------------------------------------------------------

// body — JSON-тело запроса как в Laravel ($request->input): числа — json.Number (точный литерал).
type body map[string]any

func readBody(r *http.Request) body {
	if r.Body == nil {
		return body{}
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, maxBody))
	dec.UseNumber()
	var v map[string]any
	if dec.Decode(&v) != nil || v == nil {
		v = map[string]any{}
	}
	// Параметры строки запроса тоже входят в $request->input() (тело важнее).
	for k, vals := range r.URL.Query() {
		if _, ok := v[k]; !ok && len(vals) > 0 {
			v[k] = vals[len(vals)-1]
		}
	}
	return v
}

// str — (string) $request->input($k): строка, число литералом, bool как "1"/""; иначе "" (present=false — нет поля).
func (b body) str(k string) (string, bool) {
	switch v := b[k].(type) {
	case nil:
		return "", false
	case string:
		return v, true
	case json.Number:
		return v.String(), true
	case bool:
		if v {
			return "1", true
		}
		return "", true
	}
	return "", true
}

// filled — $request->filled($k): есть и не пусто (после trim).
func (b body) filled(k string) bool {
	s, ok := b.str(k)
	return ok && strings.TrimSpace(s) != ""
}

// isString — правило 'string'.
func (b body) isString(k string) bool { _, ok := b[k].(string); return ok }

// integer — правило 'integer' (целое число или строка из цифр). ok=false — не целое.
func (b body) integer(k string) (int64, bool) { return integerOf(b[k]) }

// integerOf — правило 'integer' для значения из JSON (поле тела или элемент массива).
func integerOf(v any) (int64, bool) {
	var s string
	switch x := v.(type) {
	case string:
		s = x
	case json.Number:
		s = x.String()
	default:
		return 0, false
	}
	s = strings.TrimSpace(s)
	if f, err := strconv.ParseFloat(s, 64); err == nil && !strings.ContainsAny(s, "eE") {
		if strings.Contains(s, ".") {
			if f != float64(int64(f)) {
				return 0, false
			}
			return int64(f), true
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	return n, err == nil
}

// ids — массив целых (computer_ids). ok=false — не массив или элемент не целое.
func (b body) ids(k string) ([]int64, bool) {
	arr, ok := b[k].([]any)
	if !ok {
		return nil, false
	}
	out := make([]int64, 0, len(arr))
	for _, v := range arr {
		n, ok := integerOf(v)
		if !ok {
			return nil, false
		}
		out = append(out, n)
	}
	return out, true
}
