package observability

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/trace"
)

// redacted — замена значения секрета в журнале (как config.Secret).
const redacted = "[REDACTED]"

// LogOptions — параметры LogHandler.
type LogOptions struct {
	// SampleInitial / SampleThereafter — выборка шумных записей уровней debug/info: в пределах секунды по одному
	// сообщению пишутся первые SampleInitial записей, дальше — каждая SampleThereafter-я. 0 — без выборки.
	// warn и error не отбрасываются никогда.
	SampleInitial, SampleThereafter int
}

// NewLogHandler оборачивает обработчик журнала:
//   - добавляет request_id (HTTP и gRPC) и trace_id/span_id (если есть span-контекст) в записи с контекстом
//     (InfoContext/LogAttrs(ctx, …));
//   - маскирует значения атрибутов с ключами-секретами (password, token, secret, authorization, cookie, pepper, dsn,
//     pin, api_key …) и строки, похожие на токен (Bearer …, ключи интеграций zfk_…) — второй рубеж после правила
//     «тела, заголовки и пароли не логируются» (logging, RequestLogger);
//   - выборка шумных записей debug/info (LogOptions);
//   - считает ошибки фоновых задач: запись уровня warn+ с атрибутом job или worker → worker_errors_total.
//
// o может быть nil (без счётчиков).
func NewLogHandler(inner slog.Handler, o *Observer, opts LogOptions) slog.Handler {
	h := &logHandler{inner: inner, o: o}
	if opts.SampleInitial > 0 && opts.SampleThereafter > 0 {
		h.sampler = &sampler{initial: opts.SampleInitial, thereafter: opts.SampleThereafter, counts: map[string]*sampleCount{}}
	}
	return h
}

type logHandler struct {
	inner   slog.Handler
	o       *Observer
	sampler *sampler
	// hasRequestID — WithAttrs уже добавил request_id (не дублировать).
	hasRequestID bool
}

func (h *logHandler) Enabled(ctx context.Context, l slog.Level) bool { return h.inner.Enabled(ctx, l) }

func (h *logHandler) Handle(ctx context.Context, r slog.Record) error {
	if r.Level >= slog.LevelWarn && h.o != nil {
		h.countWorkerError(r)
	}
	if r.Level < slog.LevelWarn && h.sampler != nil && !h.sampler.allow(r.Message, r.Time) {
		if h.o != nil {
			h.o.m.logDropped.WithLabelValues(strings.ToLower(r.Level.String())).Inc()
		}
		return nil
	}
	out := slog.NewRecord(r.Time, r.Level, r.Message, r.PC)
	hasReqID, hasTrace := h.hasRequestID, false
	r.Attrs(func(a slog.Attr) bool {
		switch a.Key {
		case "request_id":
			hasReqID = true
		case "trace_id":
			hasTrace = true
		}
		out.AddAttrs(redactAttr(a))
		return true
	})
	if ctx != nil {
		if id := RequestID(ctx); id != "" && !hasReqID {
			out.AddAttrs(slog.String("request_id", id))
		}
		if sc := trace.SpanContextFromContext(ctx); sc.IsValid() && !hasTrace {
			out.AddAttrs(slog.String("trace_id", sc.TraceID().String()), slog.String("span_id", sc.SpanID().String()))
		}
	}
	return h.inner.Handle(ctx, out)
}

func (h *logHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	red := make([]slog.Attr, len(attrs))
	has := h.hasRequestID
	for i, a := range attrs {
		red[i] = redactAttr(a)
		if a.Key == "request_id" {
			has = true
		}
	}
	return &logHandler{inner: h.inner.WithAttrs(red), o: h.o, sampler: h.sampler, hasRequestID: has}
}

func (h *logHandler) WithGroup(name string) slog.Handler {
	return &logHandler{inner: h.inner.WithGroup(name), o: h.o, sampler: h.sampler, hasRequestID: h.hasRequestID}
}

// countWorkerError — запись warn/error фоновой задачи (атрибут job — имя лока задачи, worker — имя горутины app).
func (h *logHandler) countWorkerError(r slog.Record) {
	r.Attrs(func(a slog.Attr) bool {
		if (a.Key == "job" || a.Key == "worker") && a.Value.Kind() == slog.KindString {
			h.o.m.workerErrors.WithLabelValues(token(h.o.g.job, a.Value.String())).Inc()
			return false
		}
		return true
	})
}

// ---------------------------------------------------------------------------
// Маскирование
// ---------------------------------------------------------------------------

// secretWords — части ключа атрибута (через _ - . и смену регистра не делим: ключи в проекте snake_case),
// при которых значение маскируется.
var secretWords = map[string]bool{
	"password": true, "passwd": true, "pass": true, "secret": true, "token": true, "authorization": true,
	"auth": true, "cookie": true, "cookies": true, "pepper": true, "dsn": true, "pin": true, "otp": true,
	"credential": true, "credentials": true, "apikey": true, "signature": true, "csrf": true, "bearer": true,
	"refresh": true, "session_token": true, "private": true, "key_pem": true, "set-cookie": true,
}

// secretKey — ключ атрибута означает секрет.
func secretKey(key string) bool {
	k := strings.ToLower(key)
	if secretWords[k] {
		return true
	}
	if strings.Contains(k, "api_key") || strings.Contains(k, "apikey") || strings.Contains(k, "private_key") ||
		strings.Contains(k, "secret") || strings.Contains(k, "password") || strings.Contains(k, "token") {
		return true
	}
	for _, part := range strings.FieldsFunc(k, func(r rune) bool { return r == '_' || r == '-' || r == '.' }) {
		if secretWords[part] {
			return true
		}
	}
	return false
}

// secretValue — строка похожа на учётные данные независимо от ключа.
func secretValue(s string) bool {
	ls := strings.ToLower(strings.TrimSpace(s))
	return strings.HasPrefix(ls, "bearer ") || strings.HasPrefix(ls, "basic ") || strings.Contains(s, "zfk_") ||
		strings.Contains(ls, "password=") || strings.Contains(ls, "-----begin")
}

func redactAttr(a slog.Attr) slog.Attr {
	if secretKey(a.Key) {
		return slog.String(a.Key, redacted)
	}
	v := a.Value.Resolve()
	switch v.Kind() {
	case slog.KindString:
		if secretValue(v.String()) {
			return slog.String(a.Key, redacted)
		}
	case slog.KindGroup:
		g := v.Group()
		out := make([]any, len(g))
		for i, ga := range g {
			out[i] = redactAttr(ga)
		}
		return slog.Group(a.Key, out...)
	}
	return slog.Attr{Key: a.Key, Value: v}
}

// ---------------------------------------------------------------------------
// Выборка
// ---------------------------------------------------------------------------

type sampleCount struct {
	second int64
	n      int
}

type sampler struct {
	initial, thereafter int
	mu                  sync.Mutex
	counts              map[string]*sampleCount
}

// maxSampleKeys — предел различных сообщений в окне (сообщения — константы кода; страховка от роста карты).
const maxSampleKeys = 4096

func (s *sampler) allow(msg string, at time.Time) bool {
	sec := at.Unix()
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.counts[msg]
	if c == nil {
		if len(s.counts) >= maxSampleKeys {
			s.counts = map[string]*sampleCount{}
		}
		c = &sampleCount{second: sec}
		s.counts[msg] = c
	}
	if c.second != sec {
		c.second, c.n = sec, 0
	}
	c.n++
	if c.n <= s.initial {
		return true
	}
	return (c.n-s.initial)%s.thereafter == 0
}
