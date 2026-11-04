package secretbox

import (
	"errors"
	"strings"
	"testing"
)

func ring(t *testing.T, legacy string, keys ...Key) *Box {
	t.Helper()
	b, err := NewKeyring(keys, legacy)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

var (
	k1 = Key{ID: "2026-10", Secret: strings.Repeat("a", 32)}
	k2 = Key{ID: "2026-11", Secret: strings.Repeat("b", 32)}
)

func TestKeyringSealOpen(t *testing.T) {
	b := ring(t, "", k1)
	sealed, err := b.SealWith("whsec_abc", "webhook_endpoints.secret:club=1")
	if err != nil || !strings.HasPrefix(sealed, "v2:2026-10:") || strings.Contains(sealed, "whsec") {
		t.Fatalf("seal: %q %v", sealed, err)
	}
	if got, err := b.OpenWith(sealed, "webhook_endpoints.secret:club=1"); err != nil || got != "whsec_abc" {
		t.Fatalf("open: %q %v", got, err)
	}
	// Контекст другого клуба — не расшифровывается (секрет нельзя перенести в чужой клуб).
	if _, err := b.OpenWith(sealed, "webhook_endpoints.secret:club=2"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("other context: %v", err)
	}
	// Тот же kid, другой секрет — не расшифровывается.
	other := ring(t, "", Key{ID: "2026-10", Secret: strings.Repeat("z", 32)})
	if _, err := other.OpenWith(sealed, "webhook_endpoints.secret:club=1"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("other secret: %v", err)
	}
	for _, bad := range []string{"v2:", "v2:2026-10", "v2:nokey:AAAA", "v2:2026-10:!!", "v2:2026-10:AAAA", "plain", ""} {
		if _, err := b.Open(bad); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%q: %v", bad, err)
		}
	}
}

func TestKeyringRotation(t *testing.T) {
	old := ring(t, "", k1)
	sealed, _ := old.SealWith("token", "ctx")

	// Новый ключ первым, старый — для расшифровки.
	rotated := ring(t, "", k2, k1)
	if rotated.Current(sealed) {
		t.Fatal("old-key value reported current")
	}
	if got, err := rotated.OpenWith(sealed, "ctx"); err != nil || got != "token" {
		t.Fatalf("open old: %q %v", got, err)
	}
	out, changed, err := rotated.Reseal(sealed, "ctx", false)
	if err != nil || !changed || !strings.HasPrefix(out, "v2:2026-11:") {
		t.Fatalf("reseal: %q %v %v", out, changed, err)
	}
	if again, changed, _ := rotated.Reseal(out, "ctx", false); changed || again != out {
		t.Fatal("current value resealed")
	}
	// Старый ключ убран из связки — значение, перешифрованное новым, читается; старое — нет.
	only := ring(t, "", k2)
	if got, err := only.OpenWith(out, "ctx"); err != nil || got != "token" {
		t.Fatalf("open new: %q %v", got, err)
	}
	if _, err := only.OpenWith(sealed, "ctx"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("removed key still opens: %v", err)
	}
}

func TestKeyringLegacyAndPlaintext(t *testing.T) {
	legacySecret := strings.Repeat("s", 32)
	v1, _ := New(legacySecret)
	sealedV1, _ := v1.Seal("123456:ABC")

	b := ring(t, legacySecret, k1)
	if got, err := b.Open(sealedV1); err != nil || got != "123456:ABC" {
		t.Fatalf("open v1: %q %v", got, err)
	}
	out, changed, err := b.Reseal(sealedV1, "", false)
	if err != nil || !changed || !strings.HasPrefix(out, "v2:2026-10:") {
		t.Fatalf("reseal v1: %q %v %v", out, changed, err)
	}
	// Без legacy-ключа v1 не читается.
	if _, err := ring(t, "", k1).Open(sealedV1); !errors.Is(err, ErrInvalid) {
		t.Fatalf("v1 without legacy: %v", err)
	}
	// Открытый текст шифруется только при plaintextOK.
	if same, changed, _ := b.Reseal("whsec_x", "c", false); changed || same != "whsec_x" {
		t.Fatal("plaintext sealed without plaintextOK")
	}
	out, changed, err = b.Reseal("whsec_x", "c", true)
	if err != nil || !changed || !IsSealed(out) {
		t.Fatalf("plaintext reseal: %q %v %v", out, changed, err)
	}
	if got, _ := b.OpenWith(out, "c"); got != "whsec_x" {
		t.Fatalf("plaintext roundtrip: %q", got)
	}
}

func TestNewKeyringValidation(t *testing.T) {
	cases := map[string][]Key{
		"empty":     nil,
		"short":     {{ID: "a", Secret: "short"}},
		"bad id":    {{ID: "a:b", Secret: strings.Repeat("a", 32)}},
		"duplicate": {k1, k1},
	}
	for name, keys := range cases {
		if _, err := NewKeyring(keys, ""); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := NewKeyring([]Key{k1}, "short"); err == nil {
		t.Error("short legacy accepted")
	}
}
