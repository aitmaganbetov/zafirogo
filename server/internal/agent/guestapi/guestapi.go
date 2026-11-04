// Package guestapi — unary-сервисы агента ПК поверх готовых сервисов (proto/agent/v1): вход гостя
// (GuestAuthService), действия гостя (GuestService), магазин (ShopService), друзья и переводы (SocialService),
// а также AgentService.GetTranslations / VerifyStaffPin (встраиванием *stream.Service, который обслуживает
// стрим Connect) и хуки стрима (stream.WelcomeProvider, stream.EventHandler — Hooks).
//
// Идентичность ПК — только из сертификата mTLS (identity.FromContext, ставит server/internal/grpc). Гость —
// по токену гостя в метаданных `authorization: Bearer …` (Tokens, Valkey), привязанному к ПК и клубу.
// Каждая операция с БД — db.InTenantTx клуба ПК (RLS), клуб никогда не берётся из запроса.
//
// Источники (Laravel): routes/api.php (маршруты агента и гостя, throttle:*), контроллеры app/Http/Controllers/Api/*,
// сервисы AgentSignIns, QrLogins, Friends, AgentShop, GuestFeatures, GuestHistory, TranslationCatalog,
// ClubComputerSessions (resetGuestSession, startStaffVisit). Карта «маршрут → RPC» — docs/agent-protocol.md §10.
package guestapi

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/valkey-io/valkey-go"
	"google.golang.org/grpc"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/commands"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/identity"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/presence"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/stream"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/password"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/ratelimit"
	billingsvc "github.com/aitmaganbetov/zafirogo/server/internal/billing/service"
	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	grpcserver "github.com/aitmaganbetov/zafirogo/server/internal/grpc"
	"github.com/aitmaganbetov/zafirogo/server/internal/shop"
	"github.com/aitmaganbetov/zafirogo/server/internal/verifycode"
)

// Умолчания.
const (
	// DefaultTokenTTL — срок токена гостя (docs/agent-protocol.md §4: до 12 часов; в Laravel — 30 дней,
	// QrLogins::TOKEN_DAYS, AuthController::issueUserLoginResponse()).
	DefaultTokenTTL = 12 * time.Hour
	// DefaultKeyPrefix — префикс ключей Valkey (токены, идемпотентность, ожидание QR).
	DefaultKeyPrefix = "zafiro:guest:"
	// DefaultIdempotencyTTL — сколько хранится ответ по ключу идемпотентности (ApiIdempotency: 24 ч).
	DefaultIdempotencyTTL = 24 * time.Hour
	// DefaultQRBaseURL — сайт для ссылки QR-входа (route('pc-login.show', $code)).
	DefaultQRBaseURL = "https://zafiro.kz"
	// DefaultQRPollEvery — как часто WatchQrLogin перечитывает статус, если подсказка из стрима не пришла
	// (в старом — опрос агентом каждые 3 с, AgentQrLoginController::store poll_interval_seconds).
	DefaultQRPollEvery = 3 * time.Second
)

