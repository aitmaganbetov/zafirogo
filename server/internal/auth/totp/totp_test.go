package totp

import (
	"net/url"
	"strings"
	"testing"
	"time"
)

// RFC 6238, приложение B: ключ SHA-1 — ASCII "12345678901234567890", 8 цифр.
func TestRFC6238Vectors(t *testing.T) {
	key := []byte("12345678901234567890")
	for _, tc := range []struct {
		unix int64
		want string
	}{
		{59, "94287082"},
		{1111111109, "07081804"},
		{1111111111, "14050471"},
		{1234567890, "89005924"},
		{2000000000, "69279037"},
		{20000000000, "65353130"},
	} {
		step := Step(time.Unix(tc.unix, 0))
		if got := HOTP(key, uint64(step), 8); got != tc.want {
			t.Errorf("T=%d: %s, want %s", tc.unix, got, tc.want)
		}
		// 6 цифр — последние 6 того же числа (усечение по модулю 10^6).
		if got := Code(key, step); got != tc.want[2:] {
			t.Errorf("T=%d 6 digits: %s, want %s", tc.unix, got, tc.want[2:])
		}
	}
}

// RFC 4226, приложение D: HOTP с тем же ключом, счётчики 0–9.
func TestRFC4226Vectors(t *testing.T) {
	key := []byte("12345678901234567890")
	want := []string{"755224", "287082", "359152", "969429", "338314", "254676", "287922", "162583", "399871", "520489"}
	for i, w := range want {
		if got := HOTP(key, uint64(i), 6); got != w {
			t.Errorf("counter %d: %s, want %s", i, got, w)
		}
	}
}

func TestSecretRoundTrip(t *testing.T) {
	s, err := NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	if len(s) != 32 || strings.ContainsAny(s, "=") {
		t.Fatalf("secret %q", s)
	}
	key, err := DecodeSecret(strings.ToLower(s[:4]) + " " + s[4:])
	if err != nil || len(key) != SecretBytes {
		t.Fatalf("decode: %v %d", err, len(key))
	}
	if _, err := DecodeSecret("!!!"); err == nil {
		t.Fatal("bad secret accepted")
	}
	if _, err := DecodeSecret(""); err == nil {
		t.Fatal("empty secret accepted")
	}
}

// Окно ±1 шаг; шаги ≤ последнего принятого — отказ (повтор кода).
func TestVerifyWindowAndReplay(t *testing.T) {
	key := []byte("12345678901234567890")
	now := time.Unix(1_800_000_015, 0)
	cur := Step(now)
	for _, d := range []int64{-1, 0, 1} {
		code := Code(key, cur+d)
		step, ok := Verify(key, code, now, 0)
		if !ok || step != cur+d {
			t.Errorf("offset %d: %d %v", d, step, ok)
		}
	}
	for _, d := range []int64{-2, 2} {
		if _, ok := Verify(key, Code(key, cur+d), now, 0); ok {
			t.Errorf("offset %d accepted outside the window", d)
		}
	}
	// Тот же код повторно (последний принятый — cur) — отказ; следующий шаг — да.
	if _, ok := Verify(key, Code(key, cur), now, cur); ok {
		t.Fatal("replayed step accepted")
	}
	if _, ok := Verify(key, Code(key, cur-1), now, cur); ok {
		t.Fatal("older step accepted after a newer one")
	}
	if s, ok := Verify(key, Code(key, cur+1), now, cur); !ok || s != cur+1 {
		t.Fatal("next step refused")
	}
	// Пробелы допускаются, не цифры и не та длина — нет.
	c := Code(key, cur)
	if _, ok := Verify(key, c[:3]+" "+c[3:], now, 0); !ok {
		t.Fatal("code with a space refused")
	}
	for _, bad := range []string{"", "12345", "1234567", "abcdef", c + "0"} {
		if _, ok := Verify(key, bad, now, 0); ok {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestURI(t *testing.T) {
	u, err := url.Parse(URI("Zafiro", "+77010000001", "JBSWY3DPEHPK3PXP"))
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme != "otpauth" || u.Host != "totp" || u.Path != "/Zafiro:+77010000001" {
		t.Fatalf("uri %s", u)
	}
	q := u.Query()
	if q.Get("secret") != "JBSWY3DPEHPK3PXP" || q.Get("issuer") != "Zafiro" || q.Get("digits") != "6" || q.Get("period") != "30" || q.Get("algorithm") != "SHA1" {
		t.Fatalf("query %v", q)
	}
}

func TestRecoveryCodes(t *testing.T) {
	codes, err := NewRecoveryCodes(RecoveryCount)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, c := range codes {
		if len(c) != 19 || c[4] != '-' || c[9] != '-' || c[14] != '-' || !IsRecovery(c) || IsCode(c) {
			t.Fatalf("code %q", c)
		}
		if seen[c] {
			t.Fatal("duplicate code")
		}
		seen[c] = true
		// Регистр, пробелы и дефисы не важны; хэш привязан к пользователю.
		if HashRecovery(1, c) != HashRecovery(1, strings.ToUpper(strings.ReplaceAll(c, "-", " "))) {
			t.Fatal("normalization")
		}
		if HashRecovery(1, c) == HashRecovery(2, c) {
			t.Fatal("hash not bound to the user")
		}
	}
	if len(codes) != 10 {
		t.Fatalf("%d codes", len(codes))
	}
	// Крокфорд: O → 0, I/L → 1.
	if NormalizeRecovery("OIL-x") != "011x" {
		t.Fatal(NormalizeRecovery("OIL-x"))
	}
	if IsRecovery("123456") || IsRecovery("uuuu-uuuu-uuuu-uuuu") {
		t.Fatal("bad recovery format accepted")
	}
}
