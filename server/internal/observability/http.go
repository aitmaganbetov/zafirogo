package observability

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"go.opentelemetry.io/otel/attribute"
	otelcodes "go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// Метки маршрута для запросов, которые не дошли до маршрута chi (SPA, старый REST мобильных, 404 /api).
const (
	routeSPA       = "spa"
	routeUnmatched = "unmatched_api"
)

// HTTPMiddleware — метрики и спан на каждый HTTP-запрос; кладёт request id (chi middleware.RequestID) в контекст
// для журнала. Ставится сразу после middleware.RequestID, до RequestLogger: запись «http request» получает
// trace_id. Метка route — шаблон маршрута chi (/api/cash/shifts/{id}), не путь: путь с id дал бы ряд на каждый id.
func (o *Observer) HTTPMiddleware(next http.Handler) http.Handler {
	if o == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ctx := WithRequestID(r.Context(), middleware.GetReqID(r.Context()))
		ctx, span := o.tracer.Start(ctx, "HTTP "+r.Method, trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(attribute.String("http.request.method", r.Method)))
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		o.m.httpInFlight.Inc()
		defer func() {
			o.m.httpInFlight.Dec()
			status := ww.Status()
			if status == 0 {
				status = http.StatusOK
			}
			route := o.g.route.value(routeOf(r))
			method := httpMethod(r.Method)
			code := strconv.Itoa(status)
			o.m.httpRequests.WithLabelValues(method, route, code).Inc()
			if !isUpgrade(r) {
				o.m.httpDuration.WithLabelValues(method, route).Observe(time.Since(start).Seconds())
			}
			if status == http.StatusTooManyRequests {
				o.m.rateLimited.WithLabelValues("http", route).Inc()
			}
			span.SetName(method + " " + route)
			span.SetAttributes(attribute.String("http.route", route), attribute.Int("http.response.status_code", status))
			if status >= 500 {
				span.SetStatus(otelcodes.Error, http.StatusText(status))
			}
			span.End()
		}()
		next.ServeHTTP(ww, r.WithContext(ctx))
	})
}

// routeOf — шаблон маршрута chi после обработки. Пусто — запрос не дошёл до маршрута (NotFound SPA, старый REST
// мобильных перехватывается до маршрутизации, неизвестный /api).
func routeOf(r *http.Request) string {
	if rctx := chi.RouteContext(r.Context()); rctx != nil {
		if p := rctx.RoutePattern(); p != "" && p != "/*" {
			return p
		}
	}
	if r.URL.Path == "/api" || strings.HasPrefix(r.URL.Path, "/api/") {
		return routeUnmatched
	}
	return routeSPA
}

// httpMethod — метод из конечного набора (произвольный метод клиента — other).
func httpMethod(m string) string {
	switch m {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete,
		http.MethodOptions:
		return m
	}
	return labelOther
}

func isUpgrade(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
}
