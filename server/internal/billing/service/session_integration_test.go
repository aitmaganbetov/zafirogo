//go:build integration

package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/billing/pricing"
	"github.com/aitmaganbetov/zafirogo/server/internal/billing/service"
	"github.com/aitmaganbetov/zafirogo/server/internal/worker"
)

// Завершение сессии с расчётом, пакеты, пересадка, истечение.
func TestSessionEnd(t *testing.T) {
	t.Run("GS-046", func(t *testing.T) {
		// 1000.00 / 1000.00; bonus_max_percent 50, смешанная; 60 мин бонусами (600: 300 + 300); через 30 мин конец.
		e := newEnv(t, `{"finances": {"allow_mixed_payment": true, "bonus_max_percent": 50}}`)
		pc := e.computer(e.zone("Hall", ptr(int64(60000))), 4)
		u := e.customer("Клиент", 1000*tenge, 1000*tenge)
		r := must[service.PurchaseResult](t)(e.buyMinutes(u, pc, 60, pricing.ChoiceBonus))
		e.wantBalances(u, 700*tenge, 700*tenge)
		e.clock.travel(30 * time.Minute)
		res := must[service.EndSessionResult](t)(e.end(pc, r.SessionID, u, false))
		eq(t, "refunded", res.RefundMain.Tenge(), "150.00")
		eq(t, "refunded_bonus", res.RefundBonus.Tenge(), "150.00")
		eq(t, "kept", res.KeptMinutes, 0)
		e.wantBalances(u, 850*tenge, 850*tenge)
		eq(t, "pc free", e.pc(pc).SessionID == nil, true)
		eq(t, "parts", e.count("session_purchases"), 0)
		rows := e.ledgerOf(r.SessionID)
		eqRows(t, rows[4:], []ledgerRow{
			{"session_refund", "bonus_fund", 0, -15000}, {"session_refund", "member_bonus", u, 15000},
			{"session_refund", "member_main", u, 15000}, {"session_refund", "revenue_time", 0, -15000},
		})
		e.wantTotalSpent(u, 150*tenge) // 300 − 150 (GAP-002: траты уменьшаются на возврат с основного)
		eq(t, "audit", scalar[int64](t, "SELECT count(*) FROM audit_log WHERE club_id = $1 AND action = 'money.session_refund'", e.club), 1)
		e.noDrift()
	})

	t.Run("GAP-001", func(t *testing.T) {
		// 17 мин при 500 ₸/час: main 10000, bonus 4167; конец через 5 мин 20 с (осталось 700 с).
		e := newEnv(t, `{"finances": {"allow_mixed_payment": true}}`)
		pc := e.computer(e.zone("Hall", ptr(int64(50000))), 1)
		u := e.customer("Клиент", 100*tenge, 50*tenge)
		r := must[service.PurchaseResult](t)(e.buyMinutes(u, pc, 17, pricing.ChoiceNone))
		e.clock.travel(5*time.Minute + 20*time.Second)
		res := must[service.EndSessionResult](t)(e.end(pc, r.SessionID, u, false))
		eq(t, "refund_main", int64(res.RefundMain), 6862)
		eq(t, "refund_bonus", int64(res.RefundBonus), 2859)
		e.wantBalances(u, 6862, 833+2859)
		e.wantTotalSpent(u, 10000-6862)
		e.noDrift()
	})

	t.Run("GAP-002", func(t *testing.T) {
		e := newEnv(t, "")
		pc := e.computer(e.zone("Hall", ptr(int64(60000))), 1)
		u := e.customer("Клиент", 1000*tenge, 0)
		r := must[service.PurchaseResult](t)(e.buyMinutes(u, pc, 60, pricing.ChoiceMain))
		e.wantTotalSpent(u, 600*tenge)
		e.clock.travel(30 * time.Minute)
		must[service.EndSessionResult](t)(e.end(pc, r.SessionID, u, false))
		e.wantBalances(u, 700*tenge, 0)
		e.wantTotalSpent(u, 300*tenge)
	})

	t.Run("GAP-013", func(t *testing.T) {
		// Траты меньше возврата (сброшены вручную) — не ниже нуля.
		e := newEnv(t, "")
		pc := e.computer(e.zone("Hall", ptr(int64(60000))), 1)
		u := e.customer("Клиент", 1000*tenge, 0)
		r := must[service.PurchaseResult](t)(e.buyMinutes(u, pc, 60, pricing.ChoiceMain))
		exec(t, "UPDATE club_memberships SET total_spent = 20000 WHERE club_id = $1 AND user_id = $2", e.club, u)
		e.clock.travel(10 * time.Minute)
		must[service.EndSessionResult](t)(e.end(pc, r.SessionID, u, false))
		e.wantTotalSpent(u, 0)
	})

	t.Run("GS-047", func(t *testing.T) {
		// «Пакет» 120 мин, 1000 ₸, лимит использования 43200 (30 дней); 1000.00 / 0.
		e := newEnv(t, "")
		pc := e.computer(e.zone("Hall", nil), 4)
		tr := e.tariff(tariffSpec{name: "Пакет", minutes: 120, price: 1000 * tenge, usageLimit: ptr(int64(43200))})
		u := e.customer("Клиент", 1000*tenge, 0)
		r := must[service.PurchaseResult](t)(e.buyTariff(u, tr, pc, pricing.ChoiceMain))
		eq(t, "minutes_added", r.MinutesAdded, 120)
		e.clock.travel(30 * time.Minute)
		res := must[service.EndSessionResult](t)(e.end(pc, r.SessionID, u, false))
		eq(t, "refunded", res.RefundMain, 0)
		eq(t, "kept", res.KeptMinutes, 90)
		eq(t, "packages", len(res.PackageIDs), 1)
		pkg := res.PackageIDs[0]
		expires := t0.Add(30 * 24 * time.Hour)
		eq(t, "minutes_left", scalar[int64](t, "SELECT minutes_left FROM time_packages WHERE id = $1", pkg), 90)
		eq(t, "expires_at", scalar[time.Time](t, "SELECT expires_at FROM time_packages WHERE id = $1", pkg).UTC(), expires)
		eq(t, "ledger minutes", e.minutesOf(pkg), 90)
		eq(t, "kind", scalar[string](t, "SELECT kind FROM ledger_transactions WHERE club_id = $1 AND id = $2", e.club, res.LedgerTransactionID), "package_kept")

		// Старт пакета: 90 мин бесплатно, пакет удалён, конец ≈ now + 90.
		s := must[service.PurchaseResult](t)(e.svc.StartPackage(context.Background(), service.PackageStartRequest{
			ClubID: e.club, UserID: u, PackageID: pkg, ComputerID: pc, IdempotencyKey: key("pkg"),
		}))
		eq(t, "minutes_added", s.MinutesAdded, 90)
		e.wantBalances(u, 0, 0)
		eq(t, "packages left", e.count("time_packages"), 0)
		eq(t, "ends", e.pc(pc).EndsAt.UTC(), e.clock.Now().Add(90*time.Minute))
		eq(t, "ledger minutes after start", e.minutesOf(pkg), 0)
		eq(t, "part source", scalar[string](t, "SELECT source FROM session_purchases WHERE club_id = $1", e.club), "package")

		e.clock.travel(60 * time.Minute)
		res = must[service.EndSessionResult](t)(e.end(pc, s.SessionID, u, false))
		eq(t, "kept again", res.KeptMinutes, 30)
		eq(t, "same expiry", scalar[time.Time](t, "SELECT expires_at FROM time_packages WHERE id = $1", res.PackageIDs[0]).UTC(), expires)
		e.noDrift()
	})

	t.Run("GAP-003", func(t *testing.T) {
		e := newEnv(t, "")
		pc := e.computer(e.zone("Hall", nil), 1)
		tr := e.tariff(tariffSpec{name: "Пакет", minutes: 120, price: 1000 * tenge, usageLimit: ptr(int64(43200))})
		u := e.customer("Клиент", 1000*tenge, 0)
		r := must[service.PurchaseResult](t)(e.buyTariff(u, tr, pc, pricing.ChoiceMain))
		e.clock.travel(29*time.Minute + 30*time.Second)
		res := must[service.EndSessionResult](t)(e.end(pc, r.SessionID, u, false))
		eq(t, "kept", res.KeptMinutes, 90)
		e.wantBalances(u, 0, 0)
	})

	t.Run("GS-048", func(t *testing.T) {
		// 2000.00; тариф 60 мин, 500 ₸ без лимита; 30 мин поминутно (300); через 15 мин конец.
		e := newEnv(t, "")
		pc := e.computer(e.zone("Hall", ptr(int64(60000))), 1)
		tr := e.tariff(tariffSpec{name: "Час", minutes: 60, price: 500 * tenge})
		u := e.customer("Клиент", 2000*tenge, 0)
		r := must[service.PurchaseResult](t)(e.buyTariff(u, tr, pc, pricing.ChoiceMain))
		must[service.PurchaseResult](t)(e.buyMinutes(u, pc, 30, pricing.ChoiceMain))
		e.wantBalances(u, 1200*tenge, 0)
		e.clock.travel(15 * time.Minute)
		res := must[service.EndSessionResult](t)(e.end(pc, r.SessionID, u, false))
		eq(t, "refunded", res.RefundMain.Tenge(), "300.00")
		eq(t, "kept", res.KeptMinutes, 0)
		e.wantBalances(u, 1500*tenge, 0)
		eq(t, "packages", e.count("time_packages"), 0)
	})

	t.Run("GAP-004", func(t *testing.T) {
		e := newEnv(t, "")
		pc := e.computer(e.zone("Hall", ptr(int64(60000))), 1)
		tr := e.tariff(tariffSpec{name: "Час", minutes: 60, price: 500 * tenge, usageLimit: ptr(int64(1440))})
		u := e.customer("Клиент", 2000*tenge, 0)
		r := must[service.PurchaseResult](t)(e.buyTariff(u, tr, pc, pricing.ChoiceMain))
		must[service.PurchaseResult](t)(e.buyMinutes(u, pc, 30, pricing.ChoiceMain))
		e.clock.travel(10 * time.Minute)
		res := must[service.EndSessionResult](t)(e.end(pc, r.SessionID, u, false))
		eq(t, "refunded", res.RefundMain.Tenge(), "300.00")
		eq(t, "kept", res.KeptMinutes, 50)
		e.wantBalances(u, 1500*tenge, 0)
		eq(t, "expires", scalar[time.Time](t, "SELECT expires_at FROM time_packages WHERE id = $1", res.PackageIDs[0]).UTC(), t0.Add(1440*time.Minute))
		// Одна операция: деньги и минуты пакета вместе.
		eq(t, "kind", scalar[string](t, "SELECT kind FROM ledger_transactions WHERE club_id = $1 AND id = $2", e.club, res.LedgerTransactionID), "session_refund")
		eq(t, "ledger minutes", e.minutesOf(res.PackageIDs[0]), 50)
		e.noDrift()
	})

	t.Run("GAP-014", func(t *testing.T) {
		// Пакет 90 мин, истекает через 40 мин 30 с → сессия 40 мин; пакет удалён.
		e := newEnv(t, "")
		pc := e.computer(e.zone("Hall", nil), 1)
		u := e.customer("Клиент", 0, 0)
		pkg := scalar[int64](t, `INSERT INTO time_packages (club_id, user_id, name, minutes_left, expires_at, created_at, updated_at)
			VALUES ($1, $2, 'Пакет', 90, $3, now(), now()) RETURNING id`, e.club, u, t0.Add(40*time.Minute+30*time.Second))
		s := must[service.PurchaseResult](t)(e.svc.StartPackage(context.Background(), service.PackageStartRequest{
			ClubID: e.club, UserID: u, PackageID: pkg, ComputerID: pc, IdempotencyKey: key("pkg"),
		}))
		eq(t, "minutes", s.MinutesAdded, 40)
		eq(t, "packages", e.count("time_packages"), 0)
		eq(t, "ledger minutes", e.minutesOf(pkg), -90) // весь остаток (Q-011)

		// Просроченный пакет: «Срок абонемента истёк.», пакет остаётся (удаление откатывалось).
		expired := scalar[int64](t, `INSERT INTO time_packages (club_id, user_id, name, minutes_left, expires_at, created_at, updated_at)
			VALUES ($1, $2, 'Старый', 30, $3, now(), now()) RETURNING id`, e.club, u, t0.Add(-time.Minute))
		_, err := e.svc.StartPackage(context.Background(), service.PackageStartRequest{
			ClubID: e.club, UserID: u, PackageID: expired, ComputerID: pc, IdempotencyKey: key("pkg"),
		})
		wantFieldError(t, err, "package", "Срок абонемента истёк.")
		eq(t, "packages", e.count("time_packages"), 1)
	})

	t.Run("GS-088", func(t *testing.T) {
		e := newEnv(t, "")
		pc := e.computer(e.zone("Hall", ptr(int64(60000))), 1)
		u := e.customer("Клиент", 1000*tenge, 0)
		other := e.customer("Другой", 0, 0)
		r := must[service.PurchaseResult](t)(e.buyMinutes(u, pc, 60, pricing.ChoiceMain))
		_, err := e.end(pc, "wrong", u, true)
		wantFieldError(t, err, "session_id", "На компьютере нет такой активной сессии.")
		_, err = e.end(pc, r.SessionID, other, false)
		var fe *service.ForbiddenError
		if !errors.As(err, &fe) {
			t.Fatalf("err = %v, want 403", err)
		}
		must[service.EndSessionResult](t)(e.end(pc, r.SessionID, other, true)) // сотрудник
		eq(t, "pc free", e.pc(pc).SessionID == nil, true)
	})

	t.Run("GAP-036", func(t *testing.T) {
		// 60 мин поминутно (600.00), пересадка через 10 мин, конец ещё через 20 → возврат 300.00.
		e := newEnv(t, "")
		hall := e.zone("Hall", ptr(int64(60000)))
		pc1, pc2 := e.computer(hall, 1), e.computer(hall, 2)
		u := e.customer("Клиент", 1000*tenge, 0)
		r := must[service.PurchaseResult](t)(e.buyMinutes(u, pc1, 60, pricing.ChoiceMain))
		e.clock.travel(10 * time.Minute)
		exec(t, "UPDATE club_computers SET last_seen_at = $2 WHERE id = $1", pc2, e.clock.Now())
		tr := must[service.TransferResult](t)(e.svc.TransferSeat(context.Background(), service.TransferRequest{
			ClubID: e.club, UserID: u, FromComputerID: pc1, TargetComputerID: pc2,
		}))
		eq(t, "session id", tr.SessionID, r.SessionID)
		e.clock.travel(20 * time.Minute)
		res := must[service.EndSessionResult](t)(e.end(pc2, r.SessionID, u, false))
		eq(t, "refund", res.RefundMain.Tenge(), "300.00")
		e.wantBalances(u, 700*tenge, 0)
	})
}

