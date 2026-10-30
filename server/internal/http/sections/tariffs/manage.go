package tariffs

// Экран «Зоны и тарифы»: список с фильтром зоны, создание, изменение, удаление.
// Laravel: app/Http/Controllers/TariffController.php page(), store(), update(), destroy(), attributes();
// app/Http/Requests/StoreTariffRequest.php, UpdateTariffRequest.php.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/aitmaganbetov/zafirogo/server/internal/activity"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/permissions"
	"github.com/aitmaganbetov/zafirogo/server/internal/billing/pricing"
	"github.com/aitmaganbetov/zafirogo/server/internal/club"
	tariffsapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/tariffs"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
	"github.com/aitmaganbetov/zafirogo/server/internal/money"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// Тексты TariffController (ключи перевода).
const (
	msgCreated = "Тариф создан."
	msgSaved   = "Тариф сохранён."
	msgDeleted = "Тариф «:tariff» удалён."

	logCreated = "Создан тариф: :tariff"
	logUpdated = "Изменён тариф: :tariff"
	logDeleted = "Удалён тариф: :tariff"

	// subjectTariff — морф-класс журнала ($tariff).
	subjectTariff = `App\Models\Tariff`
	// maxPrice — StoreTariffRequest price max:9999999999.99 (PQ-4, CHECK tariffs.price в 0014).
	maxPrice = money.Tiyn(999_999_999_999)
)

// durationFactors — TariffController::DURATION_FACTORS.
var durationFactors = map[string]int64{"minutes": 1, "hours": 60, "days": 1440}

var weekDays = []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}

// timeRe — date_format:H:i (часы 00–23, минуты 00–59).
var timeRe = regexp.MustCompile(`^([01]\d|2[0-3]):[0-5]\d$`)

