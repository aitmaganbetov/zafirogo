package httpx

import (
	"context"
	"crypto/ed25519"
	"net/http"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
	"github.com/aitmaganbetov/zafirogo/server/internal/secretbox"
	"github.com/aitmaganbetov/zafirogo/server/internal/telegram"
)

// SuperadminDeps — зависимости кабинета суперадмина (Этап 3, группа 6; раздел superadmin).
type SuperadminDeps struct {
	// Domain — хост кабинета (SUPERADMIN_DOMAIN, Q-069); пусто — кабинет по /superadmin на любом хосте.
	Domain string
	// BackupPath, SSLHosts — экран «Сервер» (BACKUP_PATH, MONITORING_SSL_HOSTS).
	BackupPath string
	SSLHosts   []string
	// Secrets — шифрование токенов Telegram в platform_settings. nil — сохранить токен нельзя (503).
	Secrets *secretbox.Box
	// UpdateKeys — открытые ключи подписи обновлений агента (agent.update.public_keys): объявление версии с
	// автообновлением принимается только с проверенной подписью CI.
	UpdateKeys map[string]ed25519.PublicKey
	// BroadcastOnline — команда всем ПК на связи (по всем клубам), в фоне после коммита. nil — не рассылается.
	BroadcastOnline func(ctx context.Context, cmd *agentv1.Command)
	// TelegramGateway — проверка токена Telegram Gateway при сохранении (TelegramGateway::configure()). nil — 503.
	TelegramGateway *telegram.Gateway
	// TelegramAlerts — бот оповещений: проверка токена, поиск чата, проверочное сообщение. nil — 503.
	TelegramAlerts *telegram.Alerts
	// HTTPClient — загрузка манифеста подписи установщика агента (nil — клиент с таймаутом 15 с).
	HTTPClient *http.Client
}
