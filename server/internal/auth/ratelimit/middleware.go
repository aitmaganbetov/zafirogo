package ratelimit

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/agent/identity"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/principal"
)

// maxBodyPeek — сколько байт JSON-тела читает middleware, чтобы достать телефон/device_id/club_key.
const maxBodyPeek = 64 << 10

// BodyFields — какие поля JSON-тела нужны правилу.
type BodyFields struct {
	Phone, DeviceID, ClubKey bool
}

// FieldsFor — поля тела, от которых зависят ключи правила (остальные правила тело не читают).
func FieldsFor(rule string) BodyFields {
	switch rule {
	case CustomerLogin, PasswordRecovery, WebLogin, WebRegister:
		return BodyFields{Phone: true}
	case AgentEnroll:
		return BodyFields{DeviceID: true}
	case ClubResolve:
		return BodyFields{ClubKey: true}
	}
	return BodyFields{}
}

// SubjectFromContext — субъект из контекста: пользователь (Principal) и ПК (agent/identity).
func SubjectFromContext(ctx context.Context, ip string) Subject {
	s := Subject{IP: ip}
	if p, ok := principal.FromContext(ctx); ok {
		s.UserID = p.UserID
	}
	if id, ok := identity.FromContext(ctx); ok {
		s.ComputerID, s.ClubID = id.ComputerID, id.ClubID
	}
	return s
}

// SubjectFromRequest — субъект HTTP-запроса. IP — из r.RemoteAddr (его уже подставил httpserver.RealIP
// для доверенных прокси). Поля тела читаются только если нужны (f), тело восстанавливается для обработчика.
func SubjectFromRequest(r *http.Request, f BodyFields) Subject {
	ip := r.RemoteAddr
	if h, _, err := net.SplitHostPort(ip); err == nil {
		ip = h
	}
	s := SubjectFromContext(r.Context(), ip)
	if (f.Phone || f.DeviceID || f.ClubKey) && r.Body != nil {
		body, _ := io.ReadAll(io.LimitReader(r.Body, maxBodyPeek))
		r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(body), r.Body))
		var v struct {
			Phone    any `json:"phone"`
			DeviceID any `json:"device_id"`
			ClubKey  any `json:"club_key"`
			Key      any `json:"key"`
		}
		if json.Unmarshal(body, &v) == nil {
			s.Phone, s.DeviceID = str(v.Phone), str(v.DeviceID)
			// $request->input('club_key', $request->input('key'))
			if v.ClubKey != nil {
				s.ClubKey = str(v.ClubKey)
			} else {
				s.ClubKey = str(v.Key)
			}
		}
	}
	return s
}

// str — (string) $request->input(...) для скаляров; массивы/объекты — пусто.
func str(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		if t {
			return "1"
		}
	}
	return ""
}

// Middleware — HTTP-обёртка правила name. При превышении — 429 {"error":"too_many_requests","message":"Too Many Attempts."}
// и заголовки Retry-After, X-RateLimit-Limit, X-RateLimit-Remaining, X-RateLimit-Reset (как ThrottleRequests).
// Middleware ставится после аутентификации, если правило считает по пользователю.
func (l *Limiter) Middleware(name string) func(http.Handler) http.Handler {
	if !l.Has(name) {
		panic("ratelimit: unknown rule " + name)
	}
	fields := FieldsFor(name)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !l.Enabled(name) {
				next.ServeHTTP(w, r)
				return
			}
			res, err := l.Allow(r.Context(), name, SubjectFromRequest(r, fields))
			if err != nil {
				l.log.ErrorContext(r.Context(), "rate limit check failed", slog.String("rule", name), slog.String("err", err.Error()))
				writeJSON(w, http.StatusServiceUnavailable, "service_unavailable", "")
				return
			}
			WriteHeaders(w, res)
			if !res.Allowed {
				writeJSON(w, http.StatusTooManyRequests, "too_many_requests", TooManyAttemptsMessage)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// WriteHeaders — X-RateLimit-Limit/Remaining; при отказе — Retry-After (секунды, вверх) и X-RateLimit-Reset (unix).
func WriteHeaders(w http.ResponseWriter, res Result) {
	if res.Limit == 0 {
		return
	}
	h := w.Header()
	h.Set("X-RateLimit-Limit", strconv.Itoa(res.Limit))
	h.Set("X-RateLimit-Remaining", strconv.Itoa(res.Remaining))
	if !res.Allowed {
		sec := RetryAfterSeconds(res.RetryAfter)
		h.Set("Retry-After", strconv.FormatInt(sec, 10))
		h.Set("X-RateLimit-Reset", strconv.FormatInt(time.Now().Unix()+sec, 10))
	}
}

// RetryAfterSeconds — целые секунды вверх, минимум 1.
func RetryAfterSeconds(d time.Duration) int64 {
	return max(int64(math.Ceil(d.Seconds())), 1)
}

func writeJSON(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(struct {
		Error   string `json:"error"`
		Message string `json:"message,omitempty"`
	}{code, message})
}
