package enroll

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/aitmaganbetov/zafirogo/server/internal/auth/principal"
	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/pki"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// RegisterRoles — роли, которые подключают ПК к клубу.
// Laravel: app/Policies/ClubComputerPolicy.php register() → ClubComputerSessions::STAFF_ROLES
// (app/Services/ClubComputerSessions.php стр. 28): owner, admin, manager, operator. Кассир — нет.
// Это не право матрицы ClubPermissions, а проверка роли (BR-124); см. open-questions/stage2-agent-enroll.md.
var RegisterRoles = []string{"owner", "admin", "manager", "operator"}

// Значения по умолчанию.
const (
	DefaultCodeTTL = 15 * time.Minute
	// Лимиты Enroll — как RateLimiter agent-enroll (app/Providers/AppServiceProvider.php стр. 52–55)
	// и enrollment.proto: 5/мин на (IP, UUID), 300/мин на IP; неверный код — +1 к счётчику неудач IP,
	// после 20 в минуту — RATE_LIMITED.
	enrollPerDevice   = 5
	enrollPerIP       = 300
	enrollFailuresIP  = 20
	renewPerComputer  = 10
	limitWindow       = time.Minute
	maxDeviceFieldLen = 255
)

// ActivityEvent — запись журнала действий клуба (activity_logs) — подключает вызывающий (пакет журнала).
type ActivityEvent struct {
	ClubID int64
	// UserID — кто выдал код (в Laravel enroll писался от имени владельца клуба); 0 — неизвестен.
	UserID      int64
	Event       string
	Description string            // ключ перевода (русский текст) с плейсхолдерами :name
	Params      map[string]string // подстановки
	SubjectType string
	SubjectID   int64
	Details     map[string]string
	IP          string
}

// ActivityFunc пишет событие журнала в той же транзакции.
type ActivityFunc func(ctx context.Context, q *store.Queries, ev ActivityEvent) error

// RevokedFunc — после коммита отзыва (публикация в NATS для других инстансов).
type RevokedFunc func(ctx context.Context, clubID, computerID int64, serials []string)

// GuestTokenRevoker отзывает токен гостя на ПК (guestapi.Tokens.RevokeComputer; userID 0 — любой гость).
type GuestTokenRevoker interface {
	RevokeComputer(ctx context.Context, computerID, userID int64) error
}

// Options — зависимости сервиса.
type Options struct {
	// Pool — пул приложения (роль zafiro_app).
	Pool db.Beginner
	CA   *pki.CA
	// CodePepper — секрет HMAC кодов регистрации (≥ 32 байт), общий для всех инстансов.
	CodePepper []byte
	// CodeTTL — срок кода (15 минут).
	CodeTTL time.Duration
	// CertLifetime — срок клиентского сертификата (30 дней).
	CertLifetime time.Duration
	// DisklessCertLifetime — срок сертификата по ключу образа и его продлений (24 часа, решение Q-037).
	DisklessCertLifetime time.Duration
	// AgentEndpoint — адрес AgentService для ответа Enroll ("agent.zafiro.kz:443").
	AgentEndpoint string
	// Limiter — по умолчанию в памяти (NewMemoryLimiter).
	Limiter Limiter
	// Cache — кэш сертификатов этого инстанса (обновляется сразу после выпуска и отзыва).
	Cache     *CertCache
	OnRevoked RevokedFunc
	// GuestTokens — токены гостя в Valkey (общие для всех инстансов): при отзыве сертификата ПК («Отключить ПК»,
	// «Перерегистрировать», повторная регистрация, отзыв ключа образа) токен гостя этого ПК отзывается сразу, иначе
	// он до 2 минут держал бы гостя «вошедшим на другом ПК» (SIGNED_IN_ELSEWHERE, open-questions/stage5-agent.md
	// AG-5). Продление (superseded) гостя не трогает. nil — не отзывать.
	GuestTokens GuestTokenRevoker
	Activity    ActivityFunc
	Logger      *slog.Logger
	Now         func() time.Time
}

// Service — регистрация ПК и сертификаты агентов.
type Service struct {
	opts Options
}

