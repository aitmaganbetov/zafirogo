//go:build integration

package stream

import (
	"context"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/health"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/presence"
)

// liveRecorder — LiveListener для тестов.
type liveRecorder struct {
	mu  sync.Mutex
	got []presence.State
}

func (l *liveRecorder) LiveChanged(st presence.State) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.got = append(l.got, st)
}

func (l *liveRecorder) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.got)
}

func (l *liveRecorder) last() presence.State {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.got[len(l.got)-1]
}

func healthStatus(cpuTemp int32, mouse bool) *agentv1.AgentMessage {
	return &agentv1.AgentMessage{Payload: &agentv1.AgentMessage_Status{Status: &agentv1.Status{
		State: agentv1.ComputerState_COMPUTER_STATE_IDLE, Load: &agentv1.ComputerLoad{CpuBasisPoints: 2500, RamBasisPoints: 4000},
		Health: &agentv1.ComputerHealth{
			CpuTempDecicelsius: cpuTemp, LinkSpeedMbps: 1000, UptimeSeconds: 3600,
			Disks:       []*agentv1.DiskHealth{{Mount: "C:", System: true, TotalMb: 512000, FreeMb: 256000, Smart: agentv1.DiskSmart_DISK_SMART_OK}},
			Peripherals: []*agentv1.Peripheral{{Kind: agentv1.PeripheralKind_PERIPHERAL_KIND_MOUSE, Connected: mouse, Name: "G102"}},
		},
	}}}
}

func healthRows(t *testing.T, pc int64) (open, total int) {
	t.Helper()
	if err := testEnv.db.Owner.QueryRow(context.Background(), `SELECT count(*) FILTER (WHERE cleared_at IS NULL), count(*)
		FROM computer_health_events WHERE computer_id = $1`, pc).Scan(&open, &total); err != nil {
		t.Fatal(err)
	}
	return
}

func healthTune(live *liveRecorder) func(*Options) {
	return func(o *Options) {
		th := health.Defaults()
		th.RaiseAfter, th.ClearAfter = 0, 0
		o.Health = HealthOptions{Thresholds: th, LiveMinGap: time.Hour}
		o.Live = live
	}
}

// Status с health → снимок присутствия (Valkey) → проблемы по порогам → одна строка истории на переход; тот же
// Status повторно — без новых строк; Status без health прежнее здоровье не стирает; неверное health отбрасывается,
// поток не рвётся; живое обновление — при смене набора проблем.
func TestHealthStatusToSnapshotAndHistory(t *testing.T) {
	ctx := context.Background()
	club := testEnv.club(t)
	pc := testEnv.computer(t, club, 1)
	live := &liveRecorder{}
	in := testEnv.instance(t, "a", healthTune(live))
	a, _ := in.connect(t, club, pc, nil)

	a.send(healthStatus(955, false))
	eventually(t, 3*time.Second, "problems in presence", func() bool {
		st, ok, err := in.svc.Presence().Get(ctx, club, pc)
		return err == nil && ok && st.Health != nil && slices.Equal(st.Problems, []string{health.KindCPUHot, health.KindMouse})
	})
	st, _, _ := in.svc.Presence().Get(ctx, club, pc)
	if st.Health.CPUTempDeci != 955 || len(st.Health.Disks) != 1 || st.Health.Disks[0].Smart != presence.SmartOK ||
		!st.HasLoad || st.Load.RAMBasisPoints != 4000 {
		t.Fatalf("snapshot: %+v %+v", st, st.Health)
	}
	if open, total := healthRows(t, pc); open != 2 || total != 2 {
		t.Fatalf("history after raise: open %d, total %d", open, total)
	}
	var details string
	if err := testEnv.db.Owner.QueryRow(ctx, `SELECT details::text FROM computer_health_events WHERE computer_id = $1 AND kind = 'cpu_hot'`,
		pc).Scan(&details); err != nil || details != `{"temp_deci": 955, "threshold_c": 90}` {
		t.Fatalf("details = %s (%v)", details, err)
	}
	eventually(t, 2*time.Second, "live update on new problems", func() bool {
		return live.count() >= 1 && slices.Equal(live.last().Problems, []string{health.KindCPUHot, health.KindMouse})
	})
	writes, lives := in.svc.HealthJournal().Writes(), live.count()

	// Тот же Status 10 раз — ни строки в истории, ни живых обновлений (LiveMinGap час, набор проблем тот же).
	for range 10 {
		a.send(healthStatus(955, false))
		time.Sleep(5 * time.Millisecond)
	}
	// Status без health и неверное health — здоровье прежнее, поток жив.
	a.status(agentv1.ComputerState_COMPUTER_STATE_IDLE, 2600)
	time.Sleep(5 * time.Millisecond)
	bad := healthStatus(955, false)
	bad.GetStatus().Health.Peripherals[0].Name = "evil\u202Egpj.exe"
	bad.GetStatus().Load.CpuBasisPoints = 2700 // остальное из Status с неверным health принимается
	a.send(bad)
	eventually(t, 2*time.Second, "invalid health dropped", func() bool { return in.svc.HealthDropped() == 1 })
	time.Sleep(200 * time.Millisecond)
	if got := in.svc.HealthJournal().Writes(); got != writes {
		t.Fatalf("same state wrote history: %d → %d", writes, got)
	}
	if open, total := healthRows(t, pc); open != 2 || total != 2 {
		t.Fatalf("history after repeats: open %d, total %d", open, total)
	}
	if live.count() != lives {
		t.Fatalf("live updates without notable change: %d → %d", lives, live.count())
	}
	st, _, _ = in.svc.Presence().Get(ctx, club, pc)
	if st.Health == nil || st.Health.CPUTempDeci != 955 || st.Health.Peripherals[0].Name != "G102" || st.Load.CPUBasisPoints != 2700 {
		t.Fatalf("status without (valid) health must keep the previous one: %+v %+v", st, st.Health)
	}

	// Остыл, мышь вернулась — обе проблемы закрыты, новых строк нет.
	a.send(healthStatus(600, true))
	eventually(t, 3*time.Second, "problems cleared", func() bool {
		open, total := healthRows(t, pc)
		return open == 0 && total == 2
	})
	eventually(t, 2*time.Second, "presence without problems", func() bool {
		st, _, _ := in.svc.Presence().Get(ctx, club, pc)
		return len(st.Problems) == 0
	})
	if l := live.last(); len(l.Problems) != 0 {
		t.Fatalf("live update after clear: %+v", l.Problems)
	}

	// RLS: транзакция другого клуба строк истории не видит.
	other := testEnv.club(t)
	tx, err := testEnv.db.App.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "SELECT set_config('app.club_id', $1, true)", strconv.FormatInt(other, 10)); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := tx.QueryRow(ctx, "SELECT count(*) FROM computer_health_events WHERE computer_id = $1", pc).Scan(&n); err != nil || n != 0 {
		t.Fatalf("foreign club sees %d rows (%v)", n, err)
	}
	// Пул клуба не может удалить или подменить историю (только закрыть).
	if _, err := tx.Exec(ctx, "SELECT set_config('app.club_id', $1, true)", strconv.FormatInt(club, 10)); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, "DELETE FROM computer_health_events WHERE computer_id = $1", pc); err == nil {
		t.Fatal("zafiro_app deleted health history")
	}
}

