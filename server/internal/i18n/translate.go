package i18n

// Перевод текстов на сервере (решение F1-5, docs/open-questions/stage3-foundation.md): сообщения API
// (`message` ошибок и ответов) переводятся на язык клуба так же, как это делал `__()` / `trans_choice()` Laravel
// на сервере: ключ — русский текст в формате Laravel (`:name`, формы через `|`), подстановки — после перевода.

import (
	"context"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// SetClubLocale — язык клуба только что изменён этим инстансом («Настройки → Языки»): виден в сообщениях API
// сразу, не дожидаясь фоновой сверки (другие инстансы увидят не позже refreshEvery).
func (c *Catalog) SetClubLocale(clubID int64, locale string) {
	if c == nil || clubID <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.overrides == nil {
		c.overrides = map[int64]localeOverride{}
	}
	c.overrides[clubID] = localeOverride{locale: locale, at: time.Now()}
	if old := c.snap.Load(); old != nil {
		next := *old
		next.clubs = make(map[int64]string, len(old.clubs)+1)
		for id, l := range old.clubs {
			next.clubs[id] = l
		}
		setClub(next.clubs, clubID, locale)
		c.snap.Store(&next)
	}
}

// ForgetClubLocale — язык клуба мог измениться: перечитать языки клубов сейчас (фоновой загрузкой, без ожидания).
func (c *Catalog) ForgetClubLocale(clubID int64) {
	if c == nil || c.pool == nil {
		return
	}
	c.mu.Lock()
	delete(c.overrides, clubID)
	c.mu.Unlock()
	c.touch()
	c.startLoad()
}

// ClubLocale — язык клуба (clubs.locale; пусто — ru, LocaleResolver::DEFAULT_CLUB_LOCALE) из снимка каталога:
// в базу на пути запроса не ходит (PB-01). Снимка нет (база недоступна при старте) — ru: сообщение на
// языке-источнике лучше, чем ошибка вместо сообщения.
func (c *Catalog) ClubLocale(ctx context.Context, clubID int64) string {
	if clubID <= 0 || c == nil || c.pool == nil {
		return SourceLocale
	}
	s := c.current(ctx)
	if s == nil {
		return SourceLocale
	}
	if l, ok := s.clubs[clubID]; ok {
		return l
	}
	return SourceLocale
}

// EnabledCodes — коды включённых языков из снимка в памяти (без сверки с базой, в отличие от Enabled): для
// заголовков страниц сайта в index.html (webui.SiteTexts). Нет снимка — пусто.
func (c *Catalog) EnabledCodes(ctx context.Context) []string {
	if c == nil || c.pool == nil {
		return nil
	}
	s := c.current(ctx)
	if s == nil {
		return nil
	}
	codes := make([]string, len(s.enabled))
	for i, l := range s.enabled {
		codes[i] = l.Code
	}
	return codes
}

// Lookup — перевод строки key (формат Laravel) на язык locale: язык → запасной en → нет (как Translator::get()
// для JSON-фраз). Группы — ключом «группа.ключ» (validation.required). Язык не включён — нет перевода.
// Только память: см. Catalog.
func (c *Catalog) Lookup(ctx context.Context, locale, key string) (string, bool) {
	if c == nil || c.pool == nil {
		return "", false
	}
	s := c.current(ctx)
	if s == nil {
		return "", false
	}
	v, ok := s.raw[locale][key]
	return v, ok
}

// Translate — `__($key, $params)` Laravel на языке locale: перевод (нет — сам ключ), для строк trans_choice
// (формы через «|» и параметр count) — форма по числу (`trans_choice`), затем подстановки :name.
func (c *Catalog) Translate(ctx context.Context, locale, key string, params map[string]string) string {
	line, ok := c.Lookup(ctx, locale, key)
	if !ok {
		line = key
	}
	return Render(locale, line, params)
}

// Render — форма trans_choice (если в params есть count, а в строке — «|») и подстановки :name
// (makeReplacements: длинные ключи раньше коротких).
func Render(locale, line string, params map[string]string) string {
	if cnt, ok := params["count"]; ok && strings.Contains(line, "|") {
		if n, err := strconv.ParseFloat(strings.ReplaceAll(cnt, " ", ""), 64); err == nil {
			line = Choose(locale, line, n)
		}
	}
	return Substitute(line, params)
}

// Substitute — подстановки :name в тексте, как makeReplacements() Laravel: длинные ключи раньше коротких
// (":computer_name" не должен съесться ":computer"), при равной длине — по алфавиту (результат не зависит от
// порядка обхода map). Общая для сервера: переводы, ошибки агента, подписи карты.
func Substitute(line string, params map[string]string) string {
	if len(params) == 0 {
		return line
	}
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if len(keys[i]) != len(keys[j]) {
			return len(keys[i]) > len(keys[j])
		}
		return keys[i] < keys[j]
	})
	for _, k := range keys {
		line = strings.ReplaceAll(line, ":"+k, params[k])
	}
	return line
}

// explicitRe — явное условие формы: {0}, {1,2}, [2,*], ]1,Inf[ (MessageSelector::extractFromString()).
var explicitRe = regexp.MustCompile(`(?s)^[\{\[]([^\[\]\{\}]*)[\}\]](.*)`)

// Choose — MessageSelector::choose() Laravel: сначала явные условия, иначе форма по правилу языка; формы
// с таким номером нет — первая.
func Choose(locale, line string, n float64) string {
	segments := strings.Split(line, "|")
	for _, part := range segments {
		m := explicitRe.FindStringSubmatch(part)
		if m == nil {
			continue
		}
		cond, value := m[1], m[2]
		if strings.Contains(cond, ",") {
			from, to, _ := strings.Cut(cond, ",")
			if (from == "*" || n >= parseNum(from)) && (to == "*" || n <= parseNum(to)) {
				return strings.TrimSpace(value)
			}
			continue
		}
		if parseNum(cond) == n {
			return strings.TrimSpace(value)
		}
	}
	for i, s := range segments {
		segments[i] = conditionRe.ReplaceAllString(s, "")
	}
	idx := pluralIndex(locale, int(n))
	if len(segments) == 1 || idx >= len(segments) {
		return segments[0]
	}
	return segments[idx]
}

func parseNum(s string) float64 {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return -1
	}
	return f
}
