package service

import (
	"context"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/billing/session"
	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// ExpiredSession — ПК, с которого снята истёкшая сессия.
type ExpiredSession struct {
	ClubID     int64
	ComputerID int64
}

// ExpireDueSessions снимает сессии всех клубов, у которых вышло время (session_ends_at <= now).
//
// Laravel: app/Services/ClubComputerSessions.php expireAll() (стр. 189–195), команда
// club-computers:expire-sessions (everyMinute, app/Console/Commands/ExpireComputerSessions.php).
// Как в старом коде: только обнуление полей сессии ПК, БЕЗ settle() — ни возврата, ни пакетов, ни журнала,
// части session_purchases остаются (их чистит суточная задача; GAP-005, Допущение Этапа 1: Q-010).
//
// platform — пул логина zafiro_platform (запрос по всем клубам). now усекается до секунды, как строка
// Y-m-d H:i:s, которую подставлял старый код (session.IsDue — то же условие для одного ПК).
func ExpireDueSessions(ctx context.Context, platform db.Beginner, now time.Time) ([]ExpiredSession, error) {
	at := session.StoredTime(now)
	var out []ExpiredSession
	err := db.InPlatformTx(ctx, platform, func(q *store.Queries) error {
		out = nil
		rows, err := q.ExpireDueSessions(ctx, &at)
		if err != nil {
			return err
		}
		for _, r := range rows {
			out = append(out, ExpiredSession{ClubID: r.ClubID, ComputerID: r.ID})
		}
		return nil
	})
	return out, err
}
