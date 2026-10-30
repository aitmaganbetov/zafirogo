package httpx

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/aitmaganbetov/zafirogo/server/internal/auth"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/principal"
	"github.com/aitmaganbetov/zafirogo/server/internal/billing"
	billingsvc "github.com/aitmaganbetov/zafirogo/server/internal/billing/service"
	"github.com/aitmaganbetov/zafirogo/server/internal/money"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// Principal — кто выполняет запрос (после Access.Routes он есть всегда).
func Principal(ctx context.Context) principal.Principal {
	p, _ := principal.FromContext(ctx)
	return p
}

// ClubID — выбранный клуб из аутентификации (никогда из параметров запроса).
func ClubID(ctx context.Context) int64 { return Principal(ctx).ClubID }

// Actor — кто выполняет денежную операцию (ledger, audit_log).
func Actor(ctx context.Context) billing.BalanceActor {
	p := Principal(ctx)
	a := billing.BalanceActor{UserID: p.UserID, Kind: "staff", RequestID: middleware.GetReqID(ctx)}
	if p.ReadOnly {
		a.Kind = "superadmin"
	}
	if _, r := HTTPFrom(ctx); r != nil {
		if ip, err := netip.ParseAddr(HostOnly(r.RemoteAddr)); err == nil {
			a.IP = ip.Unmap()
		}
	}
	return a
}

// Tx — тенантная транзакция клуба из Principal (auth.TenantTx: app.club_id, RLS). Действия после коммита
// (уведомления агента из cash/service) копятся в CommitHooks и выполняются только после успешного COMMIT;
// при повторе транзакции (deadlock) очередь сбрасывается.
func (d *Deps) Tx(ctx context.Context, fn func(ctx context.Context, q *store.Queries) error) error {
	if d.Pool == nil {
		return ErrNotConfigured
	}
	ctx, hooks := billingsvc.WithCommitHooks(ctx)
	err := auth.TenantTx(ctx, d.Pool, func(q *store.Queries) error {
		hooks.Reset()
		return fn(ctx, q)
	})
	if err == nil {
		hooks.Run(context.WithoutCancel(ctx))
	}
	return err
}

// Idempotent — денежная операция в транзакции q под ключом Idempotency-Key: повтор с тем же телом возвращает
// сохранённый ответ первого выполнения, с другим — 409 (billing_requests, как покупки времени).
func Idempotent[T any](ctx context.Context, q *store.Queries, clubID int64, key, op string, request any, now time.Time, run func() (T, error)) (T, error) {
	var out T
	replayed, err := billingsvc.ClaimRequest(ctx, q, clubID, key, "http:"+op, request, &out, now)
	if err != nil || replayed {
		return out, err
	}
	if out, err = run(); err != nil {
		return out, err
	}
	return out, billingsvc.RememberRequest(ctx, q, clubID, key, out)
}

// ---------------------------------------------------------------------------
// Пагинация
// ---------------------------------------------------------------------------

const (
	// DefaultPageSize — paginate(25) старого кода.
	DefaultPageSize = 25
	// MaxPageSize — предел limit.
	MaxPageSize = 100
)

// PageSize — размер страницы из параметра limit.
func PageSize(limit *int) int32 {
	if limit == nil || *limit <= 0 {
		return DefaultPageSize
	}
	if *limit > MaxPageSize {
		return MaxPageSize
	}
	return int32(*limit) //nolint:gosec // ограничено MaxPageSize
}

// EncodeCursor — непрозрачный курсор keyset-пагинации (JSON в base64url).
func EncodeCursor(v any) *string {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	s := base64.RawURLEncoding.EncodeToString(b)
	return &s
}

// DecodeCursor — разбор курсора; неверный — 400 bad_cursor.
func DecodeCursor(s *string, v any) error {
	if s == nil || *s == "" {
		return nil
	}
	b, err := base64.RawURLEncoding.DecodeString(*s)
	if err != nil || json.Unmarshal(b, v) != nil {
		return NewAPIError(http.StatusBadRequest, "bad_cursor", "")
	}
	return nil
}

// LikePattern — шаблон ILIKE для подстроки (экранирование %, _ и \).
func LikePattern(q *string) string {
	if q == nil {
		return ""
	}
	s := strings.TrimSpace(*q)
	if s == "" {
		return ""
	}
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + r.Replace(s) + "%"
}

// Ptr — указатель на значение.
func Ptr[T any](v T) *T { return &v }

// NumberFormat — number_format($tenge, 2, dec, ' ') PHP для суммы в тиынах (тексты статусов старого кода).
func NumberFormat(t money.Tiyn, dec string) string {
	s := t.Tenge()
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	whole, frac, _ := strings.Cut(s, ".")
	var b strings.Builder
	for i, r := range whole {
		if i > 0 && (len(whole)-i)%3 == 0 {
			b.WriteByte(' ')
		}
		b.WriteRune(r)
	}
	out := b.String() + dec + frac
	if neg {
		out = "-" + out
	}
	return out
}

// Itoa — strconv.FormatInt(v, 10).
func Itoa(v int64) string { return strconv.FormatInt(v, 10) }
