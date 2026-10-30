// Package wsticket — одноразовые билеты для подключения браузера к /ws (решение EV-3: access-токен SPA живёт
// в памяти, а браузерный WebSocket не умеет ставить Authorization).
//
// POST /api/ws-ticket (с Bearer) → билет на TTL (30 с); GET /ws?ticket=… — билет погашается (GETDEL) и
// превращается обратно в access-токен, по которому строится Principal (как для REST: блокировка и права
// проверяются заново). В Valkey лежит не сам токен, а шифротекст AES-GCM с ключом из билета: без билета
// (он есть только у браузера) значение бесполезно — правило «секреты не в кэше». Ключ записи — хеш билета.
package wsticket

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"time"

	"github.com/valkey-io/valkey-go"
)

// TTL — срок билета по умолчанию.
const TTL = 30 * time.Second

// ErrInvalid — билета нет, он истёк или уже использован.
var ErrInvalid = errors.New("wsticket: invalid or used ticket")

// Store — билеты в Valkey.
type Store struct {
	c      valkey.Client
	prefix string
	ttl    time.Duration
}

// New — хранилище; prefix "" — "ws:ticket:", ttl 0 — TTL.
func New(c valkey.Client, prefix string, ttl time.Duration) *Store {
	if prefix == "" {
		prefix = "ws:ticket:"
	}
	if ttl <= 0 {
		ttl = TTL
	}
	return &Store{c: c, prefix: prefix, ttl: ttl}
}

// TTL — срок билета.
func (s *Store) TTL() time.Duration { return s.ttl }

func derive(label, ticket string) []byte {
	sum := sha256.Sum256([]byte(label + "\x00" + ticket))
	return sum[:]
}

func (s *Store) key(ticket string) string { return s.prefix + hex.EncodeToString(derive("id", ticket)) }

// Issue — новый билет для access-токена.
func (s *Store) Issue(ctx context.Context, access string) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	ticket := base64.RawURLEncoding.EncodeToString(raw)
	block, err := aes.NewCipher(derive("enc", ticket))
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nonce, nonce, []byte(access), nil)
	cmd := s.c.B().Set().Key(s.key(ticket)).Value(base64.RawStdEncoding.EncodeToString(sealed)).Nx().
		Px(s.ttl).Build()
	if err := s.c.Do(ctx, cmd).Error(); err != nil {
		return "", err
	}
	return ticket, nil
}

// Redeem — погасить билет (одноразово) и вернуть access-токен.
func (s *Store) Redeem(ctx context.Context, ticket string) (string, error) {
	if len(ticket) != 43 {
		return "", ErrInvalid
	}
	v, err := s.c.Do(ctx, s.c.B().Getdel().Key(s.key(ticket)).Build()).ToString()
	if valkey.IsValkeyNil(err) {
		return "", ErrInvalid
	}
	if err != nil {
		return "", err
	}
	sealed, err := base64.RawStdEncoding.DecodeString(v)
	if err != nil {
		return "", ErrInvalid
	}
	block, err := aes.NewCipher(derive("enc", ticket))
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(sealed) < gcm.NonceSize() {
		return "", ErrInvalid
	}
	plain, err := gcm.Open(nil, sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():], nil)
	if err != nil {
		return "", ErrInvalid
	}
	return string(plain), nil
}
