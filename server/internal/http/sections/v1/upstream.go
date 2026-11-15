package v1

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aitmaganbetov/zafirogo/server/internal/apikey"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// LegacyUpstream — поклубный переход для /api/v1 (S6-O1-4, по образцу прокси мобильных, docs/mobile-compat.md §3):
// балансировщик отправляет весь /api/v1 сюда; запрос с ключом правильного формата, чьего префикса нет в api_keys
// этого сервера (ключ клуба, ещё работающего в Laravel), уходит в upstream без изменений (обратный прокси). Свой
// ключ — даже отозванный, истёкший или с неверным секретом — обслуживается здесь (ответ, как дал бы Laravel по
// своей копии ключа); запрос без ключа или с ключом не того формата — тоже здесь (401 invalid_api_key, как в Laravel).
// upstream nil — middleware ничего не делает.
func LegacyUpstream(d *httpx.Deps, upstream *url.URL, host string) func(http.Handler) http.Handler {
	if upstream == nil {
		return func(next http.Handler) http.Handler { return next }
	}
	log := d.Log()
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(upstream)
			pr.SetXForwarded()
			if host != "" {
				pr.Out.Host = host
			} else {
				pr.Out.Host = pr.In.Host
			}
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.WarnContext(r.Context(), "api v1: legacy upstream", slog.String("err", err.Error()))
			errorResponse("server_error", http.StatusBadGateway).write(w)
		},
	}
	return upstreamMiddleware(upstreamOptions{
		known:    func(ctx context.Context, prefix string) (bool, error) { return keyKnown(ctx, d, prefix) },
		upstream: proxy, log: log, counter: d.APICounter, limits: d.APILimits, now: d.NowFunc,
	})
}

// Кэш решения «префикс ключа есть на этом сервере» (аудит безопасности A-06): без него каждый запрос к /api/v1 с
// ключом, которого здесь нет, открывал транзакцию Postgres. Сроки короткие: ключ, созданный здесь при переходе
// клуба, начинает обслуживаться здесь не позже чем через knownMissTTL; удалённый отсюда — уходит в Laravel через
// knownHitTTL (до того отвечает 401 здесь). Размер ограничен — перебор префиксов память не растит.
const (
	knownHitTTL   = time.Minute
	knownMissTTL  = 10 * time.Second
	knownCacheMax = 4096
)

type knownEntry struct {
	ok  bool
	exp time.Time
}

type knownCache struct {
	mu sync.Mutex
	m  map[string]knownEntry
}

func (c *knownCache) get(prefix string, now time.Time) (ok, hit bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, found := c.m[prefix]
	if !found || !now.Before(e.exp) {
		return false, false
	}
	return e.ok, true
}

func (c *knownCache) put(prefix string, ok bool, now time.Time) {
	ttl := knownMissTTL
	if ok {
		ttl = knownHitTTL
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]knownEntry{}
	}
	if len(c.m) >= knownCacheMax {
		for k, e := range c.m {
			if !now.Before(e.exp) {
				delete(c.m, k)
			}
		}
		if len(c.m) >= knownCacheMax {
			clear(c.m) // перебор префиксов: проще начать заново, чем вести порядок вытеснения
		}
	}
	c.m[prefix] = knownEntry{ok: ok, exp: now.Add(ttl)}
}

// upstreamOptions — зависимости решения «здесь или в Laravel».
type upstreamOptions struct {
	// known — есть ли префикс ключа на этом сервере (Postgres).
	known    func(ctx context.Context, prefix string) (bool, error)
	upstream http.Handler
	log      *slog.Logger
	// counter, limits — те же счётчики и лимиты, что у раздела v1 (блокировка IP за подбор ключа, предел тела).
	// counter nil — без блокировки IP (тесты).
	counter apikey.Counter
	limits  apikey.Limits
	now     func() time.Time
}

// statusRecorder запоминает статус ответа upstream.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

