package httpx

// Перевод текстов ответов API на язык клуба (решение F1-5, docs/open-questions/stage3-foundation.md).
//
// Ошибки: ResponseError переводит `message` и тексты `errors{поле: [...]}` тел Error/ValidationError на язык
// клуба (me.club.locale) по каталогу переводов (таблица translations, ключ — русский текст в формате Laravel).
// Текст с подстановками нужно создавать через ValidationMsg / ErrorMsg (ключ + параметры), иначе ключ уже
// содержит подставленные значения и не найдётся. Тексты правил валидатора (RuleError, RuleMessage) переводятся
// сами: validation.<правило> и validation.attributes.<поле>, как валидатор Laravel.
//
// Успешные ответы с текстом (например, «Уведомление появится… (:count ПК)») — d.T(ctx, ключ, параметры).
// Нет перевода — текст остаётся русским (ключ), как `__()`.

import (
	"context"
	"strings"
	"sync"

	apicommon "github.com/aitmaganbetov/zafirogo/server/internal/http/api/common"
	"github.com/aitmaganbetov/zafirogo/server/internal/i18n"
)

// Msg — текст для перевода: ключ (русский текст `__()` или «группа.ключ») и подстановки :name.
type Msg struct {
	Key    string
	Params map[string]string
	// attribute — поле правила валидатора: :attribute = validation.attributes.<поле> (или поле с пробелами).
	attribute string
}

// Text — русский текст (ключ с подстановками) — то, что видно без каталога.
func (m Msg) Text() string { return Substitute(m.Key, m.Params) }

// Locale — язык клуба запроса (LocaleResolver::forClub()); без клуба или каталога — ru.
func (d *Deps) Locale(ctx context.Context) string {
	if d.I18n == nil {
		return i18n.SourceLocale
	}
	return d.I18n.ClubLocale(ctx, ClubID(ctx))
}

// T — `__($key, $params)` на языке клуба запроса: перевод (нет — ключ), форма trans_choice по params["count"],
// подстановки. Для текстов успешных ответов.
func (d *Deps) T(ctx context.Context, key string, params map[string]string) string {
	if d.I18n == nil {
		return i18n.Render(i18n.SourceLocale, key, params)
	}
	return d.I18n.Translate(ctx, d.Locale(ctx), key, params)
}

// ValidationMsg — 422 с текстом-ключом и подстановками (переводится на язык клуба в ResponseError).
func ValidationMsg(field, key string, params map[string]string) *APIError {
	m := Msg{Key: key, Params: params}
	e := Validation(field, m.Text())
	e.remember(m)
	return e
}

// ErrorMsg — ошибка {error: code, message} с текстом-ключом и подстановками.
func ErrorMsg(status int, code, key string, params map[string]string) *APIError {
	m := Msg{Key: key, Params: params}
	e := NewAPIError(status, code, m.Text())
	e.remember(m)
	return e
}

// AddMsg — ошибка поля формы с текстом-ключом и подстановками.
func (f *FormErrors) AddMsg(field, key string, params map[string]string) {
	m := Msg{Key: key, Params: params}
	f.Add(field, m.Text())
	if len(params) > 0 {
		if f.msgs == nil {
			f.msgs = map[string]Msg{}
		}
		f.msgs[m.Text()] = m
	}
}

func (e *APIError) remember(m Msg) {
	if len(m.Params) == 0 {
		return // текст без подстановок — сам ключ
	}
	if e.msgs == nil {
		e.msgs = map[string]Msg{}
	}
	e.msgs[m.Text()] = m
}

// lookuper — каталог переводов (*i18n.Catalog).
type lookuper interface {
	Lookup(ctx context.Context, locale, key string) (string, bool)
}

// ruleMsgs — отрисованный текст правила валидатора → Msg (validation.*). Множество конечно: поля и параметры
// правил заданы в коде.
var ruleMsgs sync.Map

// laravelRuleKeys — правило RuleMessage → ключ группы validation (lang/*/validation.php).
var laravelRuleKeys = map[string]string{
	"min": "min.numeric", "max": "max.numeric", "gt": "gt.numeric",
	"min_string": "min.string", "max_string": "max.string",
}

func rememberRule(text, field, rule, param string) {
	key := rule
	if k, ok := laravelRuleKeys[rule]; ok {
		key = k
	}
	params := map[string]string{"attribute": strings.ReplaceAll(field, "_", " "), "decimal": "0-2"}
	for _, p := range []string{"min", "max", "value"} {
		params[p] = param
	}
	ruleMsgs.LoadOrStore(text, Msg{Key: "validation." + key, Params: params, attribute: field})
}

// localized — копия ошибки с текстами на языке locale (исходную не меняет: ошибки бывают общими значениями).
func (e *APIError) localized(ctx context.Context, cat lookuper, locale string) *APIError {
	tr := func(s string) string { return translateText(ctx, cat, locale, s, e.msgs) }
	switch b := e.Body.(type) {
	case apicommon.Error:
		if b.Message != nil && *b.Message != "" {
			m := tr(*b.Message)
			b.Message = &m
		}
		return &APIError{Status: e.Status, Body: b}
	case apicommon.ValidationError:
		b.Message = tr(b.Message)
		errs := make(map[string][]string, len(b.Errors))
		for k, list := range b.Errors {
			out := make([]string, len(list))
			for i, s := range list {
				out[i] = tr(s)
			}
			errs[k] = out
		}
		b.Errors = errs
		return &APIError{Status: e.Status, Body: b}
	}
	return e
}

// translateText — перевод одного текста ответа: известный Msg (ключ + подстановки), правило валидатора или
// сам текст как ключ. Перевода нет — текст без изменений.
func translateText(ctx context.Context, cat lookuper, locale, s string, msgs map[string]Msg) string {
	if m, ok := msgs[s]; ok {
		line, ok := cat.Lookup(ctx, locale, m.Key)
		if !ok {
			line = m.Key
		}
		return i18n.Render(locale, line, m.Params)
	}
	if v, ok := ruleMsgs.Load(s); ok {
		m, _ := v.(Msg) // в ruleMsgs только Msg
		line, ok := cat.Lookup(ctx, locale, m.Key)
		if !ok {
			return s
		}
		params := m.Params
		if attr, ok := cat.Lookup(ctx, locale, "validation.attributes."+m.attribute); ok {
			params = cloneParams(params)
			params["attribute"] = attr
		}
		return i18n.Render(locale, line, params)
	}
	if line, ok := cat.Lookup(ctx, locale, s); ok {
		return line
	}
	return s
}

func cloneParams(p map[string]string) map[string]string {
	out := make(map[string]string, len(p))
	for k, v := range p {
		out[k] = v
	}
	return out
}