// Options — зависимости и настройки сервисов (подключает cmd/zafiro через server/internal/app).
type Options struct {
	// Pool — пул логина zafiro_app (тенантные транзакции, RLS).
	Pool db.Beginner
	// Valkey — токены гостя, идемпотентность, лимиты.
	Valkey valkey.Client
	// Passwords — Argon2id с проверкой старых bcrypt (A1); перехеш при входе.
	Passwords *password.Hasher
	// Limiter — лимиты throttle:* старого API (A1). nil — без лимитов (только тесты).
	Limiter *ratelimit.Limiter
	// Billing — покупки, завершение сессии, пакеты, пересадка (Этап 1).
	Billing *billingsvc.Service
	// Shop — магазин; nil — shop.New(Pool, Now).
	Shop *shop.Service
	// Stream — сервис стрима Connect (A3): AgentService встраивает его, присутствие ПК для «вошёл на другом ПК».
	Stream *stream.Service
	// Presence — присутствие ПК; nil — Stream.Presence().
	Presence *presence.Store
	// Notifier — команды агенту после коммита (BalanceChanged после заказа и перевода, QrLoginApproved,
	// SignOutGuest); nil — Stream.Commands(). Покупки времени уведомляет сам billing (SetNotifier).
	Notifier commands.SessionNotifier
	// Commands — отправка команд ПК (QrLoginApproved, SignOutGuest); nil — Stream.Commands().
	Commands *commands.Sender
	// Recovery — восстановление пароля кодом из Telegram (RequestPasswordResetCode, ResetPassword); nil — «не подключено».
	Recovery *verifycode.PasswordRecovery
	// TransferCodes — коды подтверждения перевода в Telegram (RequestTransferCode); nil — «не подключено».
	TransferCodes *verifycode.TransferCodes

	// KeyPrefix — префикс ключей Valkey (пусто — DefaultKeyPrefix).
	KeyPrefix string
	// TokenTTL — срок токена гостя (0 — DefaultTokenTTL).
	TokenTTL time.Duration
	// IdempotencyTTL — хранение ответов по ключу идемпотентности (0 — DefaultIdempotencyTTL).
	IdempotencyTTL time.Duration
	// QRBaseURL — сайт для ссылки QR-входа (пусто — DefaultQRBaseURL).
	QRBaseURL string
	// PublicBaseURL — адрес сайта для картинок (логотип клуба, картинка входа, новости); пусто — QRBaseURL.
	PublicBaseURL string
	// QRPollEvery — период перечитывания статуса в WatchQrLogin (0 — DefaultQRPollEvery).
	QRPollEvery time.Duration
	// AutoBillingWorkers — параллельность пересмотра автобиллинга в тике (0 — DefaultAutoBillingWorkers).
	AutoBillingWorkers int
	// AutoBillingTickBudget — сколько тик автобиллинга выбирает новые пакеты (0 — DefaultAutoBillingTickBudget).
	AutoBillingTickBudget time.Duration

	Now func() time.Time
	Log *slog.Logger
}

