package enroll

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	grpcserver "github.com/aitmaganbetov/zafirogo/server/internal/grpc"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// CertCache — действующие сертификаты агентов в памяти инстанса (allow-list по серийному номеру).
//
// Почему allow-list, а не список отозванных: строка сертификата может исчезнуть (каскадное удаление
// клуба) — такой сертификат должен перестать действовать, а не стать «неизвестным, значит, не отозванным».
//
// Обновление: полная загрузка при старте и раз в FullReload (ловит удаления), инкрементально раз в
// Refresh по agent_certificates.changed_at с перекрытием Overlap (транзакция, начатая раньше, может
// закоммититься позже — её changed_at меньше курсора). Отзыв на своём инстансе действует сразу
// (MarkRevoked), на остальных — через Refresh или раньше, если вызывающий подключит NATS к MarkRevoked.
// Неизвестный серийный номер (выпущен другим инстансом после последнего Refresh) читается из базы.
//
// Запросы — платформенные (все клубы): пул с ролью zafiro_platform.
type CertCache struct {
	platform db.Beginner
	opts     CacheOptions

	mu        sync.RWMutex
	valid     map[string]certEntry
	revoked   map[string]time.Time // serial → not_after (держим до истечения)
	cursor    time.Time
	firstUse  func(ctx context.Context, clubID int64, serial string) error
	inFlight  map[string]bool
	callbacks []func(RevokedCert)
}

type certEntry struct {
	clubID, computerID int64
	notAfter           time.Time
	// pendingFirstUse — продлённый сертификат, ещё ни разу не использованный: при первом
	// использовании прежний отзывается (superseded).
	pendingFirstUse bool
}

// RevokedCert — событие отзыва для подписчиков (поток Connect этого ПК закрывается).
type RevokedCert struct {
	Serial             string
	ClubID, ComputerID int64
}

// CacheOptions — настройки кэша.
type CacheOptions struct {
	// Refresh — период инкрементального обновления (по умолчанию 5 с).
	Refresh time.Duration
	// FullReload — период полной перезагрузки (по умолчанию 10 мин).
	FullReload time.Duration
	// Overlap — перекрытие инкрементального окна (по умолчанию 2 мин).
	Overlap time.Duration
	// PageSize — строк за запрос (по умолчанию 5000).
	PageSize int32
	Logger   *slog.Logger
	Now      func() time.Time
}

