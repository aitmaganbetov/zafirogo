package services

// Услуги клуба: список (активные сначала), добавить, изменить, удалить.
// Laravel: app/Http/Controllers/ClubServiceController.php.

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/aitmaganbetov/zafirogo/server/internal/activity"
	billingsvc "github.com/aitmaganbetov/zafirogo/server/internal/billing/service"
	"github.com/aitmaganbetov/zafirogo/server/internal/cash"
	servicesapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/services"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
	"github.com/aitmaganbetov/zafirogo/server/internal/money"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

const (
	// perPage — index(): paginate(50).
	perPage = 50
	// Правила validated(): name max:150, unit max:30, price max:9999999.
	maxName = 150
	maxUnit = 30
	// MaxPrice — price numeric|min:0|max:9999999 (₸) в тиынах.
	MaxPrice money.Tiyn = 999_999_900
	// subjectService — морф-класс услуги в журнале (PD-14).
	subjectService = `App\Models\ClubService`
)

func view(id int64, name string, price int64, unit *string, active bool) servicesapi.ClubService {
	return servicesapi.ClubService{Id: id, Name: name, PriceTiyn: price, Unit: unit, IsActive: active}
}

// ListServices — index(): orderByDesc('is_active')->orderBy('name')->paginate(50) и права экрана.
func (h *handlers) ListServices(ctx context.Context, req servicesapi.ListServicesRequestObject) (servicesapi.ListServicesResponseObject, error) {
	p := httpx.Principal(ctx)
	out := servicesapi.ServicePage{
		Items: []servicesapi.ClubService{}, PerPage: perPage,
		CanCreate: p.Can("services.create"), CanUpdate: p.Can("services.update"), CanDelete: p.Can("services.delete"),
	}
	offset := int32(0)
	if req.Params.Page != nil && *req.Params.Page > 1 {
		offset = int32(*req.Params.Page-1) * perPage // #nosec G115 -- page ≤ 100000 (схема)
	}
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		clubID := httpx.ClubID(ctx)
		total, err := q.ServicesCount(ctx, clubID)
		if err != nil {
			return err
		}
		out.Total = total
		rows, err := q.ServicesListPage(ctx, store.ServicesListPageParams{ClubID: clubID, PageSize: perPage, PageOffset: offset})
		for _, r := range rows {
			out.Items = append(out.Items, view(r.ID, r.Name, r.Price, r.Unit, r.IsActive))
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return servicesapi.ListServices200JSONResponse(out), nil
}

type fields struct {
	name   string
	price  int64
	unit   *string
	active bool
}

// validated — ClubServiceController::validated(): все ошибки полей сразу; name и unit — trim, пустая единица — null,
// is_active по умолчанию true.
func validated(b servicesapi.ServiceRequest) (fields, error) {
	var form httpx.FormErrors
	switch {
	case strings.TrimSpace(b.Name) == "":
		form.Add("name", httpx.RuleMessage("name", cash.RuleRequired, ""))
	case utf8.RuneCountInString(b.Name) > maxName:
		form.Add("name", httpx.RuleMessage("name", "max_string", "150"))
	}
	switch {
	case b.PriceTiyn < 0:
		form.Add("price", httpx.RuleMessage("price", cash.RuleMin, ""))
	case money.Tiyn(b.PriceTiyn) > MaxPrice:
		form.Add("price", httpx.RuleMessage("price", cash.RuleMax, "9999999"))
	}
	f := fields{name: strings.TrimSpace(b.Name), price: b.PriceTiyn, active: true}
	if b.Unit != nil {
		if utf8.RuneCountInString(*b.Unit) > maxUnit {
			form.Add("unit", httpx.RuleMessage("unit", "max_string", "30"))
		}
		if u := strings.TrimSpace(*b.Unit); u != "" {
			f.unit = &u
		}
	}
	if b.IsActive != nil {
		f.active = *b.IsActive
	}
	return f, form.Err()
}

// CreateService — store(): журнал service.created {price, unit}.
func (h *handlers) CreateService(ctx context.Context, req servicesapi.CreateServiceRequestObject) (servicesapi.CreateServiceResponseObject, error) {
	f, err := validated(*req.Body)
	if err != nil {
		return nil, err
	}
	var out servicesapi.ClubService
	err = h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		clubID, now := httpx.ClubID(ctx), h.d.NowFunc()
		r, err := q.ServiceInsert(ctx, store.ServiceInsertParams{ClubID: clubID, Name: f.name, Price: f.price, Unit: f.unit, IsActive: f.active, Now: &now})
		if err != nil {
			return err
		}
		out = view(r.ID, r.Name, r.Price, r.Unit, r.IsActive)
		return billingsvc.RecordActivity(ctx, q, activity.Entry{
			ClubID: clubID, Event: "service.created", Description: "Создана услуга: " + r.Name, UserID: httpx.Principal(ctx).UserID,
			SubjectType: subjectService, SubjectID: r.ID, At: now,
			Details: map[string]any{"price": money.Tiyn(r.Price).Tenge(), "unit": r.Unit},
		})
	})
	if err != nil {
		return nil, err
	}
	return servicesapi.CreateService201JSONResponse(out), nil
}

