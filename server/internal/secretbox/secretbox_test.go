package secretbox

import (
	"errors"
	"strings"
	"testing"
)

func TestSealOpen(t *testing.T) {
	b, err := New(strings.Repeat("k", 32))
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := b.Seal("123456:ABC-token")
	if err != nil || strings.Contains(sealed, "ABC") || !strings.HasPrefix(sealed, "v1:") {
		t.Fatalf("seal: %q %v", sealed, err)
	}
	if got, err := b.Open(sealed); err != nil || got != "123456:ABC-token" {
		t.Fatalf("open: %q %v", got, err)
	}
	other, _ := New(strings.Repeat("x", 32))
	if _, err := other.Open(sealed); !errors.Is(err, ErrInvalid) {
		t.Fatalf("other key: %v", err)
	}
	if _, err := b.Open("v1:!!"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("garbage: %v", err)
	}
	if _, err := New("short"); err == nil {
		t.Fatal("short key accepted")
	}
}
