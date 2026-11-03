package stream

import (
	"testing"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/agent/presence"
)

func TestRebalanceRelease(t *testing.T) {
	o := RebalanceOptions{Threshold: 1.2, MinExcess: 50, Batch: 100}
	cases := []struct {
		name       string
		local      int
		counts     []int
		active     bool
		want       int
		wantActive bool
	}{
		{"равномерно", 2500, []int{2500, 2500}, false, 0, false},
		{"5000/0 — пачка не больше Batch", 5000, []int{5000, 0}, false, 100, true},
		{"5000/0/0 — три инстанса", 5000, []int{5000, 0, 0}, false, 100, true},
		{"6000/6000/0 — два перегруженных", 6000, []int{6000, 6000, 0}, false, 100, true},
		{"маленькая разница ниже порога 1,2", 2700, []int{2700, 2300}, false, 0, false},
		{"превышение меньше MinExcess", 40, []int{40, 0}, false, 0, false},
		{"один инстанс", 5000, []int{5000}, false, 0, false},
		{"этот инстанс недогружен", 0, []int{0, 5000}, false, 0, false},
		{"идёт: превышение 300 — ещё пачка", 2800, []int{2800, 2200}, true, 100, true},
		{"идёт: превышение 80 < Batch — ровно до доли", 2580, []int{2580, 2420}, true, 80, true},
		{"идёт: превышение ≤ MinExcess/2 — стоп", 2520, []int{2520, 2480}, true, 0, false},
		{"не идёт: та же разница не начинает", 2800, []int{2800, 2200}, false, 0, false},
		{"нечётный итог: доля с округлением вверх", 101, []int{101, 0}, true, 50, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, active := rebalanceRelease(tc.local, tc.counts, tc.active, o)
			if got != tc.want || active != tc.wantActive {
				t.Fatalf("rebalanceRelease(%d, %v, %v) = %d, %v; want %d, %v", tc.local, tc.counts, tc.active, got, active,
					tc.want, tc.wantActive)
			}
		})
	}
}

// Без «маятника»: после выравнивания 5000/0 ни один из двух инстансов не начинает снова.
func TestRebalanceConvergesWithoutPendulum(t *testing.T) {
	o := RebalanceOptions{Threshold: 1.2, MinExcess: 50, Batch: 100}
	a, b := 5000, 0
	activeA, activeB := false, false
	for round := 0; round < 100; round++ {
		var ra, rb int
		ra, activeA = rebalanceRelease(a, []int{a, b}, activeA, o)
		rb, activeB = rebalanceRelease(b, []int{b, a}, activeB, o)
		if ra > 100 || rb > 100 {
			t.Fatalf("round %d: batch exceeded: %d %d", round, ra, rb)
		}
		a, b = a-ra+rb, b+ra-rb // least_conn отдаёт отпущенных другому
		if rb > 0 {
			t.Fatalf("round %d: underloaded instance released %d (a=%d b=%d)", round, rb, a, b)
		}
	}
	if a-b > 50 || b > a {
		t.Fatalf("not converged: a=%d b=%d", a, b)
	}
}

func TestParseStreamsValue(t *testing.T) {
	if n, u, ok := parseStreamsValue("2500|1700000000000"); !ok || n != 2500 || u != 1700000000000 {
		t.Fatalf("got %d %d %v", n, u, ok)
	}
	for _, bad := range []string{"", "2500", "x|1", "1|x", "-1|5"} {
		if _, _, ok := parseStreamsValue(bad); ok {
			t.Errorf("%q must be rejected", bad)
		}
	}
}

func TestMarkMovable(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	base := func() *conn {
		return &conn{done: make(chan struct{}), inflight: map[string]*inflight{},
			st: presence.State{AgentState: presence.StateIdle, ConnectedAt: now.Add(-time.Minute)}}
	}
	cases := map[string]struct {
		mut  func(c *conn)
		want bool
	}{
		"простаивает":        {func(*conn) {}, true},
		"экран блокировки":   {func(c *conn) { c.st.AgentState = presence.StateLocked }, true},
		"сессия":             {func(c *conn) { c.st.AgentState = presence.StateInSession }, false},
		"сервисный режим":    {func(c *conn) { c.st.AgentState = presence.StateService }, false},
		"нет Status":         {func(c *conn) { c.st.AgentState = "" }, false},
		"гость вошёл":        {func(c *conn) { c.st.SignedInUserID = 7 }, false},
		"id сессии":          {func(c *conn) { c.st.SessionID = "42" }, false},
		"команда в пути":     {func(c *conn) { c.inflight["x"] = &inflight{} }, false},
		"очередь отправки":   {func(c *conn) { c.queue = append(c.queue, nil) }, false},
		"досылка":            {func(c *conn) { c.resync = true }, false},
		"только подключился": {func(c *conn) { c.st.ConnectedAt = now.Add(-time.Second) }, false},
		"уже просили":        {func(c *conn) { c.rebalanced = true }, false},
		"стрим закрыт":       {func(c *conn) { close(c.done) }, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := base()
			tc.mut(c)
			if got := c.markMovable(now, 15*time.Second); got != tc.want {
				t.Fatalf("markMovable = %v, want %v", got, tc.want)
			}
			if tc.want && (!c.rebalanced || c.markMovable(now, 15*time.Second)) {
				t.Fatal("a picked computer must be marked and not picked twice")
			}
		})
	}
}
