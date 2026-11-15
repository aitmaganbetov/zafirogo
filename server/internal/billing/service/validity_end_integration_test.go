//go:build integration

package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/billing/pricing"
	"github.com/aitmaganbetov/zafirogo/server/internal/billing/service"
)

// Время тарифа заканчивается вместе с периодом действия (Zafiro b4f08f6): GS-529…GS-531 от запроса до БД.

const monDay = `[{"start_day": "mon", "start_time": "08:00", "end_day": "mon", "end_time": "18:00"}]`

// almatyEnv — клуб в Asia/Almaty (UTC+5), часы — понедельник 2026-10-26, local по часам клуба.
func almatyEnv(t *testing.T, local string) (*env, *time.Location) {
	t.Helper()
	e := newEnv(t, "")
	exec(t, "UPDATE clubs SET timezone = 'Asia/Almaty' WHERE id = $1", e.club)
	loc, err := time.LoadLocation("Asia/Almaty")
	if err != nil {
		t.Fatal(err)
	}
	e.clock.set(localTime(t, loc, local))
	return e, loc
}

func localTime(t *testing.T, loc *time.Location, s string) time.Time {
	t.Helper()
	v, err := time.ParseInLocation("2006-01-02 15:04:05", s, loc)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestTariffEndsWithValidityPeriod(t *testing.T) {
	t.Run("GS-529", func(t *testing.T) {
		// «День» 600 мин, 1000 ₸, пн 08:00–18:00; куплен в 15:00 → до 18:00 за ту же цену, пакета нет.
		e, loc := almatyEnv(t, "2026-10-26 15:00:00")
		pc := e.computer(e.zone("Hall", nil), 1)
		day := e.tariff(tariffSpec{name: "День", minutes: 600, price: 1000 * tenge, validity: monDay})
		u := e.customer("Клиент", 2000*tenge, 0)
		six := localTime(t, loc, "2026-10-26 18:00:00")

		r := must[service.PurchaseResult](t)(e.buyTariff(u, day, pc, pricing.ChoiceMain))
		eq(t, "minutes_added", r.MinutesAdded, 180)
		eq(t, "session_ends_at", r.SessionEndsAt.UTC(), six.UTC())
		eq(t, "pc ends", e.pc(pc).EndsAt.UTC(), six.UTC())
		eq(t, "price", r.Price, 1000*tenge)
		e.wantBalances(u, 1000*tenge, 0)
		eq(t, "packages", e.count("time_packages"), 0)
		eq(t, "part minutes", scalar[int64](t, "SELECT minutes FROM session_purchases WHERE club_id = $1", e.club), 180)

		// Время уже оплачено дальше 18:00: тариф ничего не добавит и не продаётся.
		exec(t, "UPDATE club_computers SET session_ends_at = $2 WHERE id = $1", pc, six.Add(30*time.Minute))
		_, err := e.buyTariff(u, day, pc, pricing.ChoiceMain)
		wantFieldError(t, err, "tariff", "Тариф действует до :time, а время на ПК уже оплачено дальше.")
		var pe *pricing.Error
		if !errors.As(err, &pe) || pe.Params["time"] != "18:00" {
			t.Fatalf("параметры %+v", pe)
		}
		e.wantBalances(u, 1000*tenge, 0)
		eq(t, "parts", e.count("session_purchases"), 1)
		eq(t, "tariff_sales", scalar[int64](t, "SELECT COALESCE(sum(sold), 0)::bigint FROM tariff_sales WHERE club_id = $1", e.club), 1)
		e.noDrift()
	})

	t.Run("GS-530", func(t *testing.T) {
		// 120 мин, лимит использования 30 дней, пн и вт 08:00–18:00; куплен в пн 17:00 → 60 мин, 60 — пакетом.
		e, loc := almatyEnv(t, "2026-10-26 17:00:00")
		pc := e.computer(e.zone("Hall", nil), 1)
		tr := e.tariff(tariffSpec{name: "Пакет", minutes: 120, price: 1000 * tenge, usageLimit: ptr(int64(30 * 24 * 60)),
			validity: `[{"start_day": "mon", "start_time": "08:00", "end_day": "mon", "end_time": "18:00"},
				{"start_day": "tue", "start_time": "08:00", "end_day": "tue", "end_time": "18:00"}]`})
		u := e.customer("Клиент", 1000*tenge, 0)
		bought := e.clock.Now()

		r := must[service.PurchaseResult](t)(e.buyTariff(u, tr, pc, pricing.ChoiceMain))
		eq(t, "minutes_added", r.MinutesAdded, 60)
		eq(t, "kept", r.KeptMinutes, 60)
		eq(t, "packages", e.count("time_packages"), 1)
		pkg := scalar[int64](t, "SELECT id FROM time_packages WHERE club_id = $1", e.club)
		eq(t, "package id", r.KeptPackageID, pkg)
		eq(t, "minutes_left", scalar[int64](t, "SELECT minutes_left FROM time_packages WHERE id = $1", pkg), 60)
		eq(t, "expires_at", scalar[time.Time](t, "SELECT expires_at FROM time_packages WHERE id = $1", pkg).UTC(), bought.Add(30*24*time.Hour).UTC())
		eq(t, "name:tariff:user", scalar[string](t, "SELECT name || ':' || tariff_id || ':' || user_id FROM time_packages WHERE id = $1", pkg),
			"Пакет:"+itoa(tr)+":"+itoa(u))
		eq(t, "ledger minutes", e.minutesOf(pkg), 60)
		eq(t, "ledger kind", scalar[string](t, `SELECT t.kind FROM ledger_minute_entries m JOIN ledger_transactions t ON t.club_id = m.club_id AND t.id = m.transaction_id
			WHERE m.club_id = $1 AND m.time_package_id = $2`, e.club, pkg), "package_kept")
		e.wantBalances(u, 0, 0)

		// Вторник 17:30: сессии нет; полчаса до конца периода, остальные 30 минут остаются в пакете.
		e.clock.set(localTime(t, loc, "2026-10-27 17:30:00"))
		exec(t, "UPDATE club_computers SET current_session_id = NULL, current_session_user_id = NULL, session_ends_at = NULL WHERE id = $1", pc)
		exec(t, "DELETE FROM session_purchases WHERE club_id = $1", e.club)
		start := func() (service.PurchaseResult, error) {
			return e.svc.StartPackage(context.Background(), service.PackageStartRequest{
				ClubID: e.club, UserID: u, PackageID: pkg, ComputerID: pc, IdempotencyKey: key("pkg"),
			})
		}
		s := must[service.PurchaseResult](t)(start())
		eq(t, "started", s.MinutesAdded, 30)
		eq(t, "left in result", s.KeptMinutes, 30)
		eq(t, "minutes_left", scalar[int64](t, "SELECT minutes_left FROM time_packages WHERE id = $1", pkg), 30)
		eq(t, "pc ends", e.pc(pc).EndsAt.UTC(), localTime(t, loc, "2026-10-27 18:00:00").UTC())
		eq(t, "ledger minutes after start", e.minutesOf(pkg), 30)
		eq(t, "part", scalar[string](t, "SELECT kind || ':' || minutes || ':' || source FROM session_purchases WHERE club_id = $1", e.club), "tariff:30:package")

		// Сессия оплачена до конца периода — пакет больше ничего не добавит.
		_, err := start()
		wantFieldError(t, err, "tariff", "Тариф действует до :time, а время на ПК уже оплачено дальше.")
		eq(t, "minutes_left unchanged", scalar[int64](t, "SELECT minutes_left FROM time_packages WHERE id = $1", pkg), 30)

		// Следующий понедельник 10:00: остаток запускается целиком, пакет удалён.
		e.clock.set(localTime(t, loc, "2026-11-02 10:00:00"))
		s = must[service.PurchaseResult](t)(start())
		eq(t, "started rest", s.MinutesAdded, 30)
		eq(t, "packages left", e.count("time_packages"), 0)
		eq(t, "ledger minutes at zero", e.minutesOf(pkg), 0)
		e.noDrift()
	})

	t.Run("UF-1", func(t *testing.T) {
		// Пакет 90 мин, срок через 40 мин 30 с, период действия тарифа заканчивается через 10 мин (пн 18:00):
		// запускается 10 мин; до срока поместилось бы 40 → 30 остаются в пакете, 50 сгорают (Q-011), а не остаются
		// (в Laravel остались бы все 80 — срок перестал бы ограничивать пакет).
		e, loc := almatyEnv(t, "2026-10-26 17:50:00")
		pc := e.computer(e.zone("Hall", nil), 1)
		tr := e.tariff(tariffSpec{name: "Пакет", minutes: 120, price: 1000 * tenge, usageLimit: ptr(int64(30 * 24 * 60)), validity: monDay})
		u := e.customer("Клиент", 0, 0)
		pkg := scalar[int64](t, `INSERT INTO time_packages (club_id, user_id, tariff_id, name, minutes_left, expires_at, created_at, updated_at)
			VALUES ($1, $2, $3, 'Пакет', 90, $4, now(), now()) RETURNING id`, e.club, u, tr, e.clock.Now().Add(40*time.Minute+30*time.Second))
		s := must[service.PurchaseResult](t)(e.svc.StartPackage(context.Background(), service.PackageStartRequest{
			ClubID: e.club, UserID: u, PackageID: pkg, ComputerID: pc, IdempotencyKey: key("pkg"),
		}))
		eq(t, "started", s.MinutesAdded, 10)
		eq(t, "pc ends", e.pc(pc).EndsAt.UTC(), localTime(t, loc, "2026-10-26 18:00:00").UTC())
		eq(t, "minutes_left", scalar[int64](t, "SELECT minutes_left FROM time_packages WHERE id = $1", pkg), 30)
		eq(t, "ledger minutes", e.minutesOf(pkg), -60) // −10 запущено, −50 сгорело
	})

	t.Run("GS-531", func(t *testing.T) {
		// Гостевая продажа с кассы: «День» 600 мин, 2000 ₸, пн 08:00–18:00, 15:00 → до 18:00, сумма 2000.00.
		e, loc := almatyEnv(t, "2026-10-26 15:00:00")
		pc := e.computer(e.zone("Hall", nil), 1)
		day := e.tariff(tariffSpec{name: "День", minutes: 600, price: 2000 * tenge, validity: monDay})
		e.cash.shiftID = 5
		six := localTime(t, loc, "2026-10-26 18:00:00")

		r := must[service.GuestSessionResult](t)(e.sellGuest([]int64{pc}, &day, 0, "cash"))
		eq(t, "amount", r.Amount.Tenge(), "2000.00")
		eq(t, "minutes", r.Minutes, 180)
		eq(t, "pc ends", e.pc(pc).EndsAt.UTC(), six.UTC())
		eq(t, "cash minutes", e.cash.records[0].Minutes, 180)

		// Гостевая сессия уже до 18:00 — тариф не продаётся, касса не меняется.
		_, err := e.sellGuest([]int64{pc}, &day, 0, "cash")
		wantFieldError(t, err, "tariff_id", "Тариф действует до :time, а время на ПК :computer уже оплачено дальше.")
		var pe *pricing.Error
		if !errors.As(err, &pe) || pe.Params["time"] != "18:00" || pe.Params["computer"] != "PC-01" {
			t.Fatalf("параметры %+v", pe)
		}
		eq(t, "cash records", len(e.cash.records), 1)
	})
}
