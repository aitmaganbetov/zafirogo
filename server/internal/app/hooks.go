package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"github.com/valkey-io/valkey-go"
	"google.golang.org/grpc/codes"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
	"github.com/aitmaganbetov/zafirogo/server/internal/activity"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/enroll"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/stream"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/principal"
	billingsvc "github.com/aitmaganbetov/zafirogo/server/internal/billing/service"
	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	grpcserver "github.com/aitmaganbetov/zafirogo/server/internal/grpc"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// Морф-классы Laravel для activity_logs.subject_type (в старом проекте morphMap нет — полные имена классов).
const (
	morphUser         = `App\Models\User`
	morphClubComputer = `App\Models\ClubComputer`
)

// substitute подставляет :name в текст (как __() Laravel: длинные ключи раньше коротких).
// TODO(i18n): описание пишется на русском (ключ перевода); язык клуба — когда появится серверный
// каталог переводов (docs/open-questions/stage2-wiring.md W-3).
func substitute(text string, params map[string]string) string {
	if len(params) == 0 {
		return text
	}
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	// Длинные ключи первыми: ":computer_name" не должен съесться ":computer".
	sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })
	pairs := make([]string, 0, 2*len(keys))
	for _, k := range keys {
		pairs = append(pairs, ":"+k, params[k])
	}
	return strings.NewReplacer(pairs...).Replace(text)
}

// ---------------------------------------------------------------------------
// auth.Events → журнал действий клуба (activity_logs)
// ---------------------------------------------------------------------------

// authEvents пишет события входа в журнал клуба. Вызывается auth.Service после коммита, поэтому
// каждое событие — своя короткая транзакция. Ошибка журнала не ломает вход: только предупреждение в лог.
// Laravel: app/Http/Controllers/AuthController.php (login, logout), ClubController::switch,
// app/Services/ActivityLogger.php ActivityLogger::recordFailedLogin().
type authEvents struct {
	app      *pgxpool.Pool
	platform *pgxpool.Pool
	log      *slog.Logger
}

var _ auth.Events = (*authEvents)(nil)

func (e *authEvents) record(ctx context.Context, clubID int64, entry activity.Entry) {
	entry.ClubID = clubID
	err := db.InTenantTx(ctx, e.app, clubID, func(q *store.Queries) error {
		_, err := activity.Record(ctx, q, entry)
		return err
	})
	if err != nil {
		e.log.WarnContext(ctx, "activity log write failed", "event", entry.Event, "club_id", clubID, "err", err)
	}
}

// LoginSucceeded — auth.login «Вход в систему: :user».
func (e *authEvents) LoginSucceeded(ctx context.Context, userID, clubID int64, userName string) {
	if clubID == 0 {
		// Суперадмин без клуба: в старом — PlatformLogger superadmin.login (platform_logs).
		// TODO(platform_logs): запросов к platform_logs ещё нет (W-4) — пока только системный лог.
		e.log.InfoContext(ctx, "superadmin login", "user_id", userID)
		return
	}
	e.record(ctx, clubID, activity.Entry{
		Event:       "auth.login",
		Description: substitute("Вход в систему: :user", map[string]string{"user": userName}),
		UserID:      userID, SubjectType: morphUser, SubjectID: userID,
	})
}

// LoginFailed — auth.login_failed во всех клубах аккаунта; неизвестный телефон — только системный лог.
// Laravel: ActivityLogger::recordFailedLogin(). Все клубы пользователя — одна транзакция пулом
// zafiro_platform (как DB::transaction над $clubIds в старом коде).
func (e *authEvents) LoginFailed(ctx context.Context, phone, reason string) {
	var (
		userID   int64
		userName string
		clubIDs  []int64
	)
	err := db.InPlatformTx(ctx, e.platform, func(q *store.Queries) error {
		u, err := q.GetUserByPhone(ctx, phone)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		userID, userName = u.ID, u.Name
		ms, err := q.ListUserMemberships(ctx, u.ID)
		if err != nil {
			return err
		}
		for _, m := range ms {
			clubIDs = append(clubIDs, m.ClubID)
		}
		if len(clubIDs) == 0 {
			return nil
		}
		return activity.RecordInClubs(ctx, q, clubIDs, activity.Entry{
			Event:       "auth.login_failed",
			Description: substitute("Неудачный вход: :user", map[string]string{"user": userName}),
			UserID:      userID, SubjectType: morphUser, SubjectID: userID,
			Details: map[string]any{"reason": reason},
		})
	})
	if err != nil {
		e.log.WarnContext(ctx, "activity log write failed", "event", "auth.login_failed", "err", err)
		return
	}
	if len(clubIDs) == 0 {
		// Как Log::warning('Failed sign-in without a club account') старого кода; телефон — в хэше:
		// в системный лог персональные данные не пишем.
		meta, _ := activity.RequestMetaFromContext(ctx)
		e.log.WarnContext(ctx, "failed sign-in without a club account",
			"event", "auth.login_failed", "phone_sha256", hashPhone(phone), "reason", reason,
			"user_id", userID, "ip", meta.IP)
	}
}

