package session

import (
	"math/rand/v2"
	"testing"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/money"
)

// randomSession — случайная сессия: 1…6 частей подряд от t0, конец = t0 + сумма минут.
func randomSession(r *rand.Rand) ([]Purchase, time.Time) {
	n := 1 + r.IntN(6)
	parts := make([]Purchase, 0, n)
	var total int64
	for i := range n {
		minutes := 1 + r.Int64N(720) // как в агенте: 1…720 (GuestMinutesController::MAX_MINUTES)
		p := Purchase{ID: int64(i + 1), UserID: customer, Minutes: minutes, Name: "x"}
		if r.IntN(8) == 0 {
			p.UserID = 99 // чужая часть
		}
		switch r.IntN(3) {
		case 0:
			p.Kind = KindMinutes
			p.MainCents = money.Tiyn(r.Int64N(2_000_000))
			p.BonusCents = money.Tiyn(r.Int64N(2_000_000))
		case 1:
			p.Kind = KindTariff
			p.MainCents = money.Tiyn(r.Int64N(2_000_000))
		default:
			p.Kind = KindTariff
			p.ExpiresAt = at(time.Duration(r.Int64N(60*24*40)) * time.Minute) // лимит, может истечь раньше конца
		}
		total += minutes
		parts = append(parts, p)
	}
	// Перемешиваем вход: порядок задаёт ID, а не позиция в срезе.
	r.Shuffle(len(parts), func(i, j int) { parts[i], parts[j] = parts[j], parts[i] })
	return parts, t0.Add(time.Duration(total) * time.Minute)
}

func paidByCustomer(parts []Purchase) (main, bonus money.Tiyn, keepable int64) {
	for _, p := range parts {
		if p.UserID != customer {
			continue
		}
		if p.Kind == KindMinutes {
			main += p.MainCents
			bonus += p.BonusCents
		} else if p.ExpiresAt != nil && p.ExpiresAt.After(t0) {
			keepable += p.Minutes
		}
	}
	return main, bonus, keepable
}

// Возврат по каждой части и каждому балансу не больше уплаченного; минуты пакетов не больше купленных.
func TestSettlePropertyBounded(t *testing.T) {
	r := rand.New(rand.NewPCG(20261030, 1))
	for range 5000 {
		parts, ends := randomSession(r)
		now := t0.Add(time.Duration(r.Int64N(int64(ends.Sub(t0)/time.Second)+120)-60) * time.Second)
		got, err := Settle(parts, customer, &ends, now)
		if err != nil {
			t.Fatal(err)
		}
		byID := map[int64]Purchase{}
		for _, p := range parts {
			byID[p.ID] = p
		}
		var sumSeconds int64
		for _, o := range got.Parts {
			p := byID[o.PurchaseID]
			sumSeconds += o.UnusedSeconds
			if o.RefundMain < 0 || o.RefundMain > p.MainCents || o.RefundBonus < 0 || o.RefundBonus > p.BonusCents || o.KeptMinutes*60 > o.UnusedSeconds || o.UnusedSeconds > p.Minutes*60 {
				t.Fatalf("часть %+v → %+v", p, o)
			}
			if o.RefundMain+o.RefundBonus > 0 && p.Kind != KindMinutes {
				t.Fatalf("тариф вернул деньги: %+v", o)
			}
		}
		if totalSeconds := int64(ends.Sub(t0) / time.Second); sumSeconds != min(got.SecondsLeft, totalSeconds) {
			t.Fatalf("распределено %d с из %d", sumSeconds, got.SecondsLeft)
		}
		mainPaid, bonusPaid, _ := paidByCustomer(parts)
		if got.RefundMain > mainPaid || got.RefundBonus > bonusPaid {
			t.Fatalf("возврат %s/%s больше уплаченного %s/%s", got.RefundMain, got.RefundBonus, mainPaid, bonusPaid)
		}
	}
}

// Чем больше осталось времени, тем больше (не меньше) возврат и сохранённые минуты.
func TestSettlePropertyMonotonic(t *testing.T) {
	r := rand.New(rand.NewPCG(20261030, 2))
	for range 300 {
		parts, ends := randomSession(r)
		var prev Settlement
		for i, now := 0, ends.Add(time.Minute); !now.Before(t0.Add(-time.Minute)); i, now = i+1, now.Add(-time.Duration(1+r.IntN(400))*time.Second) {
			got, err := Settle(parts, customer, &ends, now)
			if err != nil {
				t.Fatal(err)
			}
			// Лимит пакета сравнивается с now: при более раннем now истёкшая часть может «ожить» — это
			// тоже рост, монотонность сохраняется.
			if i > 0 && (got.RefundMain < prev.RefundMain || got.RefundBonus < prev.RefundBonus || got.KeptMinutes < prev.KeptMinutes) {
				t.Fatalf("now %s: %s/%s/%d < %s/%s/%d", now, got.RefundMain, got.RefundBonus, got.KeptMinutes, prev.RefundMain, prev.RefundBonus, prev.KeptMinutes)
			}
			prev = got
		}
	}
}

// Ни одной использованной секунды → возвращается всё уплаченное за поминутку клиента, а тарифы с
// неистёкшим лимитом сохраняются целиком.
func TestSettlePropertyNothingUsed(t *testing.T) {
	r := rand.New(rand.NewPCG(20261030, 3))
	for range 5000 {
		parts, ends := randomSession(r)
		got, err := Settle(parts, customer, &ends, t0)
		if err != nil {
			t.Fatal(err)
		}
		mainPaid, bonusPaid, keepable := paidByCustomer(parts)
		if got.RefundMain != mainPaid || got.RefundBonus != bonusPaid || got.KeptMinutes != keepable {
			t.Fatalf("%+v: возврат %s/%s, пакеты %d; want %s/%s, %d", parts, got.RefundMain, got.RefundBonus, got.KeptMinutes, mainPaid, bonusPaid, keepable)
		}
	}
}
