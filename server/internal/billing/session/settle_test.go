package session

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/money"
)

// t0 — «сейчас» в момент первой покупки во всех сценариях (как freezeTime() в SessionTimeTest.php).
var t0 = time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC)

const customer int64 = 7

func at(d time.Duration) *time.Time { t := t0.Add(d); return &t }

func i64(v int64) *int64 { return &v }

func minutesPart(id, minutes int64, main, bonus money.Tiyn) Purchase {
	return Purchase{ID: id, UserID: customer, Kind: KindMinutes, Name: "Hall", Minutes: minutes, MainCents: main, BonusCents: bonus}
}

func tariffPart(id, minutes int64, main money.Tiyn, expires *time.Time) Purchase {
	return Purchase{ID: id, UserID: customer, Kind: KindTariff, TariffID: i64(100 + id), Name: "Пакет", Minutes: minutes, MainCents: main, ExpiresAt: expires}
}

type settleWant struct {
	main, bonus money.Tiyn
	kept        int64
	packages    []KeptPackage // nil — пакетов нет
	left        int64
}

func checkSettle(t *testing.T, got Settlement, w settleWant, parts []Purchase) {
	t.Helper()
	if got.RefundMain != w.main || got.RefundBonus != w.bonus || got.KeptMinutes != w.kept {
		t.Fatalf("возврат = %s / %s, пакет %d мин; want %s / %s, %d мин", got.RefundMain, got.RefundBonus, got.KeptMinutes, w.main, w.bonus, w.kept)
	}
	if w.left >= 0 && got.SecondsLeft != w.left {
		t.Errorf("SecondsLeft = %d, want %d", got.SecondsLeft, w.left)
	}
	if len(got.Packages) != len(w.packages) {
		t.Fatalf("пакеты = %+v, want %+v", got.Packages, w.packages)
	}
	for i, p := range w.packages {
		g := got.Packages[i]
		if g.MinutesLeft != p.MinutesLeft || !g.ExpiresAt.Equal(p.ExpiresAt) || g.UserID != customer || g.SourcePurchaseID != p.SourcePurchaseID {
			t.Errorf("пакет %d = %+v, want %+v", i, g, p)
		}
	}
	// Удаляются все части сессии.
	ids := make([]int64, 0, len(parts))
	for _, p := range parts {
		ids = append(ids, p.ID)
	}
	slices.Sort(ids)
	del := slices.Clone(got.DeletePurchaseIDs)
	slices.Sort(del)
	if !slices.Equal(ids, del) {
		t.Errorf("DeletePurchaseIDs = %v, want %v", got.DeletePurchaseIDs, ids)
	}
	// Траты лояльности уменьшаются на возврат основного баланса (BR-042), журнал — только при возврате > 0.
	wantLoyalty := money.Tiyn(0)
	if w.main+w.bonus > 0 {
		wantLoyalty = w.main
	}
	if got.LoyaltySpendingDecrease != wantLoyalty || got.LogRefunded != (w.main+w.bonus > 0) || got.LogPackageKept != (w.kept > 0) {
		t.Errorf("loyalty = %s, logRefunded %v, logKept %v; want %s", got.LoyaltySpendingDecrease, got.LogRefunded, got.LogPackageKept, wantLoyalty)
	}
}

type settleCase struct {
	name   string
	parts  []Purchase
	endsAt *time.Time
	now    time.Time
	want   settleWant
}

