package i18n

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

const (
	// SourceLocale — язык-источник: его тексты — ключи (TranslationCatalog::SOURCE_LOCALE).
	SourceLocale = "ru"
	// FallbackLocale — запасной язык JSON-фраз (TranslationCatalog::FALLBACK_LOCALE).
	FallbackLocale = "en"
)

// ErrUnknownLocale — язык не включён.
var ErrUnknownLocale = errors.New("i18n: unknown or disabled locale")

// Language — включённый язык.
type Language struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

// Pack — пакет переводов языка для i18next: тело JSON {ключ: текст} и ETag.
type Pack struct {
	Body []byte
	ETag string
}

// Catalog — пакеты переводов в памяти процесса, пересобираются при смене версии (правка в суперадминке меняет
// updated_at, удаление — число строк). На запрос — один лёгкий SELECT версии (как translations.state).
type Catalog struct {
	pool db.Beginner

	mu      sync.Mutex
	version string
	// checkedAt — когда версия сверялась с базой (refresh). Lookup в пределах lookupVersionTTL берёт строки из памяти
	// без транзакции: перевод сообщения API не должен открывать транзакцию платформенного пула на каждый вызов,
	// часто изнутри уже открытой тенантной (S6-O4-3, docs/load-test.md).
	checkedAt time.Time
	packs     map[string]Pack
	// raw — строки языка в формате Laravel (ключ «группа.ключ» для групп, кроме '*') для перевода на сервере
	// (Translate: тексты ошибок API, F1-5).
	raw     map[string]map[string]string
	enabled []Language

	clubs clubLocales
}

// NewCatalog — каталог поверх пула (таблицы платформенные, без RLS).
func NewCatalog(pool db.Beginner) *Catalog {
	return &Catalog{pool: pool, packs: map[string]Pack{}, raw: map[string]map[string]string{}}
}

func (c *Catalog) refresh(ctx context.Context, q *store.Queries) error {
	v, err := q.I18nVersion(ctx)
	if err != nil {
		return err
	}
	version := strconv.FormatInt(v.ChangedMs, 10) + "-" + strconv.FormatInt(v.Rows, 10)
	c.checkedAt = time.Now()
	if version == c.version && c.enabled != nil {
		return nil
	}
	rows, err := q.I18nEnabledLanguages(ctx)
	if err != nil {
		return err
	}
	enabled := make([]Language, 0, len(rows))
	for _, r := range rows {
		enabled = append(enabled, Language{Code: r.Code, Name: r.Name})
	}
	c.version, c.enabled, c.packs, c.raw = version, enabled, map[string]Pack{}, map[string]map[string]string{}
	return nil
}

// Enabled — включённые языки (TranslationCatalog::enabledCodes()) и версия переводов.
func (c *Catalog) Enabled(ctx context.Context) ([]Language, string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	err := db.InPlatformTx(ctx, c.pool, func(q *store.Queries) error { return c.refresh(ctx, q) })
	if err != nil {
		return nil, "", err
	}
	return slices.Clone(c.enabled), c.version, nil
}

// Pack — JSON-фразы ('*') и группы pagination/validation («группа.ключ») языка locale поверх запасного en
// (TranslationCatalog::buildPack()), в формате i18next.
func (c *Catalog) Pack(ctx context.Context, locale string) (Pack, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out Pack
	err := db.InPlatformTx(ctx, c.pool, func(q *store.Queries) error {
		if err := c.refresh(ctx, q); err != nil {
			return err
		}
		if p, ok := c.packs[locale]; ok {
			out = p
			return nil
		}
		raw, err := c.rawLocked(ctx, q, locale)
		if err != nil {
			return err
		}
		lines := map[string]string{}
		for key, v := range raw {
			for k, e := range Entries(locale, key, v) {
				lines[k] = e
			}
		}
		body, err := json.Marshal(lines)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(body)
		out = Pack{Body: body, ETag: `"` + base64.RawURLEncoding.EncodeToString(sum[:16]) + `"`}
		c.packs[locale] = out
		return nil
	})
	return out, err
}

// rawLocked — строки языка locale поверх запасного en в формате Laravel (TranslationCatalog::buildPack()):
// JSON-фразы ('*') — по ключу, группы pagination/validation — «группа.ключ». Вызывать под c.mu после refresh.
func (c *Catalog) rawLocked(ctx context.Context, q *store.Queries, locale string) (map[string]string, error) {
	if r, ok := c.raw[locale]; ok {
		return r, nil
	}
	if !slices.ContainsFunc(c.enabled, func(l Language) bool { return l.Code == locale }) {
		return nil, ErrUnknownLocale
	}
	codes := []string{locale}
	if locale != SourceLocale && locale != FallbackLocale {
		codes = append(codes, FallbackLocale)
	}
	rows, err := q.I18nLines(ctx, store.I18nLinesParams{Codes: codes, Locale: locale})
	if err != nil {
		return nil, err
	}
	lines := make(map[string]string, len(rows))
	for _, r := range rows { // запасной язык — первым, язык запроса перекрывает
		key := r.Key
		if r.Group != "*" { // __('pagination.next'): ключ «группа.ключ»
			key = r.Group + "." + r.Key
		}
		lines[key] = r.Value
	}
	c.raw[locale] = lines
	return lines, nil
}

// DefaultLanguages — языки старого проекта (миграция 2026_10_13_181908_create_languages_and_translations_tables).
var DefaultLanguages = []struct {
	Code, Name string
	Sort       int32
}{
	{"ru", "Русский", 1},
	{"kk", "Қазақша", 2},
	{"en", "English", 3},
}

// Import — TranslationCatalog::sync(): языки по умолчанию (если их нет) и отсутствующие строки из shipped
// (code → строки файла). Существующие строки (возможно, отредактированные) не меняются. Возвращает число
// вставленных строк. Одна транзакция под пулом zafiro_platform (или владельцем схемы).
func Import(ctx context.Context, pool db.Beginner, shipped map[string]Shipped) (int64, error) {
	var inserted int64
	err := db.InPlatformTx(ctx, pool, func(q *store.Queries) error {
		inserted = 0
		for _, l := range DefaultLanguages {
			if err := q.I18nEnsureLanguage(ctx, store.I18nEnsureLanguageParams{Code: l.Code, Name: l.Name, SortOrder: l.Sort}); err != nil {
				return err
			}
		}
		codes := make([]string, 0, len(shipped))
		for code := range shipped {
			codes = append(codes, code)
		}
		slices.Sort(codes)
		for _, code := range codes {
			langID, err := q.I18nLanguageID(ctx, code)
			if err != nil {
				return errors.New("i18n: language " + code + " is not in languages")
			}
			s := shipped[code]
			for _, g := range s.Groups() {
				for _, k := range s.Keys(g) {
					n, err := q.I18nInsertMissing(ctx, store.I18nInsertMissingParams{
						LanguageID: langID, Grp: g, Key: k, KeyHash: HashKey(g, k), Value: s[g][k],
					})
					if err != nil {
						return err
					}
					inserted += n
				}
			}
		}
		return nil
	})
	return inserted, err
}