// GetTariffsPage — TariffController::page(): зоны с ПК, тарифы (фильтр зоны), продажи, валюта, canDelete.
func (h *handlers) GetTariffsPage(ctx context.Context, req tariffsapi.GetTariffsPageRequestObject) (tariffsapi.GetTariffsPageResponseObject, error) {
	p := httpx.Principal(ctx)
	now := h.d.NowFunc()
	out := tariffsapi.TariffsPage{Zones: []tariffsapi.Zone{}, Tariffs: []tariffsapi.PageTariff{}, CanDelete: p.Role == permissions.RoleOwner}
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		info, err := club.Load(ctx, q, p.ClubID)
		if err != nil {
			return err
		}
		out.Currency = info.Currency()
		zones, err := q.AdminTariffPageZones(ctx, p.ClubID)
		if err != nil {
			return err
		}
		var filter int64
		for _, z := range zones {
			out.Zones = append(out.Zones, tariffsapi.Zone{Id: z.ID, Name: z.Name, PricePerHourTiyn: z.PricePerHour})
			// $zones->firstWhere('id', (int) request('zone')): фильтр — только зона из списка.
			if req.Params.Zone != nil && *req.Params.Zone == z.ID {
				filter = z.ID
			}
		}
		if filter != 0 {
			out.ZoneFilter = &filter
		}
		all, err := q.ListZonesByClub(ctx, p.ClubID)
		if err != nil {
			return err
		}
		names := make(map[int64]string, len(all))
		for _, z := range all {
			names[z.ID] = z.Name
		}
		rows, err := q.AdminTariffPage(ctx, store.AdminTariffPageParams{ClubID: p.ClubID, ZoneID: filter})
		if err != nil {
			return err
		}
		for _, t := range rows {
			v, err := pageTariff(t, names, now, info)
			if err != nil {
				return err
			}
			out.Tariffs = append(out.Tariffs, v)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return tariffsapi.GetTariffsPage200JSONResponse(out), nil
}

func pageTariff(t store.AdminTariffPageRow, zoneNames map[int64]string, at time.Time, info club.Info) (tariffsapi.PageTariff, error) {
	sale, err := pricing.ParseWeeklyPeriods(t.SalePeriods)
	if err != nil {
		sale = nil
	}
	validity, err := pricing.ParseWeeklyPeriods(t.ValidityPeriods)
	if err != nil {
		validity = nil
	}
	v := tariffsapi.PageTariff{
		Id: t.ID, Name: t.Name, Description: t.Description, DurationMinutes: t.DurationMinutes, PriceTiyn: t.Price,
		Currency: t.Currency, IsActive: t.IsActive, PaymentMethod: tariffsapi.PageTariffPaymentMethod(t.PaymentMethod),
		DiscountEnabled: t.DiscountEnabled, UsageLimitEnabled: t.UsageLimitEnabled, UsageLimitMinutes: t.UsageLimitMinutes,
		QuantityLimitEnabled: t.QuantityLimitEnabled, QuantityLimit: t.QuantityLimit, Sold: t.Sold,
		ZoneIds: t.ZoneIds, SalePeriods: periodsOut(sale), ValidityPeriods: periodsOut(validity),
		OnSale: sale.ContainsAt(at, info.Loc), InValidityPeriod: validity.ContainsAt(at, info.Loc),
	}
	if v.ZoneIds == nil {
		v.ZoneIds = []int64{}
	}
	zn := []string{}
	for _, id := range v.ZoneIds { // pivot по (club_zone_id, tariff_id) — порядок id зоны
		if n, ok := zoneNames[id]; ok {
			zn = append(zn, n)
		}
	}
	v.ZoneNames = &zn
	return v, nil
}

func periodsOut(p pricing.WeeklyPeriods) []tariffsapi.WeeklyPeriod {
	out := make([]tariffsapi.WeeklyPeriod, 0, len(p))
	for _, r := range p {
		out = append(out, tariffsapi.WeeklyPeriod{StartDay: r.StartDay, StartTime: r.StartTime, EndDay: r.EndDay, EndTime: r.EndTime})
	}
	return out
}

// tariffData — атрибуты модели после attributes(): длительности в минутах, выключенные лимиты — null.
type tariffData struct {
	name, paymentMethod                     string
	description                             *string
	durationMinutes                         int64
	price                                   money.Tiyn
	discount, usageEnabled, quantityEnabled bool
	isActive                                bool
	usageMinutes, quantityLimit             *int64
	zoneIDs                                 []int64
	validityPeriods, salePeriods            json.RawMessage
}

// validate — правила StoreTariffRequest/UpdateTariffRequest по порядку (все ошибки формы сразу).
func validate(ctx context.Context, q *store.Queries, clubID, exceptID int64, in tariffsapi.TariffInput) (tariffData, error) {
	var fe httpx.FormErrors
	d := tariffData{
		name: in.Name, description: in.Description, paymentMethod: in.PaymentMethod, discount: in.DiscountEnabled,
		usageEnabled: in.UsageLimitEnabled, quantityEnabled: in.QuantityLimitEnabled, isActive: in.IsActive,
		price: money.Tiyn(in.PriceTiyn),
	}
	// name: required|string|max:100|unique (без удалённых, без учёта регистра).
	switch n := utf8.RuneCountInString(in.Name); {
	case n == 0:
		fe.Add("name", httpx.RuleMessage("name", "required", ""))
	case n > 100:
		fe.Add("name", httpx.RuleMessage("name", "max_string", "100"))
	default:
		taken, err := q.AdminTariffNameTaken(ctx, store.AdminTariffNameTakenParams{ClubID: clubID, Name: in.Name, ExceptID: exceptID})
		if err != nil {
			return d, err
		}
		if taken {
			fe.Add("name", httpx.RuleMessage("name", "unique", ""))
		}
	}
	if in.Description != nil && utf8.RuneCountInString(*in.Description) > 500 {
		fe.Add("description", httpx.RuleMessage("description", "max_string", "500"))
	}
	if in.Description != nil && *in.Description == "" {
		d.description = nil // ConvertEmptyStringsToNull
	}
	durationOK := true
	switch {
	case in.DurationValue < 1:
		fe.Add("duration_value", httpx.RuleMessage("duration_value", "min", "1"))
		durationOK = false
	case in.DurationValue > 10080:
		fe.Add("duration_value", httpx.RuleMessage("duration_value", "max", "10080"))
		durationOK = false
	}
	factor, ok := durationFactors[in.DurationUnit]
	if !ok {
		fe.Add("duration_unit", httpx.RuleMessage("duration_unit", "in", ""))
	} else if durationOK {
		d.durationMinutes = in.DurationValue * factor
	}
	// price: required|numeric|min:0|max:9999999999.99 (тиыны, PQ-4).
	switch {
	case d.price < 0:
		fe.Add("price", httpx.RuleMessage("price", "min", "0"))
	case d.price > maxPrice:
		fe.Add("price", httpx.RuleMessage("price", "max", "9999999999.99"))
	}
	if in.PaymentMethod != "main_balance_only" && in.PaymentMethod != "main_or_bonus" {
		fe.Add("payment_method", httpx.RuleMessage("payment_method", "in", ""))
	}
	// zone_ids: nullable|array|max:100; zone_ids.*: integer|distinct|in(зоны с ПК).
	if in.ZoneIds != nil {
		ids := *in.ZoneIds
		if len(ids) > 100 {
			fe.Add("zone_ids", httpx.RuleMessage("zone_ids", "max", "100"))
		} else {
			allowed, err := q.AdminZonesWithComputersIn(ctx, store.AdminZonesWithComputersInParams{ClubID: clubID, Ids: ids})
			if err != nil {
				return d, err
			}
			seen := map[int64]int{}
			for _, id := range ids {
				seen[id]++
			}
			for i, id := range ids {
				field := "zone_ids." + strconv.Itoa(i)
				if seen[id] > 1 {
					fe.Add(field, httpx.RuleMessage(field, "distinct", ""))
				}
				if !slices.Contains(allowed, id) {
					fe.Add(field, httpx.RuleMessage(field, "in", ""))
				}
			}
			d.zoneIDs = ids
		}
	}
	// usage_limit_*: required_if:usage_limit_enabled,1, integer min:1 max:10080, unit in.
	if in.UsageLimitEnabled || in.UsageLimitValue != nil {
		switch {
		case in.UsageLimitValue == nil:
			if in.UsageLimitEnabled {
				fe.Add("usage_limit_value", httpx.RuleMessage("usage_limit_value", "required", ""))
			}
		case *in.UsageLimitValue < 1:
			fe.Add("usage_limit_value", httpx.RuleMessage("usage_limit_value", "min", "1"))
		case *in.UsageLimitValue > 10080:
			fe.Add("usage_limit_value", httpx.RuleMessage("usage_limit_value", "max", "10080"))
		}
	}
	unit := ""
	if in.UsageLimitUnit != nil {
		unit = *in.UsageLimitUnit
	}
	if unit == "" && in.UsageLimitEnabled {
		fe.Add("usage_limit_unit", httpx.RuleMessage("usage_limit_unit", "required", ""))
	} else if _, ok := durationFactors[unit]; unit != "" && !ok {
		fe.Add("usage_limit_unit", httpx.RuleMessage("usage_limit_unit", "in", ""))
	}
	if in.UsageLimitEnabled && in.UsageLimitValue != nil && !fe.Has("usage_limit_value") && !fe.Has("usage_limit_unit") {
		m := *in.UsageLimitValue * durationFactors[unit]
		d.usageMinutes = &m
	}
	// quantity_limit: required_if:quantity_limit_enabled,1, integer min:1 max:1000000; выключен — null.
	if in.QuantityLimitEnabled || in.QuantityLimit != nil {
		switch {
		case in.QuantityLimit == nil:
			if in.QuantityLimitEnabled {
				fe.Add("quantity_limit", httpx.RuleMessage("quantity_limit", "required", ""))
			}
		case *in.QuantityLimit < 1:
			fe.Add("quantity_limit", httpx.RuleMessage("quantity_limit", "min", "1"))
		case *in.QuantityLimit > 1_000_000:
			fe.Add("quantity_limit", httpx.RuleMessage("quantity_limit", "max", "1000000"))
		}
	}
	if in.QuantityLimitEnabled {
		d.quantityLimit = in.QuantityLimit
	}
	// validity_periods: required|array|min:1|max:50; sale_periods: nullable|array|max:50.
	switch {
	case len(in.ValidityPeriods) == 0:
		fe.Add("validity_periods", httpx.RuleMessage("validity_periods", "required", ""))
	case len(in.ValidityPeriods) > 50:
		fe.Add("validity_periods", httpx.RuleMessage("validity_periods", "max", "50"))
	default:
		validatePeriods(&fe, "validity_periods", in.ValidityPeriods)
	}
	if in.SalePeriods != nil {
		if len(*in.SalePeriods) > 50 {
			fe.Add("sale_periods", httpx.RuleMessage("sale_periods", "max", "50"))
		} else {
			validatePeriods(&fe, "sale_periods", *in.SalePeriods)
		}
	}
	if err := fe.Err(); err != nil {
		return d, err
	}
	var err error
	if d.validityPeriods, err = json.Marshal(in.ValidityPeriods); err != nil {
		return d, err
	}
	// Пустой список времени продажи — как форма без строк: в старом validated() даёт [] (всегда).
	if in.SalePeriods != nil {
		if d.salePeriods, err = json.Marshal(*in.SalePeriods); err != nil {
			return d, err
		}
	}
	if d.zoneIDs == nil {
		d.zoneIDs = []int64{}
	}
	return d, nil
}

func validatePeriods(fe *httpx.FormErrors, field string, periods []tariffsapi.WeeklyPeriod) {
	for i, p := range periods {
		base := field + "." + strconv.Itoa(i) + "."
		day := func(name, v string) {
			if v == "" {
				fe.Add(base+name, httpx.RuleMessage(base+name, "required", ""))
			} else if !slices.Contains(weekDays, v) {
				fe.Add(base+name, httpx.RuleMessage(base+name, "in", ""))
			}
		}
		clock := func(name, v string) {
			if v == "" {
				fe.Add(base+name, httpx.RuleMessage(base+name, "required", ""))
			} else if !timeRe.MatchString(v) {
				fe.Add(base+name, httpx.RuleMessage(base+name, "date_format", ""))
			}
		}
		day("start_day", p.StartDay)
		clock("start_time", p.StartTime)
		day("end_day", p.EndDay)
		clock("end_time", p.EndTime)
	}
}

// CreateTariff — TariffController::store(): тариф, зоны, журнал tariff.created, аудит.
func (h *handlers) CreateTariff(ctx context.Context, req tariffsapi.CreateTariffRequestObject) (tariffsapi.CreateTariffResponseObject, error) {
	if req.Body == nil {
		return nil, httpx.RuleError("name", "required")
	}
	p := httpx.Principal(ctx)
	now := h.d.NowFunc()
	var id int64
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		d, err := validate(ctx, q, p.ClubID, 0, *req.Body)
		if err != nil {
			return err
		}
		info, err := club.Load(ctx, q, p.ClubID)
		if err != nil {
			return err
		}
		currency := info.Currency()
		id, err = q.AdminInsertTariff(ctx, store.AdminInsertTariffParams{
			ClubID: p.ClubID, Name: d.name, Description: d.description, DurationMinutes: d.durationMinutes, Price: int64(d.price),
			IsActive: d.isActive, PaymentMethod: d.paymentMethod, Currency: currency, DiscountEnabled: d.discount,
			UsageLimitEnabled: d.usageEnabled, UsageLimitMinutes: d.usageMinutes, QuantityLimitEnabled: d.quantityEnabled,
			QuantityLimit: d.quantityLimit, ValidityPeriods: d.validityPeriods, SalePeriods: d.salePeriods, Now: &now,
		})
		if err != nil {
			return err
		}
		if err := syncZones(ctx, q, p.ClubID, id, d.zoneIDs); err != nil {
			return err
		}
		if _, err := activity.Record(ctx, q, activity.Entry{
			ClubID: p.ClubID, Event: "tariff.created", UserID: p.UserID, SubjectType: subjectTariff, SubjectID: id, At: now,
			Details: map[string]any{
				"price": tengeNumber(d.price), "currency": currency, "duration_minutes": d.durationMinutes, "zone_count": len(d.zoneIDs),
			},
		}.Translated(logCreated, map[string]string{"tariff": d.name})); err != nil {
			return err
		}
		return audit(ctx, q, p.ClubID, "tariff.created", id, nil, snapshot(d.price, d.durationMinutes, d.isActive, d.zoneIDs))
	})
	if err != nil {
		return nil, err
	}
	return tariffsapi.CreateTariff201JSONResponse{Id: &id, Message: h.d.T(ctx, msgCreated, nil)}, nil
}

