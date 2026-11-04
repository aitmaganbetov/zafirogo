package password

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// Настоящие хэши, сгенерированные PHP 8.3:
//
//	php -r 'echo password_hash("secret", PASSWORD_BCRYPT, ["cost"=>12]);'
//	php -r 'echo password_hash("secret", PASSWORD_ARGON2ID, ["memory_cost"=>19456,"time_cost"=>2,"threads"=>1]);'
//	php -r 'echo password_hash(str_repeat("я",40), PASSWORD_BCRYPT, ["cost"=>10]);'
const (
	laravelBcrypt       = "$2y$12$ctLLgcULumFLvY6JSuuVMuQBR2AwMHasyDWlpVVOqZi.dFt/wLjqy"
	phpArgon2id         = "$argon2id$v=19$m=19456,t=2,p=1$MG1UUFY2REZ6cy5KT3UzQQ$kgxQjfgHG6bfkWCq1fPEvwgcjuMBWgyDvE1eP+cfLDo"
	laravelBcryptLong80 = "$2y$10$HsVu/dvO1S83NJgpvK83cOOtR3RIt5J8SjcOZdZOYzDvwoVR5Lzlu" // 40 × «я» = 80 байт
)

func newHasher(t *testing.T) *Hasher {
	t.Helper()
	h, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestLaravelBcrypt(t *testing.T) {
	h := newHasher(t)
	ctx := context.Background()
	rehash, err := h.Verify(ctx, "secret", laravelBcrypt)
	if err != nil || !rehash {
		t.Fatalf("Verify(secret) = %v, %v; want rehash, nil", rehash, err)
	}
	if _, err := h.Verify(ctx, "Secret", laravelBcrypt); !errors.Is(err, ErrMismatch) {
		t.Fatalf("wrong password: %v", err)
	}
	if !IsLegacy(laravelBcrypt) || !h.NeedsRehash(laravelBcrypt) {
		t.Fatal("bcrypt must be legacy")
	}
}

// PHP password_verify учитывает только первые 72 байта: 36 × «я» + что угодно подходит к хэшу 40 × «я».
func TestBcryptTruncatesLikePHP(t *testing.T) {
	h := newHasher(t)
	ctx := context.Background()
	if _, err := h.Verify(ctx, strings.Repeat("я", 40), laravelBcryptLong80); err != nil {
		t.Fatalf("full password: %v", err)
	}
	if _, err := h.Verify(ctx, strings.Repeat("я", 36)+"zz", laravelBcryptLong80); err != nil {
		t.Fatalf("72-byte prefix must match like PHP: %v", err)
	}
	if _, err := h.Verify(ctx, strings.Repeat("я", 35), laravelBcryptLong80); !errors.Is(err, ErrMismatch) {
		t.Fatalf("shorter: %v", err)
	}
}

func TestPHPArgon2idCompatible(t *testing.T) {
	h := newHasher(t)
	rehash, err := h.Verify(context.Background(), "secret", phpArgon2id)
	if err != nil {
		t.Fatal(err)
	}
	if rehash {
		t.Fatal("same params as default: no rehash expected")
	}
}

func TestHashRoundTrip(t *testing.T) {
	h := newHasher(t)
	ctx := context.Background()
	enc, err := h.Hash(ctx, "пароль-123")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(enc, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Fatalf("format: %s", enc)
	}
	if rehash, err := h.Verify(ctx, "пароль-123", enc); err != nil || rehash {
		t.Fatalf("Verify = %v, %v", rehash, err)
	}
	if _, err := h.Verify(ctx, "пароль-124", enc); !errors.Is(err, ErrMismatch) {
		t.Fatalf("mismatch: %v", err)
	}
	enc2, _ := h.Hash(ctx, "пароль-123")
	if enc == enc2 {
		t.Fatal("salt must differ")
	}
}

func TestRehashOnParamChange(t *testing.T) {
	ctx := context.Background()
	old, err := New(Options{Params: Params{MemoryKiB: 8 * 1024, Iterations: 1, Parallelism: 1, SaltLen: 16, KeyLen: 32}})
	if err != nil {
		t.Fatal(err)
	}
	enc, _ := old.Hash(ctx, "x")
	h := newHasher(t)
	rehash, err := h.Verify(ctx, "x", enc)
	if err != nil || !rehash {
		t.Fatalf("Verify = %v, %v; want rehash", rehash, err)
	}
}

func TestMalformed(t *testing.T) {
	h := newHasher(t)
	ctx := context.Background()
	for _, enc := range []string{
		"",
		"plain",
		"$argon2i$v=19$m=19456,t=2,p=1$MG1UUFY2REZ6cy5KT3UzQQ$kgxQjfgHG6bfkWCq1fPEvwgcjuMBWgyDvE1eP+cfLDo",
		"$argon2id$v=16$m=19456,t=2,p=1$MG1UUFY2REZ6cy5KT3UzQQ$kgxQjfgHG6bfkWCq1fPEvwgcjuMBWgyDvE1eP+cfLDo",
		"$argon2id$v=19$m=99999999,t=2,p=1$MG1UUFY2REZ6cy5KT3UzQQ$kgxQjfgHG6bfkWCq1fPEvwgcjuMBWgyDvE1eP+cfLDo",
		"$argon2id$v=19$m=19456,t=2,p=0$MG1UUFY2REZ6cy5KT3UzQQ$kgxQjfgHG6bfkWCq1fPEvwgcjuMBWgyDvE1eP+cfLDo",
		"$argon2id$v=19$t=2,m=19456,p=1$MG1UUFY2REZ6cy5KT3UzQQ$kgxQjfgHG6bfkWCq1fPEvwgcjuMBWgyDvE1eP+cfLDo",
		"$argon2id$v=19$m=19456,t=2,p=1$!!!$kgxQjfgHG6bfkWCq1fPEvwgcjuMBWgyDvE1eP+cfLDo",
		"$2y$12$short",
	} {
		if _, err := h.Verify(ctx, "secret", enc); err == nil || errors.Is(err, ErrMismatch) {
			t.Errorf("%q: err = %v, want format error", enc, err)
		}
	}
}

func TestVerifyDummy(t *testing.T) {
	h := newHasher(t)
	if err := h.VerifyDummy(context.Background(), "anything"); !errors.Is(err, ErrMismatch) {
		t.Fatal(err)
	}
}

func TestContextCancelWhileQueued(t *testing.T) {
	h, err := New(Options{MaxConcurrent: 1})
	if err != nil {
		t.Fatal(err)
	}
	h.sem <- struct{}{} // занять единственное место
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := h.Hash(ctx, "x"); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	<-h.sem
}
