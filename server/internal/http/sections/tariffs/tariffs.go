package tariffs

// Тарифы (чтение). Laravel: TariffController::index().

import (
	"context"

	tariffsapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/tariffs"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// ListTariffs — TariffController::index(): тарифы клуба и цены часа зон.
func (h *handlers) ListTariffs(ctx context.Context, _ tariffsapi.ListTariffsRequestObject) (tariffsapi.ListTariffsResponseObject, error) {
	out := tariffsapi.TariffList{Tariffs: []tariffsapi.Tariff{}, Zones: []tariffsapi.Zone{}}
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		clubID := httpx.ClubID(ctx)
		rows, err := q.AdminListTariffs(ctx, clubID)
		if err != nil {
			return err
		}
		for _, t := range rows {
			v := tariffsapi.Tariff{
				Id: t.ID, Name: t.Name, Description: t.Description, DurationMinutes: t.DurationMinutes, PriceTiyn: t.Price,
				Currency: t.Currency, IsActive: t.IsActive, PaymentMethod: tariffsapi.TariffPaymentMethod(t.PaymentMethod),
				DiscountEnabled: t.DiscountEnabled, ZoneIds: t.ZoneIds,
			}
			if v.ZoneIds == nil {
				v.ZoneIds = []int64{}
			}
			if t.UsageLimitEnabled {
				v.UsageLimitMinutes = t.UsageLimitMinutes
			}
			if t.QuantityLimitEnabled {
				v.QuantityLimit = t.QuantityLimit
			}
			out.Tariffs = append(out.Tariffs, v)
		}
		zones, err := q.ListZonesByClub(ctx, clubID)
		if err != nil {
			return err
		}
		for _, z := range zones {
			out.Zones = append(out.Zones, tariffsapi.Zone{Id: z.ID, Name: z.Name, PricePerHourTiyn: z.PricePerHour})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return tariffsapi.ListTariffs200JSONResponse(out), nil
}

// guestModeEnabled — filter_var(settings()['guests']['guest_mode_enabled'] ?? true, FILTER_VALIDATE_BOOLEAN).
