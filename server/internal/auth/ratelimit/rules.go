package ratelimit

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"time"
)

// Subject — всё, из чего строятся ключи лимитов (аналог Request в замыканиях RateLimiter::for).
// Заполняет HTTP-middleware (SubjectFromRequest) или вызывающий код (gRPC).
type Subject struct {
	// IP — адрес клиента (после доверенных прокси).
	IP string
	// UserID — пользователь (Principal); 0 — без входа.
	UserID int64
	// ComputerID и ClubID — ПК клуба (agent/identity); 0 — не агент.
	ComputerID int64
	ClubID     int64
	// Поля запроса, по которым старые лимиты различали попытки.
	Phone    string // customer-login, password-recovery
	DeviceID string // agent-enroll
	ClubKey  string // club-resolve (club_key или key)
}

// pc — ключ «ПК». Laravel: AppServiceProvider::agentRateLimitKey() (стр. 126–138): sha256 токена устройства,
// иначе id Sanctum-токена, иначе IP. В новом протоколе устройство — id ПК из сертификата mTLS,
// токен пользователя — пользователь.
func (s Subject) pc() string {
	switch {
	case s.ComputerID > 0:
		return "device:" + strconv.FormatInt(s.ComputerID, 10)
	case s.UserID > 0:
		return "user:" + strconv.FormatInt(s.UserID, 10)
	}
	return s.IP
}

// userOrIP — ключ стандартного throttle:N,M (ThrottleRequests::resolveRequestSignature: пользователь, без входа — IP).
func (s Subject) userOrIP() string {
	if s.UserID > 0 {
		return "user:" + strconv.FormatInt(s.UserID, 10)
	}
	return "ip:" + s.IP
}

// digits — preg_replace('/\D+/', ”, $phone).
func digits(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, s)
}

func sha256hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// Limit — не больше Max запросов за Window с ключом Key(subject).
type Limit struct {
	Max    int
	Window time.Duration
	// Key — ключ счётчика (без имени правила). Пустой ключ — лимит не применяется.
	Key func(Subject) string
}

// Rule — именованный лимит (RateLimiter::for('<имя>') или throttle:N,M,<префикс>).
// Все лимиты правила проверяются по порядку; первый превышенный даёт 429 (как ThrottleRequests).
type Rule struct {
	Name   string
	Limits []Limit
	// Disabled — правило есть, но выключено по умолчанию (включается Options.Enable).
	Disabled bool
}

func perMinute(n int, key func(Subject) string) Limit {
	return Limit{Max: n, Window: time.Minute, Key: key}
}

func perMinutes(minutes, n int, key func(Subject) string) Limit {
	return Limit{Max: n, Window: time.Duration(minutes) * time.Minute, Key: key}
}

func byIP(s Subject) string { return s.IP }
func byPC(s Subject) string { return s.pc() }

// byUser — пользователь (без пользователя лимит не применяется).
func byUser(s Subject) string {
	if s.UserID > 0 {
		return "user:" + strconv.FormatInt(s.UserID, 10)
	}
	return ""
}

// Throttle — правило throttle:N,M,<префикс>: N запросов за M минут, ключ — пользователь или IP.
func Throttle(name string, n, minutes int) Rule {
	return Rule{Name: name, Limits: []Limit{perMinutes(minutes, n, Subject.userOrIP)}}
}

// Именованные лимиты. Laravel: app/Providers/AppServiceProvider.php boot() (стр. 47–97); значения — точно
// как в старом (docs/parts/20-api.md §1.1). Ключи — те же составные части (IP, телефон, ПК, клуб).
const (
	ClubComputers            = "club-computers"
	AgentEnroll              = "agent-enroll"
	AgentExitPin             = "agent-exit-pin"
	CustomerLogin            = "customer-login"
	PasswordRecovery         = "password-recovery"
	StaffApp                 = "staff-app"
	ClubResolve              = "club-resolve"
	AgentRegister            = "agent-register"
	AgentQrLogin             = "agent-qr-login"
	AgentQrPoll              = "agent-qr-poll"
	AgentTokenRotate         = "agent-token-rotate"
	AgentTranslations        = "agent-translations"
	ClubComputerRegistration = "club-computer-registration"

	// WebLogin и WebRegister — вход и регистрация в веб-панели. В старом лимита НЕ было
	// (routes/web.php:158, 165 без throttle:; docs/open-questions/screens.md §1).
	// Решение Q-052 (docs/DECISIONS.md): лимит включён по умолчанию, значения как у customer-login;
	// выключить можно только явно (ratelimit.disable).
	WebLogin    = "web-login"
	WebRegister = "web-register"

	// SuperadminTOTP — ввод второго фактора суперадмина (Этап 7, новая функция; docs/open-questions/stage7-totp.md):
	// как web-login — 5/мин на аккаунт (вместо телефона — пользователь полувхода) и 300/мин на IP, плюс 30/ч на
	// аккаунт (перебор 6 цифр растягивается на годы). Считает сервис второго фактора (пользователь известен только
	// из полувхода в теле запроса), не HTTP-middleware.
	SuperadminTOTP = "superadmin-totp"
)

