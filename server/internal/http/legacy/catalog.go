package legacy

// Каталог клубов гостевого приложения: до входа приложение показывает клубы города гостя (города приходят с
// координатами — ближайший приложение находит на телефоне) и страницу клуба. Тема В, docs/upstream-2026-11.md.
// Laravel: app/Http/Controllers/Api/ClubCatalogController.php (routes/api.php: api.catalog.clubs — throttle:30,1,
// api.catalog.club — throttle:60,1; маршруты публичные, без токена).
//
// Отличия от старого (docs/open-questions/upstream-catalog.md):
//   - «ПК в сети» — по присутствию агентов в Valkey (presence), а не по last_seen_at в базе (UQ-20);
//   - ответ кэшируется на catalogTTL в памяти инстанса (запрос публичный, по всем клубам города);
//   - агрегаты — в SQL: список — 3 запроса и одно обращение к Valkey, страница клуба — 4 запроса (в старом —
//     запросы на каждый счётчик и три запроса найденного железа);
//   - при поклубном переходе (UQ-2) список объединяется со списком старого сервера, страница клуба, которого здесь
//     нет, — оттуда же (docs/mobile-compat.md §3.5).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"

	"github.com/aitmaganbetov/zafirogo/server/internal/cash"
	"github.com/aitmaganbetov/zafirogo/server/internal/catalog"
	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

const (
	// catalogTTL — срок кэша ответа каталога (решение UQ-20: 30–60 с).
	catalogTTL = 45 * time.Second
	// catalogCacheMax — предел записей кэша (города × языки и страницы клубов × языки); сверх — кэш сбрасывается.
	catalogCacheMax = 4096
	// catalogLimit — ->limit(500) списка клубов города.
	catalogLimit = 500
	// catalogUpstreamTimeout — ожидание списка старого сервера; не ответил — отдаём свои клубы.
	catalogUpstreamTimeout = 5 * time.Second
	// catalogUpstreamMax — предел ответа старого сервера (500 карточек — сотни килобайт).
	catalogUpstreamMax = 4 << 20
	// catalogBuildTimeout — срок общего расчёта ответа (база, Valkey и старый сервер с его catalogUpstreamTimeout).
	catalogBuildTimeout = 15 * time.Second
	// catalogMissTTL — на сколько запоминается «клуба здесь нет»: короче catalogTTL, чтобы клуб, только что
	// переехавший или получивший город, появился быстро. catalogMissMax — предел таких записей.
	catalogMissTTL = 30 * time.Second
	catalogMissMax = 1024
)

// onlineChecker — присутствие ПК для «свободных ПК» (*presence.Store).
type onlineChecker interface {
	OnlineAmong(ctx context.Context, pcs [][2]int64, now time.Time) ([]bool, error)
}

// catalogCache — готовые ответы каталога (JSON) по ключу «вид|аргумент|язык» на catalogTTL. Часы — настоящие
// (не dev.fixed_now): срок кэша не связан со временем клуба.
type catalogCache struct {
	mu      sync.Mutex
	entries map[string]catalogEntry
	// missing — «клуба здесь нет» (errCatalogNoClub) до указанного времени: перебор номеров не ходит в базу на
	// каждый запрос. Отдельно от entries, чтобы такой перебор не вытеснял готовые ответы.
	missing map[string]time.Time
	// flights — один расчёт на ключ: одновременные запросы ждут его, а не считают каждый своё.
	flights map[string]*catalogFlight
}

type catalogEntry struct {
	body    []byte
	expires time.Time
}

type catalogFlight struct {
	done chan struct{}
	body []byte
	err  error
}

