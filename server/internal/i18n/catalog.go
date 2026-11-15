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
	"sync/atomic"
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

// Catalog — переводы в памяти процесса: неизменяемый снимок (atomic.Pointer), который пересобирает фоновая
// горутина каталога раз в refreshEvery (PB-01, docs/open-questions/audit-perf.md).
//
// Правила:
//   - Lookup / Translate / ClubLocale на пути запроса в базу не ходят и мьютексов на время ввода-вывода не держат:
//     их вызывают изнутри открытой транзакции клуба (h.d.Tx → h.d.T), и второе соединение из того же пула под общим
//     мьютексом давало взаимную блокировку пула (все соединения «idle in transaction» до таймаута запроса);
//   - снимок читает из базы отдельная горутина со своим контекстом; запросы ждут её только при «холодном» каталоге
//     (снимка ещё нет) и не дольше coldWait — затем текст остаётся русским (ключ), как `__()` без перевода;
//   - Pack / Enabled (публичные маршруты /api/i18n, не внутри транзакции клуба) сверяют версию с базой на каждый
//     запрос, как раньше: правка в суперадминке видна в пакетах i18next сразу, в текстах сервера — не позже
//     refreshEvery + время запроса (≤ 5 с);
//   - версия — правка в суперадминке меняет updated_at, удаление — число строк (PD-12).
type Catalog struct {
	pool db.Beginner

	snap atomic.Pointer[snapshot]
	// lastUsed — когда каталог последний раз спрашивали (UnixNano): простаивающий каталог (тесты, бот без оповещений)
	// останавливает фоновую сверку и забывает снимок — следующий вызов загрузит свежий.
	lastUsed atomic.Int64
	running  atomic.Bool
	// closed/stop — Close: фоновая сверка останавливается сразу и больше не запускается (остановка приложения).
	closed   atomic.Bool
	stopOnce sync.Once
	stop     chan struct{}

	// mu — только состояние управления (текущая загрузка, поправки языка клуба); на время запросов к базе не держится.
	mu        sync.Mutex
	inflight  *loadCall
	lastFail  time.Time
	overrides map[int64]localeOverride
}

// snapshot — неизменяемое состояние каталога: после публикации (snap.Store) не меняется.
type snapshot struct {
	version string
	enabled []Language
	// raw — строки языка в формате Laravel (ключ «группа.ключ» для групп, кроме '*') для перевода на сервере
	// (Translate: тексты ошибок API, F1-5). Только включённые языки: выключенный — «нет перевода» без похода в базу.
	raw   map[string]map[string]string
	packs map[string]Pack
	// clubs — языки клубов, отличные от ru (I18nClubLocales).
	clubs map[int64]string
}

type loadCall struct {
	started time.Time
	done    chan struct{}
	err     error
}

type localeOverride struct {
	locale string
	at     time.Time
}

const (
	// refreshEvery — как часто фоновая горутина сверяет версию переводов и языки клубов с базой.
	refreshEvery = 3 * time.Second
	// loadTimeout — предел одной загрузки снимка (свой контекст, не контекст запроса).
	loadTimeout = 15 * time.Second
	// coldWait — сколько запрос ждёт первую загрузку снимка, прежде чем ответить русским текстом.
	coldWait = 3 * time.Second
	// failBackoff — пауза после неудачной загрузки «холодного» каталога (база недоступна): запросы не ждут.
	failBackoff = time.Second
	// idleStop — без обращений дольше этого фоновая сверка останавливается.
	idleStop = 2 * time.Minute
	// overrideTTL — сколько живёт поправка языка клуба (SetClubLocale): дольше, чем загрузка, начатая до правки.
	overrideTTL = 2 * loadTimeout
)

// NewCatalog — каталог поверх пула (таблицы платформенные; clubs читают обе роли). Сервер передаёт платформенный
// пул: фоновая сверка не занимает соединения пула клубов.
func NewCatalog(pool db.Beginner) *Catalog {
	return &Catalog{pool: pool, stop: make(chan struct{})}
}

