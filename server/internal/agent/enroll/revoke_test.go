package enroll

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
)

type fakeGuestTokens struct {
	calls []int64
	err   error
}

func (f *fakeGuestTokens) RevokeComputer(_ context.Context, computerID, userID int64) error {
	if userID != 0 {
		return errors.New("userID must be 0 (any guest)")
	}
	f.calls = append(f.calls, computerID)
	return f.err
}

// AG-5 (open-questions/stage5-agent.md): отзыв сертификата ПК отзывает токен гостя этого ПК; продление
// (superseded) и пустой отзыв — нет; ошибка Valkey отзыв сертификата не ломает.
func TestAfterRevokeSignsGuestOut(t *testing.T) {
	tokens := &fakeGuestTokens{}
	var published []int64
	s := &Service{opts: Options{
		GuestTokens: tokens,
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		OnRevoked: func(_ context.Context, _, computerID int64, _ []string) {
			published = append(published, computerID)
		},
	}}
	ctx := context.Background()

	s.afterRevoke(ctx, 1, 10, []string{"a"}, true)
	s.afterRevoke(ctx, 1, 11, []string{"b"}, false) // продление
	s.afterRevoke(ctx, 1, 12, nil, true)            // нечего отзывать
	tokens.err = errors.New("valkey down")
	s.afterRevoke(ctx, 1, 13, []string{"c"}, true)

	if len(tokens.calls) != 2 || tokens.calls[0] != 10 || tokens.calls[1] != 13 {
		t.Fatalf("guest tokens revoked for %v, want [10 13]", tokens.calls)
	}
	if len(published) != 3 {
		t.Fatalf("published = %v, want 3 revocations", published)
	}

	// Без GuestTokens — как раньше.
	(&Service{opts: Options{Logger: slog.Default()}}).afterRevoke(ctx, 1, 14, []string{"d"}, true)
}
