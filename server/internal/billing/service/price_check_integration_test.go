//go:build integration

package service_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/aitmaganbetov/zafirogo/server/internal/billing/pricing"
)

// Решение PQ-4 (docs/DECISIONS.md): цены тарифа и зоны в схеме ограничены 0…pricing.MaxPrice
// (CHECK price BETWEEN 0 AND 999999999999, миграция 0014) — формулы перенесены только для этого диапазона.
func TestPriceChecks(t *testing.T) {
	e := newEnv(t, "")
	if pricing.MaxPrice != 999_999_999_999 {
		t.Fatalf("pricing.MaxPrice = %d: CHECK в 0014 рассчитан на 999 999 999 999", pricing.MaxPrice)
	}
	// Граничные значения проходят.
	e.tariff(tariffSpec{name: "max", minutes: 60, price: pricing.MaxPrice})
	e.tariff(tariffSpec{name: "free", minutes: 60, price: 0})
	e.zone("max", ptr(int64(pricing.MaxPrice)))
	e.zone("zero", ptr(int64(0)))
	e.zone("no per-minute", nil)

	check := func(name, sql string, args ...any) {
		t.Helper()
		_, err := fixDB.Owner.Exec(context.Background(), sql, args...)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23514" || pgErr.ConstraintName != name {
			t.Fatalf("%s: want check violation, got %v", name, err)
		}
	}
	for _, price := range []int64{-1, int64(pricing.MaxPrice) + 1} {
		check("tariffs_price_check", `INSERT INTO tariffs (club_id, name, duration_minutes, price, payment_method, created_at, updated_at)
			VALUES ($1, 'bad', 60, $2, 'main_or_bonus', now(), now())`, e.club, price)
		check("club_zones_price_per_hour_check", `INSERT INTO club_zones (club_id, name, price_per_hour, created_at, updated_at)
			VALUES ($1, 'bad', $2, now(), now())`, e.club, price)
	}
}
