package presence

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// ErrComputerNotFound — карточки ПК нет (удалена в панели).
var ErrComputerNotFound = errors.New("presence: computer not found")

// DefaultStateDebounce — сколько ждать, прежде чем записать новое agent_state: агент может переключаться
// idle ↔ locked за секунды (вход/выход гостя), в Postgres уходит итог.
const DefaultStateDebounce = 5 * time.Second

// Recorder пишет в Postgres только изменения состояния ПК.
type Recorder struct {
	pool     db.Beginner
	now      func() time.Time
	debounce time.Duration
	log      *slog.Logger
	writes   atomic.Int64
}

// NewRecorder создаёт Recorder; pool — пул логина zafiro_app (тенантные транзакции).
func NewRecorder(pool db.Beginner, debounce time.Duration, now func() time.Time, log *slog.Logger) *Recorder {
	if debounce <= 0 {
		debounce = DefaultStateDebounce
	}
	if now == nil {
		now = time.Now
	}
	if log == nil {
		log = slog.Default()
	}
	return &Recorder{pool: pool, now: now, debounce: debounce, log: log}
}

// Writes — сколько UPDATE club_computers выполнил этот Recorder (метрика и тесты «нет записей без изменений»).
func (r *Recorder) Writes() int64 { return r.writes.Load() }

