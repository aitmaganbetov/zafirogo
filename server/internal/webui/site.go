package webui

import (
	"bytes"
	"context"
	"html"
	"net/http"
	"strings"
	"sync"
)

// Публичный сайт (тема Е, решение L-1): для шести страниц сайта сервер подставляет в index.html <title>,
// description и Open Graph — те же значения, что после загрузки ставит клиент (web/src/features/public:
// LandingLayout useLandingDocument и title/description страниц) и что отдавал Laravel
// (resources/views/layouts/landing.blade.php, @section('title'/'description') страниц). Разметку страниц сервер
// не рендерит: поисковики и превью ссылок, не исполняющие JS, видят заголовок и описание страницы.
//
// Язык — как у гостя на клиенте (web/src/i18n/load.ts guestLanguage, LocaleResolver::forGuest()): язык из
// Accept-Language среди включённых, иначе en (если включён), иначе ru. Нет каталога переводов — русский текст (ключ).

// SiteTexts — переводы для заголовков сайта (i18n.Catalog): только память, без запросов к базе на пути запроса.
type SiteTexts interface {
	// EnabledCodes — коды включённых языков (пусто — переводов нет).
	EnabledCodes(ctx context.Context) []string
	// Lookup — перевод ключа (русского текста) на язык locale.
	Lookup(ctx context.Context, locale, key string) (string, bool)
}

// WithSiteMeta включает подстановку заголовков публичного сайта; texts == nil — тексты русские.
func WithSiteMeta(texts SiteTexts) Option {
	return func(o *options) { o.site, o.siteTexts = true, texts }
}

// sitePage — @section('title') и @section('description') страницы сайта (ключи переводов — русский текст).
type sitePage struct{ title, description string }

// sitePages — страницы сайта по пути. Тексты — буквально из web/src/features/public/*Page.tsx
// (Laravel: resources/views/welcome.blade.php, pricing.blade.php, product/*.blade.php).
var sitePages = map[string]sitePage{
	"/": {
		"Zafiro — система управления компьютерным клубом",
		"Касса, карта зала, брони, тарифы, магазин и агент на ПК в одной системе. Подключите свой компьютерный клуб за один вечер.",
	},
	"/pricing": {
		"Тарифы Zafiro — подписка для компьютерного клуба",
		"Тарифы Zafiro для компьютерных и PS-клубов: пробный период без карты и договора, все возможности в любом тарифе.",
	},
	"/product/features": {
		"Возможности Zafiro — касса, зал, тарифы и клиенты клуба",
		"Что умеет Zafiro: касса и смены, карта зала, тарифы и поминутная оплата, брони, клиенты и бонусы, склад и магазин, права сотрудников, API.",
	},
	"/product/agent": {
		"Агент Zafiro на ПК — вход, тарифы, магазин и защита ПК",
		"Агент Zafiro для Windows: клиент сам входит, покупает время и товары, ПК закрыт до оплаты, а сотрудник выходит только по PIN-коду.",
	},
	"/product/guest-app": {
		"Приложение Zafiro для гостей — баланс, вход на ПК по QR-коду, друзья",
		"Мобильное приложение Zafiro для гостей клуба: баланс и бонусы, вход на ПК по QR-коду, новости клуба, друзья и переводы, история операций.",
	},
	"/product/staff-app": {
		"Приложение Zafiro для сотрудников — касса, карта клуба и заказы в телефоне",
		"Мобильное приложение Zafiro для сотрудников клуба: карта ПК, касса и смены, клиенты, заказы из агента, чат, склад и отчёт за день.",
	},
}

const (
	siteSourceLocale = "ru" // LocaleResolver::DEFAULT_CLUB_LOCALE — язык-источник (ключи)
	siteGuestLocale  = "en" // LocaleResolver::DEFAULT_GUEST_LOCALE
	// maxAcceptLanguages — сколько языков Accept-Language разбирается (заголовок — ввод клиента).
	maxAcceptLanguages = 16
)

// siteLanguage — guestLanguage() клиента: первый язык Accept-Language (целиком, затем без региона) среди включённых;
// иначе en, если включён; иначе ru, если включён; иначе первый включённый. Порядок — как в заголовке (как
// navigator.languages, из которого браузер его и строит); q=0 — язык не подходит.
func siteLanguage(accept string, enabled []string) string {
	if len(enabled) == 0 {
		return siteSourceLocale
	}
	has := func(code string) bool {
		for _, e := range enabled {
			if e == code {
				return true
			}
		}
		return false
	}
	for i, part := range strings.Split(accept, ",") {
		if i >= maxAcceptLanguages {
			break
		}
		tag, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		if q := strings.TrimSpace(params); q == "q=0" || q == "q=0.0" || q == "q=0.00" || q == "q=0.000" {
			continue
		}
		lower := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(tag)), "_", "-")
		if lower == "" {
			continue
		}
		if has(lower) {
			return lower
		}
		if base, _, _ := strings.Cut(lower, "-"); has(base) {
			return base
		}
	}
	switch {
	case has(siteGuestLocale):
		return siteGuestLocale
	case has(siteSourceLocale):
		return siteSourceLocale
	}
	return enabled[0]
}