// get — ответ из кэша или build(); cache=false от build — ответ не запоминается (например, старый сервер не ответил).
//
// Расчёт общий для всех ждущих, поэтому идёт не в контексте запроса, который его начал (обрыв этого клиента не
// должен давать ошибку остальным), а в своём: значения контекста сохраняются, отмена — нет, срок — catalogBuildTimeout.
// Каждый запрос ждёт результат до отмены своего контекста.
func (c *catalogCache) get(ctx context.Context, key string, build func(context.Context) (body []byte, cache bool, err error)) ([]byte, error) {
	now := time.Now()
	c.mu.Lock()
	if e, ok := c.entries[key]; ok && now.Before(e.expires) {
		c.mu.Unlock()
		return e.body, nil
	}
	if until, ok := c.missing[key]; ok {
		if now.Before(until) {
			c.mu.Unlock()
			return nil, errCatalogNoClub
		}
		delete(c.missing, key)
	}
	f, ok := c.flights[key]
	if !ok {
		if c.flights == nil {
			c.flights, c.entries, c.missing = map[string]*catalogFlight{}, map[string]catalogEntry{}, map[string]time.Time{}
		}
		f = &catalogFlight{done: make(chan struct{})}
		c.flights[key] = f
		go c.run(context.WithoutCancel(ctx), key, f, build)
	}
	c.mu.Unlock()

	select {
	case <-f.done:
		return f.body, f.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// run — расчёт одного ключа. Расчёт закрывается всегда, и при панике build: иначе ключ не считался бы больше никогда,
// а ждущие висели бы до своих таймаутов.
func (c *catalogCache) run(ctx context.Context, key string, f *catalogFlight, build func(context.Context) ([]byte, bool, error)) {
	ctx, cancel := context.WithTimeout(ctx, catalogBuildTimeout)
	defer cancel()
	var cache bool
	defer func() {
		if p := recover(); p != nil {
			f.body, cache = nil, false
			f.err = fmt.Errorf("catalog: build panic: %v\n%s", p, debug.Stack())
		}
		c.mu.Lock()
		delete(c.flights, key)
		switch {
		case f.err == nil && cache:
			if len(c.entries) >= catalogCacheMax {
				c.entries = map[string]catalogEntry{}
			}
			c.entries[key] = catalogEntry{body: f.body, expires: time.Now().Add(catalogTTL)}
		case errors.Is(f.err, errCatalogNoClub):
			if len(c.missing) >= catalogMissMax {
				c.dropMissing()
			}
			c.missing[key] = time.Now().Add(catalogMissTTL)
		}
		c.mu.Unlock()
		close(f.done)
	}()
	f.body, cache, f.err = build(ctx)
}

// dropMissing — место для новой записи «клуба нет»: сначала уходят просроченные, если их нет — все.
func (c *catalogCache) dropMissing() {
	now := time.Now()
	for k, until := range c.missing {
		if !now.Before(until) {
			delete(c.missing, k)
		}
	}
	if len(c.missing) >= catalogMissMax {
		c.missing = map[string]time.Time{}
	}
}

// ---------------------------------------------------------------------------
// Формат ответов (порядок ключей — как в PHP-массивах контроллера)
// ---------------------------------------------------------------------------

type catalogCity struct {
	Key   string  `json:"key"`
	Name  string  `json:"name"`
	Lat   float64 `json:"lat"`
	Lng   float64 `json:"lng"`
	Clubs int64   `json:"clubs"`
}

// catalogCard — карточка клуба в списке города. price_from — (float) тенге, null без цен.
type catalogCard struct {
	ID        int64    `json:"id"`
	Name      string   `json:"name"`
	Address   *string  `json:"address"`
	LogoURL   *string  `json:"logo_url"`
	PhotoURL  *string  `json:"photo_url"`
	Hours     *string  `json:"hours"`
	Pcs       int64    `json:"pcs"`
	FreePcs   int64    `json:"free_pcs"`
	PriceFrom *float64 `json:"price_from"`
}

type catalogDevice struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Value string `json:"value"`
}

type catalogZone struct {
	ID           int64           `json:"id"`
	Name         string          `json:"name"`
	Description  *string         `json:"description"`
	PricePerHour float64         `json:"price_per_hour"`
	Pcs          int64           `json:"pcs"`
	FreePcs      int64           `json:"free_pcs"`
	Devices      []catalogDevice `json:"devices"`
}

type catalogClub struct {
	ID          int64         `json:"id"`
	Name        string        `json:"name"`
	City        *string       `json:"city"`
	Address     *string       `json:"address"`
	LogoURL     *string       `json:"logo_url"`
	Description *string       `json:"description"`
	Phone       *string       `json:"phone"`
	Hours       *string       `json:"hours"`
	Instagram   *string       `json:"instagram"`
	Photos      []string      `json:"photos"`
	Zones       []catalogZone `json:"zones"`
}

// tengeFloat — (float) decimal(…, 2) тенге из тиынов; json_encode без JSON_PRESERVE_ZERO_FRACTION: 500, 500.5.
func tengeFloat(t int64) float64 { return float64(t) / 100 }

func strOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// publicURL — route(…): адрес сайта + путь.
func (a *API) publicURL(dir string, clubID int64, p string) string {
	return strings.TrimRight(a.o.PublicBaseURL, "/") + "/" + dir + "/" + strconv.FormatInt(clubID, 10) + "/" + basename(p)
}

// logoURL — Club::logoUrl(): null без логотипа.
func (a *API) logoURL(clubID int64, p *string) *string {
	if p == nil {
		return nil
	}
	u := a.publicURL("club-logos", clubID, *p)
	return &u
}

// ---------------------------------------------------------------------------
// Свободные ПК
// ---------------------------------------------------------------------------

// freePCs — свободные ПК клубов по зонам: свободен по базе (CatalogFreeCandidates) и агент на связи (presence).
// Без присутствия (Valkey не подключён или не ответил) свободных нет — как у ПК не в сети.
// Laravel: ClubCatalogController::free().
func (a *API) freePCs(ctx context.Context, q *store.Queries, clubIDs []int64) (byClub map[int64]int64, byZone map[int64]int64, err error) {
	byClub, byZone = map[int64]int64{}, map[int64]int64{}
	online, ok := a.d.Presence.(onlineChecker)
	if !ok || online == nil || len(clubIDs) == 0 {
		return byClub, byZone, nil
	}
	rows, err := q.CatalogFreeCandidates(ctx, store.CatalogFreeCandidatesParams{ClubIds: clubIDs, Now: a.d.NowFunc()})
	if err != nil || len(rows) == 0 {
		return byClub, byZone, err
	}
	pcs := make([][2]int64, len(rows))
	for i, r := range rows {
		pcs[i] = [2]int64{r.ClubID, r.ID}
	}
	// Присутствие — по часам Valkey, не по dev.fixed_now.
	on, err := online.OnlineAmong(ctx, pcs, time.Now())
	if err != nil {
		a.o.Log.WarnContext(ctx, "legacy catalog: presence", slog.String("err", err.Error()))
		return byClub, byZone, nil
	}
	for i, r := range rows {
		if on[i] {
			byClub[r.ClubID]++
			byZone[r.ClubZoneID]++
		}
	}
	return byClub, byZone, nil
}

// ---------------------------------------------------------------------------
// GET /api/catalog/clubs?city=
// ---------------------------------------------------------------------------

// catalogIndex — ClubCatalogController::index(): города с числом клубов и клубы выбранного города.
func (a *API) catalogIndex(w http.ResponseWriter, r *http.Request) {
	// 'city' => ['nullable', 'string', Rule::in(Cities::keys())]; пустая строка — null (ConvertEmptyStringsToNull).
	q := r.URL.Query()
	if _, isArray := q["city[]"]; isArray {
		a.writeError(w, r, httpx.RuleError("city", "string"))
		return
	}
	city := catalog.Trim(q.Get("city"))
	if _, ok := catalog.CityByKey(city); city != "" && !ok {
		a.writeError(w, r, httpx.RuleError("city", cash.RuleIn))
		return
	}
	locale := a.locale(r)
	from := catalogClientOf(r)
	body, err := a.catalog.get(r.Context(), "index|"+city+"|"+locale, func(ctx context.Context) ([]byte, bool, error) {
		return a.buildCatalogIndex(ctx, from, city, locale)
	})
	switch {
	case err != nil && r.Context().Err() != nil:
		// Клиент ушёл, не дождавшись: отвечать некому, расчёт закончится для остальных.
	case err != nil:
		a.fail(w, r, "catalog index", err)
	default:
		writeRawJSON(w, http.StatusOK, body)
	}
}

// catalogClient — то из запроса, что нужно запросу к старому серверу. Снимается до расчёта: расчёт может пережить
// запрос, который его начал.
type catalogClient struct {
	remoteAddr, forwardedFor string
}

func catalogClientOf(r *http.Request) catalogClient {
	return catalogClient{remoteAddr: r.RemoteAddr, forwardedFor: r.Header.Get("X-Forwarded-For")}
}

func (a *API) buildCatalogIndex(ctx context.Context, from catalogClient, city, locale string) ([]byte, bool, error) {
	if a.d.PlatformPool == nil {
		return nil, false, httpx.ErrNotConfigured
	}
	var (
		counts []store.CatalogCityCountsRow
		clubs  []store.CatalogCityClubsRow
		free   map[int64]int64
	)
	err := db.InPlatformTx(ctx, a.d.PlatformPool, func(q *store.Queries) error {
		var err error
		if counts, err = q.CatalogCityCounts(ctx); err != nil {
			return err
		}
		if city == "" {
			return nil
		}
		if clubs, err = q.CatalogCityClubs(ctx, city); err != nil {
			return err
		}
		ids := make([]int64, len(clubs))
		for i, c := range clubs {
			ids[i] = c.ID
		}
		free, _, err = a.freePCs(ctx, q, ids)
		return err
	})
	if err != nil {
		return nil, false, err
	}
	byCity := make(map[string]int64, len(counts))
	for _, c := range counts {
		byCity[c.City] = c.Clubs
	}
	cards := make([]json.RawMessage, 0, len(clubs))
	names := make([]string, 0, len(clubs))
	local := make(map[int64]bool, len(clubs))
	for _, c := range clubs {
		card := catalogCard{ID: c.ID, Name: c.Name, Address: c.Address, LogoURL: a.logoURL(c.ID, c.LogoPath), Pcs: c.Pcs, FreePcs: free[c.ID]}
		if c.PhotoPath != "" {
			u := a.publicURL("club-photos", c.ID, c.PhotoPath)
			card.PhotoURL = &u
		}
		if c.HasHours {
			card.Hours = &c.Hours
		}
		if c.HasPrice {
			p := tengeFloat(c.PriceFrom)
			card.PriceFrom = &p
		}
		raw, err := marshalJSON(card)
		if err != nil {
			return nil, false, err
		}
		cards, names, local[c.ID] = append(cards, raw), append(names, c.Name), true
	}

	// Поклубный переход (UQ-2): клубы, которые ещё на старом сервере, — из его каталога.
	cache := true
	if a.o.Upstream != nil {
		up, err := a.upstreamCatalog(ctx, from, city, locale)
		if err != nil {
			a.o.Log.WarnContext(ctx, "legacy catalog: upstream", slog.String("err", err.Error()))
			cache = false
		} else {
			cards = up.mergeInto(byCity, city, cards, names, local)
		}
	}

	opts := catalog.CityOptions(func(name string) string { return a.tl(ctx, locale, name, nil) })
	out := struct {
		Cities []catalogCity     `json:"cities"`
		City   *string           `json:"city"`
		Clubs  []json.RawMessage `json:"clubs"`
	}{Cities: make([]catalogCity, len(opts)), City: strOrNil(city), Clubs: cards}
	for i, o := range opts {
		out.Cities[i] = catalogCity{Key: o.Key, Name: o.Label, Lat: o.Lat, Lng: o.Lng, Clubs: byCity[o.Key]}
	}
	body, err := marshalJSON(out)
	return body, cache, err
}

// nameKey — ключ сравнения названий при объединении двух списков (оба уже отсортированы своей базой без учёта
// регистра).
func nameKey(s string) string { return strings.Map(unicode.ToLower, s) }

// mergeByName — объединение двух отсортированных по названию списков карточек; не больше catalogLimit.
func mergeByName(a []json.RawMessage, an []string, b []json.RawMessage, bn []string) []json.RawMessage {
	type item struct {
		raw  json.RawMessage
		key  string
		from int
	}
	all := make([]item, 0, len(a)+len(b))
	for i := range a {
		all = append(all, item{a[i], nameKey(an[i]), 0})
	}
	for i := range b {
		all = append(all, item{b[i], nameKey(bn[i]), 1})
	}
	// Устойчиво: порядок внутри каждого списка (его задала база сервера) сохраняется при равных ключах.
	sort.SliceStable(all, func(i, j int) bool { return all[i].key < all[j].key })
	if len(all) > catalogLimit {
		all = all[:catalogLimit]
	}
	out := make([]json.RawMessage, len(all))
	for i, it := range all {
		out[i] = it.raw
	}
	return out
}

// upstreamIndex — нужное из ответа каталога старого сервера; карточки — как есть.
type upstreamIndex struct {
	Cities []struct {
		Key   string `json:"key"`
		Clubs int64  `json:"clubs"`
	} `json:"cities"`
	Clubs []json.RawMessage `json:"clubs"`
}

// mergeInto добавляет к своему списку города клубы старого сервера: его счётчики городов — в byCity, его карточки —
// в общий список по названию. Клуб, который есть в local (уже переехал, а на старом сервере его город не очищен),
// показывается один раз — наш.
func (up upstreamIndex) mergeInto(byCity map[string]int64, city string, cards []json.RawMessage, names []string, local map[int64]bool) []json.RawMessage {
	for _, c := range up.Cities {
		byCity[c.Key] += c.Clubs
	}
	var upCards []json.RawMessage
	var upNames []string
	for _, raw := range up.Clubs {
		var head struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		}
		if json.Unmarshal(raw, &head) != nil || head.ID == 0 {
			continue
		}
		if local[head.ID] {
			byCity[city]--
			continue
		}
		upCards, upNames = append(upCards, raw), append(upNames, head.Name)
	}
	return mergeByName(cards, names, upCards, upNames)
}

