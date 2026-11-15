package session

import "time"

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
	// Connected — признак «в сети» из присутствия агента (club_computers.status = 'online', его ведёт
	// server/internal/agent/presence). Агент шлёт статус в поток, а last_seen_at пишется только при
	// смене состояния (AS-2), поэтому при Connected != nil оно заменяет проверку по last_seen_at.
	Connected *bool
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
	if c.Connected != nil {
		return *c.Connected
	}
	return c.LastSeenAt != nil && !c.LastSeenAt.Before(now.Add(-HeartbeatTimeout))
}

// sessionOwnedBy — current_session_user_id === user.
func (c Computer) sessionOwnedBy(userID int64) bool {
	return c.SessionUserID != nil && *c.SessionUserID == userID
}

// ---------------------------------------------------------------------------------------------
// Начало или продление сессии покупкой (BR-020/021, GAP-016)

// Error — ошибка проверки с полем формы и текстом-ключом перевода (как ValidationException::withMessages).
type Error struct {
	Field   string
	Message string
}

func (e *Error) Error() string { return e.Field + ": " + e.Message }

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
// а части старой остаются в БД (их удалит суточная задача). Проверку ensureComputerFor
// (pricing.Computer.CheckForCustomer) вызывающий делает раньше (в старом коде — до списания денег).
func Extend(c Computer, minutes int64, now time.Time) Extension {
	add := time.Duration(minutes) * time.Minute
	if c.HasPaidSession(now) {
		return Extension{Extends: true, SessionID: *c.SessionID, EndsAt: StoredTime(c.SessionEndsAt.Add(add))}
	}
	return Extension{EndsAt: StoredTime(now.Add(add))}
}