// New проверяет опции и подключается к кэшу (первое использование продлённого сертификата).
func New(opts Options) (*Service, error) {
	if opts.Pool == nil || opts.CA == nil {
		return nil, errors.New("enroll: Pool and CA are required")
	}
	if len(opts.CodePepper) < 32 {
		return nil, errors.New("enroll: code pepper must be at least 32 bytes")
	}
	if opts.CodeTTL <= 0 {
		opts.CodeTTL = DefaultCodeTTL
	}
	if opts.CertLifetime <= 0 {
		opts.CertLifetime = pki.DefaultLifetime
	}
	if opts.DisklessCertLifetime <= 0 {
		opts.DisklessCertLifetime = DefaultDisklessCertLifetime
	}
	if opts.Limiter == nil {
		opts.Limiter = NewMemoryLimiter()
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	s := &Service{opts: opts}
	if opts.Cache != nil {
		opts.Cache.setFirstUse(s.markFirstUse)
	}
	return s, nil
}

// CanRegister — может ли сотрудник подключать и отключать ПК (ClubComputerPolicy::register).
func CanRegister(p principal.Principal) bool {
	return p.ClubID > 0 && !p.ReadOnly && slices.Contains(RegisterRoles, p.Role)
}

// ---------------------------------------------------------------------------
// Панель: коды и отзыв
// ---------------------------------------------------------------------------

// CodeRequest — что регистрирует код.
type CodeRequest struct {
	// ZoneID — зона для новой карточки (nil — «Не назначена»).
	ZoneID *int64
	// ComputerID — карточка, которую перерегистрирует код (nil — новый или найденный по MAC ПК).
	ComputerID *int64
	// IP — адрес сотрудника (аудит).
	IP netip.Addr
}

// IssuedCode — выданный код. Code показывается один раз и нигде не хранится.
type IssuedCode struct {
	ID        int64
	Code      string // ABCD-EFGH
	ExpiresAt time.Time
}

// CreateCode — «Подключить ПК»: одноразовый код на 15 минут.
func (s *Service) CreateCode(ctx context.Context, p principal.Principal, req CodeRequest) (IssuedCode, error) {
	if !CanRegister(p) {
		return IssuedCode{}, ErrForbidden
	}
	var out IssuedCode
	err := s.withCodeRetry(func() error {
		return db.InTenantTxWith(ctx, s.opts.Pool, p.ClubID, db.Options{UserID: p.UserID}, func(q *store.Queries) error {
			var err error
			out, err = s.createCode(ctx, q, p, req)
			return err
		})
	})
	return out, err
}

// Reregister — «Перерегистрировать ПК»: отзывает сертификаты карточки и выдаёт код именно для неё.
func (s *Service) Reregister(ctx context.Context, p principal.Principal, computerID int64, ip netip.Addr) (IssuedCode, error) {
	if !CanRegister(p) {
		return IssuedCode{}, ErrForbidden
	}
	var out IssuedCode
	var revoked []string
	err := s.withCodeRetry(func() error {
		return db.InTenantTxWith(ctx, s.opts.Pool, p.ClubID, db.Options{UserID: p.UserID}, func(q *store.Queries) error {
			var err error
			if revoked, err = s.revokeComputer(ctx, q, p, computerID, "staff", ip); err != nil {
				return err
			}
			out, err = s.createCode(ctx, q, p, CodeRequest{ComputerID: &computerID, IP: ip})
			return err
		})
	})
	if err == nil {
		s.afterRevoke(ctx, p.ClubID, computerID, revoked, true)
	}
	return out, err
}

// RevokeComputer — «Отключить ПК»: отзывает все сертификаты карточки (агент получит CERTIFICATE_REVOKED).
func (s *Service) RevokeComputer(ctx context.Context, p principal.Principal, computerID int64, ip netip.Addr) (int, error) {
	if !CanRegister(p) {
		return 0, ErrForbidden
	}
	var revoked []string
	err := db.InTenantTxWith(ctx, s.opts.Pool, p.ClubID, db.Options{UserID: p.UserID}, func(q *store.Queries) error {
		var err error
		revoked, err = s.revokeComputer(ctx, q, p, computerID, "staff", ip)
		return err
	})
	if err != nil {
		return 0, err
	}
	s.afterRevoke(ctx, p.ClubID, computerID, revoked, true)
	return len(revoked), nil
}

// CancelCode — отменить ещё не использованный код.
func (s *Service) CancelCode(ctx context.Context, p principal.Principal, codeID int64) error {
	if !CanRegister(p) {
		return ErrForbidden
	}
	return db.InTenantTxWith(ctx, s.opts.Pool, p.ClubID, db.Options{UserID: p.UserID}, func(q *store.Queries) error {
		n, err := q.CancelEnrollmentCode(ctx, store.CancelEnrollmentCodeParams{ClubID: p.ClubID, ID: codeID})
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrCodeNotFound
		}
		return nil
	})
}