// goldenSettleCases — сценарии GS/GAP/BR §4 по SessionSettlement::settle() (docs/golden-scenarios.md,
// docs/business-rules.md BR-032). Те же входы попадают в фикстуру для PHP-оракула (oracle_test.go).
func goldenSettleCases() []settleCase {
	limit30d := at(43200 * time.Minute)
	limit1d := at(1440 * time.Minute)
	ex1 := []Purchase{tariffPart(1, 60, 50000, nil), minutesPart(2, 60, 30000, 30000), minutesPart(3, 7, 5251, 0)}

	return []settleCase{
		{
			// SessionTimeTest «unused minutes go back to the balances they were paid from»: 60 мин за 600.00
			// (300 основным + 300 бонусами), завершено через 30 мин → 150.00 / 150.00.
			name:   "GS-046",
			parts:  []Purchase{minutesPart(1, 60, 30000, 30000)},
			endsAt: at(60 * time.Minute), now: t0.Add(30 * time.Minute),
			want: settleWant{main: 15000, bonus: 15000, left: 1800},
		},
		{
			// «a tariff with a usage limit keeps its unused time»: 120 мин, лимит 30 дней, через 30 мин → пакет 90 мин.
			name:   "GS-047",
			parts:  []Purchase{tariffPart(1, 120, 100000, limit30d)},
			endsAt: at(120 * time.Minute), now: t0.Add(30 * time.Minute),
			want: settleWant{kept: 90, left: 5400, packages: []KeptPackage{{SourcePurchaseID: 1, MinutesLeft: 90, ExpiresAt: *limit30d}}},
		},
		{
			// «a tariff without a usage limit is not refunded; minutes bought on top are, the last part first».
			name:   "GS-048",
			parts:  []Purchase{tariffPart(1, 60, 50000, nil), minutesPart(2, 30, 30000, 0)},
			endsAt: at(90 * time.Minute), now: t0.Add(15 * time.Minute),
			want: settleWant{main: 30000, left: 4500},
		},
		{
			// 17 мин: main 10000, bonus 4167; осталось 700 с → intdiv(7 000 000, 1020) = 6862; intdiv(2 916 900, 1020) = 2859.
			name:   "GAP-001",
			parts:  []Purchase{minutesPart(1, 17, 10000, 4167)},
			endsAt: at(17 * time.Minute), now: t0.Add(5*time.Minute + 20*time.Second),
			want: settleWant{main: 6862, bonus: 2859, left: 700},
		},
		{
			// 60 мин основным за 600.00, через 30 мин → 300.00; total_spent 600 − 300.
			name:   "GAP-002",
			parts:  []Purchase{minutesPart(1, 60, 60000, 0)},
			endsAt: at(60 * time.Minute), now: t0.Add(30 * time.Minute),
			want: settleWant{main: 30000, left: 1800},
		},
		{
			// Тариф 120 мин с лимитом, через 29 мин 30 с: 5430 с → 90 мин, 30 с теряются.
			name:   "GAP-003",
			parts:  []Purchase{tariffPart(1, 120, 100000, limit30d)},
			endsAt: at(120 * time.Minute), now: t0.Add(29*time.Minute + 30*time.Second),
			want: settleWant{kept: 90, left: 5430, packages: []KeptPackage{{SourcePurchaseID: 1, MinutesLeft: 90, ExpiresAt: *limit30d}}},
		},
		{
			// Тариф 60 мин с лимитом 1440 + поминутно 30 мин (300.00); через 10 мин: поминутка целиком, тариф — пакет 50 мин.
			name:   "GAP-004",
			parts:  []Purchase{tariffPart(1, 60, 50000, limit1d), minutesPart(2, 30, 30000, 0)},
			endsAt: at(90 * time.Minute), now: t0.Add(10 * time.Minute),
			want: settleWant{main: 30000, kept: 50, left: 4800, packages: []KeptPackage{{SourcePurchaseID: 1, MinutesLeft: 50, ExpiresAt: *limit1d}}},
		},
		{
			// После пересадки через 10 мин и ещё 20 мин на новом ПК — возврат за 30 мин из 60 = 300.00
			// (части сессии привязаны к session_id и не меняются при пересадке).
			name:   "GAP-036",
			parts:  []Purchase{minutesPart(1, 60, 60000, 0)},
			endsAt: at(60 * time.Minute), now: t0.Add(30 * time.Minute),
			want: settleWant{main: 30000, left: 1800},
		},
		{
			name: "BR-032/пример-1", parts: ex1, endsAt: at(10 * time.Hour), now: t0.Add(10*time.Hour - 300*time.Second),
			want: settleWant{main: 3750, left: 300},
		},
		{
			name: "BR-032/пример-2", parts: ex1, endsAt: at(10 * time.Hour), now: t0.Add(10*time.Hour - 4600*time.Second),
			want: settleWant{main: 35251, bonus: 30000, left: 4600},
		},
		{
			name: "BR-032/пример-3-5430с", parts: []Purchase{tariffPart(1, 120, 100000, limit30d)},
			endsAt: at(120 * time.Minute), now: t0.Add(120*time.Minute - 5430*time.Second),
			want: settleWant{kept: 90, left: 5430, packages: []KeptPackage{{SourcePurchaseID: 1, MinutesLeft: 90, ExpiresAt: *limit30d}}},
		},
		{
			// Тариф без лимита 60 мин + 30 мин за 300.00, завершено сразу → минуты целиком, тариф — нет.
			name: "BR-032/пример-4", parts: []Purchase{tariffPart(1, 60, 50000, nil), minutesPart(2, 30, 30000, 0)},
			endsAt: at(90 * time.Minute), now: t0,
			want: settleWant{main: 30000, left: 5400},
		},
		{
			// Сессия уже истекла, но ещё не снята → left = 0, части удаляются, возврата нет.
			name: "BR-032/конец-в-прошлом", parts: []Purchase{minutesPart(1, 60, 60000, 0)},
			endsAt: at(60 * time.Minute), now: t0.Add(61 * time.Minute),
			want: settleWant{left: 0},
		},
		{
			name: "BR-032/конец-null", parts: []Purchase{minutesPart(1, 60, 60000, 0)},
			endsAt: nil, now: t0,
			want: settleWant{left: 0},
		},
		{
			name: "BR-032/без-частей", parts: nil, endsAt: at(time.Hour), now: t0,
			want: settleWant{left: 3600},
		},
		{
			// getTimestamp() отбрасывает микросекунды now: 10:00:00.9 → до 10:30:00 ровно 1800 с (в пользу клиента).
			name: "BR-032/микросекунды-now", parts: []Purchase{minutesPart(1, 60, 60000, 0)},
			endsAt: at(30 * time.Minute), now: t0.Add(900 * time.Millisecond),
			want: settleWant{main: 30000, left: 1800},
		},
		{
			// Микросекунды у конца тоже отбрасываются (в БД старого кода их не было).
			name: "BR-032/микросекунды-конец", parts: []Purchase{minutesPart(1, 60, 60000, 0)},
			endsAt: at(30*time.Minute + 999*time.Millisecond), now: t0,
			want: settleWant{main: 30000, left: 1800},
		},
		{
			// Деление по каждому балансу отдельно: 1 мин за 0.01 + 0.01 бонусами, осталось 30 с → 0 / 0 (а не 0.01 от суммы).
			name: "BR-032/intdiv-по-балансам", parts: []Purchase{minutesPart(1, 1, 1, 1)},
			endsAt: at(time.Minute), now: t0.Add(30 * time.Second),
			want: settleWant{left: 30},
		},
		{
			// Чужая часть «съедает» своё время, но ничего не возвращает; дальше идёт часть клиента.
			name: "BR-032/чужая-часть",
			parts: []Purchase{minutesPart(1, 60, 60000, 0), func() Purchase {
				p := minutesPart(2, 10, 10000, 0)
				p.UserID = 99
				return p
			}()},
			endsAt: at(70 * time.Minute), now: t0.Add(50 * time.Minute),
			want: settleWant{main: 10000, left: 1200},
		},
		{
			// Лимит использования уже истёк — остаток сгорает.
			name: "BR-032/лимит-истёк", parts: []Purchase{tariffPart(1, 120, 100000, at(20*time.Minute))},
			endsAt: at(120 * time.Minute), now: t0.Add(30 * time.Minute),
			want: settleWant{left: 5400},
		},
		{
			// expires_at == now: isFuture() ложно — пакет не создаётся.
			name: "BR-032/лимит-ровно-сейчас", parts: []Purchase{tariffPart(1, 120, 100000, at(30*time.Minute))},
			endsAt: at(120 * time.Minute), now: t0.Add(30 * time.Minute),
			want: settleWant{left: 5400},
		},
		{
			// Остаток пакета < 60 с — ничего не сохраняется.
			name: "BR-032/пакет-меньше-минуты", parts: []Purchase{tariffPart(1, 60, 50000, limit1d)},
			endsAt: at(60 * time.Minute), now: t0.Add(60*time.Minute - 59*time.Second),
			want: settleWant{left: 59},
		},
		{
			// Каждая часть — свой пакет, не объединяются; порядок ID по убыванию, даже если вход не отсортирован.
			name:   "BR-032/два-пакета",
			parts:  []Purchase{tariffPart(2, 30, 0, limit1d), tariffPart(1, 60, 50000, limit30d)},
			endsAt: at(90 * time.Minute), now: t0.Add(10*time.Minute + 30*time.Second),
			want: settleWant{kept: 30 + 49, left: 4770, packages: []KeptPackage{
				{SourcePurchaseID: 2, MinutesLeft: 30, ExpiresAt: *limit1d},
				{SourcePurchaseID: 1, MinutesLeft: 49, ExpiresAt: *limit30d},
			}},
		},
		{
			// Часть с 0 минут (теоретически) не делит на ноль.
			name: "BR-032/ноль-минут", parts: []Purchase{minutesPart(1, 0, 100, 0), minutesPart(2, 10, 1000, 0)},
			endsAt: at(10 * time.Minute), now: t0,
			want: settleWant{main: 1000, left: 600},
		},
	}
}