// UpdateTariff — TariffController::update(): новые покупки по новым условиям; журнал с changed, аудит.
func (h *handlers) UpdateTariff(ctx context.Context, req tariffsapi.UpdateTariffRequestObject) (tariffsapi.UpdateTariffResponseObject, error) {
	if req.Body == nil {
		return nil, httpx.RuleError("name", "required")
	}
	p := httpx.Principal(ctx)
	now := h.d.NowFunc()
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		cur, err := q.AdminLockTariff(ctx, store.AdminLockTariffParams{ClubID: p.ClubID, ID: req.TariffId})
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NotFound()
		}
		if err != nil {
			return err
		}
		d, err := validate(ctx, q, p.ClubID, cur.ID, *req.Body)
		if err != nil {
			return err
		}
		if err := q.AdminUpdateTariff(ctx, store.AdminUpdateTariffParams{
			ClubID: p.ClubID, ID: cur.ID, Name: d.name, Description: d.description, DurationMinutes: d.durationMinutes,
			Price: int64(d.price), IsActive: d.isActive, PaymentMethod: d.paymentMethod, DiscountEnabled: d.discount,
			UsageLimitEnabled: d.usageEnabled, UsageLimitMinutes: d.usageMinutes, QuantityLimitEnabled: d.quantityEnabled,
			QuantityLimit: d.quantityLimit, ValidityPeriods: d.validityPeriods, SalePeriods: d.salePeriods, Now: &now,
		}); err != nil {
			return err
		}
		if err := syncZones(ctx, q, p.ClubID, cur.ID, d.zoneIDs); err != nil {
			return err
		}
		before := snapshot(money.Tiyn(cur.Price), cur.DurationMinutes, cur.IsActive, cur.ZoneIds)
		after := snapshot(d.price, d.durationMinutes, d.isActive, d.zoneIDs)
		if _, err := activity.Record(ctx, q, activity.Entry{
			ClubID: p.ClubID, Event: "tariff.updated", UserID: p.UserID, SubjectType: subjectTariff, SubjectID: cur.ID, At: now,
			Details: map[string]any{
				"changed": changed(before, after), "price": tengeNumber(d.price), "duration_minutes": d.durationMinutes,
			},
		}.Translated(logUpdated, map[string]string{"tariff": d.name})); err != nil {
			return err
		}
		return audit(ctx, q, p.ClubID, "tariff.updated", cur.ID, before, after)
	})
	if err != nil {
		return nil, err
	}
	return tariffsapi.UpdateTariff200JSONResponse{Id: &req.TariffId, Message: h.d.T(ctx, msgSaved, nil)}, nil
}

