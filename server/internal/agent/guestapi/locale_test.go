package guestapi

import (
	"context"
	"testing"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/identity"
	grpcserver "github.com/aitmaganbetov/zafirogo/server/internal/grpc"
	"github.com/aitmaganbetov/zafirogo/server/internal/i18n"
)

// fakeT — каталог: kk включён, клуб 7 — kk, остальные — ru.
type fakeT struct{}

func (fakeT) Enabled(context.Context) ([]i18n.Language, string, error) {
	return []i18n.Language{{Code: "ru"}, {Code: "kk"}, {Code: "en"}}, "1", nil
}

func (fakeT) ClubLocale(_ context.Context, clubID int64) string {
	if clubID == 7 {
		return "kk"
	}
	return "ru"
}

func (fakeT) Translate(_ context.Context, locale, key string, params map[string]string) string {
	lines := map[string]map[string]string{
		"en": {msgSignedInElsewhere: "You are signed in on another PC (:computer).", msgGuestToken: "Sign in again."},
		"kk": {msgGuestToken: "Қайта кіріңіз."},
	}
	line, ok := lines[locale][key]
	if !ok {
		line = key
	}
	return i18n.Render(locale, line, params)
}

func localized(ctx context.Context, t *testing.T, err error) (string, string) {
	t.Helper()
	out := localize(ctx, fakeT{}, err)
	st, _ := status.FromError(out)
	var loc string
	for _, d := range st.Details() {
		switch v := d.(type) {
		case *errdetails.LocalizedMessage:
			loc = v.GetLocale() + ":" + v.GetMessage()
		case *errdetails.DebugInfo:
			t.Fatalf("internal i18n detail leaked: %v", v)
		}
	}
	if grpcserver.ReasonOf(out) != grpcserver.ReasonOf(err) || st.Code() != status.Code(err) {
		t.Fatalf("code/reason changed: %v → %v", err, out)
	}
	return st.Message(), loc
}

// AG-16: текст ошибки — на языке гостя (zafiro-locale, если включён), иначе на языке клуба ПК; подстановки
// переводятся вместе с ключом; внутренняя деталь с ключом не уходит агенту.
func TestLocalizeErrors(t *testing.T) {
	elsewhere := rpcError(codes.FailedPrecondition, agentv1.ErrorCode_ERROR_CODE_SIGNED_IN_ELSEWHERE, msgSignedInElsewhere,
		map[string]string{"computer": "PC-07"}, "")
	en := metadata.NewIncomingContext(context.Background(), metadata.Pairs("zafiro-locale", "en"))
	if msg, loc := localized(en, t, elsewhere); msg != "You are signed in on another PC (PC-07)." || loc != "en:"+msg {
		t.Fatalf("en: %q %q", msg, loc)
	}
	// Без языка гостя — язык клуба ПК (kk); ключа в kk нет — русский текст с подстановкой.
	club := identity.WithIdentity(context.Background(), identity.Identity{ClubID: 7, ComputerID: 1})
	if msg, _ := localized(club, t, errTokenInvalid); msg != "Қайта кіріңіз." {
		t.Fatalf("club kk: %q", msg)
	}
	if msg, _ := localized(club, t, elsewhere); msg != "Вы уже вошли на другом ПК (PC-07). Выйдите там, чтобы войти здесь." {
		t.Fatalf("kk fallback: %q", msg)
	}
	// Выключенный/неверный язык гостя — язык клуба.
	bad := metadata.NewIncomingContext(club, metadata.Pairs("zafiro-locale", "de"))
	if msg, _ := localized(bad, t, errTokenInvalid); msg != "Қайта кіріңіз." {
		t.Fatalf("disabled locale: %q", msg)
	}
	// Без каталога — русский текст, деталь всё равно убрана.
	out := localize(context.Background(), nil, elsewhere)
	st, _ := status.FromError(out)
	for _, d := range st.Details() {
		if _, ok := d.(*errdetails.DebugInfo); ok {
			t.Fatal("internal i18n detail leaked without catalog")
		}
	}
	// Перехватчик: успешный ответ не трогается.
	ic := LocalizeErrors(fakeT{})
	resp, err := ic(en, nil, &grpc.UnaryServerInfo{}, func(context.Context, any) (any, error) { return "ok", nil })
	if resp != "ok" || err != nil {
		t.Fatalf("ok: %v %v", resp, err)
	}
}
