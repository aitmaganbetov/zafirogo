package session

import (
	"testing"
	"time"
)

// GAP-014 / GS-047 — часть сессии от запущенного пакета при досрочном завершении снова сохраняется пакетом
// до того же срока. Сам запуск (минуты, проверки ПК, зоны и периода) — pricing.QuoteStartPackage
// (billing/pricing/purchase_test.go) и service.StartPackage (billing/service/session_integration_test.go).
func TestPackagePartSettle(t *testing.T) {
	// part — часть, которую пишет service.StartPackage: вид tariff, 0 тиынов, expires_at = срок пакета.
	part := func(id, minutes int64, expires time.Time) Purchase {
		e := StoredTime(expires)
		return Purchase{ID: id, UserID: customer, Kind: KindTariff, TariffID: i64(5), Name: "Пакет", Minutes: minutes, ExpiresAt: &e}
	}
	t.Run("GAP-014", func(t *testing.T) {
		// Пакет 90 мин, истекает через 40 мин 30 с → сессия 40 мин. Через 10 мин досрочное завершение:
		// остаток 30 мин снова пакетом до того же срока.
		expires := *at(40*time.Minute + 30*time.Second)
		ends := t0.Add(40 * time.Minute)
		got, err := Settle([]Purchase{part(1, 40, expires)}, customer, &ends, t0.Add(10*time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		if got.KeptMinutes != 30 || len(got.Packages) != 1 || !got.Packages[0].ExpiresAt.Equal(expires) || got.RefundMain+got.RefundBonus != 0 {
			t.Fatalf("завершение = %+v", got)
		}
	})
	t.Run("GS-047", func(t *testing.T) {
		// Пакет 90 мин (срок — 30 дней) запущен целиком; завершение через 60 мин → 30 мин до того же срока.
		expires := *at(43200 * time.Minute)
		now := t0.Add(30 * time.Minute)
		ends := now.Add(90 * time.Minute)
		got, err := Settle([]Purchase{part(2, 90, expires)}, customer, &ends, now.Add(60*time.Minute))
		if err != nil || got.KeptMinutes != 30 || !got.Packages[0].ExpiresAt.Equal(expires) {
			t.Fatalf("= %+v, %v", got, err)
		}
	})
}
