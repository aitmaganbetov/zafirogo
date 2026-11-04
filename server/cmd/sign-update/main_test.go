package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aitmaganbetov/zafirogo/server/internal/agent/commands"
)

const testURL = "https://github.com/aitmaganbetov/Zafiro/releases/download/agent-v1.2.0/ZafiroAgentSetup-1.2.0.exe"

func testKey() (ed25519.PublicKey, ed25519.PrivateKey) {
	priv := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{7}, ed25519.SeedSize))
	return priv.Public().(ed25519.PublicKey), priv
}

// Подписанный манифест проходит VerifyUpdate (как в кабинете суперадмина); любой изменённый байт установщика,
// поля манифеста или подписи — нет.
func TestSignVerify(t *testing.T) {
	pub, priv := testKey()
	installer := bytes.Repeat([]byte("MZ installer "), 1000)
	m, err := Sign(bytes.NewReader(installer), "1.2.0", "stable", "2026-10", testURL, priv)
	if err != nil {
		t.Fatal(err)
	}
	keys := commands.UpdateKeys{"2026-10": pub}
	if err := Verify(m, testURL, keys); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if m.SizeBytes != int64(len(installer)) || len(m.SHA256) != 64 {
		t.Fatalf("manifest %+v", m)
	}

	// Изменённый байт установщика → другая sha256 → подпись не сходится.
	tampered := bytes.Clone(installer)
	tampered[100] ^= 1
	m2, _ := Sign(bytes.NewReader(tampered), "1.2.0", "stable", "2026-10", testURL, priv)
	forged := *m
	forged.SHA256 = m2.SHA256
	if err := Verify(&forged, testURL, keys); !errors.Is(err, commands.ErrInvalidCommand) {
		t.Fatalf("tampered installer: %v", err)
	}
	mutations := map[string]func(*Manifest){
		"version": func(m *Manifest) { m.Version = "1.2.1" },
		"size":    func(m *Manifest) { m.SizeBytes++ },
		"channel": func(m *Manifest) { m.Channel = "beta" },
		"key id":  func(m *Manifest) { m.KeyID = "other" },
		"signature": func(m *Manifest) {
			sig, _ := base64.StdEncoding.DecodeString(m.Signature)
			sig[0] ^= 1
			m.Signature = base64.StdEncoding.EncodeToString(sig)
		},
	}
	for name, mut := range mutations {
		c := *m
		mut(&c)
		if err := Verify(&c, testURL, keys); !errors.Is(err, commands.ErrInvalidCommand) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Чужой открытый ключ с тем же key_id.
	other := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{9}, ed25519.SeedSize)).Public().(ed25519.PublicKey)
	if err := Verify(m, testURL, commands.UpdateKeys{"2026-10": other}); !errors.Is(err, commands.ErrInvalidCommand) {
		t.Fatalf("other key: %v", err)
	}

	for name, args := range map[string][]string{
		"bad version": {"1.2", "stable", "k"},
		"bad channel": {"1.2.0", "Stable\n", "k"},
		"bad key id":  {"1.2.0", "stable", "a:b"},
	} {
		if _, err := Sign(bytes.NewReader(installer), args[0], args[1], args[2], testURL, priv); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := Sign(bytes.NewReader(nil), "1.2.0", "stable", "k", testURL, priv); err == nil {
		t.Error("empty installer accepted")
	}
}

// CLI: ключ из переменной окружения (PEM PKCS#8 от openssl и base64 seed), манифест рядом с установщиком,
// проверка ожидаемым открытым ключом сервера.
func TestRun(t *testing.T) {
	pub, priv := testKey()
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	pemKey := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	seedKey := base64.StdEncoding.EncodeToString(priv.Seed())
	expect := "2026-10:" + base64.StdEncoding.EncodeToString(pub)

	dir := t.TempDir()
	file := filepath.Join(dir, "ZafiroAgentSetup-1.2.0.exe")
	if err := os.WriteFile(file, []byte("installer bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, key := range map[string]string{"pem": pemKey, "seed": seedKey} {
		env := func(k string) string {
			if k == "AGENT_UPDATE_SIGNING_KEY" {
				return key
			}
			return ""
		}
		var out bytes.Buffer
		if err := run([]string{"-file", file, "-version", "1.2.0", "-key-id", "2026-10", "-expect-public-key", expect}, env, &out); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if strings.Contains(out.String(), seedKey) {
			t.Fatalf("%s: private key printed", name)
		}
		raw, err := os.ReadFile(file + ".update.json")
		if err != nil {
			t.Fatal(err)
		}
		var m Manifest
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		if err := Verify(&m, m.URL, commands.UpdateKeys{"2026-10": pub}); err != nil || m.URL != "https://github.com/aitmaganbetov/Zafiro/releases/download/agent-v1.2.0/ZafiroAgentSetup-1.2.0.exe" {
			t.Fatalf("%s: manifest %s: %v", name, raw, err)
		}
	}

	env := func(string) string { return seedKey }
	wrong := "2026-10:" + base64.StdEncoding.EncodeToString(ed25519.NewKeyFromSeed(bytes.Repeat([]byte{1}, 32)).Public().(ed25519.PublicKey))
	if err := run([]string{"-file", file, "-version", "1.2.0", "-key-id", "2026-10", "-expect-public-key", wrong}, env, &bytes.Buffer{}); err == nil {
		t.Fatal("wrong expected public key accepted")
	}
	if err := run([]string{"-file", file, "-version", "1.2.0", "-key-id", "2026-10"}, func(string) string { return "" }, &bytes.Buffer{}); err == nil {
		t.Fatal("missing key accepted")
	}
	if err := run([]string{"-file", file, "-version", "1.2.0", "-key-id", "2026-10"}, func(string) string { return "bm90LWEta2V5" }, &bytes.Buffer{}); err == nil {
		t.Fatal("bad key accepted")
	}

	var out bytes.Buffer
	if err := run([]string{"-genkey", "-key-id", "2027-04"}, nil, &out); err != nil || !strings.Contains(out.String(), "agent.update.public_keys: 2027-04:") {
		t.Fatalf("genkey: %q %v", out.String(), err)
	}
}