// upstreamCatalog — GET /api/catalog/clubs старого сервера (тот же город и язык). Адрес клиента — в
// X-Forwarded-For: лимит throttle:30,1 старого сервера считается по нему (доверенный прокси), а не по нашему адресу.
func (a *API) upstreamCatalog(ctx context.Context, from catalogClient, city, locale string) (upstreamIndex, error) {
	var out upstreamIndex
	u := *a.o.Upstream
	u.Path = strings.TrimRight(u.Path, "/") + "/api/catalog/clubs"
	u.RawQuery = ""
	if city != "" {
		u.RawQuery = url.Values{"city": {city}}.Encode()
	}
	ctx, cancel := context.WithTimeout(ctx, catalogUpstreamTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return out, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Language", locale)
	if ip, _, err := net.SplitHostPort(from.remoteAddr); err == nil {
		if prior := from.forwardedFor; prior != "" {
			ip = prior + ", " + ip
		}
		req.Header.Set("X-Forwarded-For", ip)
	}
	// Host — mobile.upstream_host, иначе хост адреса апстрима (не Host клиента: ответ ложится в общий кэш каталога,
	// и клиент с подставным Host мог бы подменить клубы старого сервера для всех; аудит SEC-9).
	if a.o.UpstreamHost != "" {
		req.Host = a.o.UpstreamHost
	}
	res, err := a.upstreamClient.Do(req)
	if err != nil {
		return out, err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return out, errors.New("upstream catalog: status " + strconv.Itoa(res.StatusCode))
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, catalogUpstreamMax+1))
	if err != nil {
		return out, err
	}
	if len(raw) > catalogUpstreamMax {
		return out, errors.New("upstream catalog: response too large")
	}
	return out, json.Unmarshal(raw, &out)
}

