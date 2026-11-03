package app

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aitmaganbetov/zafirogo/server/internal/agent/presence"
	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/observability"
)

// newPool — пул pgx с трассировщиком наблюдаемости (метрики запросов, события ledger/команд/вебхуков/задач,
// спаны транзакций). То же, что db.NewPool (разбор DSN, пул, пинг 5 с), плюс ConnConfig.Tracer и BeforeClose.
// tracer nil — как db.NewPool.
func newPool(ctx context.Context, dsn string, tracer *observability.PgxTracer) (*pgxpool.Pool, error) {
	cfg, err := db.ParsePoolConfig(dsn) // размер пула по умолчанию — db.DefaultMaxConns
	if err != nil {
		return nil, err
	}
	if tracer != nil {
		cfg.ConnConfig.Tracer = tracer
		cfg.BeforeClose = tracer.Forget
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("db: open pool: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("db: ping: %w", err)
	}
	return pool, nil
}

// onlineComputers — число ПК на связи по всей платформе одной командой ZCOUNT по zset «online» присутствия
// (ключ <agent.stream.key_prefix>online, presence.Store.onlineKey; срок члена — score в мс). Агрегат без разбивки
// по клубам: метка club_id в Prometheus запрещена (docs/observability.md §1).
func onlineComputers(st *presence.Store) func(ctx context.Context) (int64, error) {
	return func(ctx context.Context) (int64, error) {
		c := st.Client()
		now := strconv.FormatInt(time.Now().UnixMilli(), 10)
		return c.Do(ctx, c.B().Zcount().Key(st.Prefix()+"online").Min("("+now).Max("+inf").Build()).AsInt64()
	}
}

// setObservabilitySources — источники показателей, читаемых при сборе /metrics.
func (a *App) setObservabilitySources(serverCert tls.Certificate) {
	certs := map[string]*x509.Certificate{}
	if a.CA != nil {
		certs["agent_ca"] = a.CA.Certificate()
	}
	leaf := serverCert.Leaf
	if leaf == nil && len(serverCert.Certificate) > 0 {
		leaf, _ = x509.ParseCertificate(serverCert.Certificate[0])
	}
	if leaf != nil {
		certs["agent_server"] = leaf
	}
	a.Obs.SetSources(observability.Sources{
		Pools:           map[string]*pgxpool.Pool{"app": a.AppPool, "platform": a.PlatformPool},
		Platform:        a.PlatformPool,
		LocalStreams:    a.Stream.LocalStreams,
		OnlineComputers: onlineComputers(a.Stream.Presence()),
		NATS:            a.NATS,
		Certificates:    certs,
	})
}
