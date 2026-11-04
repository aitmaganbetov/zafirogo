package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/valkey-io/valkey-go"
)

// ZNonceTTL — срок одноразового nonce печати Z-отчёта (Q-015: «Valkey, TTL ~1 час»). В старом коде nonce жил в
// сессии сотрудника (session()->put), пока сессия жива.
const ZNonceTTL = time.Hour

// ZNonceKey — чей nonce: смена клуба и сотрудник, которому показан Z-отчёт (в старом — сессия этого сотрудника,
// ключ "z_report_nonce.{shift}").
type ZNonceKey struct {
	ClubID  int64
	ShiftID int64
	UserID  int64
}

// ZNonceStore — хранилище одноразовых nonce печати Z-отчёта (решение Q-015, CSQ-3: nonce — в Valkey, флаг печати —
// у смены в БД). Хранится только sha256 nonce (сам nonce есть только у браузера).
type ZNonceStore interface {
	// Put запоминает хеш нового nonce; прежний nonce того же ключа перестаёт действовать.
	Put(ctx context.Context, key ZNonceKey, hash []byte) error
	// Take забирает хеш (одноразово, как session()->pull); нет — nil без ошибки.
	Take(ctx context.Context, key ZNonceKey) ([]byte, error)
}

// ValkeyZNonces — ZNonceStore в Valkey: SET с TTL и GETDEL (атомарно одноразово на всех инстансах).
type ValkeyZNonces struct {
	c      valkey.Client
	prefix string
	ttl    time.Duration
}

// NewValkeyZNonces — хранилище в Valkey; prefix "" — "zafiro:z-nonce:", ttl 0 — ZNonceTTL.
func NewValkeyZNonces(c valkey.Client, prefix string, ttl time.Duration) *ValkeyZNonces {
	if prefix == "" {
		prefix = "zafiro:z-nonce:"
	}
	if ttl <= 0 {
		ttl = ZNonceTTL
	}
	return &ValkeyZNonces{c: c, prefix: prefix, ttl: ttl}
}

func (v *ValkeyZNonces) key(k ZNonceKey) string {
	return v.prefix + strconv.FormatInt(k.ClubID, 10) + ":" + strconv.FormatInt(k.ShiftID, 10) + ":" + strconv.FormatInt(k.UserID, 10)
}

// Put — SET key hex(hash) EX ttl.
func (v *ValkeyZNonces) Put(ctx context.Context, k ZNonceKey, hash []byte) error {
	cmd := v.c.B().Set().Key(v.key(k)).Value(hex.EncodeToString(hash)).Ex(v.ttl).Build()
	if err := v.c.Do(ctx, cmd).Error(); err != nil {
		return fmt.Errorf("cash: z nonce put: %w", err)
	}
	return nil
}

// Take — GETDEL key.
func (v *ValkeyZNonces) Take(ctx context.Context, k ZNonceKey) ([]byte, error) {
	s, err := v.c.Do(ctx, v.c.B().Getdel().Key(v.key(k)).Build()).ToString()
	if valkey.IsValkeyNil(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cash: z nonce take: %w", err)
	}
	h, err := hex.DecodeString(s)
	if err != nil || len(h) != sha256.Size {
		return nil, nil //nolint:nilerr // испорченное значение — как отсутствие nonce (419)
	}
	return h, nil
}

// MemoryZNonces — ZNonceStore в памяти процесса (тесты, один инстанс без Valkey). Срок — как у Valkey.
type MemoryZNonces struct {
	mu  sync.Mutex
	m   map[ZNonceKey]memNonce
	TTL time.Duration
	Now func() time.Time
}

type memNonce struct {
	hash    []byte
	expires time.Time
}

func (m *MemoryZNonces) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

// Put запоминает хеш.
func (m *MemoryZNonces) Put(_ context.Context, k ZNonceKey, hash []byte) error {
	ttl := m.TTL
	if ttl <= 0 {
		ttl = ZNonceTTL
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.m == nil {
		m.m = map[ZNonceKey]memNonce{}
	}
	m.m[k] = memNonce{hash: append([]byte(nil), hash...), expires: m.now().Add(ttl)}
	return nil
}

// Take забирает хеш.
func (m *MemoryZNonces) Take(_ context.Context, k ZNonceKey) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n, ok := m.m[k]
	delete(m.m, k)
	if !ok || !m.now().Before(n.expires) {
		return nil, nil
	}
	return n.hash, nil
}

var defaultZNonces = &MemoryZNonces{}

// zNonces — хранилище сервиса; без настройки — общее хранилище в памяти процесса (тесты, разработка без Valkey).
// В сборке приложения (app.go) задаётся Valkey: nonce должен работать на всех инстансах.
func (s *Service) zNonces() ZNonceStore {
	if s.ZNonces != nil {
		return s.ZNonces
	}
	return defaultZNonces
}
