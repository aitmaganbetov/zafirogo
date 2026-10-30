// Package service — сервисный слой покупок и сессий ПК поверх БД: повторяет транзакции старого кода
// (порядок блокировок lockForUpdate, проверок и записей) и связывает чистые расчёты (billing/pricing,
// billing/session, billing/bonus) с ledger, session_purchases, time_packages и club_computers.
//
// Источники (Laravel):
//   - app/Services/AgentTariffPurchaseService.php — purchase(), purchaseMinutes(), startPackage(), sell();
//   - app/Services/ClubComputerSessions.php — end(), expireAll();
//   - app/Services/SessionSettlement.php — settle();
//   - app/Services/SeatTransfers.php — transfer(), allowedZoneIds();
//   - app/Services/GuestSessions.php — sell();
//   - app/Services/ComputerReservations.php — blocking(), markArrivedBy();
//   - app/Services/LoyaltyProgram.php — addSpending(), subtractSpending().
//
// Каждая операция — одна транзакция db.InTenantTx клуба (клуб — из аутентификации вызывающего).
// Деньги двигаются только проводками ledger; время — через внедряемые часы (Clock), не time.Now().
// Права (кто может купить/завершить/продать) проверяет вызывающий слой (HTTP/gRPC), кроме проверки
// «владелец сессии или сотрудник» в EndSession — она часть старого сервиса.
package service

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/billing"
	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// Clock — источник текущего времени (в тестах — фиксированные часы).
type Clock interface {
	Now() time.Time
}

// ClockFunc — часы из функции.
type ClockFunc func() time.Time

// Now возвращает f().
func (f ClockFunc) Now() time.Time { return f() }

// SystemClock — системные часы.
type SystemClock struct{}

// Now возвращает текущее время.
func (SystemClock) Now() time.Time { return time.Now() }

// Actor — кто выполняет операцию (для ledger и аудита).
type Actor = billing.BalanceActor

// Service — покупки времени, сессии ПК, гостевые продажи.
type Service struct {
	pool  db.Beginner
	clock Clock
	cash  CashRecorder
	// notifier — уведомления агента после коммита (SetNotifier); nil — без уведомлений.
	notifier SessionNotifier
	// newSessionID — (string) Str::uuid(): UUID v4.
	newSessionID func() string
}

// New создаёт сервис. pool — пул логина zafiro_app; cash — кассовый слой (нужен только для гостевых продаж,
// может быть nil, тогда SellGuestSession возвращает ошибку).
func New(pool db.Beginner, clock Clock, cash CashRecorder) *Service {
	if clock == nil {
		clock = SystemClock{}
	}
	return &Service{pool: pool, clock: clock, cash: cash, newSessionID: newUUIDv4}
}

func (s *Service) tx(ctx context.Context, clubID int64, fn func(q *store.Queries) error) error {
	return db.InTenantTx(ctx, s.pool, clubID, fn)
}

// ---------------------------------------------------------------------------
// Ошибки
// ---------------------------------------------------------------------------

// Error — ошибка проверки с полем формы и текстом-ключом перевода (ValidationException::withMessages).
// Ошибки расчётов приходят как *pricing.Error и *session.Error — их поля и тексты те же, что в Laravel.
type Error struct {
	Code    string
	Field   string
	Message string            // русский текст = ключ t()
	Params  map[string]string // подстановки :name
}

func (e *Error) Error() string {
	msg := e.Message
	for k, v := range e.Params {
		msg = strings.ReplaceAll(msg, ":"+k, v)
	}
	return e.Field + ": " + msg
}

// Is сравнивает по Code.
func (e *Error) Is(target error) bool {
	var t *Error
	return errors.As(target, &t) && t.Code == e.Code
}

func (e *Error) with(params map[string]string) *Error {
	c := *e
	c.Params = params
	return &c
}

// Ошибки старого кода, которых нет в чистых пакетах.
var (
	// AgentTariffPurchaseService::sell() (стр. 276–279): время заходит на чужую бронь.
	ErrBookedFrom = &Error{Code: "computer_booked", Field: "computer_id", Message: "ПК забронирован с :time. Выберите тариф короче или другой ПК."}
	// ClubComputerSessions::end() (стр. 208–210).
	ErrNoSuchSession = &Error{Code: "no_such_session", Field: "session_id", Message: "На компьютере нет такой активной сессии."}
	// GuestSessions::sell() (стр. 190–193).
	ErrNoOpenShift = &Error{Code: "no_open_shift", Field: "payment_method", Message: "Сначала откройте смену в кассе."}
	// GuestSessions::sell() (стр. 219–222).
	ErrGuestBookedFrom = &Error{Code: "guest_computer_booked", Field: "computer_ids", Message: "ПК :computer забронирован с :time."}
)

// ErrNotFound — запись не найдена в клубе (findOrFail → 404): тариф, ПК, членство, баланс, пакет.
var ErrNotFound = errors.New("service: not found")

// ErrForbidden — abort(403) старого кода: «Завершить сессию может только её владелец или сотрудник клуба.»
var ErrForbidden = &ForbiddenError{Message: "Завершить сессию может только её владелец или сотрудник клуба."}

// ForbiddenError — отказ 403 с текстом-ключом перевода.
type ForbiddenError struct{ Message string }

func (e *ForbiddenError) Error() string { return "forbidden: " + e.Message }

// ErrIdempotencyConflict — ключ идемпотентности уже использован запросом с другим телом.
var ErrIdempotencyConflict = errors.New("service: idempotency key reused with a different request")

// ErrInvalidInput — запрос, который должна была отклонить валидация вызывающего слоя
// (пустой ключ идемпотентности, неизвестный способ оплаты и т. п.).
var ErrInvalidInput = errors.New("service: invalid input")

// ErrNoCashRecorder — гостевая продажа без подключённого кассового слоя.
var ErrNoCashRecorder = errors.New("service: cash recorder is not configured")

func notFound(what string, err error) error {
	return fmt.Errorf("%w: %s: %w", ErrNotFound, what, err)
}

// newUUIDv4 — (string) Str::uuid() (Laravel: Ramsey UUID v4, строчные шестнадцатеричные цифры).
func newUUIDv4() string {
	var b [16]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read не возвращает ошибку (Go 1.24+)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
