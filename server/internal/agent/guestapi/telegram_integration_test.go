//go:build integration

package guestapi_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/guestapi"
	"github.com/aitmaganbetov/zafirogo/server/internal/secretbox"
	"github.com/aitmaganbetov/zafirogo/server/internal/telegram"
	"github.com/aitmaganbetov/zafirogo/server/internal/telegram/telegramtest"
	"github.com/aitmaganbetov/zafirogo/server/internal/verifycode"
)

// telegramHarness — клуб с поддельным Telegram Gateway (токен в platform_settings, зашифрован).
func telegramHarness(t *testing.T, settings string) (*harness, *telegramtest.Server) {
	t.Helper()
	box, err := secretbox.New(strings.Repeat("s", 32))
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := box.Seal("AAGexampleGatewayToken_1234567890")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]string{"token": sealed})
	if _, err := env.db.Owner.Exec(context.Background(), `INSERT INTO platform_settings (key, value, created_at, updated_at)
		VALUES ($1, $2, now(), now()) ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, telegram.SettingGateway, raw); err != nil {
		t.Fatal(err)
	}
	tg := telegramtest.New(t)
	var h *harness
	h = newHarness(t, harnessOpts{noLimits: true, settings: settings, options: func(o *guestapi.Options) {
		gw := &telegram.Gateway{API: &telegram.GatewayClient{URL: tg.GatewayURL()}, Store: &telegram.Store{Pool: env.db.Platform, Box: box}}
		o.Recovery = &verifycode.PasswordRecovery{Platform: env.db.Platform, Gateway: gw, Hasher: env.pw, Now: o.Now,
			SignOut: func(ctx context.Context, userID int64) { _, _ = h.srv.Tokens().RevokeEverywhere(ctx, userID) }}
		o.TransferCodes = &verifycode.TransferCodes{App: env.db.App, Platform: env.db.Platform, Gateway: gw, Hasher: env.pw, Now: o.Now}
	}})
	return h, tg
}

// GS-236 (агент): код в Telegram, новый пароль по коду, токены гостя отозваны, вход с новым паролем.
func TestGuestPasswordResetByTelegramCode(t *testing.T) {
	h, tg := telegramHarness(t, "")
	ph := uniqPhone()
	h.customer("Арман", ph, "old-secret", 0, 0)
	p := h.enrollPC()
	token := p.mustLogin(ph, "old-secret")

	_, err := p.authc().RequestPasswordResetCode(context.Background(), &agentv1.RequestPasswordResetCodeRequest{Phone: "abc"})
	wantReason(t, err, codes.InvalidArgument, agentv1.ErrorCode_ERROR_CODE_VALIDATION_FAILED)
	resp, err := p.authc().RequestPasswordResetCode(context.Background(), &agentv1.RequestPasswordResetCodeRequest{Phone: "8" + ph[2:]})
	if err != nil || resp.GetCodeValidFor().AsDuration() != 5*time.Minute || resp.GetResendAfter().AsDuration() != time.Minute {
		t.Fatalf("code = %+v, %v", resp, err)
	}
	codesSent := tg.Codes()
	if len(codesSent) != 1 {
		t.Fatalf("codes = %v", codesSent)
	}
	_, err = p.authc().RequestPasswordResetCode(context.Background(), &agentv1.RequestPasswordResetCodeRequest{Phone: ph})
	wantReason(t, err, codes.ResourceExhausted, agentv1.ErrorCode_ERROR_CODE_RATE_LIMITED)
	// Неизвестный номер — тот же ответ, кода нет.
	if _, err := p.authc().RequestPasswordResetCode(context.Background(), &agentv1.RequestPasswordResetCodeRequest{Phone: "+77000000001"}); err != nil {
		t.Fatal(err)
	}

	wrong := "111111"
	if codesSent[0] == wrong {
		wrong = "222222"
	}
	_, err = p.authc().ResetPassword(context.Background(), &agentv1.ResetPasswordRequest{Phone: ph, Code: wrong, NewPassword: "new-secret"})
	wantReason(t, err, codes.InvalidArgument, agentv1.ErrorCode_ERROR_CODE_CONFIRMATION_CODE_INVALID)
	_, err = p.authc().ResetPassword(context.Background(), &agentv1.ResetPasswordRequest{Phone: ph, Code: codesSent[0], NewPassword: "123"})
	wantReason(t, err, codes.InvalidArgument, agentv1.ErrorCode_ERROR_CODE_VALIDATION_FAILED)
	if _, err := p.authc().ResetPassword(context.Background(), &agentv1.ResetPasswordRequest{Phone: ph, Code: codesSent[0], NewPassword: "new-secret"}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.guest().GetProfile(withToken(token), &agentv1.GetProfileRequest{}); err == nil {
		t.Fatal("old guest token still works")
	}
	if _, err := p.login(ph, "old-secret"); err == nil {
		t.Fatal("old password still works")
	}
	p.mustLogin(ph, "new-secret")
	_, err = p.authc().ResetPassword(context.Background(), &agentv1.ResetPasswordRequest{Phone: ph, Code: codesSent[0], NewPassword: "again-secret"})
	wantReason(t, err, codes.FailedPrecondition, agentv1.ErrorCode_ERROR_CODE_CONFIRMATION_CODE_EXPIRED)
}

// GS-521, GS-522: код перевода — только для возможного перевода, привязан к сумме, перевод по коду один раз;
// переводы выключены в клубе — FEATURE_DISABLED.
func TestGuestTransferByTelegramCode(t *testing.T) {
	h, tg := telegramHarness(t, `{"guests": {"allow_adding_friends": true, "allow_balance_transfer_to_friends": true}}`)
	arman, dana := uniqPhone(), uniqPhone()
	a := h.customer("Арман", arman, "password", 10000, 0)
	d := h.customer("Дана", dana, "password", 1000, 0)
	fid := env.db.Friendship(t, h.club, a, d, true)
	p := h.enrollPC()
	ca := withToken(p.mustLogin(arman, "password"))

	_, err := p.social().RequestTransferCode(ca, &agentv1.RequestTransferCodeRequest{FriendshipId: fid, AmountTiyn: 50000})
	wantReason(t, err, codes.FailedPrecondition, agentv1.ErrorCode_ERROR_CODE_INSUFFICIENT_FUNDS)
	_, err = p.social().RequestTransferCode(ca, &agentv1.RequestTransferCodeRequest{FriendshipId: fid + 1_000_000, AmountTiyn: 4000})
	wantReason(t, err, codes.NotFound, agentv1.ErrorCode_ERROR_CODE_FRIENDSHIP_NOT_FOUND)
	if len(tg.Codes()) != 0 {
		t.Fatal("code sent for an impossible transfer")
	}
	resp, err := p.social().RequestTransferCode(ca, &agentv1.RequestTransferCodeRequest{FriendshipId: fid, AmountTiyn: 4000})
	if err != nil || resp.GetCodeValidFor().AsDuration() != 5*time.Minute {
		t.Fatalf("code = %+v, %v", resp, err)
	}
	code := tg.Codes()[0]
	_, err = p.social().RequestTransferCode(ca, &agentv1.RequestTransferCodeRequest{FriendshipId: fid, AmountTiyn: 4000})
	wantReason(t, err, codes.ResourceExhausted, agentv1.ErrorCode_ERROR_CODE_RATE_LIMITED)

	byCode := func(amount int64, c string) (*agentv1.TransferBalanceResponse, error) {
		return p.social().TransferBalance(ca, &agentv1.TransferBalanceRequest{IdempotencyKey: key(), FriendshipId: fid, AmountTiyn: amount,
			Confirmation: &agentv1.TransferBalanceRequest_TelegramCode{TelegramCode: c}})
	}
	_, err = byCode(9000, code)
	wantReason(t, err, codes.InvalidArgument, agentv1.ErrorCode_ERROR_CODE_CONFIRMATION_CODE_INVALID)
	h.wantBalances(a, 10000, 0)
	res, err := byCode(4000, code)
	if err != nil || res.GetBalance().GetMainTiyn() != 6000 {
		t.Fatalf("transfer = %+v, %v", res, err)
	}
	h.wantBalances(d, 5000, 0)
	if _, err := byCode(4000, code); err == nil {
		t.Fatal("code reused")
	}
	h.wantBalances(a, 6000, 0)
	h.noDrift()

	h.exec(`UPDATE clubs SET settings = '{"guests": {"allow_adding_friends": true, "allow_balance_transfer_to_friends": false}}'::jsonb WHERE id = $1`, h.club)
	h.clock.advance(2 * time.Minute)
	_, err = p.social().RequestTransferCode(ca, &agentv1.RequestTransferCodeRequest{FriendshipId: fid, AmountTiyn: 1000})
	wantReason(t, err, codes.PermissionDenied, agentv1.ErrorCode_ERROR_CODE_FEATURE_DISABLED)
	if len(tg.Codes()) != 1 {
		t.Fatalf("codes = %v", tg.Codes())
	}
}