// DeleteTariff — TariffController::destroy(): мягкое удаление, журнал tariff.deleted, аудит.
func (h *handlers) DeleteTariff(ctx context.Context, req tariffsapi.DeleteTariffRequestObject) (tariffsapi.DeleteTariffResponseObject, error) {
	p := httpx.Principal(ctx)
	now := h.d.NowFunc()
	var name string
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		cur, err := q.AdminLockTariff(ctx, store.AdminLockTariffParams{ClubID: p.ClubID, ID: req.TariffId})
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NotFound()
		}
		if err != nil {
			return err
		}
		name = cur.Name
		if err := q.AdminSoftDeleteTariff(ctx, store.AdminSoftDeleteTariffParams{ClubID: p.ClubID, ID: cur.ID, Now: &now}); err != nil {
			return err
		}
		if _, err := activity.Record(ctx, q, activity.Entry{
			ClubID: p.ClubID, Event: "tariff.deleted", UserID: p.UserID, SubjectType: subjectTariff, SubjectID: cur.ID, At: now,
			Details: map[string]any{"price": tengeNumber(money.Tiyn(cur.Price)), "duration_minutes": cur.DurationMinutes},
		}.Translated(logDeleted, map[string]string{"tariff": cur.Name})); err != nil {
			return err
		}
		return audit(ctx, q, p.ClubID, "tariff.deleted", cur.ID,
			snapshot(money.Tiyn(cur.Price), cur.DurationMinutes, cur.IsActive, cur.ZoneIds), nil)
	})
	if err != nil {
		return nil, err
	}
	return tariffsapi.DeleteTariff200JSONResponse{Id: &req.TariffId, Message: h.d.T(ctx, msgDeleted, map[string]string{"tariff": name})}, nil
}