// Close — остановить фоновую сверку (и дождаться, пока она выйдет). После Close каталог отвечает последним снимком
// и в базу больше не ходит. Повторный вызов безопасен.
func (c *Catalog) Close() {
	if c == nil {
		return
	}
	c.closed.Store(true)
	c.stopOnce.Do(func() {
		if c.stop != nil {
			close(c.stop)
		}
	})
	for c.running.Load() {
		time.Sleep(5 * time.Millisecond)
	}
	c.mu.Lock()
	call := c.inflight
	c.mu.Unlock()
	if call != nil {
		<-call.done
	}
}

var errClosed = errors.New("i18n: catalog is closed")

// Warm — начать загрузку снимка, не дожидаясь первого запроса (при старте сервера). Не блокирует.
func (c *Catalog) Warm() {
	if c == nil || c.pool == nil {
		return
	}
	c.touch()
	if c.snap.Load() == nil {
		c.startLoad()
	}
}

// touch — отметить обращение и убедиться, что фоновая сверка идёт.
func (c *Catalog) touch() {
	c.lastUsed.Store(time.Now().UnixNano())
	if c.closed.Load() {
		return
	}
	if c.running.CompareAndSwap(false, true) {
		if c.closed.Load() { // Close успел между проверкой и запуском
			c.running.Store(false)
			return
		}
		go c.loop()
	}
}

func (c *Catalog) loop() {
	t := time.NewTicker(refreshEvery)
	defer t.Stop()
	for {
		select {
		case <-c.stop:
			c.running.Store(false)
			return
		case <-t.C:
		}
		if time.Since(time.Unix(0, c.lastUsed.Load())) > idleStop {
			c.mu.Lock()
			if time.Since(time.Unix(0, c.lastUsed.Load())) > idleStop {
				c.snap.Store(nil)
				c.running.Store(false)
				c.mu.Unlock()
				return
			}
			c.mu.Unlock()
		}
		select {
		case <-c.stop:
			c.running.Store(false)
			return
		case <-c.startLoad().done:
		}
	}
}

func (c *Catalog) startLoad() *loadCall {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.inflight != nil {
		return c.inflight
	}
	call := &loadCall{started: time.Now(), done: make(chan struct{})}
	if c.closed.Load() { // после Close в базу не ходим: отвечает последний снимок
		call.err = errClosed
		close(call.done)
		return call
	}
	c.inflight = call
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		go func() { // Close прерывает загрузку
			select {
			case <-c.stop:
				cancel()
			case <-ctx.Done():
			}
		}()
		next, err := c.load(ctx, c.snap.Load())
		c.mu.Lock()
		if err == nil {
			c.publish(next)
			c.lastFail = time.Time{}
		} else {
			c.lastFail = time.Now()
		}
		call.err = err
		c.inflight = nil
		c.mu.Unlock()
		close(call.done)
	}()
	return call
}

// publish — опубликовать снимок с поправками языка клуба поверх (правка, сделанная этим инстансом, пока шла загрузка).
// Под c.mu.
func (c *Catalog) publish(s *snapshot) {
	for id, o := range c.overrides {
		if time.Since(o.at) > overrideTTL {
			delete(c.overrides, id)
			continue
		}
		setClub(s.clubs, id, o.locale)
	}
	c.snap.Store(s)
}

func setClub(m map[int64]string, clubID int64, locale string) {
	if locale == "" || locale == SourceLocale {
		delete(m, clubID)
		return
	}
	m[clubID] = locale
}