func (o *Options) defaults() {
	if o.KeyPrefix == "" {
		o.KeyPrefix = DefaultKeyPrefix
	}
	if o.TokenTTL <= 0 {
		o.TokenTTL = DefaultTokenTTL
	}
	if o.IdempotencyTTL <= 0 {
		o.IdempotencyTTL = DefaultIdempotencyTTL
	}
	if o.QRBaseURL == "" {
		o.QRBaseURL = DefaultQRBaseURL
	}
	if o.PublicBaseURL == "" {
		o.PublicBaseURL = o.QRBaseURL
	}
	if o.QRPollEvery <= 0 {
		o.QRPollEvery = DefaultQRPollEvery
	}
	if o.AutoBillingWorkers <= 0 {
		o.AutoBillingWorkers = DefaultAutoBillingWorkers
	}
	if o.AutoBillingTickBudget <= 0 {
		o.AutoBillingTickBudget = DefaultAutoBillingTickBudget
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.Stream != nil {
		if o.Presence == nil {
			o.Presence = o.Stream.Presence()
		}
		if o.Commands == nil {
			o.Commands = o.Stream.Commands()
		}
		if o.Notifier == nil {
			o.Notifier = o.Stream.Notifier()
		}
	}
	if o.Shop == nil && o.Pool != nil {
		o.Shop = shop.New(o.Pool, o.Now)
	}
}

// Server — все unary-сервисы агента и гостя.
type Server struct {
	o         Options
	tokens    *Tokens
	idem      *idempotency
	qrWaiters waiters
}

// New проверяет зависимости и собирает сервисы.
func New(o Options) (*Server, error) {
	o.defaults()
	if o.Pool == nil || o.Valkey == nil || o.Passwords == nil || o.Billing == nil {
		return nil, errors.New("guestapi: Pool, Valkey, Passwords and Billing are required")
	}
	return &Server{
		o:      o,
		tokens: NewTokens(o.Valkey, o.KeyPrefix, o.TokenTTL, o.Now),
		idem:   &idempotency{c: o.Valkey, prefix: o.KeyPrefix + "idem:", ttl: o.IdempotencyTTL},
	}, nil
}

// Tokens — хранилище токенов гостя (для хуков и тестов).
func (s *Server) Tokens() *Tokens { return s.tokens }

// Register регистрирует в gRPC-сервере AgentService (стрим Connect из Options.Stream + GetTranslations и
// VerifyStaffPin), GuestAuthService, GuestService, ShopService и SocialService. Вызывать из register-функции
// grpcserver.NewServer ВМЕСТО stream.Register (AgentService регистрируется здесь).
func Register(gs *grpc.Server, o Options) (*Server, error) {
	s, err := New(o)
	if err != nil {
		return nil, err
	}
	s.RegisterOn(gs)
	return s, nil
}

// RegisterOn регистрирует сервисы уже собранного Server.
func (s *Server) RegisterOn(gs *grpc.Server) {
	agentv1.RegisterAgentServiceServer(gs, &AgentService{Service: s.o.Stream, g: s})
	agentv1.RegisterGuestAuthServiceServer(gs, &authService{s: s})
	agentv1.RegisterGuestServiceServer(gs, &guestService{s: s})
	agentv1.RegisterShopServiceServer(gs, &shopService{s: s})
	agentv1.RegisterSocialServiceServer(gs, &socialService{s: s})
}

// ---------------------------------------------------------------------------
// Общее для всех RPC
// ---------------------------------------------------------------------------

// pc — ПК вызова (из сертификата). Без идентичности (перехватчик не пропустил бы) — UNAUTHENTICATED.
func pc(ctx context.Context) (identity.Identity, error) {
	id, ok := identity.FromContext(ctx)
	if !ok || id.ClubID <= 0 || id.ComputerID <= 0 {
		return identity.Identity{}, errNoCertificate
	}
	return id, nil
}

// call — начало обработки RPC: ПК, данные запроса для журнала (IP), лимит.
func (s *Server) call(ctx context.Context, rule string, subj func(*ratelimit.Subject)) (context.Context, identity.Identity, error) {
	id, err := pc(ctx)
	if err != nil {
		return ctx, id, err
	}
	ctx = withRequestMeta(ctx)
	if rule != "" {
		sub := ratelimit.Subject{IP: peerIP(ctx), ComputerID: id.ComputerID, ClubID: id.ClubID}
		if subj != nil {
			subj(&sub)
		}
		if err := s.limit(ctx, rule, sub); err != nil {
			return ctx, id, err
		}
	}
	return ctx, id, nil
}

// guestCall — RPC с токеном гостя: ПК, гость, лимит throttle:N,M (ключ — пользователь).
func (s *Server) guestCall(ctx context.Context, rule string) (context.Context, identity.Identity, Guest, error) {
	id, err := pc(ctx)
	if err != nil {
		return ctx, id, Guest{}, err
	}
	ctx = withRequestMeta(ctx)
	g, err := s.tokens.FromContext(ctx, id)
	if err != nil {
		return ctx, id, Guest{}, err
	}
	if rule != "" {
		if err := s.limit(ctx, rule, ratelimit.Subject{IP: peerIP(ctx), UserID: g.UserID, ComputerID: id.ComputerID, ClubID: id.ClubID}); err != nil {
			return ctx, id, g, err
		}
	}
	return ctx, id, g, nil
}

func (s *Server) limit(ctx context.Context, rule string, sub ratelimit.Subject) error {
	if s.o.Limiter == nil {
		return nil
	}
	res, err := s.o.Limiter.Allow(ctx, rule, sub)
	if err != nil {
		s.o.Log.ErrorContext(ctx, "guest rate limit unavailable", "rule", rule, "err", err)
		return grpcserver.Error(unavailable, agentv1.ErrorCode_ERROR_CODE_INTERNAL, msgInternal, nil)
	}
	if !res.Allowed {
		return grpcserver.RateLimited(res.RetryAfter, ratelimit.TooManyAttemptsMessage)
	}
	return nil
}

// fail — ошибка сервиса → статус gRPC; неизвестные ошибки — в лог и INTERNAL без подробностей.
func (s *Server) fail(ctx context.Context, method string, err error) error {
	known, st := toStatus(err)
	if !known {
		s.o.Log.ErrorContext(ctx, "guest rpc failed", "method", method, "err", err)
	}
	return st
}
