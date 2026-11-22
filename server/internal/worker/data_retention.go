package worker

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aitmaganbetov/zafirogo/server/internal/store"
	"github.com/aitmaganbetov/zafirogo/server/internal/worker/leader"
)

// DataRetentionJob — имя задачи и ключ advisory lock.
const DataRetentionJob = "data:retention"

// DataRetentionInterval — Schedule::command('integrations:prune')->hourly() (Laravel: routes/console.php). Суточные
// очистки старого (session-purchases:prune, agent-qr-logins:prune) идут тем же часовым тиком: строка удаляется не
// раньше своего срока, только позже не на сутки, а на час.
const DataRetentionInterval = time.Hour

// DataRetentionBatch — строк в одной пачке: PruneIntegrationData::prune() — limit(1000).
const DataRetentionBatch = 1000

// Сроки хранения — как в старом коде и решениях владельца (у каждого — ссылка в комментарии запроса,
// db/queries/retention.sql).
const (
	// IdempotencyKeyRetention — integrations.idempotency_retention_hours (24 ч), если IdempotencyKeys не задан.
	IdempotencyKeyRetention = 24 * time.Hour
	// WebhookDeliveryRetention — integrations.webhook_delivery_retention_days (30 дней): финальные доставки.
	WebhookDeliveryRetention = 30 * 24 * time.Hour
	// SessionPurchaseRetention — session-purchases:prune: now()->subDays(8).
	SessionPurchaseRetention = 8 * 24 * time.Hour
	// QrLoginRetention — agent-qr-logins:prune: expires_at < now()->subDay().
	QrLoginRetention = 24 * time.Hour
	// EnrollmentCodeRetention — решение A2 §8: коды регистрации ПК старше 30 дней.
	EnrollmentCodeRetention = 30 * 24 * time.Hour
	// CertificateRetention — решение A2 §8: сертификаты, истёкшие больше 90 дней назад.
	CertificateRetention = 90 * 24 * time.Hour
)

// DataRetention — построчная очистка устаревших строк по всем клубам пачками (каждая пачка — своя короткая
// транзакция, длинных блокировок нет). Переносит из старого: integrations:prune (api_idempotency_keys,
// webhook_deliveries; api_request_logs удаляет LogRetention партициями), session-purchases:prune (части сессий;
// сгорание пакетов — BurnPackages), agent-qr-logins:prune; и решение A2 §8 (коды регистрации и сертификаты агентов).
// Не трогает: ledger_*, audit_log (только добавление), ledger_idempotency и billing_requests (L-6: 13 месяцев,
// нужна функция владельца схемы — docs/backlog.md), mobile_api_tokens (до конца поклубного перехода, docs/backlog.md).
// Один исполнитель на кластер (Q-086); повтор тика на другом инстансе безопасен — удаляются только строки старше срока.
type DataRetention struct {
	// Platform — пул zafiro_platform (все клубы, advisory lock).
	Platform *pgxpool.Pool
	// Interval — период (0 — DataRetentionInterval).
	Interval time.Duration
	// Batch — строк в пачке (0 — DataRetentionBatch).
	Batch int32
	// IdempotencyKeys — срок api_idempotency_keys (integrations.idempotency_retention; 0 — IdempotencyKeyRetention).
	// Должен совпадать со сроком, который проверяет запрос с ключом, иначе повтор в окне получит новый ответ.
	IdempotencyKeys time.Duration
	// Now — часы (nil — time.Now).
	Now func() time.Time
	// Log — журнал (nil — slog.Default()).
	Log *slog.Logger
}

// retentionRule — одна очистка: таблица и удаление одной пачки строк старше now − keep.
type retentionRule struct {
	table string
	keep  time.Duration
	prune func(ctx context.Context, q *store.Queries, now, before time.Time, batch int32) (int64, error)
}