// syncZones — $tariff->zones()->syncWithPivotValues($zoneIds, ['club_id' => …]).
func syncZones(ctx context.Context, q *store.Queries, clubID, tariffID int64, ids []int64) error {
	if err := q.AdminDeleteTariffZones(ctx, store.AdminDeleteTariffZonesParams{ClubID: clubID, TariffID: tariffID, KeepZoneIds: ids}); err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	return q.AdminAddTariffZones(ctx, store.AdminAddTariffZonesParams{ClubID: clubID, TariffID: tariffID, ZoneIds: ids})
}

// tariffSnapshot — поля, изменения которых пишет журнал ($before/$after в update()): цена, длительность,
// активность, зоны (по возрастанию).
type tariffSnapshot struct {
	Price           string  `json:"price"`
	DurationMinutes int64   `json:"duration_minutes"`
	IsActive        bool    `json:"is_active"`
	Zones           []int64 `json:"zones"`
}

func snapshot(price money.Tiyn, duration int64, active bool, zones []int64) *tariffSnapshot {
	z := slices.Clone(zones)
	if z == nil {
		z = []int64{}
	}
	slices.Sort(z)
	return &tariffSnapshot{Price: price.Tenge(), DurationMinutes: duration, IsActive: active, Zones: z}
}

// changed — array_keys(array_filter($after, fn ($v, $k) => $before[$k] !== $v)) в порядке ключей $after.
func changed(before, after *tariffSnapshot) []string {
	out := []string{}
	if before.Price != after.Price {
		out = append(out, "price")
	}
	if before.DurationMinutes != after.DurationMinutes {
		out = append(out, "duration_minutes")
	}
	if before.IsActive != after.IsActive {
		out = append(out, "is_active")
	}
	if !slices.Equal(before.Zones, after.Zones) {
		out = append(out, "zones")
	}
	return out
}

