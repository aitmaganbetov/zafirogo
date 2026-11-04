// Package principal — кто выполняет запрос админки: сотрудник/владелец/суперадмин и выбранный клуб.
//
// Principal кладёт в контекст middleware аутентификации (auth/session); остальные слои только читают.
// Клуб берётся отсюда, никогда из параметров запроса (CLAUDE.md, «Тенантность»).
package principal

import (
	"context"
	"slices"
)

// Principal — аутентифицированный пользователь админки.
type Principal struct {
	UserID int64
	// ClubID — выбранный клуб; 0 — клуб не выбран.
	ClubID int64
	// Role — роль в выбранном клубе (owner, admin, …; см. auth/permissions). Пусто — нет членства.
	Role string
	// Permissions — права в выбранном клубе (уже вычисленные: роль + права вне матрицы).
	Permissions []string
	// Superadmin — суперадмин платформы (доступ к суперадминке; прав в клубах не даёт).
	Superadmin bool
	// ReadOnly — режим просмотра клуба суперадмином: любые изменения (не GET/HEAD) запрещены.
	ReadOnly bool
	// SessionID — идентификатор сессии (семейства refresh-токенов), не секрет.
	SessionID string
}

type ctxKey struct{}

// WithPrincipal возвращает контекст с p.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

// FromContext достаёт Principal из контекста.
func FromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(ctxKey{}).(Principal)
	return p, ok
}

// Can — есть ли у пользователя право permission в выбранном клубе ("section.action", auth/permissions).
//
// Суперадмин платформы сам по себе прав в клубе НЕ имеет (Laravel: ClubPermissions::allows смотрит только
// на членство). В режиме просмотра клуба суперадмином Principal собирается как владелец (Role "owner",
// все права) с ReadOnly = true — как SetCurrentClub::viewAsSuperadmin(); запрет изменений в этом режиме
// проверяет middleware (permissions.RequireClub/RequirePermission), не Can.
func (p Principal) Can(permission string) bool {
	if p.ClubID <= 0 {
		return false
	}
	return slices.Contains(p.Permissions, permission)
}
