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
	"sync"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// clubLocaleTTL — сколько держать язык клуба в памяти (смена языка в «Настройки → Языки» видна в сообщениях
// API не позже чем через это время; интерфейс берёт язык из /api/auth/me сразу).
const clubLocaleTTL = 30 * time.Second

// lookupVersionTTL — сколько Lookup доверяет сверенной версии переводов без похода в базу (правка в суперадминке
// видна в сообщениях API сервера не позже чем через это время; пакеты i18next — Pack — сверяются на каждый запрос).
const lookupVersionTTL = 5 * time.Second

type clubLocale struct {
	locale string
	at     time.Time
}

type clubLocales struct {
	mu sync.Mutex
	m  map[int64]clubLocale
}

// ForgetClubLocale — сбросить язык клуба из кэша этого инстанса (после смены языка в «Настройки → Языки»;
// другие инстансы увидят новый язык не позже clubLocaleTTL).
func (c *Catalog) ForgetClubLocale(clubID int64) {
	if c == nil {
		return
	}
	c.clubs.mu.Lock()
	delete(c.clubs.m, clubID)
	c.clubs.mu.Unlock()
}

// ClubLocale — язык клуба (clubs.locale; пусто — ru, LocaleResolver::DEFAULT_CLUB_LOCALE). Кэш в памяти на
// clubLocaleTTL. Ошибка БД — ru (сообщение на языке-источнике лучше, чем ошибка вместо сообщения).
func (c *Catalog) ClubLocale(ctx context.Context, clubID int64) string {
	if clubID <= 0 || c == nil || c.pool == nil {
		return SourceLocale
	}
	now := time.Now()
	c.clubs.mu.Lock()
	if v, ok := c.clubs.m[clubID]; ok && now.Sub(v.at) < clubLocaleTTL {
		c.clubs.mu.Unlock()
		return v.locale
	}
	c.clubs.mu.Unlock()
	locale := SourceLocale
	err := db.InPlatformTx(ctx, c.pool, func(q *store.Queries) error {
		row, err := q.ClubLocaleTimezone(ctx, clubID)
		if err != nil {
			return err
		}
		if row.Locale != nil && *row.Locale != "" {
			locale = *row.Locale
		}
		return nil
	})
	if err != nil {
		return SourceLocale
	}
	c.clubs.mu.Lock()
	if c.clubs.m == nil || len(c.clubs.m) > 10000 {
		c.clubs.m = map[int64]clubLocale{}
	}
	c.clubs.m[clubID] = clubLocale{locale: locale, at: now}
	c.clubs.mu.Unlock()
	return locale
}

// Lookup — перевод строки key (формат Laravel) на язык locale: язык → запасной en → нет (как Translator::get()
// для JSON-фраз). Группы — ключом «группа.ключ» (validation.required). Язык не включён — нет перевода.
func (c *Catalog) Lookup(ctx context.Context, locale, key string) (string, bool) {
	if c == nil || c.pool == nil {
		return "", false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.enabled != nil && time.Since(c.checkedAt) < lookupVersionTTL {
		if raw, cached := c.raw[locale]; cached {
			v, ok := raw[key]
			return v, ok
		}
	}
	var (
		v  string
		ok bool
	)
	err := db.InPlatformTx(ctx, c.pool, func(q *store.Queries) error {
		if err := c.refresh(ctx, q); err != nil {
			return err
		}
		raw, err := c.rawLocked(ctx, q, locale)
		if err != nil {
			return err
		}
		v, ok = raw[key]
		return nil
	})
	if err != nil {
		return "", false
	}
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
