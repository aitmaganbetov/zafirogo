package pricing

import (
	"testing"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/money"
)

// GS-039, GS-049, GS-053, GS-055, GAP-010: поминутная оплата для агента.
func TestMinuteOfferFor(t *testing.T) {
	t.Run("GS-039", func(t *testing.T) {
		o, err := MinuteOfferFor(tiyn(50000), RulesFromFinances(RawFinances{"allow_mixed_payment": true}), 0)
		assertErr(t, err, nil)
		if !o.Available || o.PricePerHour != 50000 || o.MaxMinutes != 720 || o.MinMinutes != 1 {
			t.Fatalf("%+v", o)
		}
	})
	t.Run("GS-040/зона_без_цены", func(t *testing.T) {
		o, err := MinuteOfferFor(nil, DefaultPaymentRules(), 0)
		assertErr(t, err, nil)
		if o.Available {
			t.Fatalf("%+v", o)
		}
	})
	t.Run("GS-049", func(t *testing.T) {
		o, _ := MinuteOfferFor(tiyn(60000), RulesFromFinances(RawFinances{"bonus_max_percent": 30, "allow_mixed_payment": true}), 0)
		if o.BonusMaxPercent != 30 || !o.MixedPayment {
			t.Fatalf("%+v", o)
		}
	})
	t.Run("GS-050", func(t *testing.T) {
		o, _ := MinuteOfferFor(tiyn(60000), RulesFromFinances(RawFinances{"bonus_max_percent": 30}), 0)
		if o.BonusMaxPercent != 0 {
			t.Fatalf("%+v", o)
		}
	})
	t.Run("GS-053", func(t *testing.T) {
		off, _ := MinuteOfferFor(tiyn(60000), DefaultPaymentRules(), 0)
		on, _ := MinuteOfferFor(tiyn(60000), RulesFromFinances(RawFinances{"allow_whole_balance": true}), 0)
		if off.WholeBalance || !on.WholeBalance {
			t.Fatal("whole_balance")
		}
	})
	t.Run("GS-055", func(t *testing.T) {
		def, _ := MinuteOfferFor(tiyn(60000), DefaultPaymentRules(), 0)
		on, _ := MinuteOfferFor(tiyn(60000), RulesFromFinances(RawFinances{"auto_billing": true, "auto_billing_pause_minutes": 3}), 0)
		if def.AutoBilling || def.AutoBillingPauseMinutes != 5 || !on.AutoBilling || on.AutoBillingPauseMinutes != 3 {
			t.Fatalf("%+v %+v", def, on)
		}
	})
	t.Run("GAP-010", func(t *testing.T) {
		o, _ := MinuteOfferFor(tiyn(55500), RulesFromFinances(RawFinances{"discount_scope": "all"}), 7.5)
		if o.PricePerHour != 51337 || o.OriginalPricePerHour != 55500 {
			t.Fatalf("%+v", o)
		}
		p, err := QuoteMinutesPurchase(MinutesPurchaseInput{ZonePricePerHour: tiyn(55500), Rules: RulesFromFinances(RawFinances{"discount_scope": "all"}),
			DiscountPercent: 7.5, UserID: 1, Minutes: 7, Choice: ChoiceMain, Balances: Balances{100000, 0}, Now: t0})
		assertErr(t, err, nil)
		if p.Price != 5989 || p.Payment.After.Main != 94011 {
			t.Fatalf("%+v", p)
		}
	})
}

