package migrate

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

type laravelFixture struct {
	PHP          string `json:"php"`
	Laravel      string `json:"laravel"`
	AppKey       string `json:"app_key"`
	PreviousKeys string `json:"previous_keys"`
	Cases        []struct {
		Name        string `json:"name"`
		Plain       string `json:"plain"`
		Unserialize bool   `json:"unserialize"`
		Payload     string `json:"payload"`
	} `json:"cases"`
}

func loadLaravelFixture(t *testing.T) laravelFixture {
	t.Helper()
	raw, err := os.ReadFile("testdata/laravel_crypt.json")
	if err != nil {
		t.Fatal(err)
	}
	var f laravelFixture
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

// Значения зашифрованы настоящим Illuminate\Encryption\Encrypter в php:8.4.27-cli
// (tools/migrate-from-mysql/laravel-crypt-fixture.sh).
func TestLaravelDecryptPHPFixture(t *testing.T) {
	f := loadLaravelFixture(t)
	if !strings.HasPrefix(f.PHP, "8.4.") || f.Laravel == "" || len(f.Cases) == 0 {
		t.Fatalf("fixture: php %q laravel %q, %d cases", f.PHP, f.Laravel, len(f.Cases))
	}
	keys, err := ParseLaravelKeys(f.AppKey, f.PreviousKeys)
	if err != nil {
		t.Fatal(err)
	}
	only, err := ParseLaravelKeys(f.AppKey, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range f.Cases {
		t.Run(c.Name, func(t *testing.T) {
			if !LooksEncrypted(c.Payload) {
				t.Fatal("payload not recognized")
			}
			got, err := keys.Decrypt(c.Payload, c.Unserialize)
			if err != nil || got != c.Plain {
				t.Fatalf("decrypt: %v (match %v)", err, got == c.Plain)
			}
			_, err = only.Decrypt(c.Payload, c.Unserialize)
			if c.Name == "previous_key" {
				if !errors.Is(err, ErrLaravelMAC) {
					t.Fatalf("previous-key value without APP_PREVIOUS_KEYS: %v", err)
				}
			} else if err != nil {
				t.Fatalf("current key only: %v", err)
			}
			if c.Unserialize {
				// Без unserialize — сериализованная строка PHP как есть.
				raw, err := keys.DecryptString(c.Payload)
				if err != nil || !strings.HasPrefix(raw, "s:") {
					t.Fatalf("raw serialized: %v", err)
				}
			}
		})
	}
}

func TestLaravelDecryptRejects(t *testing.T) {
	f := loadLaravelFixture(t)
	keys, err := ParseLaravelKeys(f.AppKey, "")
	if err != nil {
		t.Fatal(err)
	}
	good := f.Cases[0].Payload
	var p map[string]string
	raw, _ := base64.StdEncoding.DecodeString(good)
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	mutate := func(k, v string) string {
		q := map[string]string{}
		for kk, vv := range p {
			q[kk] = vv
		}
		q[k] = v
		b, _ := json.Marshal(q)
		return base64.StdEncoding.EncodeToString(b)
	}
	for name, tc := range map[string]struct {
		payload string
		want    error
	}{
		"not base64":   {"!!!", ErrLaravelPayload},
		"not json":     {base64.StdEncoding.EncodeToString([]byte("x")), ErrLaravelPayload},
		"plain secret": {"whsec_abc", ErrLaravelPayload},
		"bad mac":      {mutate("mac", strings.Repeat("0", 64)), ErrLaravelMAC},
		"bad iv":       {mutate("iv", "AAAA"), ErrLaravelPayload},
		"gcm tag":      {mutate("tag", "AAAAAAAAAAAAAAAAAAAAAA=="), ErrLaravelPayload},
	} {
		if _, err := keys.DecryptString(tc.payload); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", name, err, tc.want)
		}
	}
	// Сообщения ошибок не содержат ни ключа, ни значения.
	other, _ := ParseLaravelKeys("base64:"+base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32))), "")
	if _, err := other.DecryptString(good); !errors.Is(err, ErrLaravelMAC) || strings.Contains(err.Error(), "whsec") {
		t.Fatalf("other key: %v", err)
	}
}

func TestParseLaravelKeys(t *testing.T) {
	for _, bad := range []string{"", "base64:short", "base64:!!", "short-raw-key"} {
		if _, err := ParseLaravelKeys(bad, ""); !errors.Is(err, ErrLaravelKey) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	if _, err := ParseLaravelKeys(strings.Repeat("r", 32), " , "); err != nil {
		t.Errorf("raw 32-byte key: %v", err)
	}
	if _, err := ParseLaravelKeys(strings.Repeat("r", 32), "base64:bad"); !errors.Is(err, ErrLaravelKey) {
		t.Errorf("bad previous key: %v", err)
	}
}

func TestPHPUnserializeString(t *testing.T) {
	for in, want := range map[string]string{`s:0:"";`: "", `s:3:"a;b";`: "a;b", `s:6:"ЖЁ!";`: ""} {
		got, err := phpUnserializeString(in)
		if in == `s:6:"ЖЁ!";` { // 5 байт, не 6
			if err == nil {
				t.Errorf("%q accepted", in)
			}
			continue
		}
		if err != nil || got != want {
			t.Errorf("%q: %q %v", in, got, err)
		}
	}
	for _, bad := range []string{"i:5;", `s:2:"abc";`, `s:x:"";`, `s:1:"a"`} {
		if _, err := phpUnserializeString(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
