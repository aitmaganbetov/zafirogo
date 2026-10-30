// Package secretbox — шифрование секретов платформы в базе (токены Telegram в platform_settings): AES-256-GCM,
// ключ — SHA-256 от superadmin.secret_key. Аналог Crypt::encryptString()/decryptString() старого кода
// (app/Services/TelegramGateway.php, Monitoring/TelegramAlerts.php): секрет в базе только зашифрованным,
// расшифровать его без ключа сервера нельзя. Формат: "v1:" + base64(nonce ‖ ciphertext).
package secretbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
)

const prefix = "v1:"

// ErrInvalid — шифротекст испорчен или зашифрован другим ключом.
var ErrInvalid = errors.New("secretbox: cannot decrypt")

// Box — шифр с ключом сервера.
type Box struct{ aead cipher.AEAD }

// New — шифр из секрета конфигурации (любой длины; используется его SHA-256).
func New(secret string) (*Box, error) {
	if len(secret) < 32 {
		return nil, errors.New("secretbox: secret must be at least 32 bytes")
	}
	key := sha256.Sum256([]byte(secret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

// Seal шифрует открытый текст.
func (b *Box) Seal(plain string) (string, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	out := b.aead.Seal(nonce, nonce, []byte(plain), nil)
	return prefix + base64.StdEncoding.EncodeToString(out), nil
}

// Open расшифровывает; чужой ключ или испорченные данные — ErrInvalid.
func (b *Box) Open(sealed string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(sealed, prefix))
	if !strings.HasPrefix(sealed, prefix) || err != nil || len(raw) < b.aead.NonceSize() {
		return "", ErrInvalid
	}
	n := b.aead.NonceSize()
	plain, err := b.aead.Open(nil, raw[:n], raw[n:], nil)
	if err != nil {
		return "", ErrInvalid
	}
	return string(plain), nil
}
