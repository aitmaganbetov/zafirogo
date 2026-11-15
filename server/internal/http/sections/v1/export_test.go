package v1

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/apikey"
)

// RouteInfo — маршрут для тестов пакета v1_test.
type RouteInfo struct{ Method, Path, Name, Scope string }

// Routes — routes/api_v1.php в виде, удобном тестам.
func Routes() []RouteInfo {
	out := make([]RouteInfo, len(routes))
	for i, r := range routes {
		out[i] = RouteInfo{r.method, r.path, r.name, r.scope}
	}
	return out
}

// Exported for tests.
var (
	MaskPhone    = maskPhone
	EncodeCursor = encodeCursor
	DecodeCursor = decodeCursor
)

// UpstreamMiddleware — решение «здесь или в Laravel» без базы (тесты).
func UpstreamMiddleware(known func(ctx context.Context, prefix string) (bool, error), upstream http.Handler, log *slog.Logger) func(http.Handler) http.Handler {
	return upstreamMiddleware(upstreamOptions{known: known, upstream: upstream, log: log})
}

// UpstreamGate — то же с лимитами раздела (блокировка IP, предел тела) и часами.
func UpstreamGate(known func(ctx context.Context, prefix string) (bool, error), upstream http.Handler, log *slog.Logger,
	counter apikey.Counter, limits apikey.Limits, now func() time.Time) func(http.Handler) http.Handler {
	return upstreamMiddleware(upstreamOptions{known: known, upstream: upstream, log: log, counter: counter, limits: limits, now: now})
}
