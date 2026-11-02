package v1

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aitmaganbetov/zafirogo/server/internal/cash"
	apicommon "github.com/aitmaganbetov/zafirogo/server/internal/http/api/common"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
	"github.com/aitmaganbetov/zafirogo/server/internal/money"
)

// ---------------------------------------------------------------------------
// Ввод: $request->all() после TrimStrings и ConvertEmptyStringsToNull
// ---------------------------------------------------------------------------

// parseInput — query и тело (JSON или форма) в c.input; тело перекрывает query (InteractsWithInput::input()).
// Неразборчивый JSON — пустое тело, как $request->json() Laravel.
func (c *call) parseInput() {
	in := map[string]any{}
	for k, v := range c.query() {
		in[k] = v
	}
	for k, v := range c.bodyInput() {
		in[k] = v
	}
	c.input = in
}

func (c *call) query() map[string]any { return valuesMap(c.r.URL.Query()) }

// bodyInput — тело запроса: JSON (Content-Type …/json, …+json) или application/x-www-form-urlencoded.
func (c *call) bodyInput() map[string]any {
	if len(bytes.TrimSpace(c.body)) == 0 {
		return map[string]any{}
	}
	ct, _, _ := mime.ParseMediaType(c.r.Header.Get("Content-Type"))
	switch {
	case strings.HasSuffix(ct, "/json") || strings.HasSuffix(ct, "+json"):
		dec := json.NewDecoder(bytes.NewReader(c.body))
		dec.UseNumber()
		var m map[string]any
		if err := dec.Decode(&m); err != nil || m == nil {
			return map[string]any{}
		}
		out, _ := clean(m).(map[string]any)
		return out
	case ct == "application/x-www-form-urlencoded":
		v, err := url.ParseQuery(string(c.body))
		if err != nil {
			return map[string]any{}
		}
		return valuesMap(v)
	}
	return map[string]any{}
}

// valuesMap — query/форма как PHP: a[]=1&a[]=2 — массив, повтор ключа — последнее значение.
func valuesMap(v url.Values) map[string]any {
	out := make(map[string]any, len(v))
	for k, list := range v {
		if name, ok := strings.CutSuffix(k, "[]"); ok {
			arr := make([]any, len(list))
			for i, s := range list {
				arr[i] = clean(s)
			}
			out[name] = arr
			continue
		}
		if len(list) > 0 {
			out[k] = clean(list[len(list)-1])
		}
	}
	return out
}

// clean — TrimStrings + ConvertEmptyStringsToNull (рекурсивно).
func clean(v any) any {
	switch x := v.(type) {
	case string:
		s := strings.TrimSpace(x)
		if s == "" {
			return nil
		}
		return s
	case map[string]any:
		for k, e := range x {
			x[k] = clean(e)
		}
		return x
	case []any:
		for i, e := range x {
			x[i] = clean(e)
		}
		return x
	}
	return v
}

// scalarString — (string) скалярного значения PHP; массив/объект — не скаляр.
func scalarString(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case json.Number:
		return x.String(), true
	case bool:
		if x {
			return "1", true
		}
		return "", true
	}
	return "", false
}

// ---------------------------------------------------------------------------
// Правила валидатора Laravel (по одной ошибке на поле, все поля сразу)
// ---------------------------------------------------------------------------

type form struct {
	in map[string]any
	fe httpx.FormErrors
}

func (c *call) form() *form { return &form{in: c.input} }

func (f *form) fail(field, rule, param string) {
	f.fe.Add(field, httpx.RuleMessage(field, rule, param))
}

func (f *form) err() error { return f.fe.Err() }

// value — значение поля; ok = false — поля нет или null.
func (f *form) value(field string) (any, bool) {
	v, ok := f.in[field]
	return v, ok && v != nil
}

// has — поле передано (sometimes), даже null.
func (f *form) has(field string) bool { _, ok := f.in[field]; return ok }

var intRe = regexp.MustCompile(`^[+-]?(0|[1-9][0-9]*)$`)

