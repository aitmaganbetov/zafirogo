package integrations

// config/integrations.php старого проекта: права ключей, события вебхуков, лимиты.

// scope — право ключа API (integrations.scopes): label — ключ перевода, dangerous — подсветка в интерфейсе.
type scope struct {
	name, label string
	dangerous   bool
}

var scopes = []scope{
	{"products:read", "Чтение товаров и остатков", false},
	{"products:write", "Создание и изменение товаров", false},
	{"stock:write", "Оприходование и списание товаров", false},
	{"sales:read", "Чтение продаж и чеков", false},
	{"computers:read", "Чтение компьютеров и зон", false},
	{"bookings:write", "Бронирование компьютеров", false},
	{"computers:control", "Включение, перезагрузка и выключение компьютеров", true},
	{"tariffs:read", "Чтение тарифов", false},
	{"members:read", "Чтение клиентов клуба (персональные данные)", true},
}

// event — событие вебхука (integrations.webhook_events).
type event struct{ name, label string }

var events = []event{
	{"product.created", "Создан товар"},
	{"product.updated", "Изменён товар"},
	{"stock.changed", "Изменился остаток товара"},
	{"sale.completed", "Проведена продажа"},
	{"computer.reservation_changed", "Изменилась бронь компьютера"},
	{"computer.controlled", "Команда управления компьютером"},
}

const (
	// keyPrefix — integrations.key_prefix.
	keyPrefix = "zfk_"
	// defaultRateLimit, maxRateLimit — integrations.default_rate_limit_per_minute (API_KEY_RATE_LIMIT) и
	// max_rate_limit_per_minute (API_KEY_RATE_LIMIT_MAX). Конфигурации API v1 в Go пока нет (Этап 4) — значения
	// по умолчанию старого конфига.
	defaultRateLimit = 120
	maxRateLimit     = 1000
	// maxActiveKeys, maxWebhooks — IntegrationController::MAX_ACTIVE_KEYS, MAX_WEBHOOKS.
	maxActiveKeys = 50
	maxWebhooks   = 20
	// deliveriesPage — доставки на странице (в старом — 20 последних).
	deliveriesPage = 20
)

func knownScope(s string) bool {
	for _, sc := range scopes {
		if sc.name == s {
			return true
		}
	}
	return false
}

func knownEvent(s string) bool {
	for _, e := range events {
		if e.name == s {
			return true
		}
	}
	return false
}