// tengeNumber — (float) $tariff->price в details журнала: число тенге без float (json.Number из тиынов).
func tengeNumber(t money.Tiyn) json.Number { return json.Number(t.Tenge()) }

// audit — запись audit_log категории tariff (только добавление).
func audit(ctx context.Context, q *store.Queries, clubID int64, action string, tariffID int64, before, after *tariffSnapshot) error {
	actor := httpx.Actor(ctx)
	subject := "tariff"
	params := store.InsertAuditLogParams{
		ClubID: &clubID, ActorKind: actor.Kind, Category: "tariff", Action: action, SubjectType: &subject, SubjectID: &tariffID,
	}
	var err error
	if before != nil {
		if params.Before, err = json.Marshal(before); err != nil {
			return err
		}
	}
	if after != nil {
		if params.After, err = json.Marshal(after); err != nil {
			return err
		}
	}
	if actor.UserID > 0 {
		params.ActorUserID = &actor.UserID
	}
	if actor.IP.IsValid() {
		ip := actor.IP
		params.Ip = &ip
	}
	if actor.RequestID != "" {
		params.RequestID = &actor.RequestID
	}
	if err := q.InsertAuditLog(ctx, params); err != nil {
		return fmt.Errorf("tariffs: audit: %w", err)
	}
	return nil
}
