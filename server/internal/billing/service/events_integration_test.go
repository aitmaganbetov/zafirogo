//go:build integration

package service_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
	"github.com/aitmaganbetov/zafirogo/server/internal/activity"
	"github.com/aitmaganbetov/zafirogo/server/internal/billing/pricing"
	"github.com/aitmaganbetov/zafirogo/server/internal/billing/service"
)

// recNotifier — уведомления агента (что и когда вызвано).
type recNotifier struct {
	mu    sync.Mutex
	calls []string
}

func (n *recNotifier) add(s string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.calls = append(n.calls, s)
}

func (n *recNotifier) SessionStarted(_ context.Context, _, _ int64, s *agentv1.ActiveSession, _ int64) error {
	n.add("started:" + s.GetSessionId())
	return nil
}

func (n *recNotifier) SessionEnded(_ context.Context, _, _ int64, e *agentv1.EndSession, _ int64) error {
	n.add("ended:" + e.GetReason().String())
	return nil
}

func (n *recNotifier) BalanceChanged(_ context.Context, _, _ int64, _ *agentv1.Balance) error {
	n.add("balance")
	return nil
}

func (n *recNotifier) list() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.calls...)
}

// metaCtx — контекст запроса с IP и User-Agent (как ставит HTTP/gRPC-middleware): журнал пишется.
func metaCtx() context.Context {
	return activity.WithRequestMeta(context.Background(), activity.RequestMeta{IP: "10.0.0.7", UserAgent: "test"})
}

func (e *env) details(event string) map[string]any {
	e.t.Helper()
	raw := scalar[[]byte](e.t, "SELECT details FROM activity_logs WHERE club_id = $1 AND event = $2 ORDER BY id DESC LIMIT 1", e.club, event)
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		e.t.Fatal(err)
	}
	return m
}

func (e *env) events(event string) int64 {
	return scalar[int64](e.t, "SELECT count(*) FROM activity_logs WHERE club_id = $1 AND event = $2", e.club, event)
}

func (e *env) outbox(typ string) int64 {
	return scalar[int64](e.t, "SELECT count(*) FROM outbox_events WHERE club_id = $1 AND type = $2", e.club, typ)
}