// Unwrap — для http.ResponseController (Flush обратного прокси).
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// upstreamMiddleware — решение «здесь или в Laravel».
//
// Порядок для ключа, которого здесь нет (аудит безопасности A-06 — раньше такой запрос шёл в Laravel мимо всех
// лимитов Go и с чтением Postgres на каждый запрос):
//  1. блокировка IP за подбор ключа — тот же счётчик auth-failed:<ip> и тот же ответ 429, что в authenticate
//     (AuthenticateApiKey: failed_auth_per_minute_per_ip); счётчик растёт и от 401 из Laravel;
//  2. предел размера тела — как limitSize (LimitApiRequestSize): 413 по Content-Length, а тело без него
//     обрезается при чтении;
//  3. префикс — из кэша; в Postgres — только при промахе;
//  4. прокси в Laravel; ответ 401 считается неудачной попыткой этого IP.
func upstreamMiddleware(o upstreamOptions) func(http.Handler) http.Handler {
	if o.now == nil {
		o.now = time.Now
	}
	cache := &knownCache{}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != Prefix && !strings.HasPrefix(r.URL.Path, Prefix+"/") {
				next.ServeHTTP(w, r)
				return
			}
			prefix := apikey.KeyPrefix(presentedToken(r))
			if prefix == "" {
				next.ServeHTTP(w, r)
				return
			}
			ctx := r.Context()
			ok, hit := cache.get(prefix, o.now())
			if hit && ok {
				next.ServeHTTP(w, r) // свой ключ: лимиты и предел тела — в цепочке раздела
				return
			}
			ip := httpx.HostOnly(r.RemoteAddr)
			failKey := "auth-failed:" + ip
			if o.counter != nil && o.limits.FailedAuthPerMinute > 0 {
				n, err := o.counter.Get(ctx, failKey)
				if err != nil {
					o.log.ErrorContext(ctx, "api v1: failed auth counter for legacy upstream", slog.String("err", err.Error()))
					errorResponse("server_error", http.StatusInternalServerError).write(w)
					return
				}
				if n >= int64(o.limits.FailedAuthPerMinute) {
					ttl, _ := o.counter.TTL(ctx, failKey)
					res := errorResponse("too_many_failed_attempts", http.StatusTooManyRequests)
					res.setHeader("Retry-After", strconv.FormatInt(max(int64(math.Ceil(ttl.Seconds())), 0), 10))
					res.write(w)
					return
				}
			}
			if !hit {
				var err error
				if ok, err = o.known(ctx, prefix); err != nil {
					o.log.ErrorContext(ctx, "api v1: key lookup for legacy upstream", slog.String("err", err.Error()))
					errorResponse("server_error", http.StatusInternalServerError).write(w)
					return
				}
				cache.put(prefix, ok, o.now())
			}
			if ok {
				next.ServeHTTP(w, r)
				return
			}
			if maxBytes := o.limits.MaxRequestBytes; maxBytes > 0 {
				if r.ContentLength > maxBytes {
					errorResponse("payload_too_large", http.StatusRequestEntityTooLarge).write(w)
					return
				}
				if r.Body != nil && r.Body != http.NoBody {
					r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
				}
			}
			rec := &statusRecorder{ResponseWriter: w}
			o.upstream.ServeHTTP(rec, r)
			if rec.status == http.StatusUnauthorized && o.counter != nil {
				// Laravel ключ не признал — подбор (как reason == "invalid_api_key" в authenticate).
				if _, err := o.counter.Incr(context.WithoutCancel(ctx), failKey, failedAuthWindow); err != nil {
					o.log.WarnContext(ctx, "api v1: failed auth counter", slog.String("err", err.Error()))
				}
			}
		})
	}
}

// keyKnown — есть ли ключ с этим префиксом на этом сервере (политика api_key_lookup, как verify).
func keyKnown(ctx context.Context, d *httpx.Deps, prefix string) (bool, error) {
	if d.Pool == nil {
		return true, nil // без базы — обслуживаем здесь (verify ответит «не настроено»)
	}
	found := false
	err := pgx.BeginTxFunc(ctx, d.Pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT set_config('app.api_key_prefix', $1, true)", prefix); err != nil {
			return err
		}
		_, err := store.New(tx).ApiV1FindKey(ctx, prefix)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return nil
		case err != nil:
			return err
		}
		found = true
		return nil
	})
	return found, err
}
