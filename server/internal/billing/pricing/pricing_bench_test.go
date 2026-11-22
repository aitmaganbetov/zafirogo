package pricing

import (
	"testing"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/money"
)

// Микробенчмарки расчёта цены (аудит PERF-5): поминутное предложение и покупка минут — на каждый экран гостя и
// тик автобиллинга. Запуск: go test -run '^$' -bench . -benchmem ./internal/billing/pricing/.

var (
	sinkOffer    MinuteOffer
	sinkPurchase Purchase
	sinkMinutes  int64
)

func benchRules(b *testing.B) PaymentRules {
	b.Helper()
	f, err := ParseFinances([]byte(`{"allow_mixed_payment":true,"bonus_max_percent":50,"discount_scope":"all"}`))
	if err != nil {
		b.Fatal(err)
	}
	return RulesFromFinances(f)
}

func BenchmarkRulesFromSettings(b *testing.B) {
	raw := []byte(`{"allow_mixed_payment":true,"bonus_max_percent":50,"discount_scope":"all","auto_billing":true}`)
	for b.Loop() {
		f, err := ParseFinances(raw)
		if err != nil {
			b.Fatal(err)
		}
		sinkOffer.MixedPayment = RulesFromFinances(f).MixedPayment
	}
}

func BenchmarkMinuteOfferAndAffordable(b *testing.B) {
	rules, price := benchRules(b), money.Tiyn(60000)
	for b.Loop() {
		o, err := MinuteOfferFor(&price, rules, 7.5)
		if err != nil {
			b.Fatal(err)
		}
		sinkOffer, sinkMinutes = o, AffordableMinutes(o, 123456)
	}
}

func BenchmarkQuoteMinutesPurchase(b *testing.B) {
	rules, price, zone := benchRules(b), money.Tiyn(60000), int64(3)
	in := MinutesPurchaseInput{ZonePricePerHour: &price, Rules: rules, DiscountPercent: 7.5, UserID: 42,
		Computer: Computer{ZoneID: &zone}, Minutes: 95, Choice: ChoiceMain,
		Balances: Balances{Main: 500000, Bonus: 20000}, Now: time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)}
	for b.Loop() {
		p, err := QuoteMinutesPurchase(in)
		if err != nil {
			b.Fatal(err)
		}
		sinkPurchase = p
	}
}
