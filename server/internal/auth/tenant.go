package auth

import (
	"context"
	"errors"

	"github.com/aitmaganbetov/zafirogo/server/internal/auth/principal"
	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// ErrNoClub — в контексте нет Principal или клуб не выбран.
var ErrNoClub = errors.New("auth: no club in context")

// TenantTx — тенантная транзакция клуба из аутентификации: app.club_id и app.user_id берутся из Principal
// в контексте, никогда из параметров запроса (CLAUDE.md, «Тенантность»). Обёртка над db.InTenantTxWith.
// В режиме просмотра суперадмином запись запрещает HTTP-слой (permissions.RequireClub).
func TenantTx(ctx context.Context, pool db.Beginner, fn func(q *store.Queries) error) error {
	p, ok := principal.FromContext(ctx)
	if !ok || p.ClubID <= 0 {
		return ErrNoClub
	}
	opts := db.Options{UserID: p.UserID}
	if p.ReadOnly {
		// Суперадмин не член клуба: app.user_id не ставим (политики «свои строки» к нему не относятся).
		opts.UserID = 0
	}
	return db.InTenantTxWith(ctx, pool, p.ClubID, opts, fn)
}