// NewCertCache — кэш поверх платформенного пула.
func NewCertCache(platform db.Beginner, opts CacheOptions) *CertCache {
	if opts.Refresh <= 0 {
		opts.Refresh = 5 * time.Second
	}
	if opts.FullReload <= 0 {
		opts.FullReload = 10 * time.Minute
	}
	if opts.Overlap <= 0 {
		opts.Overlap = 2 * time.Minute
	}
	if opts.PageSize <= 0 {
		opts.PageSize = 5000
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &CertCache{platform: platform, opts: opts, valid: map[string]certEntry{},
		revoked: map[string]time.Time{}, inFlight: map[string]bool{}}
}

// OnRevoked подписывает fn на отзыв сертификатов (вызывается без блокировок кэша, синхронно).
func (c *CertCache) OnRevoked(fn func(RevokedCert)) {
	c.mu.Lock()
	c.callbacks = append(c.callbacks, fn)
	c.mu.Unlock()
}

func (c *CertCache) setFirstUse(fn func(ctx context.Context, clubID int64, serial string) error) {
	c.mu.Lock()
	c.firstUse = fn
	c.mu.Unlock()
}

// Run загружает кэш и обновляет его до отмены ctx.
func (c *CertCache) Run(ctx context.Context) error {
	if err := c.Load(ctx); err != nil {
		return err
	}
	refresh := time.NewTicker(c.opts.Refresh)
	defer refresh.Stop()
	full := time.NewTicker(c.opts.FullReload)
	defer full.Stop()
	for {
		var err error
		select {
		case <-ctx.Done():
			return nil
		case <-refresh.C:
			err = c.Refresh(ctx)
		case <-full.C:
			err = c.Load(ctx)
		}
		if err != nil && ctx.Err() == nil {
			c.opts.Logger.WarnContext(ctx, "agent certificate cache refresh failed", "error", err)
		}
	}
}

// Load — полная загрузка действующих сертификатов.
func (c *CertCache) Load(ctx context.Context) error {
	start := c.opts.Now()
	valid := map[string]certEntry{}
	after := ""
	for {
		var rows []store.ListValidAgentCertificatesRow
		err := db.InPlatformTx(ctx, c.platform, func(q *store.Queries) error {
			var err error
			rows, err = q.ListValidAgentCertificates(ctx, store.ListValidAgentCertificatesParams{
				Now: start, AfterSerial: after, PageSize: c.opts.PageSize})
			return err
		})
		if err != nil {
			return err
		}
		for _, r := range rows {
			valid[r.Serial] = certEntry{clubID: r.ClubID, computerID: r.ClubComputerID, notAfter: r.NotAfter,
				pendingFirstUse: r.ReplacesSerial != nil && r.FirstUsedAt == nil}
		}
		if len(rows) < int(c.opts.PageSize) {
			break
		}
		after = rows[len(rows)-1].Serial
	}
	var gone []RevokedCert
	c.mu.Lock()
	for serial, e := range c.valid {
		if _, ok := valid[serial]; !ok {
			if _, rev := c.revoked[serial]; !rev {
				gone = append(gone, RevokedCert{Serial: serial, ClubID: e.clubID, ComputerID: e.computerID})
			}
			c.revoked[serial] = e.notAfter
		}
	}
	for serial := range valid {
		if _, rev := c.revoked[serial]; rev {
			// Отзыв окончателен: снимок мог быть прочитан до коммита отзыва.
			delete(valid, serial)
		}
	}
	c.valid = valid
	c.cursor = start
	c.pruneLocked(start)
	callbacks := c.callbacks
	c.mu.Unlock()
	notify(callbacks, gone)
	return nil
}

// Refresh — изменения с прошлого раза (с перекрытием Overlap).
func (c *CertCache) Refresh(ctx context.Context) error {
	c.mu.RLock()
	since := c.cursor.Add(-c.opts.Overlap)
	c.mu.RUnlock()
	now := c.opts.Now()
	afterAt, afterSerial := since, ""
	var changes []store.ListAgentCertificateChangesRow
	for {
		var rows []store.ListAgentCertificateChangesRow
		err := db.InPlatformTx(ctx, c.platform, func(q *store.Queries) error {
			var err error
			rows, err = q.ListAgentCertificateChanges(ctx, store.ListAgentCertificateChangesParams{
				Since: afterAt, AfterSerial: afterSerial, PageSize: c.opts.PageSize})
			return err
		})
		if err != nil {
			return err
		}
		changes = append(changes, rows...)
		if len(rows) < int(c.opts.PageSize) {
			break
		}
		last := rows[len(rows)-1]
		afterAt, afterSerial = last.ChangedAt, last.Serial
	}
	var gone []RevokedCert
	c.mu.Lock()
	for _, r := range changes {
		gone = append(gone, c.applyLocked(r.Serial, r.ClubID, r.ClubComputerID, r.NotAfter, r.RevokedAt,
			r.ReplacesSerial != nil && r.FirstUsedAt == nil, now)...)
	}
	c.cursor = now
	c.pruneLocked(now)
	callbacks := c.callbacks
	c.mu.Unlock()
	notify(callbacks, gone)
	return nil
}

func (c *CertCache) applyLocked(serial string, clubID, computerID int64, notAfter time.Time, revokedAt *time.Time,
	pending bool, now time.Time) []RevokedCert {
	if revokedAt != nil || !notAfter.After(now) {
		_, wasValid := c.valid[serial]
		_, wasRevoked := c.revoked[serial]
		delete(c.valid, serial)
		c.revoked[serial] = notAfter
		if wasValid && !wasRevoked && revokedAt != nil {
			return []RevokedCert{{Serial: serial, ClubID: clubID, ComputerID: computerID}}
		}
		return nil
	}
	if _, rev := c.revoked[serial]; rev {
		// Отзыв окончателен: строка без revoked_at могла прийти из старого снимка.
		return nil
	}
	c.valid[serial] = certEntry{clubID: clubID, computerID: computerID, notAfter: notAfter, pendingFirstUse: pending}
	return nil
}

func (c *CertCache) pruneLocked(now time.Time) {
	for serial, notAfter := range c.revoked {
		if notAfter.Before(now.Add(-time.Hour)) {
			delete(c.revoked, serial)
		}
	}
	for serial, e := range c.valid {
		if e.notAfter.Before(now) {
			delete(c.valid, serial)
		}
	}
}

// Add — сертификат, выпущенный этим инстансом (действует сразу, без ожидания Refresh).
func (c *CertCache) Add(serial string, clubID, computerID int64, notAfter time.Time, pendingFirstUse bool) {
	c.mu.Lock()
	if _, rev := c.revoked[serial]; !rev {
		c.valid[serial] = certEntry{clubID: clubID, computerID: computerID, notAfter: notAfter, pendingFirstUse: pendingFirstUse}
	}
	c.mu.Unlock()
}

// MarkRevoked — отзыв (после коммита на этом инстансе или из события NATS другого инстанса).
func (c *CertCache) MarkRevoked(serial string) {
	c.mu.Lock()
	e, wasValid := c.valid[serial]
	_, wasRevoked := c.revoked[serial]
	delete(c.valid, serial)
	notAfter := e.notAfter
	if !wasValid {
		notAfter = c.opts.Now().Add(400 * 24 * time.Hour) // срок неизвестен: держим до полной перезагрузки
	}
	c.revoked[serial] = notAfter
	callbacks := c.callbacks
	c.mu.Unlock()
	if wasValid && !wasRevoked {
		notify(callbacks, []RevokedCert{{Serial: serial, ClubID: e.clubID, ComputerID: e.computerID}})
	}
}

func notify(callbacks []func(RevokedCert), events []RevokedCert) {
	for _, ev := range events {
		for _, fn := range callbacks {
			fn(ev)
		}
	}
}

// CheckCertificate — grpcserver.CertChecker.
func (c *CertCache) CheckCertificate(ctx context.Context, serial string, clubID, computerID int64, notAfter time.Time) error {
	c.mu.RLock()
	e, ok := c.valid[serial]
	_, revoked := c.revoked[serial]
	c.mu.RUnlock()
	if revoked {
		return grpcserver.ErrCertRevoked
	}
	if !ok {
		var err error
		if e, err = c.lookup(ctx, serial); err != nil {
			return err
		}
	}
	if e.clubID != clubID || e.computerID != computerID {
		return grpcserver.ErrCertMismatch
	}
	if e.pendingFirstUse {
		c.triggerFirstUse(serial, clubID)
	}
	return nil
}

func (c *CertCache) lookup(ctx context.Context, serial string) (certEntry, error) {
	var row store.GetAgentCertificateAnyClubRow
	err := db.InPlatformTx(ctx, c.platform, func(q *store.Queries) error {
		var err error
		row, err = q.GetAgentCertificateAnyClub(ctx, serial)
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return certEntry{}, grpcserver.ErrCertRevoked
	}
	if err != nil {
		return certEntry{}, err
	}
	now := c.opts.Now()
	c.mu.Lock()
	c.applyLocked(row.Serial, row.ClubID, row.ClubComputerID, row.NotAfter, row.RevokedAt,
		row.ReplacesSerial != nil && row.FirstUsedAt == nil, now)
	e, ok := c.valid[serial]
	c.mu.Unlock()
	if !ok {
		return certEntry{}, grpcserver.ErrCertRevoked
	}
	return e, nil
}

// triggerFirstUse — отметка первого использования продлённого сертификата в фоне (не задерживает RPC).
func (c *CertCache) triggerFirstUse(serial string, clubID int64) {
	c.mu.Lock()
	fn := c.firstUse
	if fn == nil || c.inFlight[serial] {
		c.mu.Unlock()
		return
	}
	c.inFlight[serial] = true
	c.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		err := fn(ctx, clubID, serial)
		c.mu.Lock()
		delete(c.inFlight, serial)
		if e, ok := c.valid[serial]; ok && err == nil {
			e.pendingFirstUse = false
			c.valid[serial] = e
		}
		c.mu.Unlock()
	}()
}

// Len — число действующих сертификатов в кэше (метрики, тесты).
func (c *CertCache) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.valid)
}