// asInt — правило integer (filter_var FILTER_VALIDATE_INT).
func asInt(v any) (int64, bool) {
	s, ok := scalarString(v)
	if !ok {
		return 0, false
	}
	if _, isBool := v.(bool); isBool {
		return 0, false
	}
	if !intRe.MatchString(s) {
		return 0, false
	}
	n, err := strconv.ParseInt(s, 10, 64)
	return n, err == nil
}

// integer — required|nullable integer с min/max (nil — без границы); nil результата — поля нет или ошибка.
func (f *form) integer(field string, required bool, minV, maxV *int64) *int64 {
	v, ok := f.value(field)
	if !ok {
		if required {
			f.fail(field, cash.RuleRequired, "")
		}
		return nil
	}
	n, ok := asInt(v)
	if !ok {
		f.fail(field, cash.RuleInteger, "")
		return nil
	}
	if minV != nil && n < *minV {
		f.fail(field, cash.RuleMin, strconv.FormatInt(*minV, 10))
		return nil
	}
	if maxV != nil && n > *maxV {
		f.fail(field, cash.RuleMax, strconv.FormatInt(*maxV, 10))
		return nil
	}
	return &n
}

// str — required|nullable string|max:n.
func (f *form) str(field string, required bool, maxLen int) *string {
	v, ok := f.value(field)
	if !ok {
		if required {
			f.fail(field, cash.RuleRequired, "")
		}
		return nil
	}
	s, isStr := v.(string)
	if !isStr {
		f.fail(field, "string", "")
		return nil
	}
	if utf8.RuneCountInString(s) > maxLen {
		f.fail(field, "max_string", strconv.Itoa(maxLen))
		return nil
	}
	return &s
}

// oneOf — required|in:…: значение не из списка (или не строка) — ошибка in; "" — ошибка.
func (f *form) oneOf(field string, allowed ...string) string {
	v, ok := f.value(field)
	if !ok {
		f.fail(field, cash.RuleRequired, "")
		return ""
	}
	if s, isStr := scalarString(v); isStr && slices.Contains(allowed, s) {
		return s
	}
	f.fail(field, cash.RuleIn, "")
	return ""
}

// boolean — правило boolean: true, false, 1, 0, "1", "0".
func (f *form) boolean(field string, required bool) *bool {
	v, ok := f.value(field)
	if !ok {
		if required {
			f.fail(field, cash.RuleRequired, "")
		}
		return nil
	}
	var b bool
	switch x := v.(type) {
	case bool:
		b = x
	default:
		s, _ := scalarString(v)
		switch s {
		case "1":
			b = true
		case "0":
			b = false
		default:
			f.fail(field, "boolean", "")
			return nil
		}
	}
	return &b
}

// price — required|numeric|min:0|max:9999999999.99; решение Q-002: > 2 знаков после точки — decimal (422).
func (f *form) price(field string, required bool) *money.Tiyn {
	v, ok := f.value(field)
	if !ok {
		if required {
			f.fail(field, cash.RuleRequired, "")
		}
		return nil
	}
	s, isScalar := scalarString(v)
	if _, isBool := v.(bool); isBool || !isScalar {
		f.fail(field, cash.RuleNumeric, "")
		return nil
	}
	t, err := money.ParseTenge(s)
	switch {
	case errors.Is(err, money.ErrTooManyDecimals):
		f.fail(field, cash.RuleDecimal, "")
		return nil
	case errors.Is(err, money.ErrOutOfRange):
		f.fail(field, cash.RuleMax, "")
		return nil
	case err != nil:
		f.fail(field, cash.RuleNumeric, "")
		return nil
	case t < 0:
		f.fail(field, cash.RuleMin, "")
		return nil
	case t > maxPrice:
		f.fail(field, cash.RuleMax, "")
		return nil
	}
	return &t
}

// maxPrice — max:9999999999.99.
const maxPrice money.Tiyn = 999_999_999_999

// dateLayouts — форматы правила date (strtotime) и Carbon::parse(), которые реально присылают интеграции.
var dateLayouts = []string{
	time.RFC3339Nano, "2006-01-02T15:04:05Z0700", "2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02 15:04:05",
	"2006-01-02 15:04:05Z07:00", "2006-01-02 15:04", "2006-01-02", "2006/01/02",
}