// ListCodes — действующие коды клуба (без самих кодов).
func (s *Service) ListCodes(ctx context.Context, p principal.Principal) ([]store.ListActiveEnrollmentCodesRow, error) {
	if !CanRegister(p) {
		return nil, ErrForbidden
	}
	var rows []store.ListActiveEnrollmentCodesRow
	err := db.InTenantTx(ctx, s.opts.Pool, p.ClubID, func(q *store.Queries) error {
		now := s.opts.Now()
		var err error
		rows, err = q.ListActiveEnrollmentCodes(ctx, store.ListActiveEnrollmentCodesParams{
			ClubID: p.ClubID, Since: now.Add(-s.opts.CodeTTL), Now: now})
		return err
	})
	return rows, err
}

func (s *Service) createCode(ctx context.Context, q *store.Queries, p principal.Principal, req CodeRequest) (IssuedCode, error) {
	if req.ZoneID != nil {
		if _, err := q.GetZoneForEnroll(ctx, store.GetZoneForEnrollParams{ClubID: p.ClubID, ID: *req.ZoneID}); err != nil {
			return IssuedCode{}, notFound(err, ErrZoneNotFound)
		}
	}
	if req.ComputerID != nil {
		if _, err := q.GetComputerForEnroll(ctx, store.GetComputerForEnrollParams{ClubID: p.ClubID, ID: *req.ComputerID}); err != nil {
			return IssuedCode{}, notFound(err, ErrComputerNotFound)
		}
	}
	code, err := newCode()
	if err != nil {
		return IssuedCode{}, err
	}
	row, err := q.CreateEnrollmentCode(ctx, store.CreateEnrollmentCodeParams{
		ClubID: p.ClubID, CodeHash: hashCode(s.opts.CodePepper, code), ClubZoneID: req.ZoneID,
		ClubComputerID: req.ComputerID, CreatedByUserID: ptr(p.UserID), ExpiresAt: s.opts.Now().Add(s.opts.CodeTTL),
	})
	if err != nil {
		return IssuedCode{}, err
	}
	// Сам код в аудит не попадает (секрет).
	if err := s.audit(ctx, q, p.ClubID, ptr(p.UserID), "staff", "agent.enroll_code.created", req.ComputerID, req.IP,
		map[string]any{"code_id": row.ID, "zone_id": req.ZoneID, "computer_id": req.ComputerID, "expires_at": row.ExpiresAt}); err != nil {
		return IssuedCode{}, err
	}
	return IssuedCode{ID: row.ID, Code: FormatCode(code), ExpiresAt: row.ExpiresAt}, nil
}

func (s *Service) revokeComputer(ctx context.Context, q *store.Queries, p principal.Principal, computerID int64, reason string, ip netip.Addr) ([]string, error) {
	if _, err := q.GetComputerForEnroll(ctx, store.GetComputerForEnrollParams{ClubID: p.ClubID, ID: computerID}); err != nil {
		return nil, notFound(err, ErrComputerNotFound)
	}
	revoked, err := q.RevokeComputerCertificates(ctx, store.RevokeComputerCertificatesParams{
		Reason: reason, RevokedByUserID: ptr(p.UserID), ClubID: p.ClubID, ClubComputerID: computerID})
	if err != nil {
		return nil, err
	}
	if len(revoked) > 0 {
		if err := s.audit(ctx, q, p.ClubID, ptr(p.UserID), "staff", "agent.certificate.revoked", &computerID, ip,
			map[string]any{"serials": revoked, "reason": reason}); err != nil {
			return nil, err
		}
	}
	return revoked, nil
}