// DefaultRules — все лимиты старого проекта.
func DefaultRules() []Rule {
	rules := []Rule{
		{Name: ClubComputers, Limits: []Limit{perMinute(60, byPC)}},
		// Diskless club restarts all its PCs at once behind one IP: limit each PC, and the IP loosely.
		{Name: AgentEnroll, Limits: []Limit{
			perMinute(5, func(s Subject) string { return s.IP + "|" + sha256hex(strings.ToLower(s.DeviceID)) }),
			perMinute(300, byIP),
		}},
		{Name: AgentExitPin, Limits: []Limit{
			perMinutes(5, 5, byPC),
			// Общий лимит на клуб ПК (иначе IP). Limit::perHour(100).
			perMinutes(60, 100, func(s Subject) string {
				if s.ClubID > 0 {
					return "club:" + strconv.FormatInt(s.ClubID, 10)
				}
				return s.IP
			}),
		}},
		{Name: CustomerLogin, Limits: phoneAndIP(5, 300)},
		// Password recovery codes cost money (Telegram Gateway) and guess attempts are limited too.
		{Name: PasswordRecovery, Limits: phoneAndIP(10, 30)},
		{Name: StaffApp, Limits: []Limit{perMinute(240, func(s Subject) string {
			if s.UserID > 0 {
				return strconv.FormatInt(s.UserID, 10)
			}
			return s.IP
		})}},
		{Name: ClubResolve, Limits: []Limit{
			perMinute(10, func(s Subject) string { return s.IP + "|" + sha256hex(s.ClubKey) }),
			perMinute(300, byIP),
		}},
		{Name: AgentRegister, Limits: []Limit{perMinutes(10, 5, byPC), perMinutes(10, 60, byIP)}},
		{Name: AgentQrLogin, Limits: []Limit{perMinute(20, byPC)}},
		{Name: AgentQrPoll, Limits: []Limit{perMinute(60, byPC)}},
		{Name: AgentTokenRotate, Limits: []Limit{perMinute(10, byPC)}},
		{Name: AgentTranslations, Limits: []Limit{perMinute(600, byIP)}},
		{Name: ClubComputerRegistration, Limits: []Limit{perMinute(10, byPC)}},

		// Решение Q-052 (docs/DECISIONS.md): включены по умолчанию.
		{Name: WebLogin, Limits: phoneAndIP(5, 300)},
		{Name: WebRegister, Limits: phoneAndIP(5, 300)},
		{Name: SuperadminTOTP, Limits: []Limit{perMinute(5, byUser), perMinutes(60, 30, byUser), perMinute(300, byIP)}},
	}
	// throttle:N,M,<префикс> — routes/web.php и routes/api.php, routes/api_staff.php.
	for _, t := range throttles {
		rules = append(rules, Throttle(t.name, t.n, t.minutes))
	}
	return rules
}

func phoneAndIP(perPhone, perIP int) []Limit {
	return []Limit{
		perMinute(perPhone, func(s Subject) string { return s.IP + "|" + digits(s.Phone) }),
		perMinute(perIP, byIP),
	}
}

// throttles — все throttle:N,M,<префикс> старого проекта (имя = префикс).
var throttles = []struct {
	name       string
	n, minutes int
}{
	// routes/web.php
	{"superadmin.telegram.alerts.test", 5, 1},
	{"languages.import", 20, 1},
	{"club-invitations.accept", 10, 1},
	{"pc-login.approve", 10, 1},
	{"chat.reply", 60, 1},
	{"club-computers.guest-session", 30, 1},
	{"users.password", 20, 1},
	{"reservations.customers", 60, 1},
	// routes/api.php
	{"api.auth.clubs", 30, 1},
	{"api.auth.switch-club", 10, 1},
	{"api.products.index", 60, 1},
	{"api.tariffs.index", 60, 1},
	{"api.tariffs.purchase", 10, 1},
	{"api.settings.show", 60, 1},
	{"api.guest.friends.index", 60, 1},
	{"api.guest.friends.store", 10, 1},
	{"api.guest.friends.accept", 30, 1},
	{"api.guest.friends.remove", 30, 1},
	{"api.guest.friends.transfer", 10, 1},
	{"api.guest.friends.transfer.code", 5, 1},
	{"api.guest.friends.transfer.confirm", 10, 1},
	{"api.guest.minutes", 60, 1},
	{"api.guest.minutes.purchase", 10, 1},
	{"api.guest.packages", 60, 1},
	{"api.guest.packages.start", 10, 1},
	{"api.guest.chat", 60, 1},
	{"api.guest.chat.store", 20, 1},
	{"api.guest.telegram", 30, 1},
	{"api.guest.news", 30, 1},
	{"api.guest.shop.purchase", 10, 1},
	{"api.guest.profile.name", 10, 1},
	{"api.guest.profile.password", 5, 1},
	{"api.guest.history", 60, 1},
	{"api.guest.seats.index", 30, 1},
	{"api.guest.seat-transfer", 10, 1},
	{"api.guest.qr-login.show", 30, 1},
	{"api.guest.qr-login.approve", 10, 1},
	// routes/api_staff.php
	{"api.staff.guest-session", 30, 1},
	{"api.staff.chat.reply", 60, 1},
}
