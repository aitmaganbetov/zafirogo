package auth

import (
	"errors"
	"testing"

	apicommon "github.com/aitmaganbetov/zafirogo/server/internal/http/api/common"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
)

// Аудит SEC-8: регистрация владельца и аккаунт сотрудника по приглашению не принимают пароль из списка самых частых;
// прочие ошибки пароля (короткий, не совпал) — как в Laravel, без второй ошибки.
func TestRejectCommonPassword(t *testing.T) {
	for _, tc := range []struct {
		pw, confirm string
		want        []string
	}{
		{"123456", "123456", []string{msgPasswordCommon}},
		{"Qwerty123", "Qwerty123", []string{msgPasswordCommon}},
		{"klub-abaya-12", "klub-abaya-12", nil},
		{"12345", "12345", []string{msgPasswordMin}},
		{"123456", "x", []string{msgPasswordMismatch}},
	} {
		var fe httpx.FormErrors
		validatePassword(&fe, tc.pw, tc.confirm, "required", msgPasswordMin, msgPasswordMismatch)
		rejectCommonPassword(&fe, tc.pw)
		var got []string
		var ae *httpx.APIError
		if err := fe.Err(); err != nil {
			if !errors.As(err, &ae) {
				t.Fatalf("%q: %T", tc.pw, err)
			}
			body, ok := ae.Body.(apicommon.ValidationError)
			if !ok {
				t.Fatalf("%q: body %T", tc.pw, ae.Body)
			}
			got = body.Errors["password"]
		}
		if len(got) != len(tc.want) || (len(got) > 0 && got[0] != tc.want[0]) {
			t.Errorf("%q/%q: errors %v, want %v", tc.pw, tc.confirm, got, tc.want)
		}
	}
}
