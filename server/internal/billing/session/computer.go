package session

import (
	"errors"
	"time"
)

// HeartbeatTimeout — ПК онлайн, если последний heartbeat не старше 90 с.
// Laravel: app/Models/ClubComputer.php HEARTBEAT_TIMEOUT_SECONDS = 90.
const HeartbeatTimeout = 90 * time.Second

// Computer — поля ПК, от которых зависят расчёты сессии (строка club_computers).
type Computer struct {
	ID            int64
	ZoneID        int64
	IsReserved    bool
	LastSeenAt    *time.Time
	SessionID     *string // current_session_id
	SessionUserID *int64  // current_session_user_id; nil — гость с кассы или старый агент
	SessionGuest  bool
	SessionEndsAt *time.Time
	StaffUntil    *time.Time
}

// HasPaidSession — оплаченная и ещё не истёкшая сессия.
// Laravel: app/Models/ClubComputer.php hasPaidSession() (стр. 97–102), BR-030.
func (c Computer) HasPaidSession(now time.Time) bool {
	return c.SessionID != nil &&
		(c.SessionUserID != nil || c.SessionGuest) &&
		c.SessionEndsAt != nil && c.SessionEndsAt.After(now) // isFuture()
}

// HasStaffVisit — на ПК работает сотрудник. Laravel: ClubComputer::hasStaffVisit() (стр. 53–56).
func (c Computer) HasStaffVisit(now time.Time) bool {
	return c.StaffUntil != nil && c.StaffUntil.After(now)
}

// Online — connectionStatus() === 'online': last_seen_at >= now − 90 с (с микросекундами now).
// Laravel: app/Models/ClubComputer.php connectionStatus() (стр. 88–94).
func (c Computer) Online(now time.Time) bool {
	return c.LastSeenAt != nil && !c.LastSeenAt.Before(now.Add(-HeartbeatTimeout))
}

// sessionOwnedBy — current_session_user_id === user.
func (c Computer) sessionOwnedBy(userID int64) bool {
	return c.SessionUserID != nil && *c.SessionUserID == userID
}

// ---------------------------------------------------------------------------------------------
// Истечение (BR-033)

// IsDue — сессия ПК должна быть снята: session_ends_at != null && session_ends_at <= now.
//
// Laravel: app/Services/ClubComputerSessions.php expireIfDue() (стр. 177–186) — `! isFuture()`,
// и expireAll() (стр. 189–195) — SQL `session_ends_at <= now()`, где now() подставлен строкой
// `Y-m-d H:i:s` (усечён). Для конца, хранимого с точностью до секунды, обе проверки совпадают:
// ends <= now ⇔ ends <= floor(now). Сессия без конца (старые агенты, ends = null) не истекает.
func IsDue(c Computer, now time.Time) bool {
	return c.SessionEndsAt != nil && !c.SessionEndsAt.After(now)
}

// Expiry — что делает старый код при естественном окончании сессии.
//
// Laravel: ClubComputerSessions::expireIfDue()/expireAll(): обнуляет current_session_id,
// current_session_user_id, session_guest=false, session_ends_at. SessionSettlement::settle() НЕ
// вызывается: нет возврата, нет пакетов (весь остаток уже 0 — время вышло), нет журнала, части
// session_purchases НЕ удаляются (их чистит суточная задача через 8 дней, BR-034). GAP-005.
//
// Допущение Этапа 1: Q-010 — пока повторяем старое поведение (DeletePurchases = false).
type Expiry struct {
	ClearSession    bool // обнулить поля сессии ПК
	Settle          bool // всегда false
	DeletePurchases bool // всегда false (Q-010)
	Log             bool // всегда false
	// AgentStateIdle — если heartbeat прислал in_session, записать idle (BR-036, только heartbeat).
	AgentStateIdle bool
}

// ExpiryFor — план истечения для ПК; ok = false, если истекать нечему.
func ExpiryFor(c Computer, now time.Time) (Expiry, bool) {
	if !IsDue(c, now) {
		return Expiry{}, false
	}
	return Expiry{ClearSession: true, AgentStateIdle: true}, true
}

// ---------------------------------------------------------------------------------------------
// Начало или продление сессии покупкой (BR-020/021, GAP-016)

// Ошибки продажи времени на ПК (поле формы computer_id, тексты — ключи t() старого кода).
var (
	ErrComputerReserved = &Error{Field: "computer_id", Message: "Забронированный компьютер нельзя использовать для сессии."}
	ErrComputerBusy     = &Error{Field: "computer_id", Message: "На компьютере уже идёт сессия другого пользователя."}
)

// Error — ошибка проверки с полем формы и текстом-ключом перевода (как ValidationException::withMessages).
type Error struct {
	Field   string
	Message string
}

func (e *Error) Error() string { return e.Field + ": " + e.Message }

// IsError сообщает, что err — ошибка проверки этого пакета.
func IsError(err error) bool {
	var e *Error
	return errors.As(err, &e)
}

// EnsureComputerFor — ПК может принять время клиента.
// Laravel: app/Services/AgentTariffPurchaseService.php ensureComputerFor() (стр. 212–221).
func EnsureComputerFor(c Computer, userID int64, now time.Time) error {
	if c.IsReserved {
		return ErrComputerReserved
	}
	if c.HasPaidSession(now) && !c.sessionOwnedBy(userID) {
		return ErrComputerBusy
	}
	return nil
}

// Extension — новая или продлённая сессия после покупки.
type Extension struct {
	Extends bool // true — тот же session_id, конец сдвигается; false — новая сессия (новый uuid)
	// SessionID — текущий id при продлении; пусто — вызывающий создаёт новый uuid.
	SessionID string
	// EndsAt — новый session_ends_at, усечённый до секунды (как сохранил бы старый код).
	EndsAt time.Time
}

// Extend — конец сессии после покупки minutes минут.
//
// Laravel: app/Services/AgentTariffPurchaseService.php sell() (стр. 271–273):
//
//	$hasActiveSession = $computer->hasPaidSession();
//	$sessionId = $hasActiveSession ? $computer->current_session_id : (string) Str::uuid();
//	$sessionEndsAt = ($hasActiveSession ? $computer->session_ends_at->copy() : now())->addMinutes($minutes);
//
// Истёкшая, но ещё не снятая сессия (ends <= now) не продлевается: начинается новая с новым id,
// а части старой остаются в БД (их удалит суточная задача). Проверку EnsureComputerFor вызывающий
// делает раньше (в старом коде — до списания денег).
func Extend(c Computer, minutes int64, now time.Time) Extension {
	add := time.Duration(minutes) * time.Minute
	if c.HasPaidSession(now) {
		return Extension{Extends: true, SessionID: *c.SessionID, EndsAt: StoredTime(c.SessionEndsAt.Add(add))}
	}
	return Extension{EndsAt: StoredTime(now.Add(add))}
}