// withCodeRetry — повтор при совпадении хеша кода с существующим (вероятность ~N/2^40).
func (s *Service) withCodeRetry(fn func() error) error {
	var err error
	for range 3 {
		if err = fn(); !isUniqueViolation(err, "agent_enrollment_codes_code_hash_unique") {
			return err
		}
	}
	return err
}

// afterRevoke — после коммита отзыва: кэш, публикация для других инстансов и (signOutGuest) отзыв токена гостя ПК.
func (s *Service) afterRevoke(ctx context.Context, clubID, computerID int64, serials []string, signOutGuest bool) {
	if len(serials) == 0 {
		return
	}
	if signOutGuest && s.opts.GuestTokens != nil {
		if err := s.opts.GuestTokens.RevokeComputer(ctx, computerID, 0); err != nil {
			// Токен всё равно неприменим (привязан к ПК с отозванным сертификатом) и истечёт сам.
			s.opts.Logger.WarnContext(ctx, "revoke guest token of revoked computer failed",
				"club_id", clubID, "computer_id", computerID, "err", err)
		}
	}
	if s.opts.Cache != nil {
		for _, serial := range serials {
			s.opts.Cache.MarkRevoked(serial)
		}
	}
	if s.opts.OnRevoked != nil {
		s.opts.OnRevoked(ctx, clubID, computerID, serials)
	}
}

// ---------------------------------------------------------------------------
// Общие помощники
// ---------------------------------------------------------------------------

func (s *Service) audit(ctx context.Context, q *store.Queries, clubID int64, actor *int64, kind, action string,
	computerID *int64, ip netip.Addr, after map[string]any) error {
	body, err := json.Marshal(after)
	if err != nil {
		return err
	}
	params := store.InsertAuditLogParams{
		ClubID: &clubID, ActorUserID: actor, ActorKind: kind, Category: "security", Action: action, After: body,
	}
	if computerID != nil {
		params.SubjectType, params.SubjectID = ptr("club_computer"), computerID
	}
	if ip.IsValid() {
		params.Ip = &ip
	}
	return q.InsertAuditLog(ctx, params)
}

func ptr[T comparable](v T) *T {
	var zero T
	if v == zero {
		return nil
	}
	return &v
}

func notFound(err error, nf *Error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return nf
	}
	return err
}

func isUniqueViolation(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && (constraint == "" || pgErr.ConstraintName == constraint)
}

// subscriptionActive — ClubSubscription::isActive() (app/Models/ClubSubscription.php стр. 31–35, BR-130):
// (active и (нет конца периода или он в будущем)) или (trialing и конец пробного периода в будущем).
func subscriptionActive(status string, trialEndsAt, periodEndsAt *time.Time, now time.Time) bool {
	switch status {
	case "active":
		return periodEndsAt == nil || periodEndsAt.After(now)
	case "trialing":
		return trialEndsAt != nil && trialEndsAt.After(now)
	}
	return false
}

// maxComputers — data_get(plan.features, 'max_computers'), если число (is_numeric). Laravel:
// ClubComputerRegistrar::register() стр. 44–45 (BR-134). features — список строк (через UI ключ не задать)
// или объект; ok=false — лимита нет.
func maxComputers(features json.RawMessage) (int64, bool) {
	var obj map[string]json.RawMessage
	if len(features) == 0 || json.Unmarshal(features, &obj) != nil {
		return 0, false
	}
	raw, ok := obj["max_computers"]
	if !ok {
		return 0, false
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return 0, false
	}
	var f float64
	switch x := v.(type) {
	case float64:
		f = x
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		if err != nil {
			return 0, false
		}
		f = parsed
	default:
		return 0, false
	}
	// (int) $computerLimit — усечение к нулю.
	return int64(f), true
}

func displayName(displayName, deviceName *string, number int64) string {
	if displayName != nil && *displayName != "" {
		return *displayName
	}
	if deviceName != nil && *deviceName != "" {
		return *deviceName
	}
	return fmt.Sprintf("PC-%02d", number)
}

func deviceKey(uuid string) string {
	h := sha256.Sum256([]byte(strings.ToLower(uuid)))
	return hex.EncodeToString(h[:])
}