// siteOrigin — адрес сайта для og:image (клиент: location.origin; Laravel: asset()). Хост — из запроса и только
// из допустимых символов имени хоста (иначе og:image не ставится); схема — https за TLS или прокси с
// X-Forwarded-Proto: https.
func siteOrigin(r *http.Request) string {
	host := r.Host
	if host == "" || len(host) > 255 {
		return ""
	}
	for _, c := range []byte(host) {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '.', c == '-', c == ':', c == '[', c == ']':
		default:
			return ""
		}
	}
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	return scheme + "://" + host
}

// siteHead — <title> и meta страницы; все значения экранируются (переводы правятся в суперадминке — это текст,
// не HTML).
func siteHead(title, description, origin string) []byte {
	t, d := html.EscapeString(title), html.EscapeString(description)
	var b strings.Builder
	b.WriteString("<title>" + t + "</title>\n")
	b.WriteString(`    <meta name="description" content="` + d + "\">\n")
	b.WriteString(`    <meta property="og:title" content="` + t + "\">\n")
	b.WriteString(`    <meta property="og:description" content="` + d + "\">\n")
	if origin != "" {
		b.WriteString(`    <meta property="og:image" content="` + html.EscapeString(origin) + "/icons/icon-512.png\">\n")
	}
	b.WriteString(`    <meta property="og:type" content="website">`)
	return []byte(b.String())
}

// siteIndex — index.html страниц сайта: общий index.html, в котором <title>…</title> заменён на siteHead.
// Готовые страницы (тело, ETag, сжатая копия) хранятся по ключу «путь + язык» — набор конечен (шесть путей ×
// включённые языки); запись пересобирается, когда изменился перевод (правка в суперадминке) или адрес сайта.
type siteIndex struct {
	texts          SiteTexts
	prefix, suffix []byte // index.html до <title> и после </title>

	mu    sync.RWMutex
	pages map[string]*siteEntry
}

type siteEntry struct {
	title, description, origin string
	body                       []byte
	v                          *variant
}

// newSiteIndex — nil, если в index.html нет <title> (заглушка без фронта его содержит; чужой index.html — без подстановки).
func newSiteIndex(index []byte, texts SiteTexts) *siteIndex {
	start := bytes.Index(index, []byte("<title>"))
	if start < 0 {
		return nil
	}
	end := bytes.Index(index[start:], []byte("</title>"))
	if end < 0 {
		return nil
	}
	return &siteIndex{
		texts: texts, prefix: index[:start], suffix: index[start+end+len("</title>"):],
		pages: map[string]*siteEntry{},
	}
}

// page — готовый index.html страницы p для запроса r; ok == false — путь не страница сайта.
func (s *siteIndex) page(r *http.Request, p string) (body []byte, v *variant, ok bool) {
	pg, found := sitePages[p]
	if !found {
		return nil, nil, false
	}
	title, description, lang := pg.title, pg.description, siteSourceLocale
	if s.texts != nil {
		lang = siteLanguage(r.Header.Get("Accept-Language"), s.texts.EnabledCodes(r.Context()))
		if lang != siteSourceLocale {
			if t, ok := s.texts.Lookup(r.Context(), lang, pg.title); ok && t != "" {
				title = t
			}
			if d, ok := s.texts.Lookup(r.Context(), lang, pg.description); ok && d != "" {
				description = d
			}
		}
	}
	origin := siteOrigin(r)
	key := p + "\x00" + lang
	s.mu.RLock()
	e := s.pages[key]
	s.mu.RUnlock()
	if e == nil || e.title != title || e.description != description || e.origin != origin {
		head := siteHead(title, description, origin)
		b := make([]byte, 0, len(s.prefix)+len(head)+len(s.suffix))
		b = append(append(append(b, s.prefix...), head...), s.suffix...)
		e = &siteEntry{title: title, description: description, origin: origin, body: b, v: newVariant("index.html", b)}
		s.mu.Lock()
		s.pages[key] = e
		s.mu.Unlock()
	}
	return e.body, e.v, true
}
