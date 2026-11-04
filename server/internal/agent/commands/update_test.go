package commands

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
)

func signedUpdate(t *testing.T, priv ed25519.PrivateKey, keyID string) *agentv1.UpdateAvailable {
	t.Helper()
	u := &agentv1.UpdateAvailable{
		Version: "2.1.0", Url: "https://updates.zafiro.kz/agent/2.1.0/ZafiroAgentSetup.exe",
		Sha256: strings.Repeat("ab", 32), SizeBytes: 12345678, KeyId: keyID, Channel: "stable",
	}
	u.SignatureEd25519 = ed25519.Sign(priv, UpdateSigningMessage(u))
	return u
}

// Решение «Обновления агента» (docs/DECISIONS.md): подпись Ed25519 обязательна и проверяется до рассылки.
func TestVerifyUpdate(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keys := UpdateKeys{"2026-10": pub}
	good := signedUpdate(t, priv, "2026-10")
	if got := string(UpdateSigningMessage(good)); got != "zafiro-agent-update/v1\n2.1.0\n"+strings.Repeat("ab", 32)+"\n12345678\nstable" {
		t.Fatalf("signing message = %q", got)
	}
	if err := VerifyUpdate(good, keys); err != nil {
		t.Fatal(err)
	}
	if err := Validate(&agentv1.Command{Kind: &agentv1.Command_UpdateAvailable{UpdateAvailable: good}}); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	_, otherPriv, _ := ed25519.GenerateKey(rand.Reader)
	mutate := func(f func(u *agentv1.UpdateAvailable)) *agentv1.UpdateAvailable {
		u, ok := proto.Clone(good).(*agentv1.UpdateAvailable)
		if !ok {
			t.Fatal("clone")
		}
		f(u)
		return u
	}
	bad := map[string]*agentv1.UpdateAvailable{
		"no signature":       mutate(func(u *agentv1.UpdateAvailable) { u.SignatureEd25519 = nil }),
		"short signature":    mutate(func(u *agentv1.UpdateAvailable) { u.SignatureEd25519 = u.SignatureEd25519[:63] }),
		"no key id":          mutate(func(u *agentv1.UpdateAvailable) { u.KeyId = "" }),
		"unknown key id":     mutate(func(u *agentv1.UpdateAvailable) { u.KeyId = "2025-01" }),
		"other key":          signedUpdate(t, otherPriv, "2026-10"),
		"tampered version":   mutate(func(u *agentv1.UpdateAvailable) { u.Version = "2.1.1" }),
		"tampered sha256":    mutate(func(u *agentv1.UpdateAvailable) { u.Sha256 = strings.Repeat("cd", 32) }),
		"tampered size":      mutate(func(u *agentv1.UpdateAvailable) { u.SizeBytes++ }),
		"tampered channel":   mutate(func(u *agentv1.UpdateAvailable) { u.Channel = "beta" }),
		"http url":           mutate(func(u *agentv1.UpdateAvailable) { u.Url = "http://updates.zafiro.kz/a.exe" }),
		"upper sha256":       mutate(func(u *agentv1.UpdateAvailable) { u.Sha256 = strings.Repeat("AB", 32) }),
		"zero size":          mutate(func(u *agentv1.UpdateAvailable) { u.SizeBytes = 0 }),
		"newline in version": mutate(func(u *agentv1.UpdateAvailable) { u.Version = "2.1.0\nbeta" }),
	}
	for name, u := range bad {
		if err := VerifyUpdate(u, keys); !errors.Is(err, ErrInvalidCommand) {
			t.Errorf("%s: want ErrInvalidCommand, got %v", name, err)
		}
	}
	// Без настроенных ключей обновление не рассылается.
	if err := VerifyUpdate(good, nil); !errors.Is(err, ErrInvalidCommand) {
		t.Errorf("no keys: %v", err)
	}
	// Подпись обязательна уже в Validate (до проверки ключом).
	if err := Validate(&agentv1.Command{Kind: &agentv1.Command_UpdateAvailable{UpdateAvailable: bad["no signature"]}}); !errors.Is(err, ErrInvalidCommand) {
		t.Errorf("Validate without signature: %v", err)
	}
	// Send отклоняет неподписанное до записи в очередь (зависимости Sender не нужны).
	s := NewSender(SenderOptions{UpdateKeys: keys})
	if _, err := s.Send(t.Context(), 1, 1, &agentv1.Command{Kind: &agentv1.Command_UpdateAvailable{UpdateAvailable: bad["other key"]}}); !errors.Is(err, ErrInvalidCommand) {
		t.Errorf("Send with a foreign signature: %v", err)
	}
}
