// Команда sign-update — подпись выпуска агента ПК для CI (.github/workflows/agent-release.yml, решение
// «Обновления агента», docs/agent-protocol.md §6).
//
// Считает sha256 и размер установщика, подписывает Ed25519 строку commands.UpdateSigningMessage и пишет манифест
// <установщик>.update.json — его читает кабинет суперадмина (server/internal/http/sections/superadmin/release.go)
// и проверяет commands.VerifyUpdate открытым ключом agent.update.public_keys. Перед записью манифест проверяется
// тем же VerifyUpdate.
//
// Приватный ключ — только из переменной окружения (по умолчанию AGENT_UPDATE_SIGNING_KEY): PEM PKCS#8
// (openssl genpkey -algorithm ed25519) или base64 seed (32 байта) / ключа (64 байта). В аргументах и выводе ключа нет.
//
//	sign-update -file ZafiroAgentSetup-1.2.0.exe -version 1.2.0 -key-id 2026-10 [-channel stable] [-url https://…]
//	            [-expect-public-key 2026-10:base64] [-out файл.update.json]
//	sign-update -genkey -key-id 2026-10   # новый ключ: приватный (в секрет CI) и строка для agent.update.public_keys
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/commands"
)

// Manifest — <установщик>.update.json (поля как у manifest в superadmin/release.go).
type Manifest struct {
	Version   string `json:"version"`
	SHA256    string `json:"sha256"`
	SizeBytes int64  `json:"size_bytes"`
	Channel   string `json:"channel"`
	KeyID     string `json:"key_id"`
	Signature string `json:"signature_ed25519"`
	// URL — адрес установщика (справочно; сервер берёт его из шаблона INSTALLER_URL).
	URL string `json:"url,omitempty"`
}

var (
	// versionRe — как versionRe кабинета суперадмина (три числа через точку).
	versionRe = regexp.MustCompile(`^\d{1,4}\.\d{1,4}\.\d{1,6}$`)
	// keyIDRe — как updateKeyIDRe конфигурации сервера.
	keyIDRe   = regexp.MustCompile(`^[A-Za-z0-9._-]{1,32}$`)
	channelRe = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)
)

func main() {
	if err := run(os.Args[1:], os.Getenv, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "sign-update:", err)
		os.Exit(1)
	}
}