// BR-022, GS-053: «игра на весь баланс» — сколько минут оплачивает основной баланс.
func TestAffordableMinutes(t *testing.T) {
	offer := func(hour money.Tiyn, whole bool) MinuteOffer {
		o, err := MinuteOfferFor(&hour, RulesFromFinances(RawFinances{"allow_whole_balance": whole}), 0)
		if err != nil {
			t.Fatal(err)
		}
		return o
	}
	cases := []struct {
		name    string
		o       MinuteOffer
		balance money.Tiyn
		want    int64
	}{
		{"BR-022/пример", offer(60000, true), 140000, 140},
		{"GS-053", offer(60000, true), 200000, 200},
		{"BR-022/максимум_720", offer(60000, true), 10000000, 720},
		{"BR-022/выключено", offer(60000, false), 200000, 0},
		{"BR-022/нет_баланса", offer(60000, true), 0, 0},
		{"BR-022/отрицательный_баланс", offer(60000, true), -100, 0},
		{"BR-022/меньше_минуты", offer(60000, true), 999, 0},
		{"BR-022/ровно_минута", offer(60000, true), 1000, 1},
		// 500 ₸/ч: 1 мин = 8.33; 100.00 → floor(10000*60/50000) = 12 мин, цена intdiv(50000*12+30, 60) = 10000.
		{"BR-022/500_в_час", offer(50000, true), 10000, 12},
		{"BR-022/1_тиын_в_час", offer(1, true), 5, 300},
		{"BR-022/нет_цены_зоны", MinuteOffer{WholeBalance: true}, 100000, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := WholeBalanceMinutes(c.o, c.balance)
			if got != c.want {
				t.Fatalf("got %d, want %d", got, c.want)
			}
			if got > 0 {
				price, _ := MinutesPrice(c.o.PricePerHour, got)
				if price > c.balance {
					t.Fatalf("цена %d больше баланса %d", price, c.balance)
				}
			}
		})
	}
	// Перебор: цена купленных минут не больше баланса, минут — floor(balance*60/hour) (или 720).
	t.Run("BR-022/перебор", func(t *testing.T) {
		for hour := money.Tiyn(1); hour <= 200000; hour += 997 {
			o := MinuteOffer{Available: true, PricePerHour: hour, MaxMinutes: 720, WholeBalance: true}
			for balance := money.Tiyn(1); balance <= 2000000; balance = balance*3 + 7 {
				m := AffordableMinutes(o, balance)
				p, _ := MinutesPrice(hour, m)
				if p > balance {
					t.Fatalf("hour %d balance %d: %d мин за %d", hour, balance, m, p)
				}
				// Максимальности формула агента не гарантирует: из-за «+30» при дешёвом часе лишняя минута
				// иногда стоит столько же (1 тиын/ч, 5 тиынов: 300 мин, а 301 тоже за 5) — так было и в старом агенте.
				if m < 720 && m < int64(balance)*60/int64(hour) {
					t.Fatalf("hour %d balance %d: %d мин меньше floor(balance*60/hour)", hour, balance, m)
				}
			}
		}
	})
}

// BR-023, GS-055: автобиллинг — пауза после входа, сразу после тарифа, остановлен клиентом.
func TestAutoBilling(t *testing.T) {
	o, _ := MinuteOfferFor(tiyn(60000), RulesFromFinances(RawFinances{"auto_billing": true, "auto_billing_pause_minutes": 3}), 0)
	signed := AutoBillingState{SignedIn: true, PauseStartedAt: t0}
	cases := []struct {
		name    string
		o       MinuteOffer
		st      AutoBillingState
		balance money.Tiyn
		now     time.Time
		want    AutoBillingDecision
	}{
		{"BR-023/пауза_идёт", o, signed, 140000, t0.Add(2*time.Minute + 59*time.Second), AutoBillingDecision{Waiting: true, PauseEndsAt: t0.Add(3 * time.Minute)}},
		{"BR-023/пауза_кончилась", o, signed, 140000, t0.Add(3 * time.Minute), AutoBillingDecision{Waiting: true, PauseEndsAt: t0.Add(3 * time.Minute), BuyMinutes: 140}},
		{"BR-023/первый_тик", o, AutoBillingState{SignedIn: true}, 140000, t0, AutoBillingDecision{Waiting: true, PauseEndsAt: t0.Add(3 * time.Minute)}},
		{"BR-023/после_тарифа_сразу", o, AutoBillingState{SignedIn: true, AfterTariff: true}, 140000, t0, AutoBillingDecision{Waiting: true, BuyMinutes: 140}},
		{"BR-023/остановлен", o, AutoBillingState{SignedIn: true, Stopped: true, PauseStartedAt: t0}, 140000, t0.Add(time.Hour), AutoBillingDecision{}},
		{"BR-023/идёт_сессия", o, AutoBillingState{SignedIn: true, HasActiveSession: true}, 140000, t0, AutoBillingDecision{}},
		{"BR-023/не_вошёл", o, AutoBillingState{}, 140000, t0, AutoBillingDecision{}},
		{"BR-023/нет_денег", o, signed, 999, t0.Add(time.Hour), AutoBillingDecision{}},
		{"GS-055/выключено", MinuteOffer{Available: true, PricePerHour: 60000, MaxMinutes: 720, AutoBillingPauseMinutes: 5}, signed, 140000, t0.Add(time.Hour), AutoBillingDecision{}},
		{"BR-023/нет_цены_зоны", MinuteOffer{AutoBilling: true}, signed, 140000, t0.Add(time.Hour), AutoBillingDecision{}},
		{"BR-023/пауза_0", MinuteOffer{Available: true, PricePerHour: 60000, MaxMinutes: 720, AutoBilling: true}, AutoBillingState{SignedIn: true}, 60000, t0, AutoBillingDecision{Waiting: true, PauseEndsAt: t0, BuyMinutes: 60}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := AutoBilling(c.o, c.st, c.balance, c.now); got != c.want {
				t.Fatalf("got %+v, want %+v", got, c.want)
			}
		})
	}
}