// load — новый снимок: версия и языки клубов — каждый раз, строки и пакеты — только при смене версии.
func (c *Catalog) load(ctx context.Context, old *snapshot) (*snapshot, error) {
	next := &snapshot{}
	err := db.InPlatformTx(ctx, c.pool, func(q *store.Queries) error {
		v, err := q.I18nVersion(ctx)
		if err != nil {
			return err
		}
		clubs, err := q.I18nClubLocales(ctx)
		if err != nil {
			return err
		}
		next.clubs = make(map[int64]string, len(clubs))
		for _, r := range clubs {
			next.clubs[r.ID] = r.Locale
		}
		next.version = strconv.FormatInt(v.ChangedMs, 10) + "-" + strconv.FormatInt(v.Rows, 10)
		if old != nil && old.version == next.version {
			next.enabled, next.raw, next.packs = old.enabled, old.raw, old.packs
			return nil
		}
		rows, err := q.I18nEnabledLanguages(ctx)
		if err != nil {
			return err
		}
		next.enabled = make([]Language, 0, len(rows))
		next.raw = make(map[string]map[string]string, len(rows))
		next.packs = make(map[string]Pack, len(rows))
		for _, r := range rows {
			next.enabled = append(next.enabled, Language{Code: r.Code, Name: r.Name})
		}
		for _, l := range next.enabled {
			raw, err := loadRaw(ctx, q, l.Code)
			if err != nil {
				return err
			}
			pack, err := buildPack(l.Code, raw)
			if err != nil {
				return err
			}
			next.raw[l.Code], next.packs[l.Code] = raw, pack
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return next, nil
}

// current — снимок для пути запроса: готовый — сразу; «холодный» каталог — ждать первую загрузку не дольше coldWait
// (и не ждать вовсе сразу после неудачи). nil — переводов нет (текст остаётся русским).
func (c *Catalog) current(ctx context.Context) *snapshot {
	c.touch()
	if s := c.snap.Load(); s != nil {
		return s
	}
	c.mu.Lock()
	failed := !c.lastFail.IsZero() && time.Since(c.lastFail) < failBackoff && c.inflight == nil
	c.mu.Unlock()
	if failed {
		return nil
	}
	call := c.startLoad()
	t := time.NewTimer(coldWait)
	defer t.Stop()
	select {
	case <-call.done:
	case <-ctx.Done():
	case <-t.C:
	}
	return c.snap.Load()
}

// fresh — снимок, сверенный с базой после момента вызова (для Pack / Enabled: правка в суперадминке видна сразу).
func (c *Catalog) fresh(ctx context.Context) (*snapshot, error) {
	c.touch()
	arrived := time.Now()
	for {
		call := c.startLoad()
		select {
		case <-call.done:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		if call.started.Before(arrived) {
			continue // загрузка началась до вызова и могла не увидеть только что сохранённую правку
		}
		if call.err != nil {
			return nil, call.err
		}
		if s := c.snap.Load(); s != nil {
			return s, nil
		}
		// снимок забыт простаивающим каталогом между загрузкой и чтением — повторить
		arrived = time.Now()
	}
}

// Enabled — включённые языки (TranslationCatalog::enabledCodes()) и версия переводов.
func (c *Catalog) Enabled(ctx context.Context) ([]Language, string, error) {
	s, err := c.fresh(ctx)
	if err != nil {
		return nil, "", err
	}
	return slices.Clone(s.enabled), s.version, nil
}

// Pack — JSON-фразы ('*') и группы pagination/validation («группа.ключ») языка locale поверх запасного en
// (TranslationCatalog::buildPack()), в формате i18next.
func (c *Catalog) Pack(ctx context.Context, locale string) (Pack, error) {
	s, err := c.fresh(ctx)
	if err != nil {
		return Pack{}, err
	}
	p, ok := s.packs[locale]
	if !ok {
		return Pack{}, ErrUnknownLocale
	}
	return p, nil
}

// buildPack — пакет i18next языка locale из строк формата Laravel.
//
// Записи «ключ = значение» в пакет не кладутся (FE-04): i18next без перевода возвращает сам ключ (web/src/i18n:
// fallbackLng: false, returnEmptyString: false) и подставляет в него {{name}} так же, как в перевод. В языке-источнике
// ru таких записей 2 040 из 2 234 — пакет в разы меньше. Формы множественного числа (ключ_one …) остаются всегда.
// Серверный перевод (Lookup) берёт строки из raw — он от этого не зависит.
func buildPack(locale string, raw map[string]string) (Pack, error) {
	lines := map[string]string{}
	for key, v := range raw {
		for k, e := range Entries(locale, key, v) {
			if k == e {
				continue
			}
			lines[k] = e
		}
	}
	body, err := json.Marshal(lines)
	if err != nil {
		return Pack{}, err
	}
	sum := sha256.Sum256(body)
	return Pack{Body: body, ETag: `"` + base64.RawURLEncoding.EncodeToString(sum[:16]) + `"`}, nil
}

// loadRaw — строки языка locale поверх запасного en в формате Laravel (TranslationCatalog::buildPack()):
// JSON-фразы ('*') — по ключу, группы pagination/validation — «группа.ключ».
func loadRaw(ctx context.Context, q *store.Queries, locale string) (map[string]string, error) {
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
