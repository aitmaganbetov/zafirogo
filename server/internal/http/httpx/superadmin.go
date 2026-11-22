package httpx

import (
	"context"
	"crypto/ed25519"
	"net/http"
	"time"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
	"github.com/aitmaganbetov/zafirogo/server/internal/secretbox"
	"github.com/aitmaganbetov/zafirogo/server/internal/telegram"
)

// SuperadminDeps — зависимости кабинета суперадмина (Этап 3, группа 6; раздел superadmin).
type SuperadminDeps struct {
	// Domain — хост кабинета (SUPERADMIN_DOMAIN, Q-069); пусто — кабинет по /superadmin на любом хосте.
	Domain string
	// PublicURL — адрес сайта из конфигурации (http.public_url), без «/» в конце; пусто — не задан. Ссылки, которые
	// уходят за пределы ответа на запрос (оповещения Telegram), строятся только из конфигурации, не из заголовка Host.
	PublicURL string
	// BackupPath, SSLHosts — экран «Сервер» (BACKUP_PATH, MONITORING_SSL_HOSTS).
	BackupPath string
	SSLHosts   []string
	// LedgerReconcileInterval — период фоновой сверки с журналом операций (workers.ledger_reconcile.interval): экран
	// «Сервер» считает сверку остановившейся, если успешной не было три периода. 0 — час.
	LedgerReconcileInterval time.Duration
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

// CabinetURL — адрес страницы кабинета суперадмина (route('superadmin.…')): на хосте кабинета (SUPERADMIN_DOMAIN) —
// path с корня, иначе <адрес сайта>/superadmin + path. Схема хоста кабинета — https (панель работает только по
// HTTPS: cookie Secure). Ни домен кабинета, ни адрес сайта не заданы — пустая строка: адрес не строится из
// заголовка Host запроса (его задаёт клиент), ссылка просто не отправляется.
func (s SuperadminDeps) CabinetURL(path string) string {
	switch {
	case s.Domain != "":
		return "https://" + s.Domain + path
	case s.PublicURL != "":
		return s.PublicURL + "/superadmin" + path
	}
	return ""
}