func TestSeatTransfer(t *testing.T) {
	t.Run("GS-103", func(t *testing.T) {
		// Зоны Hall, VIP; ПК №1 (сессия клиента до 11:30), №2 свободен, №3 занят другим, №4 офлайн, №5 в VIP,
		// №6 is_reserved, №7 бронь 11:00–12:00; тариф «Hall» 120 мин только для Hall.
		e := newEnv(t, "")
		hall, vip := e.zone("Hall", nil), e.zone("VIP", nil)
		e.clock.set(t0.Add(-30 * time.Minute)) // покупка в 09:30 → конец 11:30
		pcs := map[int]int64{}
		for _, n := range []int{1, 2, 3, 4, 6, 7} {
			pcs[n] = e.computer(hall, int64(n))
		}
		pcs[5] = e.computer(vip, 5)
		tr := e.tariff(tariffSpec{name: "Hall", minutes: 120, price: 100 * tenge, zones: []int64{hall}})
		u := e.customer("Клиент", 1000*tenge, 0)
		other := e.customer("Другой", 1000*tenge, 0)
		r := must[service.PurchaseResult](t)(e.buyTariff(u, tr, pcs[1], pricing.ChoiceMain))
		e.clock.set(t0)
		must[service.PurchaseResult](t)(e.buyTariff(other, tr, pcs[3], pricing.ChoiceMain))
		for n, id := range pcs {
			if n != 4 {
				exec(t, "UPDATE club_computers SET last_seen_at = $2 WHERE id = $1", id, t0)
			} else {
				exec(t, "UPDATE club_computers SET last_seen_at = $2, status = 'offline' WHERE id = $1", id, t0.Add(-5*time.Minute))
			}
		}
		exec(t, "UPDATE club_computers SET is_reserved = true WHERE id = $1", pcs[6])
		exec(t, `INSERT INTO computer_reservations (club_id, club_computer_id, starts_at, ends_at, created_at, updated_at)
			VALUES ($1, $2, '2026-10-15 11:00:00+00', '2026-10-15 12:00:00+00', now(), now())`, e.club, pcs[7])

		move := func(user int64, target int64) (service.TransferResult, error) {
			return e.svc.TransferSeat(context.Background(), service.TransferRequest{
				ClubID: e.club, UserID: user, FromComputerID: pcs[1], TargetComputerID: target,
			})
		}
		for _, n := range []int{3, 4, 5, 6, 7} {
			_, err := move(u, pcs[n])
			wantFieldError(t, err, "target_computer_id", "Этот ПК уже занят или ваш тариф на нём не действует. Выберите другой.")
		}
		_, err := move(u, pcs[1])
		wantFieldError(t, err, "target_computer_id", "Выберите другой свободный ПК.")
		_, err = move(other, pcs[2])
		wantFieldError(t, err, "computer_id", "На этом ПК нет вашей оплаченной сессии.")

		res := must[service.TransferResult](t)(move(u, pcs[2]))
		eq(t, "computer", res.ComputerID, pcs[2])
		eq(t, "session", res.SessionID, r.SessionID)
		eq(t, "from free", e.pc(pcs[1]).SessionID == nil, true)
		to := e.pc(pcs[2])
		eq(t, "to session", *to.SessionID, r.SessionID)
		eq(t, "to user", *to.UserID, u)
		eq(t, "to ends", to.EndsAt.UTC(), time.Date(2026, 10, 15, 11, 30, 0, 0, time.UTC))
		e.wantBalances(u, 900*tenge, 0) // денег не списывается
	})

	t.Run("GAP-035", func(t *testing.T) {
		// Минуты, купленные в Hall, — только в Hall.
		e := newEnv(t, "")
		hall, vip := e.zone("Hall", ptr(int64(60000))), e.zone("VIP", ptr(int64(90000)))
		from, toVIP, toHall := e.computer(hall, 1), e.computer(vip, 2), e.computer(hall, 3)
		u := e.customer("Клиент", 1000*tenge, 0)
		r := must[service.PurchaseResult](t)(e.buyMinutes(u, from, 30, pricing.ChoiceMain))
		_, err := e.svc.TransferSeat(context.Background(), service.TransferRequest{ClubID: e.club, UserID: u, FromComputerID: from, TargetComputerID: toVIP})
		wantFieldError(t, err, "target_computer_id", "Этот ПК уже занят или ваш тариф на нём не действует. Выберите другой.")
		res := must[service.TransferResult](t)(e.svc.TransferSeat(context.Background(), service.TransferRequest{ClubID: e.club, UserID: u, FromComputerID: from, TargetComputerID: toHall}))
		eq(t, "session", res.SessionID, r.SessionID)
		e.wantBalances(u, 700*tenge, 0)
	})
}

