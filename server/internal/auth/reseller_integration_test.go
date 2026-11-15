//go:build integration

package auth

import (
	"context"
	"errors"
	"testing"

	"github.com/aitmaganbetov/zafirogo/server/internal/auth/permissions"
)

// GS-561 (ResellerTest.php «only resellers sign in on the reseller host»): вход на хосте кабинета реселлера —
// LoginInput.IsReseller. Не реселлер — ошибка поля phone и запись auth.login_failed с причиной not_reseller;
// реселлер — сессия без клуба и без записи журнала входа (в старом вход реселлера ничего не пишет);
// суперадмин-реселлер — второй фактор.
func TestResellerHostLogin(t *testing.T) {
	ctx := context.Background()
	s, rec := newService(t)
	resellers := map[int64]bool{}
	isReseller := func(_ context.Context, userID int64) (bool, error) { return resellers[userID], nil }

	// Сотрудник клуба с верным паролем — не реселлер.
	c := club(t)
	staffPhone := phone()
	staff := user(t, staffPhone, cheapHash(t, "pw"), false)
	member(t, c, staff, permissions.RoleOwner, "")
	_, err := s.Login(ctx, LoginInput{Phone: staffPhone, Password: "pw", IsReseller: isReseller})
	var fe *FieldError
	if !errors.As(err, &fe) || fe.Field != "phone" || fe.Message != MsgResellersOnly || fe.Reason != ReasonNotReseller {
		t.Fatalf("вход не реселлера: %v", err)
	}
	if len(rec.failed) != 1 || rec.failed[0] != staffPhone+" "+ReasonNotReseller {
		t.Fatalf("журнал неудач: %v", rec.failed)
	}
	// Неверный пароль — обычная ошибка входа, до проверки реселлера.
	if _, err := s.Login(ctx, LoginInput{Phone: staffPhone, Password: "nope", IsReseller: isReseller}); !errors.As(err, &fe) || fe.Reason != ReasonInvalidCredentials {
		t.Fatalf("неверный пароль: %v", err)
	}

	// Реселлер без клубов: сессия без клуба (на обычном хосте он бы не вошёл — «нет доступа ни к одному клубу»).
	resPhone := phone()
	res := user(t, resPhone, cheapHash(t, "pw"), false)
	resellers[res] = true
	if _, err := s.Login(ctx, LoginInput{Phone: resPhone, Password: "pw"}); !errors.As(err, &fe) || fe.Reason != ReasonNoClubAccess {
		t.Fatalf("реселлер на хосте панели: %v", err)
	}
	out, err := s.Login(ctx, LoginInput{Phone: resPhone, Password: "pw", IsReseller: isReseller})
	if err != nil || out.Tokens.Access == "" || out.Challenge != nil || out.ClubID != 0 || out.Principal.ClubID != 0 || out.Principal.Superadmin {
		t.Fatalf("вход реселлера: %+v %v", out, err)
	}
	if rec.success != 0 {
		t.Fatalf("вход реселлера записан в журнал: %d", rec.success)
	}
	// Реселлер, он же сотрудник клуба: на хосте реселлера сессия всё равно без клуба.
	resellers[staff] = true
	out, err = s.Login(ctx, LoginInput{Phone: staffPhone, Password: "pw", IsReseller: isReseller})
	if err != nil || out.Principal.ClubID != 0 || out.Principal.Role != "" || len(out.Principal.Permissions) != 0 {
		t.Fatalf("реселлер-сотрудник на хосте реселлера: %+v %v", out.Principal, err)
	}

	// Суперадмин-реселлер: пароль даёт только полувход — второй фактор обязателен и на этом хосте.
	saPhone := phone()
	sa := user(t, saPhone, cheapHash(t, "pw"), true)
	resellers[sa] = true
	out, err = s.Login(ctx, LoginInput{Phone: saPhone, Password: "pw", IsReseller: isReseller})
	if err != nil || out.Challenge == nil || out.Tokens.Access != "" {
		t.Fatalf("суперадмин-реселлер: %+v %v", out, err)
	}
	// Ошибка проверки реселлера — не вход.
	boom := errors.New("boom")
	if _, err := s.Login(ctx, LoginInput{Phone: resPhone, Password: "pw", IsReseller: func(context.Context, int64) (bool, error) { return false, boom }}); !errors.Is(err, boom) {
		t.Fatalf("ошибка проверки: %v", err)
	}
}