// Переподключение с той же открытой проблемой не пишет её второй раз (открытые читаются из журнала).
func TestHealthReconnectNoDuplicate(t *testing.T) {
	club := testEnv.club(t)
	pc := testEnv.computer(t, club, 2)
	in := testEnv.instance(t, "a", healthTune(&liveRecorder{}))
	a, _ := in.connect(t, club, pc, nil)
	a.send(healthStatus(600, false))
	eventually(t, 3*time.Second, "mouse problem", func() bool { open, _ := healthRows(t, pc); return open == 1 })
	a.hangUp()

	h := defaultHello()
	h.Status = healthStatus(600, false).GetStatus()
	b, _ := in.connect(t, club, pc, h)
	b.send(healthStatus(600, false))
	eventually(t, 3*time.Second, "problem restored in presence", func() bool {
		st, ok, _ := in.svc.Presence().Get(context.Background(), club, pc)
		return ok && st.Online && slices.Equal(st.Problems, []string{health.KindMouse})
	})
	time.Sleep(200 * time.Millisecond)
	if open, total := healthRows(t, pc); open != 1 || total != 1 {
		t.Fatalf("reconnect duplicated the problem: open %d, total %d", open, total)
	}
}

// Время ответа агента: Ping по стриму, ответ CommandResult → rtt_ms в снимке.
func TestHealthPingRTT(t *testing.T) {
	club := testEnv.club(t)
	pc := testEnv.computer(t, club, 3)
	in := testEnv.instance(t, "a", func(o *Options) { o.Health.PingInterval = 200 * time.Millisecond })
	a, _ := in.connect(t, club, pc, nil)
	cmd := a.command(3 * time.Second)
	if cmd.GetPing() == nil || cmd.GetSeq() != 0 || !validUUID(cmd.GetCommandId()) {
		t.Fatalf("expected Ping, got %v", cmd)
	}
	time.Sleep(15 * time.Millisecond)
	a.result(cmd, agentv1.CommandStatus_COMMAND_STATUS_SUCCEEDED)
	a.status(agentv1.ComputerState_COMPUTER_STATE_IDLE, 100) // снимок пишется с ближайшим Status
	eventually(t, 3*time.Second, "rtt in presence", func() bool {
		st, ok, _ := in.svc.Presence().Get(context.Background(), club, pc)
		return ok && st.RTTMillis >= 15
	})
}

// S7-HA-2: агент шлёт Status с health сразу при отключении периферии — он не отбрасывается ограничением частоты
// Status (MinStatusGap), снимок (и GET /api/club-computers/{id}/health) видит изменение сразу; температура в
// пределах того же окна — отбрасывается, как прежде.
func TestHealthPeripheralChangeBypassesStatusGap(t *testing.T) {
	ctx := context.Background()
	club := testEnv.club(t)
	pc := testEnv.computer(t, club, 4)
	in := testEnv.instance(t, "a", func(o *Options) { o.MinStatusGap = time.Hour })
	a, _ := in.connect(t, club, pc, nil)
	a.send(healthStatus(600, true))
	eventually(t, 3*time.Second, "health in presence", func() bool {
		st, ok, _ := in.svc.Presence().Get(ctx, club, pc)
		return ok && st.Health != nil && st.Health.Peripherals[0].Connected
	})
	a.send(healthStatus(700, true)) // только температура — в пределах MinStatusGap отбрасывается
	a.send(healthStatus(600, false))
	eventually(t, 3*time.Second, "mouse disconnected in presence", func() bool {
		st, _, _ := in.svc.Presence().Get(ctx, club, pc)
		return st.Health != nil && !st.Health.Peripherals[0].Connected
	})
	st, _, _ := in.svc.Presence().Get(ctx, club, pc)
	if st.Health.CPUTempDeci != 600 {
		t.Fatalf("temperature-only status within the gap must still be dropped: %d", st.Health.CPUTempDeci)
	}
}