// ---------------------------------------------------------------------------
// GET /api/catalog/clubs/{club}
// ---------------------------------------------------------------------------

// errCatalogNoClub — клуба с городом здесь нет.
var errCatalogNoClub = errors.New("catalog: no club")

// catalogShow — ClubCatalogController::show(): страница клуба; клуб без города — 404.
func (a *API) catalogShow(w http.ResponseWriter, r *http.Request) {
	id, _, ok := pathID(w, r, "club", "Club")
	if !ok {
		return
	}
	locale := a.locale(r)
	body, err := a.catalog.get(r.Context(), "club|"+strconv.FormatInt(id, 10)+"|"+locale, func(ctx context.Context) ([]byte, bool, error) {
		b, err := a.buildCatalogClub(ctx, id, locale)
		return b, true, err
	})
	switch {
	case errors.Is(err, errCatalogNoClub):
		// «Клуба нет» запоминается на catalogMissTTL (catalogCache.missing).
		// Клуб ещё на старом сервере (UQ-2) — его страницу отдаёт он.
		if a.forward(w, r) {
			return
		}
		modelNotFound(w, "Club", strconv.FormatInt(id, 10))
	case err != nil && r.Context().Err() != nil:
		// Клиент ушёл, не дождавшись.
	case err != nil:
		a.fail(w, r, "catalog club", err)
	default:
		writeRawJSON(w, http.StatusOK, body)
	}
}