// Load читает карточку ПК клуба (для Hello и Welcome); Diskless — бездисковый ПК (0031 zafiro_computer_diskless —
// то же правило, что у панели ПК, S7-H-18).
func (r *Recorder) Load(ctx context.Context, clubID, computerID int64) (store.GetAgentComputerRow, error) {
	var row store.GetAgentComputerRow
	err := db.InTenantTx(ctx, r.pool, clubID, func(q *store.Queries) error {
		var err error
		row, err = q.GetAgentComputer(ctx, store.GetAgentComputerParams{ClubID: clubID, ComputerID: computerID})
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return row, ErrComputerNotFound
	}
	if err != nil {
		return row, fmt.Errorf("presence: load computer: %w", err)
	}
	return row, nil
}

// HelloFacts — что ПК сообщил при подключении.
type HelloFacts struct {
	AgentVersion string
	BootID       string
	RemoteIP     string
	Device       *agentv1.DeviceInfo
}

// HelloUpdate — что нужно записать (nil — не менять). Пусто — записи не будет.
type HelloUpdate struct {
	Online          bool
	FreshBoot       bool
	HardwareChanged bool
	Params          store.RecordAgentHelloParams
}

// Empty — нечего писать.
func (u HelloUpdate) Empty() bool {
	p := u.Params
	return !u.Online && !u.HardwareChanged && p.AgentVersion == nil && p.AgentBootID == nil && p.RemoteIp == nil &&
		p.DeviceName == nil
}

func changedStr(cur *string, v string) *string {
	if v == "" || (cur != nil && *cur == v) {
		return nil
	}
	return &v
}

// DiffHello сравнивает Hello с карточкой. Повторяет набор полей старого changes()
// (Laravel: app/Services/ClubComputerSessions.php changes(): status, remote_ip, agent_version, железо,
// hardware_reported_at, device_name), но пишет только изменившиеся. mac_address heartbeat только дополнял —
// теперь MAC задаётся при регистрации ПК (EnrollmentService), здесь не пишется.
func DiffHello(c store.GetAgentComputerRow, f HelloFacts) HelloUpdate {
	u := HelloUpdate{Online: c.Status != "online"}
	p := &u.Params
	p.ClubID, p.ComputerID = c.ClubID, c.ID
	p.AgentVersion = changedStr(c.AgentVersion, f.AgentVersion)
	p.AgentBootID = changedStr(c.AgentBootID, f.BootID)
	u.FreshBoot = p.AgentBootID != nil
	p.RemoteIp = changedStr(c.RemoteIp, f.RemoteIP)
	if d := f.Device; d != nil {
		p.LocalIp = changedStr(c.LocalIp, d.GetLocalIp())
		p.CpuModel = changedStr(c.CpuModel, d.GetCpuModel())
		p.GpuModel = changedStr(c.GpuModel, d.GetGpuModel())
		p.OsVersion = changedStr(c.OsVersion, d.GetOsVersion())
		p.DeviceName = changedStr(c.DeviceName, d.GetHostname())
		if v := d.GetCpuCores(); v > 0 && (c.CpuCores == nil || *c.CpuCores != v) {
			p.CpuCores = &v
		}
		if v := d.GetRamTotalMb(); v > 0 && (c.RamTotalMb == nil || *c.RamTotalMb != v) {
			p.RamTotalMb = &v
		}
		u.HardwareChanged = p.LocalIp != nil || p.CpuModel != nil || p.GpuModel != nil || p.OsVersion != nil ||
			p.CpuCores != nil || p.RamTotalMb != nil
	}
	p.HardwareChanged = u.HardwareChanged
	return u
}

// RecordHello пишет изменения Hello (если есть). freshBoot — новый boot_id (свежая загрузка Windows).
func (r *Recorder) RecordHello(ctx context.Context, c store.GetAgentComputerRow, f HelloFacts) (freshBoot bool, err error) {
	u := DiffHello(c, f)
	if u.Empty() {
		return false, nil
	}
	now := r.now().UTC()
	u.Params.Now = &now
	err = db.InTenantTx(ctx, r.pool, c.ClubID, func(q *store.Queries) error {
		return q.RecordAgentHello(ctx, u.Params)
	})
	if err != nil {
		return false, fmt.Errorf("presence: record hello: %w", err)
	}
	r.writes.Add(1)
	return u.FreshBoot, nil
}

// recordState пишет agent_state (запрос сам не пишет, если значение то же).
func (r *Recorder) recordState(ctx context.Context, clubID, computerID int64, state string) error {
	now := r.now().UTC()
	var n int64
	err := db.InTenantTx(ctx, r.pool, clubID, func(q *store.Queries) error {
		var err error
		n, err = q.RecordAgentState(ctx, store.RecordAgentStateParams{AgentState: &state, Now: &now, ClubID: clubID, ComputerID: computerID})
		return err
	})
	if n > 0 {
		r.writes.Add(1)
	}
	return err
}

// StateTracker — дебаунс записи agent_state одного соединения.
type StateTracker struct {
	r                  *Recorder
	clubID, computerID int64

	mu        sync.Mutex
	persisted string
	pending   string
	timer     *time.Timer
	closed    bool
}

// Track начинает отслеживание; persisted — agent_state в Postgres сейчас (nil — не задано).
func (r *Recorder) Track(clubID, computerID int64, persisted *string) *StateTracker {
	t := &StateTracker{r: r, clubID: clubID, computerID: computerID}
	if persisted != nil {
		t.persisted = *persisted
	}
	t.pending = t.persisted
	return t
}

// Observe — агент сообщил состояние. Запись — через debounce после первого изменения, итоговым значением.
func (t *StateTracker) Observe(state string) {
	if state == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return
	}
	t.pending = state
	if t.pending != t.persisted && t.timer == nil {
		t.timer = time.AfterFunc(t.r.debounce, t.flushAsync)
	}
}

func (t *StateTracker) flushAsync() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	t.flush(ctx)
}

func (t *StateTracker) flush(ctx context.Context) {
	t.mu.Lock()
	if t.timer != nil {
		t.timer.Stop()
		t.timer = nil
	}
	state := t.pending
	if state == t.persisted || state == "" {
		t.mu.Unlock()
		return
	}
	t.mu.Unlock()

	if err := t.r.recordState(ctx, t.clubID, t.computerID, state); err != nil {
		t.r.log.Warn("agent state not recorded", "club_id", t.clubID, "computer_id", t.computerID, "err", err)
		return
	}
	t.mu.Lock()
	t.persisted = state
	if t.pending != t.persisted && t.timer == nil && !t.closed {
		t.timer = time.AfterFunc(t.r.debounce, t.flushAsync)
	}
	t.mu.Unlock()
}

// Close записывает несохранённое состояние сразу и прекращает отслеживание (стрим закрыт).
func (t *StateTracker) Close(ctx context.Context) {
	t.mu.Lock()
	t.closed = true
	t.mu.Unlock()
	t.flush(ctx)
}