func run(args []string, getenv func(string) string, stdout io.Writer) error {
	fs := flag.NewFlagSet("sign-update", flag.ContinueOnError)
	file := fs.String("file", "", "путь к установщику")
	version := fs.String("version", "", "версия агента, например 1.2.0")
	channel := fs.String("channel", "stable", "канал обновлений")
	keyID := fs.String("key-id", "", "key_id ключа подписи (как в agent.update.public_keys)")
	keyEnv := fs.String("key-env", "AGENT_UPDATE_SIGNING_KEY", "переменная окружения с приватным ключом")
	rawURL := fs.String("url", "", "https-адрес установщика (по умолчанию — выпуск agent-v<версия> на GitHub)")
	expect := fs.String("expect-public-key", "", "key_id:base64 — открытый ключ сервера; подпись должна им проверяться")
	out := fs.String("out", "", "путь манифеста (по умолчанию <file>.update.json)")
	genkey := fs.Bool("genkey", false, "создать новый ключ и напечатать его")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *genkey {
		return generate(*keyID, stdout)
	}
	priv, err := parsePrivateKey(getenv(*keyEnv))
	if err != nil {
		return fmt.Errorf("%s: %w", *keyEnv, err)
	}
	f, err := os.Open(*file)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	u := *rawURL
	if u == "" {
		u = "https://github.com/aitmaganbetov/Zafiro/releases/download/agent-v" + *version + "/" + filepath.Base(*file)
	}
	m, err := Sign(f, *version, *channel, *keyID, u, priv)
	if err != nil {
		return err
	}
	if *expect != "" {
		if err := checkExpected(m, u, *expect); err != nil {
			return err
		}
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	path := *out
	if path == "" {
		path = *file + ".update.json"
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil { // #nosec G306 -- манифест публичный
		return err
	}
	_, err = fmt.Fprintf(stdout, "%s: version=%s sha256=%s size=%d channel=%s key_id=%s\n", path, m.Version, m.SHA256, m.SizeBytes, m.Channel, m.KeyID)
	return err
}

// Sign считает sha256 и размер установщика, подписывает и проверяет манифест (commands.VerifyUpdate).
func Sign(installer io.Reader, version, channel, keyID, rawURL string, priv ed25519.PrivateKey) (*Manifest, error) {
	switch {
	case !versionRe.MatchString(version):
		return nil, fmt.Errorf("version %q: ожидается три числа через точку", version)
	case !channelRe.MatchString(channel):
		return nil, fmt.Errorf("channel %q: ожидается [a-z0-9_-]{1,32}", channel)
	case !keyIDRe.MatchString(keyID):
		return nil, fmt.Errorf("key-id %q: ожидается [A-Za-z0-9._-]{1,32}", keyID)
	}
	h := sha256.New()
	size, err := io.Copy(h, installer)
	if err != nil {
		return nil, err
	}
	if size == 0 {
		return nil, errors.New("установщик пуст")
	}
	u := &agentv1.UpdateAvailable{Version: version, Url: rawURL, Sha256: hex.EncodeToString(h.Sum(nil)), SizeBytes: size, Channel: channel, KeyId: keyID}
	u.SignatureEd25519 = ed25519.Sign(priv, commands.UpdateSigningMessage(u))
	pub, _ := priv.Public().(ed25519.PublicKey)
	if err := commands.VerifyUpdate(u, commands.UpdateKeys{keyID: pub}); err != nil {
		return nil, err
	}
	return &Manifest{Version: version, SHA256: u.Sha256, SizeBytes: size, Channel: channel, KeyID: keyID,
		Signature: base64.StdEncoding.EncodeToString(u.SignatureEd25519), URL: rawURL}, nil
}

// Verify — проверка манифеста так же, как кабинет суперадмина (superadmin/release.go signedUpdate).
func Verify(m *Manifest, rawURL string, keys commands.UpdateKeys) error {
	sig, err := base64.StdEncoding.DecodeString(m.Signature)
	if err != nil {
		return err
	}
	return commands.VerifyUpdate(&agentv1.UpdateAvailable{Version: m.Version, Url: rawURL, Sha256: strings.ToLower(m.SHA256),
		SizeBytes: m.SizeBytes, SignatureEd25519: sig, KeyId: m.KeyID, Channel: m.Channel}, keys)
}

func checkExpected(m *Manifest, rawURL, expect string) error {
	id, b64, ok := strings.Cut(strings.TrimSpace(expect), ":")
	raw, err := base64.StdEncoding.DecodeString(b64)
	if !ok || err != nil || len(raw) != ed25519.PublicKeySize {
		return errors.New("expect-public-key: ожидается key_id:base64 (32 байта)")
	}
	if err := Verify(m, rawURL, commands.UpdateKeys{id: ed25519.PublicKey(raw)}); err != nil {
		return fmt.Errorf("подпись не проверяется открытым ключом сервера: %w", err)
	}
	return nil
}

// parsePrivateKey — PEM PKCS#8 или base64 seed (32 байта) / ключа (64 байта).
func parsePrivateKey(v string) (ed25519.PrivateKey, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil, errors.New("ключ подписи не задан")
	}
	if strings.HasPrefix(v, "-----BEGIN") {
		block, _ := pem.Decode([]byte(v))
		if block == nil {
			return nil, errors.New("ключ: неверный PEM")
		}
		k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, errors.New("ключ: ожидается PKCS#8 Ed25519")
		}
		priv, ok := k.(ed25519.PrivateKey)
		if !ok {
			return nil, errors.New("ключ: не Ed25519")
		}
		return priv, nil
	}
	raw, err := base64.StdEncoding.DecodeString(v)
	if err != nil {
		return nil, errors.New("ключ: ожидается PEM или base64")
	}
	switch len(raw) {
	case ed25519.SeedSize:
		return ed25519.NewKeyFromSeed(raw), nil
	case ed25519.PrivateKeySize:
		priv := ed25519.NewKeyFromSeed(raw[:ed25519.SeedSize])
		if !priv.Equal(ed25519.PrivateKey(raw)) {
			return nil, errors.New("ключ: открытая часть не соответствует seed")
		}
		return priv, nil
	}
	return nil, errors.New("ключ: ожидается 32 или 64 байта")
}

func generate(keyID string, stdout io.Writer) error {
	if !keyIDRe.MatchString(keyID) {
		return fmt.Errorf("key-id %q: ожидается [A-Za-z0-9._-]{1,32}", keyID)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "AGENT_UPDATE_SIGNING_KEY (секрет CI, base64 seed): %s\nagent.update.public_keys: %s:%s\n",
		base64.StdEncoding.EncodeToString(priv.Seed()), keyID, base64.StdEncoding.EncodeToString(pub))
	return err
}