func (a *API) buildCatalogClub(ctx context.Context, id int64, locale string) ([]byte, error) {
	if a.d.PlatformPool == nil {
		return nil, httpx.ErrNotConfigured
	}
	var (
		club   store.CatalogClubRow
		zones  []store.ClubProfileZonesRow
		photos []store.ClubProfilePhotosRow
		free   map[int64]int64
	)
	err := db.InPlatformTx(ctx, a.d.PlatformPool, func(q *store.Queries) error {
		var err error
		if club, err = q.CatalogClub(ctx, id); err != nil {
			return err
		}
		if zones, err = q.ClubProfileZones(ctx, id); err != nil {
			return err
		}
		if photos, err = q.ClubProfilePhotos(ctx, id); err != nil {
			return err
		}
		_, free, err = a.freePCs(ctx, q, []int64{id})
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errCatalogNoClub
	}
	if err != nil {
		return nil, err
	}
	var profile catalog.Profile
	if len(club.Profile) > 0 {
		_ = json.Unmarshal(club.Profile, &profile)
	}
	tr := func(key string, params map[string]string) string { return a.tl(ctx, locale, key, params) }
	out := catalogClub{ID: club.ID, Name: club.Name, Address: club.Address, LogoURL: a.logoURL(club.ID, club.LogoPath),
		Description: strOrNil(profile.Description), Phone: strOrNil(profile.Phone), Hours: strOrNil(profile.Hours),
		Instagram: strOrNil(profile.Instagram), Photos: make([]string, 0, len(photos)), Zones: make([]catalogZone, 0, len(zones))}
	// Cities::name($club->city): null, если ключа нет в списке.
	if c, ok := catalog.CityByKey(club.City); ok {
		name := tr(c.Name, nil)
		out.City = &name
	}
	for _, p := range photos {
		out.Photos = append(out.Photos, a.publicURL("club-photos", club.ID, p.Path))
	}
	fields := catalog.DeviceFields()
	for _, z := range zones {
		zone := catalogZone{ID: z.ID, Name: z.Name, Description: z.Description, Pcs: z.ComputersCount, FreePcs: free[z.ID],
			Devices: make([]catalogDevice, 0, len(fields))}
		if z.PricePerHour != nil {
			zone.PricePerHour = tengeFloat(*z.PricePerHour) // (float) null — 0
		}
		// $zone->devices[$key] ?? $detected[$zone->id][$key] ?? null: значение клуба, иначе найденное агентами.
		var own map[string]any
		if len(z.Devices) > 0 {
			_ = json.Unmarshal(z.Devices, &own)
		}
		detected := catalog.Detected{CPU: z.CpuModel, HasCPU: z.HasCpu, GPU: z.GpuModel, HasGPU: z.HasGpu,
			RAMMegabytes: z.RamTotalMb, HasRAM: z.HasRam}.Values(tr)
		for _, f := range fields {
			value, ok := own[f.Key].(string)
			if !ok {
				if value, ok = detected[f.Key]; !ok {
					continue
				}
			}
			zone.Devices = append(zone.Devices, catalogDevice{Key: f.Key, Label: tr(f.Label, nil), Value: value})
		}
		out.Zones = append(out.Zones, zone)
	}
	return marshalJSON(out)
}
