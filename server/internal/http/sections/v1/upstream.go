package v1

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

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
	return upstreamMiddleware(func(ctx context.Context, prefix string) (bool, error) {
		return keyKnown(ctx, d, prefix)
	}, proxy, log)
}

// upstreamMiddleware — решение «здесь или в Laravel» (known — есть ли префикс ключа на этом сервере).
func upstreamMiddleware(known func(ctx context.Context, prefix string) (bool, error), upstream http.Handler, log *slog.Logger) func(http.Handler) http.Handler {
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
			ok, err := known(r.Context(), prefix)
			if err != nil {
				log.ErrorContext(r.Context(), "api v1: key lookup for legacy upstream", slog.String("err", err.Error()))
				errorResponse("server_error", http.StatusInternalServerError).write(w)
				return
			}
			if ok {
				next.ServeHTTP(w, r)
				return
			}
			upstream.ServeHTTP(w, r)
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
