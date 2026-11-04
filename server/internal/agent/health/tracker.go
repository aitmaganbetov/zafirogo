package health

import (
	"slices"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/agent/presence"
)

// Transition — переход подтверждённой проблемы: появилась (Raised) или ушла.
type Transition struct {
	Kind    string
	Raised  bool
	Details map[string]any
}

// Open — открытая проблема из журнала (cleared_at IS NULL): восстанавливается при новом подключении ПК.
type Open struct {
	Kind    string
	Details map[string]any
}

// Tracker — защита от дребезга проблем одного ПК (живёт в соединении стрима; одна горутина чтения).
//
// Проблема считается появившейся, когда она есть во всех измерениях не меньше RaiseAfter (по времени
// сервера); ушедшей — когда её нет во всех измерениях не меньше ClearAfter. Переходы фиксируются только после
// записи в журнал (Commit): не записалось — повторится на следующем измерении.
type Tracker struct {
	th      Thresholds
	active  map[string]bool      // подтверждённые (записаны в журнал)
	seen    map[string]time.Time // есть в измерениях с этого момента (ещё не подтверждена)
	gone    map[string]time.Time // подтверждённой нет в измерениях с этого момента
	linkMax uint32
}

// NewTracker — трекер без открытых проблем (Restore — открытые из журнала).
func NewTracker(th Thresholds) *Tracker {
	return &Tracker{th: th, active: map[string]bool{}, seen: map[string]time.Time{}, gone: map[string]time.Time{}}
}

// Restore — открытые проблемы ПК из журнала (новое подключение): они уже записаны, повторной строки не будет.
// Для упавшей скорости сети прежняя «полная» скорость берётся из подробностей (previous_mbps).
func (t *Tracker) Restore(open []Open) {
	for _, o := range open {
		t.active[o.Kind] = true
		if o.Kind == KindNetworkSlow {
			if v, ok := uintOf(o.Details["previous_mbps"]); ok && v > uint64(t.linkMax) && v <= 1<<31 {
				t.linkMax = uint32(v) // #nosec G115 -- ограничено выше
			}
		}
	}
}

func uintOf(v any) (uint64, bool) {
	switch x := v.(type) {
	case float64:
		if x >= 0 {
			return uint64(x), true
		}
	case uint32:
		return uint64(x), true
	case uint64:
		return x, true
	case int64:
		if x >= 0 {
			return uint64(x), true
		}
	}
	return 0, false
}

// Active — подтверждённые проблемы по возрастанию.
func (t *Tracker) Active() []string {
	out := make([]string, 0, len(t.active))
	for k := range t.active {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// Observe — новое измерение в момент now; возвращает переходы, которые надо записать (Commit после записи).
func (t *Tracker) Observe(h *presence.Health, now time.Time) []Transition {
	if h == nil {
		return nil
	}
	raw := Evaluate(h, t.th, t.active, t.linkMax)
	if h.LinkMbps > t.linkMax {
		t.linkMax = h.LinkMbps
	}
	var out []Transition
	for _, kind := range raw.Sorted() {
		delete(t.gone, kind)
		if t.active[kind] {
			continue
		}
		since, ok := t.seen[kind]
		if !ok {
			since = now
			t.seen[kind] = now
		}
		if now.Sub(since) >= t.th.RaiseAfter {
			out = append(out, Transition{Kind: kind, Raised: true, Details: raw[kind]})
		}
	}
	for kind := range t.seen {
		if _, ok := raw[kind]; !ok {
			delete(t.seen, kind)
		}
	}
	for _, kind := range t.Active() {
		if _, ok := raw[kind]; ok {
			continue
		}
		since, ok := t.gone[kind]
		if !ok {
			since = now
			t.gone[kind] = now
		}
		if now.Sub(since) >= t.th.ClearAfter {
			out = append(out, Transition{Kind: kind})
		}
	}
	return out
}

// Commit фиксирует записанные переходы.
func (t *Tracker) Commit(trs []Transition) {
	for _, tr := range trs {
		if tr.Raised {
			t.active[tr.Kind] = true
			delete(t.seen, tr.Kind)
		} else {
			delete(t.active, tr.Kind)
			delete(t.gone, tr.Kind)
		}
	}
}
