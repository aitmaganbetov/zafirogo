//go:build integration

package service_test

import (
	"testing"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/billing/pricing"
	"github.com/aitmaganbetov/zafirogo/server/internal/billing/service"
	"github.com/aitmaganbetov/zafirogo/server/internal/money"
)

// Покупки тарифа и минут: от запроса до балансов, проводок, session_purchases и total_spent.
func TestPurchase(t *testing.T) {
	t.Run("GS-039", func(t *testing.T) {
		// 10:00 UTC; 100.00 / 50.00; смешанная оплата; Hall 500 ₸/час; 17 мин без выбора → 0.00 / 8.33.
		e := newEnv(t, `{"finances": {"allow_mixed_payment": true}}`)
		hall := e.zone("Hall", ptr(int64(50000)))
		pc := e.computer(hall, 1)
		u := e.customer("Клиент", 100*tenge, 50*tenge)

		r := must[service.PurchaseResult](t)(e.buyMinutes(u, pc, 17, pricing.ChoiceNone))
		eq(t, "minutes_added", r.MinutesAdded, 17)
		eq(t, "new_balance", r.NewBalance.Tenge(), "0.00")
		eq(t, "new_bonus", r.NewBonus.Tenge(), "8.33")
		eq(t, "price", r.Price, money.Tiyn(14167)) // intdiv(50000 × 17 + 30, 60)
		e.wantBalances(u, 0, 833)
		st := e.pc(pc)
		eq(t, "session user", *st.UserID, u)
		eq(t, "session_ends_at", st.EndsAt.UTC(), t0.Add(17*time.Minute))
		eq(t, "session id", *st.SessionID, r.SessionID)
		// Часть сессии: поминутка, main 10000 + bonus 4167, зона Hall, source minutes.
		eq(t, "part", scalar[string](t, `SELECT kind || ':' || main_cents || ':' || bonus_cents || ':' || zone_id || ':' || source
			FROM session_purchases WHERE club_id = $1 AND session_id = $2`, e.club, r.SessionID),
			"minutes:10000:4167:"+itoa(hall)+":minutes")
		got := e.ledgerOf(r.SessionID)
		want := []ledgerRow{
			{"minutes_purchase", "bonus_fund", 0, 4167}, {"minutes_purchase", "member_bonus", u, -4167},
			{"minutes_purchase", "member_main", u, -10000}, {"minutes_purchase", "revenue_time", 0, 10000},
		}
		eqRows(t, got, want)
		e.wantTotalSpent(u, 10000) // только основной баланс (BR-042)
		eq(t, "audit", scalar[int64](t, "SELECT count(*) FROM audit_log WHERE club_id = $1 AND action = 'money.minutes_purchase'", e.club), 1)

		// 60 мин (500.00) — не хватает: ошибка balance, ничего не меняется.
		_, err := e.buyMinutes(u, pc, 60, pricing.ChoiceNone)
		wantFieldError(t, err, "balance", "Недостаточно средств для покупки тарифа.")
		e.wantBalances(u, 0, 833)
		eq(t, "parts", e.count("session_purchases"), 1)
		e.noDrift()
	})

	t.Run("GS-040", func(t *testing.T) {
		e := newEnv(t, "")
		plain := e.zone("Plain", nil)
		hall := e.zone("Hall", ptr(int64(60000)))
		pc1 := e.computer(plain, 1)
		pc2 := e.computer(hall, 2)
		u := e.customer("Клиент", 10000*tenge, 0)
		other := e.customer("Другой", 1000*tenge, 0)
		must[service.PurchaseResult](t)(e.buyMinutes(other, pc2, 60, pricing.ChoiceMain))

		_, err := e.buyMinutes(u, pc1, 30, pricing.ChoiceNone)
		wantFieldError(t, err, "minutes", "В этой зоне время поминутно не продаётся.")
		_, err = e.buyMinutes(u, pc2, 30, pricing.ChoiceNone)
		wantFieldError(t, err, "computer_id", "На компьютере уже идёт сессия другого пользователя.")
		e.wantBalances(u, 10000*tenge, 0)
	})

	t.Run("GS-049", func(t *testing.T) {
		// bonus_max_percent 30, смешанная; 2000.00 / 500.00.
		e := newEnv(t, `{"finances": {"allow_mixed_payment": true, "bonus_max_percent": 30}}`)
		z := e.zone("Hall", ptr(int64(60000)))
		pc := e.computer(z, 1)
		pack := e.tariff(tariffSpec{name: "Пакет", minutes: 120, price: 1000 * tenge})
		only := e.tariff(tariffSpec{name: "Только деньги", minutes: 60, price: 100 * tenge, method: "main_balance_only"})
		u := e.customer("Клиент", 2000*tenge, 500*tenge)

		r := must[service.PurchaseResult](t)(e.buyTariff(u, pack, pc, pricing.ChoiceBonus))
		eq(t, "new_balance", r.NewBalance.Tenge(), "1300.00")
		eq(t, "new_bonus", r.NewBonus.Tenge(), "200.00")
		r = must[service.PurchaseResult](t)(e.buyTariff(u, pack, pc, pricing.ChoiceMain))
		eq(t, "new_balance", r.NewBalance.Tenge(), "300.00")
		eq(t, "new_bonus", r.NewBonus.Tenge(), "200.00")
		_, err := e.buyTariff(u, only, pc, pricing.ChoiceBonus)
		wantFieldError(t, err, "balance", "Этот тариф оплачивается только с основного баланса.")
		e.wantBalances(u, 300*tenge, 200*tenge)
		e.wantTotalSpent(u, 1700*tenge)
		e.noDrift()
	})

	t.Run("GAP-008", func(t *testing.T) {
		e := newEnv(t, `{"finances": {"allow_mixed_payment": true, "bonus_max_percent": 30}}`)
		pc := e.computer(e.zone("Hall", nil), 1)
		tr := e.tariff(tariffSpec{name: "999.99", minutes: 60, price: 99999})
		u := e.customer("Клиент", 2000*tenge, 500*tenge)
		must[service.PurchaseResult](t)(e.buyTariff(u, tr, pc, pricing.ChoiceBonus))
		e.wantBalances(u, 130000, 20001)
	})

	t.Run("GS-050", func(t *testing.T) {
		e := newEnv(t, `{"finances": {"bonus_max_percent": 100}}`)
		pc := e.computer(e.zone("Hall", nil), 1)
		tr := e.tariff(tariffSpec{name: "Пакет", minutes: 60, price: 1000 * tenge})
		u := e.customer("Клиент", 2000*tenge, 500*tenge)
		_, err := e.buyTariff(u, tr, pc, pricing.ChoiceBonus)
		wantFieldError(t, err, "balance", "Бонусов не хватает на всю цену. Оплатите с основного баланса.")
		// Бонусный 1500: владелец докидывает 1000 бонусов (через ledger — фикстура).
		u2 := e.customer("Клиент 2", 2000*tenge, 1500*tenge)
		r := must[service.PurchaseResult](t)(e.buyTariff(u2, tr, pc, pricing.ChoiceBonus))
		eq(t, "new_balance", r.NewBalance.Tenge(), "2000.00")
		eq(t, "new_bonus", r.NewBonus.Tenge(), "500.00")
		e.wantTotalSpent(u2, 0)
		// Без выбора (старый агент) — основной.
		r = must[service.PurchaseResult](t)(e.buyTariff(u2, tr, pc, pricing.ChoiceNone))
		eq(t, "new_balance", r.NewBalance.Tenge(), "1000.00")
		e.wantBalances(u2, 1000*tenge, 500*tenge)
		e.noDrift()
	})

	t.Run("GAP-007", func(t *testing.T) {
		e := newEnv(t, `{"finances": {"bonus_max_percent": 100}}`)
		pc := e.computer(e.zone("Hall", nil), 1)
		tr := e.tariff(tariffSpec{name: "Час", minutes: 60, price: 1000 * tenge})
		u := e.customer("Клиент", 500*tenge, 1500*tenge)
		must[service.PurchaseResult](t)(e.buyTariff(u, tr, pc, pricing.ChoiceNone))
		e.wantBalances(u, 500*tenge, 500*tenge)
		e.wantTotalSpent(u, 0)
	})

	t.Run("GAP-006", func(t *testing.T) {
		e := newEnv(t, `{"finances": {"allow_mixed_payment": true, "bonus_max_percent": 30}}`)
		pc := e.computer(e.zone("Hall", nil), 1)
		tr := e.tariff(tariffSpec{name: "Час", minutes: 60, price: 1000 * tenge})
		u := e.customer("Клиент", 500*tenge, 1000*tenge)
		_, err := e.buyTariff(u, tr, pc, pricing.ChoiceNone)
		wantFieldError(t, err, "balance", "Недостаточно средств для покупки тарифа.")
		e.wantBalances(u, 500*tenge, 1000*tenge)
		eq(t, "pc free", e.pc(pc).SessionID == nil, true)
	})

	t.Run("GS-035", func(t *testing.T) {
		e := newEnv(t, `{"finances": {"allow_mixed_payment": true}}`)
		pc := e.computer(e.zone("Hall", nil), 1)
		tr := e.tariff(tariffSpec{name: "Час", minutes: 60, price: 1000 * tenge})
		u := e.customer("Клиент", 400*tenge, 1000*tenge)
		must[service.PurchaseResult](t)(e.buyTariff(u, tr, pc, pricing.ChoiceNone))
		e.wantBalances(u, 0, 400*tenge)
		e.wantTotalSpent(u, 400*tenge)
	})

	t.Run("GS-034", func(t *testing.T) {
		// Личная 5 %, лояльность {1500 → 10 %}, {3000 → 20 %}; клиент 5000.00.
		e := newEnv(t, `{"finances": {"loyalty_enabled": true, "loyalty_levels": [{"spent": 1500, "percent": 10}, {"spent": 3000, "percent": 20}]}}`)
		hall := e.zone("Hall", ptr(int64(60000)))
		pc := e.computer(hall, 1)
		hour := e.tariff(tariffSpec{name: "Час", minutes: 60, price: 1000 * tenge, discount: true})
		u := e.customer("Клиент", 5000*tenge, 0)
		exec(t, "UPDATE club_memberships SET discount = 5 WHERE club_id = $1 AND user_id = $2", e.club, u)

		r := must[service.PurchaseResult](t)(e.buyTariff(u, hour, pc, pricing.ChoiceNone))
		eq(t, "new_balance", r.NewBalance.Tenge(), "4050.00")
		e.wantTotalSpent(u, 950*tenge)
		must[service.PurchaseResult](t)(e.buyMinutes(u, pc, 60, pricing.ChoiceNone)) // scope tariffs: без скидки
		e.wantTotalSpent(u, 1550*tenge)
		r = must[service.PurchaseResult](t)(e.buyTariff(u, hour, pc, pricing.ChoiceNone))
		eq(t, "price", r.Price.Tenge(), "900.00")
		eq(t, "new_balance", r.NewBalance.Tenge(), "2550.00")
		e.wantTotalSpent(u, 2450*tenge)
		e.noDrift()
	})

	t.Run("GS-051", func(t *testing.T) {
		// Скидка 10 % только на поминутку: 30 мин при 540 ₸/час = 270.00.
		e := newEnv(t, `{"finances": {"discount_scope": "hourly"}}`)
		pc := e.computer(e.zone("Hall", ptr(int64(60000))), 1)
		u := e.customer("Клиент", 1000*tenge, 0)
		exec(t, "UPDATE club_memberships SET discount = 10 WHERE club_id = $1 AND user_id = $2", e.club, u)
		r := must[service.PurchaseResult](t)(e.buyMinutes(u, pc, 30, pricing.ChoiceMain))
		eq(t, "new_balance", r.NewBalance.Tenge(), "730.00")
	})

	t.Run("GS-059", func(t *testing.T) {
		// Скидка 10 %, смешанная; тариф 120 мин, 100 ₸, лимит использования 90, лимит количества 1; 80.00 / 20.00.
		e := newEnv(t, `{"finances": {"allow_mixed_payment": true}}`)
		z := e.zone("Игровой зал", nil)
		pc := e.computer(z, 7)
		pc2 := e.computer(z, 8)
		tr := e.tariff(tariffSpec{name: "Ночной", minutes: 120, price: 100 * tenge, discount: true, usageLimit: ptr(int64(90)), quantityLimit: ptr(int64(1))})
		u := e.customer("Клиент", 80*tenge, 20*tenge)
		exec(t, "UPDATE club_memberships SET discount = 10 WHERE club_id = $1 AND user_id = $2", e.club, u)

		r := must[service.PurchaseResult](t)(e.buyTariff(u, tr, pc, pricing.ChoiceNone))
		eq(t, "new_balance", r.NewBalance.Tenge(), "0.00")
		eq(t, "new_bonus", r.NewBonus.Tenge(), "10.00")
		eq(t, "minutes_added", r.MinutesAdded, 90)
		st := e.pc(pc)
		eq(t, "session id", *st.SessionID, r.SessionID)
		eq(t, "ends", st.EndsAt.UTC(), t0.Add(90*time.Minute))
		// expires_at части = покупка + 90 мин.
		eq(t, "expires_at", scalar[time.Time](t, "SELECT expires_at FROM session_purchases WHERE club_id = $1", e.club).UTC(), t0.Add(90*time.Minute))
		eq(t, "tariff_sales", scalar[int64](t, "SELECT sold FROM tariff_sales WHERE club_id = $1 AND tariff_id = $2", e.club, tr), 1)

		// Лимит количества исчерпан — другому клиенту тариф недоступен.
		u2 := e.customer("Второй", 1000*tenge, 0)
		_, err := e.buyTariff(u2, tr, pc2, pricing.ChoiceNone)
		wantFieldError(t, err, "tariff", "Тариф недоступен.")
		e.wantBalances(u2, 1000*tenge, 0)
	})

	t.Run("GAP-011", func(t *testing.T) {
		// Личная скидка 100 %: цена 0.00, покупка проходит без проводок, total_spent не меняется.
		e := newEnv(t, "")
		pc := e.computer(e.zone("Hall", nil), 1)
		tr := e.tariff(tariffSpec{name: "Час", minutes: 60, price: 1000 * tenge, discount: true})
		u := e.customer("Клиент", 0, 0)
		exec(t, "UPDATE club_memberships SET discount = 100 WHERE club_id = $1 AND user_id = $2", e.club, u)
		r := must[service.PurchaseResult](t)(e.buyTariff(u, tr, pc, pricing.ChoiceNone))
		eq(t, "price", r.Price, 0)
		eq(t, "ledger", r.LedgerTransactionID, 0)
		eq(t, "ends", e.pc(pc).EndsAt.UTC(), t0.Add(60*time.Minute))
		e.wantTotalSpent(u, 0)
		eq(t, "ledger rows", len(e.ledgerOf(r.SessionID)), 0)
	})

	t.Run("GAP-015", func(t *testing.T) {
		e := newEnv(t, "")
		pc := e.computer(e.zone("Hall", nil), 1)
		tr := e.tariff(tariffSpec{name: "Клиентам", minutes: 60, price: 10 * tenge, userRoles: `["client"]`})
		u := e.customer("Клиент", 100*tenge, 0)
		must[service.PurchaseResult](t)(e.buyTariff(u, tr, pc, pricing.ChoiceNone))
		cashier := e.customer("Кассир", 100*tenge, 0)
		exec(t, "UPDATE club_memberships SET role = 'cashier' WHERE club_id = $1 AND user_id = $2", e.club, cashier)
		pc2 := e.computer(e.zone("VIP", nil), 2)
		_, err := e.buyTariff(cashier, tr, pc2, pricing.ChoiceNone)
		wantFieldError(t, err, "tariff", "Тариф недоступен.")
	})

	t.Run("GS-060", func(t *testing.T) {
		// Тариф зоны «Зал» на ПК в VIP — «Тариф недоступен для этого компьютера.»
		e := newEnv(t, "")
		hall := e.zone("Зал", nil)
		vip := e.zone("VIP", nil)
		tr := e.tariff(tariffSpec{name: "Зальный", minutes: 60, price: 10 * tenge, zones: []int64{hall}})
		u := e.customer("Клиент", 100*tenge, 0)
		_, err := e.buyTariff(u, tr, e.computer(vip, 2), pricing.ChoiceNone)
		wantFieldError(t, err, "tariff", "Тариф недоступен для этого компьютера.")
		must[service.PurchaseResult](t)(e.buyTariff(u, tr, e.computer(hall, 1), pricing.ChoiceNone))
	})

	t.Run("GS-063", func(t *testing.T) {
		// Среда 12:00 по часам клуба: «Ночь» (сб 22:00 – вс 06:00) не продаётся.
		e := newEnv(t, "")
		exec(t, "UPDATE clubs SET timezone = 'Asia/Almaty' WHERE id = $1", e.club)
		e.clock.set(time.Date(2026, 10, 28, 12, 0, 0, 0, time.FixedZone("ALMT", 5*3600)))
		pc := e.computer(e.zone("Hall", nil), 1)
		night := e.tariff(tariffSpec{name: "Ночь", minutes: 60, price: 10 * tenge,
			sale: `[{"start_day": "sat", "start_time": "22:00", "end_day": "sun", "end_time": "06:00"}]`})
		u := e.customer("Клиент", 100*tenge, 0)
		_, err := e.buyTariff(u, night, pc, pricing.ChoiceNone)
		wantFieldError(t, err, "tariff", "Тариф недоступен.")
		e.clock.set(time.Date(2026, 10, 31, 23, 0, 0, 0, time.FixedZone("ALMT", 5*3600)))
		must[service.PurchaseResult](t)(e.buyTariff(u, night, pc, pricing.ChoiceNone))
	})

	t.Run("GAP-016", func(t *testing.T) {
		// Сессия до 11:00 (сейчас 10:00); покупка 30 мин → 11:30, тот же session_id, две части.
		e := newEnv(t, "")
		pc := e.computer(e.zone("Hall", ptr(int64(60000))), 1)
		u := e.customer("Клиент", 2000*tenge, 0)
		r1 := must[service.PurchaseResult](t)(e.buyMinutes(u, pc, 60, pricing.ChoiceMain))
		r2 := must[service.PurchaseResult](t)(e.buyMinutes(u, pc, 30, pricing.ChoiceMain))
		eq(t, "same session", r2.SessionID, r1.SessionID)
		eq(t, "extends", r2.ExtendsSession, true)
		eq(t, "ends", e.pc(pc).EndsAt.UTC(), t0.Add(90*time.Minute))
		eq(t, "parts", e.count("session_purchases"), 2)
	})

	t.Run("GS-121", func(t *testing.T) {
		// Интервал 10; бронь 07:30–09:30 для booked; «Час» 60 мин, 100 ₸.
		e := newEnv(t, `{"club": {"booking_interval": 10}}`)
		e.clock.set(time.Date(2026, 10, 15, 6, 0, 0, 0, time.UTC))
		pc := e.computer(e.zone("Hall", nil), 1)
		tr := e.tariff(tariffSpec{name: "Час", minutes: 60, price: 100 * tenge})
		booked := e.customer("Бронь", 1000*tenge, 0)
		stranger := e.customer("Чужой", 1000*tenge, 0)
		resID := scalar[int64](t, `INSERT INTO computer_reservations (club_id, club_computer_id, user_id, starts_at, ends_at, created_at, updated_at)
			VALUES ($1, $2, $3, '2026-10-15 07:30:00+00', '2026-10-15 09:30:00+00', now(), now()) RETURNING id`, e.club, pc, booked)

		r := must[service.PurchaseResult](t)(e.buyTariff(stranger, tr, pc, pricing.ChoiceNone))
		eq(t, "new_balance", r.NewBalance.Tenge(), "900.00")
		exec(t, "UPDATE club_computers SET current_session_id = NULL, current_session_user_id = NULL, session_ends_at = NULL WHERE id = $1", pc)

		e.clock.set(time.Date(2026, 10, 15, 6, 25, 0, 0, time.UTC))
		_, err := e.buyTariff(stranger, tr, pc, pricing.ChoiceNone)
		var ve *service.Error
		if !asErr(err, &ve) || ve.Field != "computer_id" || ve.Params["time"] != "07:30" {
			t.Fatalf("err = %v, want booking error at 07:30", err)
		}
		e.wantBalances(stranger, 900*tenge, 0)
		must[service.PurchaseResult](t)(e.buyTariff(booked, tr, pc, pricing.ChoiceNone))
		eq(t, "arrived", scalar[bool](t, "SELECT arrived_at IS NOT NULL FROM computer_reservations WHERE id = $1", resID), true)
	})

	t.Run("RLS/чужой клуб", func(t *testing.T) {
		a := newEnv(t, "")
		b := newEnv(t, "")
		pcB := b.computer(b.zone("Hall", ptr(int64(60000))), 1)
		u := a.customer("Клиент", 1000*tenge, 0)
		// ПК клуба B из клуба A не виден: 404, деньги не тронуты.
		_, err := a.buyMinutes(u, pcB, 30, pricing.ChoiceMain)
		if !isNotFound(err) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
		a.wantBalances(u, 1000*tenge, 0)
		// Покупка в клубе B не видна из транзакции клуба A.
		ub := b.customer("Клиент B", 1000*tenge, 0)
		rb := must[service.PurchaseResult](t)(b.buyMinutes(ub, pcB, 30, pricing.ChoiceMain))
		_, err = a.end(pcB, rb.SessionID, ub, true)
		if !isNotFound(err) {
			t.Fatalf("end from other club: err = %v, want ErrNotFound", err)
		}
		eq(t, "tables without RLS", scalar[int64](t, "SELECT count(*) FROM zafiro_tables_without_rls()"), 0)
	})
}

func eqRows(t *testing.T, got, want []ledgerRow) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("ledger = %+v, want %+v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("ledger[%d] = %+v, want %+v (all: %+v)", i, got[i], want[i], got)
		}
	}
}
