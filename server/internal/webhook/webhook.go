// Package webhook — исходящие вебхуки интеграций клуба.
//
// Laravel: app/Services/Integrations/WebhookDispatcher.php (постановка), app/Jobs/DeliverWebhook.php
// (доставка), app/Services/Integrations/WebhookUrlGuard.php (SSRF), config/integrations.php (параметры).
//
// Путь события:
//
//	действие + outbox.Add (одна транзакция) → публикатор outbox → JetStream (zafiro-events)
//	→ Consumer (durable consumer "webhooks", только типы-вебхуки) → строки webhook_deliveries (pending)
//	→ Deliverer (N горутин на инстанс, SKIP LOCKED + аренда) → POST на endpoint, подпись HMAC, ретраи.
//
// Отличие от старого: строка доставки появляется не в транзакции действия, а при обработке события из
// JetStream (через миллисекунды после COMMIT). Набор слушателей — на момент обработки, а не на момент
// COMMIT. Откат действия по-прежнему не создаёт доставок (события в outbox нет). id доставки (payload.id,
// X-Zafiro-Delivery) детерминирован: uuid v5 от (id события, id endpoint) — повторная обработка того же
// сообщения не создаёт вторую доставку (UNIQUE webhook_deliveries.event_id).
//
// Секрет endpoint'а хранится зашифрованным (secretbox, ключи security.data_keys, контекст SecretContext —
// как каст 'encrypted' в app/Models/WebhookEndpoint.php); Deliverer расшифровывает его перед подписью. Открытый
// секрет строки до миграции 0025 используется как есть, пока его не перешифрует воркер reencrypt-secrets.
// Секреты Laravel (Crypt, APP_KEY) инструмент переноса расшифровывает старым ключом и шифрует новым (Q-050).
package webhook

import (
	"strconv"
	"time"
)

// SecretContext — контекст шифрования (associated data) секрета вебхука: шифротекст привязан к клубу и не
// расшифруется, если его скопировать в endpoint другого клуба.
func SecretContext(clubID int64) string {
	return "webhook_endpoints.secret:club=" + strconv.FormatInt(clubID, 10)
}

// DefaultEvents — события, на которые можно подписать вебхук (config/integrations.php webhook_events).
var DefaultEvents = []string{
	"product.created",
	"product.updated",
	"stock.changed",
	"sale.completed",
	"computer.reservation_changed",
	"computer.controlled",
}

// DefaultBackoff — integrations.webhook_retry_backoff_seconds = [10, 60, 300, 1800, 7200]:
// до 6 попыток за ~2,6 ч, затем failed.
var DefaultBackoff = []time.Duration{
	10 * time.Second, 60 * time.Second, 300 * time.Second, 1800 * time.Second, 7200 * time.Second,
}

// Параметры по умолчанию (config/integrations.php, DeliverWebhook::send()).
const (
	DefaultTimeout        = 5 * time.Second // webhook_timeout_seconds
	DefaultConnectTimeout = 3 * time.Second // ->connectTimeout(3)
	DefaultDisableAfter   = 20              // webhook_disable_after_failures (подряд)
	DefaultSignatureTTL   = 300 * time.Second
)

// Статусы webhook_deliveries.status.
const (
	StatusPending   = "pending"
	StatusDelivered = "delivered"
	StatusFailed    = "failed"
	StatusSkipped   = "skipped"
)