// UpdateService — update(): журнал service.updated {price, unit, is_active}.
func (h *handlers) UpdateService(ctx context.Context, req servicesapi.UpdateServiceRequestObject) (servicesapi.UpdateServiceResponseObject, error) {
	f, err := validated(*req.Body)
	if err != nil {
		return nil, err
	}
	var out servicesapi.ClubService
	err = h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		clubID, now := httpx.ClubID(ctx), h.d.NowFunc()
		r, err := q.ServiceUpdate(ctx, store.ServiceUpdateParams{ClubID: clubID, ID: req.ServiceId, Name: f.name, Price: f.price, Unit: f.unit, IsActive: f.active, Now: &now})
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NotFound()
		}
		if err != nil {
			return err
		}
		out = view(r.ID, r.Name, r.Price, r.Unit, r.IsActive)
		return billingsvc.RecordActivity(ctx, q, activity.Entry{
			ClubID: clubID, Event: "service.updated", Description: "Изменена услуга: " + r.Name, UserID: httpx.Principal(ctx).UserID,
			SubjectType: subjectService, SubjectID: r.ID, At: now,
			Details: map[string]any{"price": money.Tiyn(r.Price).Tenge(), "unit": r.Unit, "is_active": r.IsActive},
		})
	})
	if err != nil {
		return nil, err
	}
	return servicesapi.UpdateService200JSONResponse(out), nil
}

// DeleteService — destroy(): чеки хранят имя и цену; журнал service.deleted.
func (h *handlers) DeleteService(ctx context.Context, req servicesapi.DeleteServiceRequestObject) (servicesapi.DeleteServiceResponseObject, error) {
	var out servicesapi.ClubService
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		clubID := httpx.ClubID(ctx)
		r, err := q.ServiceGet(ctx, store.ServiceGetParams{ClubID: clubID, ID: req.ServiceId})
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NotFound()
		}
		if err != nil {
			return err
		}
		if _, err := q.ServiceDelete(ctx, store.ServiceDeleteParams{ClubID: clubID, ID: r.ID}); err != nil {
			return err
		}
		out = view(r.ID, r.Name, r.Price, r.Unit, r.IsActive)
		return billingsvc.RecordActivity(ctx, q, activity.Entry{
			ClubID: clubID, Event: "service.deleted", Description: "Удалена услуга: " + r.Name, UserID: httpx.Principal(ctx).UserID,
			SubjectType: subjectService, SubjectID: r.ID, At: h.d.NowFunc(),
		})
	})
	if err != nil {
		return nil, err
	}
	return servicesapi.DeleteService200JSONResponse(out), nil
}