func TestSettle(t *testing.T) {
	for _, tc := range goldenSettleCases() {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Settle(tc.parts, customer, tc.endsAt, tc.now)
			if err != nil {
				t.Fatal(err)
			}
			checkSettle(t, got, tc.want, tc.parts)
		})
	}
}

func TestSettleOutcomes(t *testing.T) {
	// BR-032 пример 2 по частям: P3 → 420 с, P2 → 3600 с, P1 (тариф без лимита) → 580 с сгорают.
	parts := []Purchase{tariffPart(1, 60, 50000, nil), minutesPart(2, 60, 30000, 30000), minutesPart(3, 7, 5251, 0)}
	got, err := Settle(parts, customer, at(time.Hour), t0.Add(time.Hour-4600*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	want := []PartOutcome{
		{PurchaseID: 3, UnusedSeconds: 420, RefundMain: 5251, Outcome: OutcomeRefunded},
		{PurchaseID: 2, UnusedSeconds: 3600, RefundMain: 30000, RefundBonus: 30000, Outcome: OutcomeRefunded},
		{PurchaseID: 1, UnusedSeconds: 580, LostSeconds: 580, Outcome: OutcomeLostNoLimit},
	}
	if !slices.Equal(got.Parts, want) {
		t.Fatalf("parts = %+v\nwant %+v", got.Parts, want)
	}
}

func TestSettleInvalid(t *testing.T) {
	for _, p := range []Purchase{minutesPart(1, -1, 0, 0), minutesPart(1, 1, -1, 0), minutesPart(1, 1, 0, -1)} {
		if _, err := Settle([]Purchase{p}, customer, at(time.Hour), t0); !errors.Is(err, ErrInvalidPurchase) {
			t.Errorf("%+v: err = %v", p, err)
		}
	}
}
