package service

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aitmaganbetov/zafirogo/server/internal/activity"
	"github.com/aitmaganbetov/zafirogo/server/internal/billing/session"
	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// SessionExpiredEvent — код события журнала о естественном окончании сессии ПК (Решение Q-010).
const SessionExpiredEvent = "computer.session_expired"

// ExpiredSession — ПК, с которого снята истёкшая сессия.
type ExpiredSession struct {
	ClubID     int64
	ComputerID int64
	// SessionID — снятая сессия ("" — сессия без id, старые агенты).
	SessionID string
	// UserID — клиент сессии (0 — гостевая сессия с кассы).
	UserID int64
}

// ExpireDueSessions снимает сессии всех клубов, у которых вышло время (session_ends_at <= now).
//
// Laravel: app/Services/ClubComputerSessions.php expireAll() (стр. 189–195), команда
// club-computers:expire-sessions (everyMinute, app/Console/Commands/ExpireComputerSessions.php).
// Как в старом коде: только обнуление полей сессии ПК, БЕЗ settle() — ни возврата, ни пакетов,
// части session_purchases остаются (их чистит суточная задача; GAP-005).
// Решение Q-010 (docs/DECISIONS.md): в той же транзакции пишется журнал computer.session_expired (в старом журнала
// не было) — от имени клиента сессии, у гостевой сессии — от имени владельца клуба (как computer.guest_session_reset).
// meta — «запрос» системного действия (IP обязателен для журнала).
//
// platform — пул логина zafiro_platform (запрос по всем клубам). now усекается до секунды, как строка
// Y-m-d H:i:s, которую подставлял старый код (session.IsDue — то же условие для одного ПК).
func ExpireDueSessions(ctx context.Context, platform db.Beginner, now time.Time, meta activity.RequestMeta) ([]ExpiredSession, error) {
	at := session.StoredTime(now)
	var out []ExpiredSession
	err := db.InPlatformTx(ctx, platform, func(q *store.Queries) error {
		out = nil
		rows, err := q.ExpireDueSessions(ctx, &at)
		if err != nil {
			return err
		}
		owners := map[int64]int64{}
		for _, r := range rows {
			es := ExpiredSession{ClubID: r.ClubID, ComputerID: r.ID}
			if r.SessionID != nil {
				es.SessionID = *r.SessionID
			}
			if r.SessionUserID != nil {
				es.UserID = *r.SessionUserID
			}
			out = append(out, es)
			if err := recordSessionExpired(ctx, q, r, es, owners, now, meta); err != nil {
				return err
			}
		}
		return nil
	})
	return out, err
}

// recordSessionExpired — журнал computer.session_expired (Решение Q-010 (docs/DECISIONS.md)).
func recordSessionExpired(ctx context.Context, q *store.Queries, r store.ExpireDueSessionsRow, es ExpiredSession,
	owners map[int64]int64, now time.Time, meta activity.RequestMeta) error {
	user := es.UserID
	if user == 0 {
		owner, ok := owners[es.ClubID]
		if !ok {
			id, err := q.GuestClubOwnerID(ctx, es.ClubID)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			owner, owners[es.ClubID] = id, id
		}
		user = owner
	}
	if user == 0 {
		return nil // клуб без владельца: журналу не от чьего имени писать
	}
	name := computerRow{DisplayName: r.DisplayName, DeviceName: r.DeviceName, Number: r.Number}.displayName()
	details := map[string]any{"session_id": es.SessionID, "planned_end": ISO8601(r.SessionEndsAt)}
	if es.UserID != 0 {
		details["user_id"] = es.UserID
	}
	if r.SessionGuest {
		details["guest"] = true
	}
	_, err := activity.Record(ctx, q, activity.Entry{
		ClubID: es.ClubID, Event: SessionExpiredEvent,
		// Ключ перевода: «Время сессии на ПК :computer истекло» (новый текст; Решение Q-010).
		Description: "Время сессии на ПК " + name + " истекло",
		UserID:      user, SubjectType: SubjectComputer, SubjectID: es.ComputerID,
		Details: details, Meta: &meta, At: now,
	})
	return err
}