func hashPhone(phone string) string {
	sum := sha256.Sum256([]byte(phone))
	return hex.EncodeToString(sum[:8])
}

// LoggedOut — auth.logout «Выход из системы: :user».
func (e *authEvents) LoggedOut(ctx context.Context, p principal.Principal) {
	if p.ClubID == 0 || p.ReadOnly {
		return
	}
	name := e.userName(ctx, p.UserID)
	e.record(ctx, p.ClubID, activity.Entry{
		Event:       "auth.logout",
		Description: substitute("Выход из системы: :user", map[string]string{"user": name}),
		UserID:      p.UserID, SubjectType: morphUser, SubjectID: p.UserID,
	})
}

// ClubSwitched — club.switched «Переход в клуб из клуба #:id» (журнал нового клуба).
func (e *authEvents) ClubSwitched(ctx context.Context, userID, fromClubID, toClubID int64) {
	e.record(ctx, toClubID, activity.Entry{
		Event:       "club.switched",
		Description: substitute("Переход в клуб из клуба #:id", map[string]string{"id": strconv.FormatInt(fromClubID, 10)}),
		UserID:      userID,
		Details:     map[string]any{"from_club_id": fromClubID},
	})
}

// ClubViewed — superadmin.club_viewed (журнал платформы).
// TODO(platform_logs): запись в platform_logs, когда появятся её запросы (W-4).
func (e *authEvents) ClubViewed(ctx context.Context, userID, clubID int64) {
	e.log.InfoContext(ctx, "superadmin club viewed", "user_id", userID, "club_id", clubID)
}

func (e *authEvents) userName(ctx context.Context, userID int64) string {
	var name string
	_ = db.InPlatformTx(ctx, e.platform, func(q *store.Queries) error {
		u, err := q.GetUserAuth(ctx, userID)
		if err == nil {
			name = u.Name
		}
		return nil
	})
	return name
}

// ---------------------------------------------------------------------------
// enroll.ActivityFunc → activity.Record (в транзакции регистрации)
// ---------------------------------------------------------------------------

func enrollActivity(log *slog.Logger) enroll.ActivityFunc {
	return func(ctx context.Context, q *store.Queries, ev enroll.ActivityEvent) error {
		userID := ev.UserID
		if userID == 0 {
			// Код выдан пользователем, которого уже нет: как Laravel при enroll по ключу — от имени
			// первого владельца клуба (ClubComputerRegistrar::register()).
			owners, err := q.ListClubUserIDsByRole(ctx, store.ListClubUserIDsByRoleParams{ClubID: ev.ClubID, Role: "owner"})
			if err != nil {
				return err
			}
			if len(owners) == 0 {
				log.WarnContext(ctx, "activity log skipped: club has no owner", "event", ev.Event, "club_id", ev.ClubID)
				return nil
			}
			userID = owners[0]
		}
		ip := ev.IP
		if ip == "" {
			if m, ok := activity.RequestMetaFromContext(ctx); ok {
				ip = m.IP
			}
		}
		if ip == "" {
			if p := grpcserver.PeerIP(ctx); p != nil {
				ip = p.String()
			}
		}
		subjectType := ev.SubjectType
		if subjectType == "club_computer" {
			subjectType = morphClubComputer
		}
		details := make(map[string]any, len(ev.Details))
		for k, v := range ev.Details {
			details[k] = v
		}
		_, err := activity.Record(ctx, q, activity.Entry{
			ClubID: ev.ClubID, Event: ev.Event, Description: substitute(ev.Description, ev.Params),
			UserID: userID, SubjectType: subjectType, SubjectID: ev.SubjectID, Details: details,
			Meta: &activity.RequestMeta{IP: ip},
		})
		return err
	}
}

// ---------------------------------------------------------------------------
// enroll.Limiter в Valkey (общий для инстансов)
// ---------------------------------------------------------------------------

// valkeyLimiter — фиксированное окно в Valkey (INCR + PEXPIRE при первом попадании), как MemoryLimiter
// и RateLimiter::hit Laravel, но общее для всех инстансов. Ключи — sha256 (без IP и UUID в открытом виде).
type valkeyLimiter struct {
	c      valkey.Client
	prefix string
}

var _ enroll.Limiter = (*valkeyLimiter)(nil)

func (l *valkeyLimiter) key(k string) string {
	sum := sha256.Sum256([]byte(k))
	return l.prefix + hex.EncodeToString(sum[:16])
}

// Hit — enroll.Limiter.Hit.
func (l *valkeyLimiter) Hit(ctx context.Context, key string, limit int, window time.Duration) (bool, time.Duration, error) {
	k := l.key(key)
	res := l.c.DoMulti(ctx,
		l.c.B().Incr().Key(k).Build(),
		l.c.B().Pexpire().Key(k).Milliseconds(window.Milliseconds()).Nx().Build(),
		l.c.B().Pttl().Key(k).Build(),
	)
	n, err := res[0].AsInt64()
	if err != nil {
		return false, 0, fmt.Errorf("enroll limiter: %w", err)
	}
	if err := res[1].Error(); err != nil {
		return false, 0, fmt.Errorf("enroll limiter: %w", err)
	}
	if n > int64(limit) {
		return false, pttl(res[2], window), nil
	}
	return true, 0, nil
}

