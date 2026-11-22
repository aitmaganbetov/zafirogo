package stream

import (
	"testing"
	"time"
)

// A-02: потолок частоты Status потока — всплеск burst, затем не чаще одного за every.
func TestStatusLimit(t *testing.T) {
	t0 := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	var l msgLimit
	passed := 0
	for i := 0; i < 1000; i++ { // 1 000 Status за одну секунду
		if l.allow(t0.Add(time.Duration(i)*time.Millisecond), 10, 2*time.Second) {
			passed++
		}
	}
	if passed != 10 {
		t.Fatalf("passed %d of 1000 in one second, want the burst of 10", passed)
	}
	// Через 2 с — ещё ровно один.
	if !l.allow(t0.Add(3*time.Second), 10, 2*time.Second) || l.allow(t0.Add(3*time.Second), 10, 2*time.Second) {
		t.Fatal("one Status per interval after the burst")
	}
	// Долгая пауза ведро не переполняет.
	late := t0.Add(time.Hour)
	passed = 0
	for i := 0; i < 50; i++ {
		if l.allow(late, 10, 2*time.Second) {
			passed++
		}
	}
	if passed != 10 {
		t.Fatalf("after a long pause passed %d, want 10", passed)
	}
	// Честный агент: Status раз в минуту и по три подряд при входе гостя — ничего не отбрасывается.
	var honest msgLimit
	for m := 0; m < 600; m++ {
		at := t0.Add(time.Duration(m) * time.Minute)
		for k := 0; k < 3; k++ {
			if !honest.allow(at.Add(time.Duration(k)*50*time.Millisecond), DefaultStatusBurst, DefaultMinStatusGap) {
				t.Fatalf("honest agent dropped at minute %d", m)
			}
		}
	}
}

func TestStatusFlood(t *testing.T) {
	t0 := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	var l msgLimit
	for i := 1; i < 300; i++ {
		if l.flooding(t0.Add(time.Duration(i)*time.Millisecond), 300) {
			t.Fatalf("flood after %d drops", i)
		}
	}
	if !l.flooding(t0.Add(time.Second), 300) {
		t.Fatal("300 drops within a minute must be a flood")
	}
	// Редкие отбрасывания (окно истекло) потоком не считаются.
	var slow msgLimit
	for i := 0; i < 1000; i++ {
		if slow.flooding(t0.Add(time.Duration(i)*time.Second), 300) {
			t.Fatalf("slow drops counted as a flood at %d", i)
		}
	}
}

// Умолчания AgentEvent и CommandResult: честный агент после переподключения досылает весь журнал событий (1000) и
// недоставленные итоги (32) подряд — ничего не отбрасывается; дальше — не чаще одного за интервал.
func TestEventAndResultLimitDefaults(t *testing.T) {
	t0 := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	var ev, res msgLimit
	for i := 0; i < 1000; i++ {
		if !ev.allow(t0.Add(time.Duration(i)*time.Microsecond), DefaultEventBurst, DefaultEventEvery) {
			t.Fatalf("pending event %d of the agent journal dropped", i)
		}
	}
	for i := 0; i < 32; i++ {
		if !res.allow(t0, DefaultResultBurst, DefaultResultEvery) {
			t.Fatalf("undelivered result %d dropped", i)
		}
	}
	passed := 0
	for i := 0; i < 10000; i++ { // 10 000 сообщений за 10 с
		if ev.allow(t0.Add(time.Second+time.Duration(i)*time.Millisecond), DefaultEventBurst, DefaultEventEvery) {
			passed++
		}
	}
	if passed > 200+11 {
		t.Fatalf("events passed %d, the cap is the rest of the burst (200) and one per second", passed)
	}
}