func (w *DataRetention) rules() []retentionRule {
	return []retentionRule{
		{"api_idempotency_keys", intervalOr(w.IdempotencyKeys, IdempotencyKeyRetention),
			func(ctx context.Context, q *store.Queries, _, before time.Time, batch int32) (int64, error) {
				return q.RetentionDeleteApiIdempotencyKeys(ctx, store.RetentionDeleteApiIdempotencyKeysParams{Before: before, Batch: batch})
			}},
		{"webhook_deliveries", WebhookDeliveryRetention,
			func(ctx context.Context, q *store.Queries, _, before time.Time, batch int32) (int64, error) {
				return q.RetentionDeleteWebhookDeliveries(ctx, store.RetentionDeleteWebhookDeliveriesParams{Before: before, Batch: batch})
			}},
		{"session_purchases", SessionPurchaseRetention,
			func(ctx context.Context, q *store.Queries, _, before time.Time, batch int32) (int64, error) {
				return q.RetentionDeleteSessionPurchases(ctx, store.RetentionDeleteSessionPurchasesParams{Before: before, Batch: batch})
			}},
		{"agent_qr_logins", QrLoginRetention,
			func(ctx context.Context, q *store.Queries, _, before time.Time, batch int32) (int64, error) {
				return q.RetentionDeleteAgentQrLogins(ctx, store.RetentionDeleteAgentQrLoginsParams{Before: before, Batch: batch})
			}},
		{"agent_enrollment_codes", EnrollmentCodeRetention,
			func(ctx context.Context, q *store.Queries, now, before time.Time, batch int32) (int64, error) {
				return q.RetentionDeleteEnrollmentCodes(ctx, store.RetentionDeleteEnrollmentCodesParams{Before: before, Now: now, Batch: batch})
			}},
		{"agent_certificates", CertificateRetention,
			func(ctx context.Context, q *store.Queries, _, before time.Time, batch int32) (int64, error) {
				return q.RetentionDeleteAgentCertificates(ctx, store.RetentionDeleteAgentCertificatesParams{Before: before, Batch: batch})
			}},
	}
}

// Run выполняет тики до отмены ctx (первый — сразу).
func (w *DataRetention) Run(ctx context.Context) error {
	return loop{
		job: DataRetentionJob, failed: "data retention tick failed", log: w.Log,
		interval: intervalOr(w.Interval, DataRetentionInterval),
	}.run(ctx, func(ctx context.Context) error { _, _, err := w.Tick(ctx); return err })
}

// Tick — один запуск: ran = false, если лок держит другой исполнитель; deleted — сколько строк удалено по таблицам.
// Ошибка одной таблицы не останавливает остальные (возвращается первая).
func (w *DataRetention) Tick(ctx context.Context) (ran bool, deleted map[string]int64, err error) {
	now := time.Now()
	if w.Now != nil {
		now = w.Now()
	}
	batch := w.Batch
	if batch <= 0 {
		batch = DataRetentionBatch
	}
	deleted = map[string]int64{}
	ran, err = leader.RunOnce(ctx, w.Platform, DataRetentionJob, func(ctx context.Context, conn *pgxpool.Conn) error {
		q := store.New(conn) // без транзакции: каждая пачка фиксируется сама
		var first error
		for _, r := range w.rules() {
			n, err := pruneBatches(ctx, q, r, now, batch)
			deleted[r.table] = n
			if n > 0 {
				logOr(w.Log).Info("expired rows deleted", "job", DataRetentionJob, "table", r.table, "count", n)
			}
			if err != nil && first == nil {
				first = fmt.Errorf("%s: %w", r.table, err)
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
		}
		return first
	})
	return ran, deleted, err
}

// pruneBatches удаляет пачки, пока пачка полная (do … while ($ids->count() === 1000), PruneIntegrationData::prune()).
func pruneBatches(ctx context.Context, q *store.Queries, r retentionRule, now time.Time, batch int32) (int64, error) {
	before := now.Add(-r.keep)
	var total int64
	for {
		n, err := r.prune(ctx, q, now, before, batch)
		total += n
		if err != nil || n < int64(batch) {
			return total, err
		}
	}
}