// Exceeded — enroll.Limiter.Exceeded.
func (l *valkeyLimiter) Exceeded(ctx context.Context, key string, limit int, window time.Duration) (bool, time.Duration, error) {
	k := l.key(key)
	res := l.c.DoMulti(ctx, l.c.B().Get().Key(k).Build(), l.c.B().Pttl().Key(k).Build())
	n, err := res[0].AsInt64()
	if valkey.IsValkeyNil(err) {
		return false, 0, nil
	}
	if err != nil {
		return false, 0, fmt.Errorf("enroll limiter: %w", err)
	}
	if n >= int64(limit) {
		return true, pttl(res[1], window), nil
	}
	return false, 0, nil
}

func pttl(r valkey.ValkeyResult, window time.Duration) time.Duration {
	ms, err := r.AsInt64()
	if err != nil || ms < 0 {
		return window
	}
	return time.Duration(ms) * time.Millisecond
}

// ---------------------------------------------------------------------------
// Отзыв сертификатов: enroll.OnRevoked → NATS → CertCache.MarkRevoked на всех инстансах;
// CertCache.OnRevoked → stream.Kick (поток этого ПК закрывается с CERTIFICATE_REVOKED).
// ---------------------------------------------------------------------------

type revokedMsg struct {
	ClubID     int64    `json:"club_id"`
	ComputerID int64    `json:"computer_id"`
	Serials    []string `json:"serials"`
}

func revokedSubject(prefix string) string { return prefix + ".cert.revoked" }

// publishRevoked — enroll.Options.OnRevoked.
func publishRevoked(nc *nats.Conn, subject string, log *slog.Logger) enroll.RevokedFunc {
	return func(ctx context.Context, clubID, computerID int64, serials []string) {
		b, err := json.Marshal(revokedMsg{ClubID: clubID, ComputerID: computerID, Serials: serials})
		if err == nil {
			err = nc.Publish(subject, b)
		}
		if err != nil {
			// Другие инстансы всё равно увидят отзыв через CertCache.Refresh (agent.cert_cache.refresh).
			log.WarnContext(ctx, "publish certificate revocation failed", "club_id", clubID, "computer_id", computerID, "err", err)
		}
	}
}

// subscribeRevoked — подписка каждого инстанса на отзывы.
func subscribeRevoked(nc *nats.Conn, subject string, cache *enroll.CertCache, log *slog.Logger) (*nats.Subscription, error) {
	return nc.Subscribe(subject, func(m *nats.Msg) {
		var msg revokedMsg
		if err := json.Unmarshal(m.Data, &msg); err != nil {
			log.Warn("bad certificate revocation message", "err", err)
			return
		}
		for _, s := range msg.Serials {
			cache.MarkRevoked(s)
		}
	})
}

// kickRevoked — CertCache.OnRevoked: закрыть поток ПК (на любом инстансе — Kick маршрутизирует через NATS).
func kickRevoked(base context.Context, svc *stream.Service, log *slog.Logger) func(enroll.RevokedCert) {
	return func(r enroll.RevokedCert) {
		go func() {
			ctx, cancel := context.WithTimeout(base, 5*time.Second)
			defer cancel()
			if err := svc.Kick(ctx, r.ClubID, r.ComputerID, codes.Unauthenticated,
				agentv1.ErrorCode_ERROR_CODE_CERTIFICATE_REVOKED, "certificate revoked"); err != nil {
				log.Warn("kick revoked agent failed", "club_id", r.ClubID, "computer_id", r.ComputerID, "err", err)
			}
		}()
	}
}

// ---------------------------------------------------------------------------
// worker.ExpireSessions.OnExpired → EndSession(EXPIRED) агенту
// ---------------------------------------------------------------------------

func notifyExpired(svc *stream.Service, log *slog.Logger) func(context.Context, []billingsvc.ExpiredSession) {
	n := svc.Notifier()
	return func(ctx context.Context, sessions []billingsvc.ExpiredSession) {
		for _, s := range sessions {
			// TODO(B2): ExpiredSession пока без id сессии — берём сессию, о которой сообщил агент.
			end := &agentv1.EndSession{Reason: agentv1.SessionEndReason_SESSION_END_REASON_EXPIRED}
			if st, ok, err := svc.Presence().Get(ctx, s.ClubID, s.ComputerID); err == nil && ok {
				end.SessionId = st.SessionID
			}
			if err := n.SessionEnded(ctx, s.ClubID, s.ComputerID, end, 0); err != nil {
				log.WarnContext(ctx, "notify agent about expired session failed",
					"club_id", s.ClubID, "computer_id", s.ComputerID, "err", err)
			}
		}
	}
}