// Журнал действий, события админки и уведомления агента сервиса покупок/сессий (SV-8).
func TestServiceEvents(t *testing.T) {
	t.Run("time.purchased+GS-088", func(t *testing.T) {
		e := newEnv(t, "")
		n := &recNotifier{}
		e.svc.SetNotifier(n)
		pc := e.computer(e.zone("Hall", ptr(int64(60000))), 1)
		u := e.customer("Клиент", 1000*tenge, 0)
		r, err := e.svc.PurchaseMinutes(metaCtx(), service.MinutesPurchaseRequest{
			ClubID: e.club, UserID: u, ComputerID: pc, Minutes: 60, Balance: pricing.ChoiceMain, IdempotencyKey: key("m"),
			Actor: service.Actor{UserID: u, Kind: "guest"},
		})
		if err != nil {
			t.Fatal(err)
		}
		d := e.details("time.purchased")
		for k, want := range map[string]any{
			"amount": "600.00", "currency": "KZT", "price_per_hour": "600.00", "main_balance_amount": "600.00",
			"bonus_balance_amount": "0.00", "duration_minutes": float64(60), "session_id": r.SessionID,
			"session_ends_at": "2026-10-15T11:00:00+00:00",
		} {
			if d[k] != want {
				t.Errorf("time.purchased %s = %v, want %v", k, d[k], want)
			}
		}
		eq(t, "computer.state", e.outbox("computer.state"), 1)
		eq(t, "balance.changed", e.outbox("balance.changed"), 1)
		eq(t, "notify", len(n.list()), 2)

		// GS-088: сотрудник завершает досрочно — computer.session_ended от него, возврат time.refunded.
		staff := e.customer("Кассир", 0, 0)
		e.clock.travel(30 * time.Minute)
		_, err = e.svc.EndSession(metaCtx(), service.EndSessionRequest{
			ClubID: e.club, ComputerID: pc, SessionID: r.SessionID, Actor: service.Actor{UserID: staff, Kind: "staff"}, IsStaff: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		eq(t, "session_ended by staff", scalar[int64](t,
			"SELECT count(*) FROM activity_logs WHERE club_id = $1 AND event = 'computer.session_ended' AND user_id = $2 AND subject_id = $3",
			e.club, staff, pc), 1)
		eq(t, "planned_end", e.details("computer.session_ended")["planned_end"], any("2026-10-15T11:00:00+00:00"))
		eq(t, "time.refunded", e.details("time.refunded")["main_balance_amount"], any("300.00"))
		eq(t, "computer.state", e.outbox("computer.state"), 2)
		calls := n.list()
		eq(t, "ended", calls[len(calls)-2], "ended:SESSION_END_REASON_STAFF")
		eq(t, "balance after end", calls[len(calls)-1], "balance")
	})

	t.Run("tariff.purchased+time.package_kept", func(t *testing.T) {
		e := newEnv(t, "")
		pc := e.computer(e.zone("Hall", nil), 1)
		tr := e.tariff(tariffSpec{name: "Ночь", minutes: 120, price: 100 * tenge, usageLimit: ptr(int64(600))})
		u := e.customer("Клиент", 1000*tenge, 0)
		r, err := e.svc.PurchaseTariff(metaCtx(), service.TariffPurchaseRequest{
			ClubID: e.club, UserID: u, TariffID: tr, ComputerID: pc, Balance: pricing.ChoiceMain, IdempotencyKey: key("t"),
			Actor: service.Actor{UserID: u, Kind: "guest"},
		})
		if err != nil {
			t.Fatal(err)
		}
		d := e.details("tariff.purchased")
		eq(t, "amount", d["amount"], any("100.00"))
		eq(t, "currency", d["currency"], any("KZT"))
		eq(t, "session_id", d["session_id"], any(r.SessionID))
		e.clock.travel(60 * time.Minute)
		if _, err := e.svc.EndSession(metaCtx(), service.EndSessionRequest{
			ClubID: e.club, ComputerID: pc, SessionID: r.SessionID, Actor: service.Actor{UserID: u, Kind: "guest"},
		}); err != nil {
			t.Fatal(err)
		}
		eq(t, "kept", e.details("time.package_kept")["minutes"], any(float64(60)))
	})

	t.Run("GS-103/session.transferred", func(t *testing.T) {
		e := newEnv(t, "")
		n := &recNotifier{}
		e.svc.SetNotifier(n)
		hall := e.zone("Hall", ptr(int64(60000)))
		from, to := e.computer(hall, 1), e.computer(hall, 2)
		u := e.customer("Арман", 1000*tenge, 0)
		r := must[service.PurchaseResult](t)(e.buyMinutes(u, from, 60, pricing.ChoiceMain))
		if _, err := e.svc.TransferSeat(metaCtx(), service.TransferRequest{ClubID: e.club, UserID: u, FromComputerID: from, TargetComputerID: to}); err != nil {
			t.Fatal(err)
		}
		eq(t, "event", e.events("session.transferred"), 1)
		d := e.details("session.transferred")
		eq(t, "from", d["from"], any("PC-01"))
		eq(t, "to", d["to"], any("PC-02"))
		eq(t, "session", d["session_id"], any(r.SessionID))
		eq(t, "description", scalar[string](t, "SELECT description FROM activity_logs WHERE club_id = $1 AND event = 'session.transferred'", e.club),
			"Арман пересел с ПК PC-01 на ПК PC-02")
		calls := n.list()
		eq(t, "notify ended", calls[len(calls)-2], "ended:SESSION_END_REASON_SEAT_TRANSFER")
		eq(t, "notify started", calls[len(calls)-1], "started:"+r.SessionID)
	})

	t.Run("без данных запроса журнал не пишется, ошибка откатывает событие", func(t *testing.T) {
		e := newEnv(t, "")
		pc := e.computer(e.zone("Hall", ptr(int64(60000))), 1)
		u := e.customer("Клиент", 10*tenge, 0)
		must[service.PurchaseResult](t)(e.buyMinutes(u, pc, 1, pricing.ChoiceMain))
		eq(t, "no meta — no journal", e.events("time.purchased"), 0)
		eq(t, "outbox", e.outbox("computer.state"), 1)
		// Недостаточно средств — транзакция откатилась, события нет.
		if _, err := e.svc.PurchaseMinutes(metaCtx(), service.MinutesPurchaseRequest{
			ClubID: e.club, UserID: u, ComputerID: pc, Minutes: 600, Balance: pricing.ChoiceMain, IdempotencyKey: key("m"),
		}); err == nil {
			t.Fatal("want error")
		}
		eq(t, "rolled back", e.outbox("computer.state"), 1)
		eq(t, "rolled back journal", e.events("time.purchased"), 0)
	})
}