// date — nullable|date; без пояса — UTC (CurrentClub::useUtc()).
func (f *form) date(field string) *time.Time {
	v, ok := f.value(field)
	if !ok {
		return nil
	}
	s, isStr := v.(string)
	if isStr {
		for _, l := range dateLayouts {
			if t, err := time.ParseInLocation(l, s, time.UTC); err == nil {
				t = t.UTC()
				return &t
			}
		}
	}
	f.fail(field, "date", "")
	return nil
}

// validationFields — тексты ошибок формы (422 httpx) → error.fields.
func validationFields(e *httpx.APIError) (map[string][]string, bool) {
	if e.Status != http.StatusUnprocessableEntity {
		return nil, false
	}
	if b, ok := e.Body.(apicommon.ValidationError); ok {
		return b.Errors, true
	}
	return nil, false
}

// ---------------------------------------------------------------------------
// Курсорная пагинация (ApiV1Controller::paginate(): cursorPaginate по id)
// ---------------------------------------------------------------------------

const (
	defaultLimit = 50
	maxLimit     = 100
)

// page — limit (nullable|integer|min:1|max:100, по умолчанию 50) и курсор Laravel: base64url JSON
// {"<таблица>.id": N, "_pointsToNextItems": true}. Неразборчивый курсор — первая страница (Cursor::fromEncoded → null).
type page struct {
	limit  int
	after  int64
	column string
}

func (f *form) page(table string) page {
	one, hundred := int64(1), int64(maxLimit)
	p := page{limit: defaultLimit, column: table + ".id"}
	if n := f.integer("limit", false, &one, &hundred); n != nil {
		p.limit = int(*n)
	}
	if v, ok := f.value("cursor"); ok {
		if s, isStr := v.(string); isStr {
			p.after = decodeCursor(s, p.column)
		}
	}
	return p
}

func decodeCursor(s, column string) int64 {
	s = strings.NewReplacer("-", "+", "_", "/").Replace(s)
	raw, err := base64.StdEncoding.DecodeString(s + strings.Repeat("=", (4-len(s)%4)%4))
	if err != nil {
		return 0
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var m map[string]any
	if dec.Decode(&m) != nil {
		return 0
	}
	if next, _ := m["_pointsToNextItems"].(bool); !next {
		return 0 // курсоры «назад» API не выдаёт (meta содержит только next_cursor)
	}
	n, ok := asInt(m[column])
	if !ok || n < 0 {
		return 0
	}
	return n
}

// encodeCursor — Cursor::encode(): base64 JSON без «=», с «-» и «_» вместо «+» и «/».
func encodeCursor(column string, id int64) string {
	// Порядок ключей как у json_encode(array_merge($parameters, ['_pointsToNextItems' => true])).
	b := `{"` + column + `":` + strconv.FormatInt(id, 10) + `,"_pointsToNextItems":true}`
	s := base64.StdEncoding.EncodeToString([]byte(b))
	return strings.TrimRight(strings.NewReplacer("+", "-", "/", "_").Replace(s), "=")
}

// size — сколько строк читать: limit + 1 (есть ли следующая страница).
func (p page) size() int32 { return int32(p.limit + 1) } // #nosec G115 -- limit ≤ 100

// pageOf — {"data": [...], "meta": {"per_page", "next_cursor"}} из rows (до limit + 1 строк).
func pageOf[T any](p page, rows []T, id func(T) int64, view func(T) any) response {
	items := make([]any, 0, min(len(rows), p.limit))
	var next any
	for i, r := range rows {
		if i == p.limit {
			next = encodeCursor(p.column, id(rows[i-1]))
			break
		}
		items = append(items, view(r))
	}
	return jsonResponse(http.StatusOK, map[string]any{
		"data": items,
		"meta": map[string]any{"per_page": p.limit, "next_cursor": next},
	})
}

func ptr[T any](v T) *T { return &v }
