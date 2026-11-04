package session

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Access-токен — подписанный HMAC-SHA256 (не JWT, без выбора алгоритма в заголовке):
//
//	<kid>.<base64url(JSON {sid, uid, iat, exp})>.<base64url(HMAC-SHA256(key[kid], "zafiro.access.v1." + kid + "." + payload))>
//
// Почему HMAC, а не Ed25519: токен выпускает и проверяет один и тот же сервис (все инстансы делят ключ
// из конфигурации), сторонних проверяющих нет — асимметрия ничего не даёт, а проверка HMAC в десятки раз
// быстрее (≈1 мкс против ≈50 мкс у Ed25519) на каждом запросе админки. Ротация ключей — по kid:
// первый ключ подписывает, все перечисленные проверяют.
//
// Токен короткий (AccessTTL ≈ 10 мин) и всё равно проверяется по сессии в Valkey на каждом запросе:
// выход, отзыв всех сессий и блокировка действуют сразу, а не по истечении токена.

// Key — ключ подписи access-токенов.
type Key struct {
	// ID — короткий идентификатор ключа (kid), [A-Za-z0-9_-]{1,16}.
	ID string
	// Secret — не меньше 32 случайных байт. Секрет: не логировать.
	Secret []byte
}

// claims — содержимое access-токена.
type claims struct {
	SID string `json:"sid"`
	UID int64  `json:"uid"`
	IAT int64  `json:"iat"`
	EXP int64  `json:"exp"`
}

// ErrInvalidToken — токен испорчен, подпись неверна или срок истёк.
var ErrInvalidToken = errors.New("session: invalid token")

const tokenContext = "zafiro.access.v1." // #nosec G101 -- метка домена подписи, не секрет

type signer struct {
	keys []Key
}

func newSigner(keys []Key) (*signer, error) {
	if len(keys) == 0 {
		return nil, errors.New("session: at least one signing key is required")
	}
	seen := map[string]bool{}
	for _, k := range keys {
		if len(k.Secret) < 32 {
			return nil, fmt.Errorf("session: key %q: secret must be at least 32 bytes", k.ID)
		}
		if k.ID == "" || len(k.ID) > 16 || strings.Trim(k.ID, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-") != "" {
			return nil, fmt.Errorf("session: invalid key id %q", k.ID)
		}
		if seen[k.ID] {
			return nil, fmt.Errorf("session: duplicate key id %q", k.ID)
		}
		seen[k.ID] = true
	}
	return &signer{keys: keys}, nil
}

func mac(secret []byte, kid, payload string) []byte {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(tokenContext + kid + "." + payload))
	return m.Sum(nil)
}

func (s *signer) sign(c claims) (string, error) {
	body, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	k := s.keys[0]
	payload := base64.RawURLEncoding.EncodeToString(body)
	return k.ID + "." + payload + "." + base64.RawURLEncoding.EncodeToString(mac(k.Secret, k.ID, payload)), nil
}

func (s *signer) verify(token string, now time.Time) (claims, error) {
	if len(token) > 1024 {
		return claims{}, ErrInvalidToken
	}
	kid, rest, ok := strings.Cut(token, ".")
	if !ok {
		return claims{}, ErrInvalidToken
	}
	payload, sig, ok := strings.Cut(rest, ".")
	if !ok || strings.Contains(sig, ".") {
		return claims{}, ErrInvalidToken
	}
	got, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil {
		return claims{}, ErrInvalidToken
	}
	var key *Key
	for i := range s.keys {
		if s.keys[i].ID == kid {
			key = &s.keys[i]
			break
		}
	}
	if key == nil || !hmac.Equal(got, mac(key.Secret, kid, payload)) {
		return claims{}, ErrInvalidToken
	}
	body, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return claims{}, ErrInvalidToken
	}
	var c claims
	if err := json.Unmarshal(body, &c); err != nil || c.SID == "" || c.UID <= 0 {
		return claims{}, ErrInvalidToken
	}
	if now.Unix() >= c.EXP {
		return claims{}, ErrInvalidToken
	}
	return c, nil
}