func TestExpireSessions(t *testing.T) {
	t.Run("GS-087/GAP-005", func(t *testing.T) {
		// ПК1 до +30 мин, ПК2 до +10 мин; через 31 мин задача снимает обе сессии без расчёта.
		e := newEnv(t, "")
		hall := e.zone("Hall", ptr(int64(60000)))
		pc1, pc2 := e.computer(hall, 1), e.computer(hall, 2)
		u := e.customer("Клиент", 1000*tenge, 0)
		must[service.PurchaseResult](t)(e.buyMinutes(u, pc1, 30, pricing.ChoiceMain))
		must[service.PurchaseResult](t)(e.buyMinutes(u, pc2, 10, pricing.ChoiceMain))
		w := &worker.ExpireSessions{Pool: fixDB.Platform, Now: func() time.Time { return t0.Add(20 * time.Minute) }}
		ran, n, err := w.Tick(context.Background())
		if err != nil || !ran {
			t.Fatalf("tick: ran=%v err=%v", ran, err)
		}
		if n < 1 || e.pc(pc2).SessionID != nil || e.pc(pc1).SessionID == nil {
			t.Fatalf("after 20 min: n=%d pc1=%+v pc2=%+v", n, e.pc(pc1), e.pc(pc2))
		}
		w.Now = func() time.Time { return t0.Add(31 * time.Minute) }
		must[bool](t)(func() (bool, error) { ran, _, err := w.Tick(context.Background()); return ran, err }())
		st := e.pc(pc1)
		eq(t, "pc1 cleared", st.SessionID == nil && st.UserID == nil && st.EndsAt == nil && !st.Guest, true)
		// Без расчёта: части остаются, денег не возвращается.
		eq(t, "parts kept", e.count("session_purchases"), 2)
		e.wantBalances(u, 600*tenge, 0)

		// Лок держит другой исполнитель — тик пропускается.
		conn, err := fixDB.Platform.Acquire(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Release()
		if _, err := conn.Exec(context.Background(), "SELECT pg_advisory_lock(hashtext('job:'||$1))", worker.ExpireSessionsJob); err != nil {
			t.Fatal(err)
		}
		ran, _, err = w.Tick(context.Background())
		if err != nil || ran {
			t.Fatalf("tick under foreign lock: ran=%v err=%v", ran, err)
		}
		if _, err := conn.Exec(context.Background(), "SELECT pg_advisory_unlock(hashtext('job:'||$1))", worker.ExpireSessionsJob); err != nil {
			t.Fatal(err)
		}
		ran, _, err = w.Tick(context.Background())
		if err != nil || !ran {
			t.Fatalf("tick after unlock: ran=%v err=%v", ran, err)
		}
	})
}
