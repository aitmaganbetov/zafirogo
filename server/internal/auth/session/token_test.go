package session

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"
)

func testKeys() []Key {
	return []Key{{ID: "k2", Secret: bytes.Repeat([]byte{2}, 32)}, {ID: "k1", Secret: bytes.Repeat([]byte{1}, 32)}}
}

func TestTokenRoundTrip(t *testing.T) {
	s, err := newSigner(testKeys())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0)
	tok, err := s.sign(claims{SID: "abc", UID: 7, IAT: now.Unix(), EXP: now.Add(time.Minute).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(tok, "k2.") {
		t.Fatalf("first key must sign: %s", tok)
	}
	c, err := s.verify(tok, now)
	if err != nil || c.SID != "abc" || c.UID != 7 {
		t.Fatalf("verify = %+v, %v", c, err)
	}
	if _, err := s.verify(tok, now.Add(time.Minute)); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("expired: %v", err)
	}
}

func TestTokenOldKeyStillVerifies(t *testing.T) {
	old, _ := newSigner(testKeys()[1:])
	now := time.Now()
	tok, _ := old.sign(claims{SID: "x", UID: 1, IAT: now.Unix(), EXP: now.Add(time.Minute).Unix()})
	s, _ := newSigner(testKeys())
	if _, err := s.verify(tok, now); err != nil {
		t.Fatalf("rotated key: %v", err)
	}
	only2, _ := newSigner(testKeys()[:1])
	if _, err := only2.verify(tok, now); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("removed key must fail: %v", err)
	}
}

func TestTokenTampered(t *testing.T) {
	s, _ := newSigner(testKeys())
	now := time.Now()
	tok, _ := s.sign(claims{SID: "x", UID: 1, IAT: now.Unix(), EXP: now.Add(time.Minute).Unix()})
	parts := strings.Split(tok, ".")
	forged, _ := s.sign(claims{SID: "x", UID: 2, IAT: now.Unix(), EXP: now.Add(time.Minute).Unix()})
	fp := strings.Split(forged, ".")
	for _, bad := range []string{
		"", "a.b", "a.b.c.d", tok + "x",
		parts[0] + "." + fp[1] + "." + parts[2], // чужой payload со старой подписью
		"k1." + parts[1] + "." + parts[2],       // другой kid
		"zz." + parts[1] + "." + parts[2],       // неизвестный kid
		strings.Repeat("a", 2000),
	} {
		if _, err := s.verify(bad, now); !errors.Is(err, ErrInvalidToken) {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

func TestSignerValidation(t *testing.T) {
	for _, keys := range [][]Key{
		nil,
		{{ID: "a", Secret: []byte("short")}},
		{{ID: "", Secret: bytes.Repeat([]byte{1}, 32)}},
		{{ID: "bad.id", Secret: bytes.Repeat([]byte{1}, 32)}},
		{{ID: "a", Secret: bytes.Repeat([]byte{1}, 32)}, {ID: "a", Secret: bytes.Repeat([]byte{2}, 32)}},
	} {
		if _, err := newSigner(keys); err == nil {
			t.Errorf("keys %v accepted", keys)
		}
	}
}

func TestSplitRefresh(t *testing.T) {
	sid, _ := randomToken(16)
	sec, _ := randomToken(32)
	if got, ok := splitRefresh(sid + "." + sec); !ok || got != sid {
		t.Fatal("valid refresh")
	}
	for _, bad := range []string{"", sid, sid + ".", "." + sec, sid + "x." + sec} {
		if _, ok := splitRefresh(bad); ok {
			t.Errorf("%q accepted", bad)
		}
	}
}
