// Package secretbox — шифрование секретов в базе: секреты вебхуков клубов (webhook_endpoints.secret), токены
// Telegram платформы (platform_settings). Аналог Crypt::encryptString()/decryptString() и каста 'encrypted'
// старого кода (app/Models/WebhookEndpoint.php, app/Services/TelegramGateway.php,
// Monitoring/TelegramAlerts.php): секрет в базе только зашифрованным, расшифровать его без ключа сервера нельзя.
//
// Шифр — AES-256-GCM. Ключи — связка security.data_keys ("kid:секрет", первый шифрует, остальные только
// расшифровывают — ротация, docs/schema-changes.md «Шифрование секретов в базе»). Ключ AES — HKDF-SHA256 от секрета
// с меткой "zafiro secretbox v2".
//
// Форматы:
//   - "v2:<kid>:" + base64(nonce ‖ ciphertext) — текущий; kid — id ключа связки. Необязательный контекст
//     (associated data) привязывает шифротекст к месту хранения: секрет вебхука нельзя перенести в другой клуб.
//   - "v1:" + base64(nonce ‖ ciphertext) — Этап 3 (кабинет суперадмина): ключ — SHA-256 от
//     superadmin.secret_key, без контекста. Только расшифровка (Legacy); воркер ReencryptSecrets
//     перешифровывает такие значения текущим ключом.
package secretbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"regexp"
	"strings"
)

const (
	prefixV1 = "v1:"
	prefixV2 = "v2:"
	// hkdfInfo — метка вывода ключа AES из секрета конфигурации (разные назначения — разные ключи).
	hkdfInfo = "zafiro secretbox v2"
	// MinSecretLen — минимальная длина секрета ключа (байт).
	MinSecretLen = 32
)

// ErrInvalid — шифротекст испорчен, зашифрован неизвестным ключом или с другим контекстом.
var ErrInvalid = errors.New("secretbox: cannot decrypt")

var kidRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,16}$`)

// Key — ключ связки: ID попадает в шифротекст, Secret — секрет конфигурации (≥ 32 байт).
type Key struct {
	ID     string
	Secret string
}

// Box — связка ключей сервера. Безопасен для конкурентного использования.
type Box struct {
	active string                 // kid текущего ключа ("" — Box из New: шифрует в v1)
	keys   map[string]cipher.AEAD // v2 по kid
	legacy cipher.AEAD            // v1 (superadmin.secret_key), может быть nil
}

// New — шифр формата v1 из одного секрета (Этап 3, superadmin.secret_key). Оставлен для совместимости
// и тестов; сервер использует NewKeyring.
func New(secret string) (*Box, error) {
	if len(secret) < MinSecretLen {
		return nil, errors.New("secretbox: secret must be at least 32 bytes")
	}
	aead, err := legacyAEAD(secret)
	if err != nil {
		return nil, err
	}
	return &Box{legacy: aead, keys: map[string]cipher.AEAD{}}, nil
}

// NewKeyring — связка ключей: keys[0] шифрует, все расшифровывают. legacy — секрет формата v1
// (superadmin.secret_key Этапа 3) только для расшифровки; "" — нет.
func NewKeyring(keys []Key, legacy string) (*Box, error) {
	if len(keys) == 0 {
		return nil, errors.New("secretbox: at least one key is required")
	}
	b := &Box{active: keys[0].ID, keys: make(map[string]cipher.AEAD, len(keys))}
	for _, k := range keys {
		if !kidRe.MatchString(k.ID) {
			return nil, errors.New("secretbox: key id must match [A-Za-z0-9_-]{1,16}")
		}
		if _, dup := b.keys[k.ID]; dup {
			return nil, errors.New("secretbox: duplicate key id " + k.ID)
		}
		if len(k.Secret) < MinSecretLen {
			return nil, errors.New("secretbox: key secret must be at least 32 bytes")
		}
		raw, err := hkdf.Key(sha256.New, []byte(k.Secret), nil, hkdfInfo, 32)
		if err != nil {
			return nil, err
		}
		aead, err := gcm(raw)
		if err != nil {
			return nil, err
		}
		b.keys[k.ID] = aead
	}
	if legacy != "" {
		if len(legacy) < MinSecretLen {
			return nil, errors.New("secretbox: legacy secret must be at least 32 bytes")
		}
		aead, err := legacyAEAD(legacy)
		if err != nil {
			return nil, err
		}
		b.legacy = aead
	}
	return b, nil
}

func legacyAEAD(secret string) (cipher.AEAD, error) {
	key := sha256.Sum256([]byte(secret))
	return gcm(key[:])
}

func gcm(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// ActiveKeyID — kid ключа, которым шифруются новые значения ("" — формат v1).
func (b *Box) ActiveKeyID() string { return b.active }

// Seal шифрует открытый текст без контекста.
func (b *Box) Seal(plain string) (string, error) { return b.SealWith(plain, "") }

// Open расшифровывает значение без контекста; чужой ключ или испорченные данные — ErrInvalid.
func (b *Box) Open(sealed string) (string, error) { return b.OpenWith(sealed, "") }

// SealWith шифрует текущим ключом; context (associated data) нужно передать и в OpenWith.
func (b *Box) SealWith(plain, context string) (string, error) {
	if b.active == "" {
		return seal(b.legacy, prefixV1, plain, nil)
	}
	return seal(b.keys[b.active], prefixV2+b.active+":", plain, []byte(context))
}

func seal(aead cipher.AEAD, prefix, plain string, ad []byte) (string, error) {
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	out := aead.Seal(nonce, nonce, []byte(plain), ad)
	return prefix + base64.StdEncoding.EncodeToString(out), nil
}

// OpenWith расшифровывает v2 (с тем же context, что при шифровании) или v1 (context не используется).
func (b *Box) OpenWith(sealed, context string) (string, error) {
	var (
		aead cipher.AEAD
		body string
		ad   []byte
	)
	switch {
	case strings.HasPrefix(sealed, prefixV2):
		kid, rest, ok := strings.Cut(sealed[len(prefixV2):], ":")
		if !ok {
			return "", ErrInvalid
		}
		aead, body, ad = b.keys[kid], rest, []byte(context)
	case strings.HasPrefix(sealed, prefixV1):
		aead, body = b.legacy, sealed[len(prefixV1):]
	}
	if aead == nil {
		return "", ErrInvalid
	}
	raw, err := base64.StdEncoding.DecodeString(body)
	n := aead.NonceSize()
	if err != nil || len(raw) < n {
		return "", ErrInvalid
	}
	plain, err := aead.Open(nil, raw[:n], raw[n:], ad)
	if err != nil {
		return "", ErrInvalid
	}
	return string(plain), nil
}

// IsSealed — значение в формате secretbox (v1/v2), а не открытый текст.
func IsSealed(s string) bool { return strings.HasPrefix(s, prefixV1) || strings.HasPrefix(s, prefixV2) }

// CurrentPrefix — начало значений, зашифрованных текущим ключом ("v2:<kid>:"; Box из New — "v1:").
func (b *Box) CurrentPrefix() string {
	if b.active == "" {
		return prefixV1
	}
	return prefixV2 + b.active + ":"
}

// Current — значение зашифровано текущим ключом связки (перешифровка не нужна).
func (b *Box) Current(sealed string) bool { return strings.HasPrefix(sealed, b.CurrentPrefix()) }

// Reseal — значение, зашифрованное текущим ключом: открытый текст (plaintextOK) шифруется, старый ключ —
// расшифровывается и шифруется заново. changed = false — уже текущий ключ (или открытый текст при
// !plaintextOK) — значение возвращается как есть.
func (b *Box) Reseal(value, context string, plaintextOK bool) (out string, changed bool, err error) {
	if b.Current(value) {
		return value, false, nil
	}
	plain := value
	if IsSealed(value) {
		if plain, err = b.OpenWith(value, context); err != nil {
			return "", false, err
		}
	} else if !plaintextOK {
		return value, false, nil
	}
	out, err = b.SealWith(plain, context)
	return out, err == nil, err
}
