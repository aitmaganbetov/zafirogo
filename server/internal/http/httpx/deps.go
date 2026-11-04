// Package httpx — общая основа REST-разделов (api/<раздел>.yaml → sections/<раздел>): зависимости, монтирование
// strict-сервера раздела, права маршрутов Laravel, тенантная транзакция, идемпотентность, ошибки как в Laravel,
// пагинация. Разделы не импортируют друг друга и пакет httpserver — только httpx. Конвенции — api/README.md.
package httpx

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/enroll"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/presence"
	"github.com/aitmaganbetov/zafirogo/server/internal/apikey"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/password"
	billingsvc "github.com/aitmaganbetov/zafirogo/server/internal/billing/service"
	cashsvc "github.com/aitmaganbetov/zafirogo/server/internal/cash/service"
	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/wsticket"
	"github.com/aitmaganbetov/zafirogo/server/internal/i18n"
	"github.com/aitmaganbetov/zafirogo/server/internal/secretbox"
	"github.com/aitmaganbetov/zafirogo/server/internal/verifycode"
)

// Checker — проверка зависимости для /readyz (например, пинг Postgres).
type Checker interface {
	Name() string
	Check(ctx context.Context) error
}

// CheckFunc адаптирует функцию к Checker.
type CheckFunc struct {
	CheckName string
	Fn        func(ctx context.Context) error
}

// Name возвращает имя проверки.
func (c CheckFunc) Name() string { return c.CheckName }

// Check выполняет проверку.
func (c CheckFunc) Check(ctx context.Context) error { return c.Fn(ctx) }

// Presence — присутствие агентов клуба (*presence.Store). nil — все ПК «offline».
type Presence interface {
	List(ctx context.Context, clubID int64) ([]presence.State, error)
}

// AgentCommands — отправка команды агенту ПК клуба (agent/commands.Sender.Send).
type AgentCommands interface {
	Send(ctx context.Context, clubID, computerID int64, cmd *agentv1.Command) (string, error)
}

// Deps — зависимости разделов REST. Поле, нужное новому разделу, добавляется сюда (одна строка) и
// заполняется в httpserver.NewRouter из Options. nil-сервис — раздел отвечает 503 (ErrNotConfigured).
type Deps struct {
	Logger *slog.Logger
	// Auth — вход, сессии, Principal.
	Auth *auth.Service
	// Pool — пул логина zafiro_app (тенантные транзакции, RLS).
	Pool db.Beginner
	// PlatformPool — пул логина zafiro_platform (нетенантные таблицы: переводы, регистрация клуба).
	PlatformPool db.Beginner
	Billing      *billingsvc.Service
	Cash         *cashsvc.Service
	Presence     Presence
	// Checkers / CheckTimeout — /readyz.
	Checkers     []Checker
	CheckTimeout time.Duration
	// Passwords — хешер паролей Argon2id (регистрация, приглашения).
	Passwords *password.Hasher
	// WSTickets — одноразовые билеты WebSocket (POST /api/ws-ticket, решение EV-3).
	WSTickets *wsticket.Store
	// AgentCommands — команды агентам ПК (*commands.Sender): питание, техрежим, сообщение. nil — 503.
	AgentCommands AgentCommands
	// Enroll — подключение ПК: коды регистрации, ключи образа, ожидающие ПК (*enroll.Service). nil — 503.
	Enroll *enroll.Service
	// I18n — каталог переводов: тексты ответов API на языке клуба (i18n.go, решение F1-5). nil — русский текст.
	I18n *i18n.Catalog
	// SignOutGuest — отозвать токены гостя на ПК клуба и прислать ПК SignOutGuest с причиной (сброс пароля клиента,
	// UserController::resetPassword: $user->tokens()->delete()). nil — не отзываются.
	SignOutGuest func(ctx context.Context, clubID, userID int64, reason string)
	// ChatToGuest — ответ сотрудника в чате на ПК клуба, где вошёл гость (команда ChatMessageReceived; после
	// коммита). nil — не доставляется (агент получит его списком при открытии чата).
	ChatToGuest func(ctx context.Context, clubID, userID, messageID int64, author, body string, at time.Time)
	// ClubConfigChanged — команда ConfigChanged агентам ПК клуба в сети (настройки клуба изменены; после коммита,
	// не блокирует ответ). nil — агенты узнают изменения при следующем подключении.
	ClubConfigChanged func(ctx context.Context, clubID int64)
	// WebhookURLCheck — SSRF-проверка адреса вебхука (webhook.Guard.Check): "" — адрес годится, иначе причина
	// (https_required, private_address, port_not_allowed, host_not_resolved, invalid_url). nil — 503.
	WebhookURLCheck func(ctx context.Context, rawURL string) string
	// WakeWebhooks — разбудить отправку вебхуков (повтор доставки). nil — доставку возьмёт очередной опрос.
	WakeWebhooks func()
	// APILimits, APICounter — интеграционный API /api/v1 (раздел v1): лимиты config/integrations.php и счётчики окон
	// (Valkey; nil — в памяти процесса).
	APILimits  apikey.Limits
	APICounter apikey.Counter
	// Secrets — шифрование секретов в базе (security.data_keys, Q-050): секрет вебхука хранится зашифрованным
	// (webhook.SecretContext). nil — выдать секрет нельзя (503).
	Secrets *secretbox.Box
	// PasswordRecovery — «Забыли пароль» кодом из Telegram (раздел recovery). nil — «не подключено».
	PasswordRecovery *verifycode.PasswordRecovery
	// ApproveQrLogin — подтверждение входа на ПК по QR с телефона (guestapi.Server.ApproveQrLogin; ошибки — статусы
	// gRPC). nil — 503.
	ApproveQrLogin func(ctx context.Context, clubID int64, code string, userID int64) error
	// Superadmin — кабинет суперадмина: домен, секреты платформы, рассылка агентам (superadmin.go).
	Superadmin SuperadminDeps
	// Now — часы (nil — time.Now).
	Now func() time.Time
}

// Section — раздел REST: свой api/<name>.yaml, свой сгенерированный пакет, свой обработчик.
// Регистрация — одна строка в server/internal/http/sections.go.
type Section struct {
	// Name — имя раздела (= имя файла api/<name>.yaml).
	Name string
	// Mount вешает маршруты раздела на r (общий роутер с таймаутом запроса и middleware).
	Mount func(r chi.Router, d *Deps)
	// Limits — лимиты маршрутов раздела: "МЕТОД /путь" → правило ratelimit (как throttle: в routes/web.php).
	Limits map[string]string
}

// NowFunc — часы зависимостей.
func (d *Deps) NowFunc() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

// Log — логгер (slog.Default, если не задан).
func (d *Deps) Log() *slog.Logger {
	if d.Logger != nil {
		return d.Logger
	}
	return slog.Default()
}

// httpCtxKey — *http.Request и http.ResponseWriter для strict-обработчиков (cookie, заголовки).
type httpCtxKey struct{}

type httpPair struct {
	w http.ResponseWriter
	r *http.Request
}

// WithHTTPContext кладёт w и r в контекст (делает strict-middleware WithHTTP).
func WithHTTPContext(ctx context.Context, w http.ResponseWriter, r *http.Request) context.Context {
	return context.WithValue(ctx, httpCtxKey{}, httpPair{w: w, r: r})
}

// HTTPFrom — w и r запроса внутри strict-обработчика (nil вне HTTP).
func HTTPFrom(ctx context.Context) (http.ResponseWriter, *http.Request) {
	p, _ := ctx.Value(httpCtxKey{}).(httpPair)
	return p.w, p.r
}
