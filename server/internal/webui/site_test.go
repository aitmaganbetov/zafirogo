package webui

import (
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

// siteFS — index.html как у сборки: <title> и достаточно текста, чтобы ответ сжимался.
func siteFS() fstest.MapFS {
	return fstest.MapFS{"index.html": {Data: []byte(`<!doctype html>
<html lang="ru">
  <head>
    <meta charset="UTF-8" />
    <title>Zafiro</title>
    <link rel="manifest" href="/manifest.webmanifest" />` + strings.Repeat("\n    <!-- заполнитель заполнитель заполнитель -->", 40) + `
  </head>
  <body><div id="root"></div></body>
</html>`)}}
}

type fakeTexts struct {
	enabled []string
	tr      map[string]map[string]string
}

func (f *fakeTexts) EnabledCodes(context.Context) []string { return f.enabled }

func (f *fakeTexts) Lookup(_ context.Context, locale, key string) (string, bool) {
	v, ok := f.tr[locale][key]
	return v, ok
}

func siteGet(h http.Handler, target string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	for k, v := range hdr {
		if k == "Host" {
			req.Host = v
			continue
		}
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// L-1: каждая из шести страниц сайта отдаёт свои <title>, description и Open Graph; остальные пути — общий index.html.
func TestSiteMetaPerPage(t *testing.T) {
	h := Handler(siteFS(), WithSiteMeta(nil))
	if len(sitePages) != 6 {
		t.Fatalf("страниц сайта %d, ожидалось 6", len(sitePages))
	}
	seen := map[string]bool{}
	for p, pg := range sitePages {
		rec := siteGet(h, p, map[string]string{"Host": "zafiro.kz", "X-Forwarded-Proto": "https"})
		body := rec.Body.String()
		for _, want := range []string{
			"<title>" + pg.title + "</title>",
			`<meta name="description" content="` + pg.description + `">`,
			`<meta property="og:title" content="` + pg.title + `">`,
			`<meta property="og:description" content="` + pg.description + `">`,
			`<meta property="og:image" content="https://zafiro.kz/icons/icon-512.png">`,
			`<meta property="og:type" content="website">`,
			`<link rel="manifest"`, `<div id="root">`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("%s: нет %q", p, want)
			}
		}
		if strings.Contains(body, "<title>Zafiro</title>") || strings.Count(body, "<title>") != 1 {
			t.Errorf("%s: общий заголовок не заменён", p)
		}
		if rec.Header().Get("Cache-Control") != "no-cache" || rec.Header().Get("ETag") == "" {
			t.Errorf("%s: Cache-Control %q, ETag %q", p, rec.Header().Get("Cache-Control"), rec.Header().Get("ETag"))
		}
		if vary := strings.Join(rec.Header().Values("Vary"), ","); !strings.Contains(vary, "Accept-Language") || !strings.Contains(vary, "Accept-Encoding") || !strings.Contains(vary, "Host") {
			t.Errorf("%s: Vary %q", p, vary)
		}
		if seen[pg.title] || seen[rec.Header().Get("ETag")] {
			t.Errorf("%s: заголовок или ETag повторяет другую страницу", p)
		}
		seen[pg.title], seen[rec.Header().Get("ETag")] = true, true
	}
	// Путь с косой чертой в конце — та же страница.
	if body := siteGet(h, "/pricing/", nil).Body.String(); !strings.Contains(body, "<title>"+sitePages["/pricing"].title+"</title>") {
		t.Errorf("/pricing/: %q", body)
	}
	// Прочие пути — общий index.html без описания.
	common := siteGet(h, "/login", nil)
	for _, p := range []string{"/login", "/dashboard", "/product", "/product/features/x", "/pricing2"} {
		rec := siteGet(h, p, nil)
		body := rec.Body.String()
		if !strings.Contains(body, "<title>Zafiro</title>") || strings.Contains(body, "og:title") || strings.Contains(body, `name="description"`) {
			t.Errorf("%s: не общий index.html", p)
		}
		if rec.Header().Get("ETag") != common.Header().Get("ETag") || strings.Contains(strings.Join(rec.Header().Values("Vary"), ","), "Accept-Language") {
			t.Errorf("%s: ETag %q, Vary %q", p, rec.Header().Get("ETag"), rec.Header().Values("Vary"))
		}
	}
	// Без WithSiteMeta — как раньше.
	if body := siteGet(Handler(siteFS()), "/", nil).Body.String(); !strings.Contains(body, "<title>Zafiro</title>") {
		t.Errorf("без WithSiteMeta: %q", body)
	}
}

// На хостах кабинетов суперадмина и реселлера сайт не показывается — подстановки нет.
func TestSiteMetaNotOnCabinetHosts(t *testing.T) {
	h := Handler(siteFS(), WithSuperadminDomain("admin.example.kz"), WithResellerDomain("resseler.example.kz"), WithSiteMeta(nil))
	for host, attr := range map[string]string{"admin.example.kz": "data-superadmin-host", "resseler.example.kz:8443": "data-reseller-host"} {
		for _, p := range []string{"/", "/pricing", "/product/agent"} {
			body := siteGet(h, p, map[string]string{"Host": host}).Body.String()
			if !strings.Contains(body, "<title>Zafiro</title>") || strings.Contains(body, "og:title") || !strings.Contains(body, attr) {
				t.Errorf("%s%s: %q", host, p, body[:120])
			}
		}
	}
	body := siteGet(h, "/pricing", map[string]string{"Host": "example.kz"}).Body.String()
	if !strings.Contains(body, sitePages["/pricing"].title) || strings.Contains(body, "data-superadmin-host") || strings.Contains(body, "data-reseller-host") {
		t.Errorf("основной хост: %q", body[:200])
	}
}

// Язык — как guestLanguage() клиента; перевод изменился — страница пересобирается.
func TestSiteMetaLanguage(t *testing.T) {
	home := sitePages["/"]
	texts := &fakeTexts{enabled: []string{"ru", "kk", "en"}, tr: map[string]map[string]string{
		"en": {home.title: "Zafiro — computer club management", home.description: "All in one."},
		"kk": {home.title: "Zafiro — клубты басқару"},
	}}
	h := Handler(siteFS(), WithSiteMeta(texts))
	title := func(accept string) string {
		t.Helper()
		body := siteGet(h, "/", map[string]string{"Accept-Language": accept}).Body.String()
		_, rest, _ := strings.Cut(body, "<title>")
		v, _, _ := strings.Cut(rest, "</title>")
		return v
	}
	for accept, want := range map[string]string{
		"ru-RU,ru;q=0.9,en;q=0.8": home.title,
		"kk-KZ,ru;q=0.8":          "Zafiro — клубты басқару",
		"en-US":                   "Zafiro — computer club management",
		"":                        "Zafiro — computer club management", // нет языка — en (DEFAULT_GUEST_LOCALE)
		"de-DE,de":                "Zafiro — computer club management",
		"de, ru;q=0, kk":          "Zafiro — клубты басқару",
	} {
		if got := title(accept); got != want {
			t.Errorf("Accept-Language %q: %q, want %q", accept, got, want)
		}
	}
	// Нет перевода описания (kk) — остаётся русский текст (ключ).
	if body := siteGet(h, "/", map[string]string{"Accept-Language": "kk"}).Body.String(); !strings.Contains(body, `content="`+home.description+`"`) {
		t.Errorf("kk description: %q", body)
	}
	before := siteGet(h, "/", map[string]string{"Accept-Language": "en"}).Header().Get("ETag")
	texts.tr["en"][home.title] = "Zafiro v2"
	rec := siteGet(h, "/", map[string]string{"Accept-Language": "en"})
	if !strings.Contains(rec.Body.String(), "<title>Zafiro v2</title>") || rec.Header().Get("ETag") == before {
		t.Errorf("правка перевода не видна: ETag %q → %q", before, rec.Header().Get("ETag"))
	}

	for _, c := range []struct {
		accept  string
		enabled []string
		want    string
	}{
		{"en", nil, "ru"}, {"en", []string{"ru", "kk"}, "ru"}, {"fr", []string{"kk"}, "kk"}, {"KK_kz", []string{"ru", "kk"}, "kk"},
		{strings.Repeat("xx,", 100) + "kk", []string{"ru", "kk", "en"}, "en"},
	} {
		if got := siteLanguage(c.accept, c.enabled); got != c.want {
			t.Errorf("siteLanguage(%.20q, %v) = %q, want %q", c.accept, c.enabled, got, c.want)
		}
	}
}

// Значения экранируются: ни перевод из суперадминки, ни заголовок Host не дают разметки.
func TestSiteMetaEscapes(t *testing.T) {
	home := sitePages["/"]
	const evil = `"><script>alert(1)</script><img src=x onerror='a()'>&`
	texts := &fakeTexts{enabled: []string{"en"}, tr: map[string]map[string]string{"en": {home.title: "</title>" + evil, home.description: evil}}}
	h := Handler(siteFS(), WithSiteMeta(texts))
	for _, host := range []string{"zafiro.kz", `evil"><script>alert(2)</script>`, "a b", "xn--80a.kz:8080", "[::1]:8080"} {
		body := siteGet(h, "/", map[string]string{"Host": host}).Body.String()
		if strings.Contains(body, "<script") || strings.Contains(body, "<img") || strings.Contains(body, "onerror='") || strings.Count(body, "</title>") != 1 {
			t.Fatalf("host %q: неэкранированная разметка: %q", host, body)
		}
		if !strings.Contains(body, "<title>&lt;/title&gt;&#34;&gt;&lt;script&gt;alert(1)&lt;/script&gt;") {
			t.Fatalf("host %q: заголовок: %q", host, body)
		}
		valid := !strings.ContainsAny(host, `"< `)
		if got := strings.Contains(body, `og:image" content="http://`+host+`/icons/icon-512.png"`); got != valid {
			t.Errorf("host %q: og:image присутствует = %v", host, got)
		}
		if !valid && strings.Contains(body, "og:image") {
			t.Errorf("host %q: og:image с недопустимым хостом", host)
		}
	}
}

// Сжатый ответ страницы сайта: корректный gzip того же тела, Content-Length, свой ETag и 304 по нему.
func TestSiteMetaGzip(t *testing.T) {
	h := Handler(siteFS(), WithSiteMeta(nil))
	plain := siteGet(h, "/pricing", nil)
	if plain.Header().Get("Content-Encoding") != "" {
		t.Fatalf("без Accept-Encoding ответ сжат")
	}
	rec := siteGet(h, "/pricing", map[string]string{"Accept-Encoding": "gzip, br"})
	if rec.Header().Get("Content-Encoding") != "gzip" || rec.Header().Get("Cache-Control") != "no-cache" ||
		rec.Header().Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatalf("заголовки: %v", rec.Header())
	}
	if rec.Header().Get("Content-Length") == "" || rec.Body.Len() >= plain.Body.Len() {
		t.Fatalf("Content-Length %q, %d ≥ %d", rec.Header().Get("Content-Length"), rec.Body.Len(), plain.Body.Len())
	}
	zr, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != plain.Body.String() || !strings.Contains(string(data), sitePages["/pricing"].title) {
		t.Fatalf("распакованное тело отличается от несжатого")
	}
	tag := rec.Header().Get("ETag")
	if tag == "" || tag == plain.Header().Get("ETag") {
		t.Fatalf("ETag gzip %q, identity %q", tag, plain.Header().Get("ETag"))
	}
	if again := siteGet(h, "/pricing", map[string]string{"Accept-Encoding": "gzip", "If-None-Match": tag}); again.Code != http.StatusNotModified {
		t.Fatalf("If-None-Match: %d", again.Code)
	}
	// ETag другой страницы и общего index.html не подходит.
	if other := siteGet(h, "/", map[string]string{"Accept-Encoding": "gzip", "If-None-Match": tag}); other.Code != http.StatusOK {
		t.Fatalf("ETag /pricing принят для /: %d", other.Code)
	}
	if other := siteGet(h, "/login", map[string]string{"Accept-Encoding": "gzip", "If-None-Match": tag}); other.Code != http.StatusOK {
		t.Fatalf("ETag /pricing принят для /login: %d", other.Code)
	}
}

// Тексты заголовков — буквально те же, что ставит клиент (web/src/features/public/*Page.tsx).
func TestSitePagesMatchClient(t *testing.T) {
	const dir = "../../../web/src/features/public"
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Skipf("нет исходников фронта: %v", err)
	}
	var src strings.Builder
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), "Page.tsx") {
			data, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			src.Write(data)
		}
	}
	for p, pg := range sitePages {
		for _, text := range []string{pg.title, pg.description} {
			if !strings.Contains(src.String(), "t('"+text+"')") {
				t.Errorf("%s: текста нет в страницах клиента: %q", p, text)
			}
		}
	}
}
