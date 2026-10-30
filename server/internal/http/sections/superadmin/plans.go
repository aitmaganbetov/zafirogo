package superadmin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/aitmaganbetov/zafirogo/server/internal/activity"
	superadminapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/superadmin"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// Laravel: app/Http/Controllers/Superadmin/PlanController.php. Тариф не удаляется — выключается.
const (
	subjectPlan = `App\Models\SaasPlan`
	// maxPlanPriceTiyn — price_monthly max:99999999 (тенге) в тиынах.
	maxPlanPriceTiyn = 99999999 * 100
)

var (
	alphaDashRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`) // alpha_dash:ascii
	lineSplitRe = regexp.MustCompile(`\r\n|\n|\r|\x{2028}|\x{2029}|\x0B|\x0C|\x{85}`)
)

func planFeatures(raw json.RawMessage) []string {
	var f []string
	if len(raw) == 0 || json.Unmarshal(raw, &f) != nil || f == nil {
		return []string{}
	}
	return f
}

// SaListPlans — PlanController::index(): по цене, с числом клубов.
func (h *handlers) SaListPlans(ctx context.Context, _ superadminapi.SaListPlansRequestObject) (superadminapi.SaListPlansResponseObject, error) {
	out := superadminapi.PlanList{Items: []superadminapi.Plan{}}
	err := h.tx(ctx, func(q *store.Queries) error {
		plans, err := q.SuperadminPlans(ctx)
		for _, p := range plans {
			out.Items = append(out.Items, superadminapi.Plan{Id: p.ID, Name: p.Name, Slug: p.Slug, PriceMonthlyTiyn: p.PriceMonthly,
				TrialDays: int(p.TrialDays), Features: planFeatures(p.Features), IsActive: p.IsActive, ClubsCount: p.ClubsCount})
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return superadminapi.SaListPlans200JSONResponse(out), nil
}

type planData struct {
	slug, name string
	price      *int64
	trialDays  int32
	features   json.RawMessage
	featList   []string
	active     bool
}

// validatePlan — PlanController::validated(): все ошибки формы сразу.
func validatePlan(ctx context.Context, q *store.Queries, f superadminapi.PlanForm, exceptID int64) (planData, error) {
	var fe httpx.FormErrors
	d := planData{slug: strings.TrimSpace(f.Slug), name: strings.TrimSpace(f.Name), price: f.PriceMonthlyTiyn}
	switch {
	case d.slug == "":
		fe.Add("slug", httpx.RuleMessage("slug", "required", ""))
	case utf8.RuneCountInString(d.slug) > 80:
		fe.Add("slug", httpx.RuleMessage("slug", "max_string", "80"))
	case !alphaDashRe.MatchString(d.slug):
		fe.Add("slug", httpx.RuleMessage("slug", "regex", ""))
	default:
		taken, err := q.SuperadminPlanSlugTaken(ctx, store.SuperadminPlanSlugTakenParams{Slug: d.slug, ExceptID: exceptID})
		if err != nil {
			return d, err
		}
		if taken {
			fe.Add("slug", httpx.RuleMessage("slug", "unique", ""))
		}
	}
	switch {
	case d.name == "":
		fe.Add("name", httpx.RuleMessage("name", "required", ""))
	case utf8.RuneCountInString(d.name) > 120:
		fe.Add("name", httpx.RuleMessage("name", "max_string", "120"))
	}
	if d.price != nil {
		switch {
		case *d.price < 0:
			fe.Add("price_monthly", httpx.RuleMessage("price_monthly", "min", "0"))
		case *d.price > maxPlanPriceTiyn:
			fe.Add("price_monthly", httpx.RuleMessage("price_monthly", "max", "99999999"))
		}
	}
	switch {
	case f.TrialDays < 0:
		fe.Add("trial_days", httpx.RuleMessage("trial_days", "min", "0"))
	case f.TrialDays > 365:
		fe.Add("trial_days", httpx.RuleMessage("trial_days", "max", "365"))
	}
	d.trialDays = int32(f.TrialDays) // #nosec G115 -- 0..365 проверено
	if f.Features != nil {
		if utf8.RuneCountInString(*f.Features) > 2000 {
			fe.Add("features", httpx.RuleMessage("features", "max_string", "2000"))
		}
		// По одному пункту в строке: preg_split('/\R/'), trim, без пустых; пусто — NULL.
		for _, line := range lineSplitRe.Split(*f.Features, -1) {
			if line = strings.TrimSpace(line); line != "" {
				d.featList = append(d.featList, line)
			}
		}
	}
	if len(d.featList) > 0 {
		d.features, _ = json.Marshal(d.featList)
	}
	d.active = f.IsActive != nil && *f.IsActive
	return d, fe.Err()
}

// SaCreatePlan — PlanController::store().
func (h *handlers) SaCreatePlan(ctx context.Context, req superadminapi.SaCreatePlanRequestObject) (superadminapi.SaCreatePlanResponseObject, error) {
	if req.Body == nil {
		return nil, httpx.RuleError("name", "required")
	}
	now := h.d.NowFunc()
	err := h.tx(ctx, func(q *store.Queries) error {
		d, err := validatePlan(ctx, q, *req.Body, 0)
		if err != nil {
			return err
		}
		id, err := q.SuperadminInsertPlan(ctx, store.SuperadminInsertPlanParams{Slug: d.slug, Name: d.name, PriceMonthly: d.price,
			TrialDays: d.trialDays, Features: d.features, IsActive: d.active, Now: &now})
		if err != nil {
			return err
		}
		if err := h.log(ctx, q, activity.PlatformEntry{Event: "superadmin.plan_created", Key: "Создан тариф платформы: :name",
			Params: map[string]string{"name": d.name}, SubjectType: subjectPlan, SubjectID: id}); err != nil {
			return err
		}
		return h.audit(ctx, q, 0, "tariff", "saas_plan.created", subjectPlan, id, nil, planAudit(d))
	})
	if err != nil {
		return nil, err
	}
	return superadminapi.SaCreatePlan201JSONResponse(msg(h.d.T(ctx, "Тариф создан.", nil))), nil
}

func planAudit(d planData) map[string]any {
	return map[string]any{"slug": d.slug, "name": d.name, "price_monthly_tiyn": d.price, "trial_days": d.trialDays,
		"features": d.featList, "is_active": d.active}
}

// tenge — вывод decimal:2 модели (price_monthly) для журнала: "1500.00".
func tenge(t *int64) string {
	if t == nil {
		return ""
	}
	v := *t
	sign := ""
	if v < 0 {
		sign, v = "-", -v
	}
	return fmt.Sprintf("%s%d.%02d", sign, v/100, v%100)
}

// SaUpdatePlan — PlanController::update(): журнал — только изменённые поля (getChanges() без updated_at).
func (h *handlers) SaUpdatePlan(ctx context.Context, req superadminapi.SaUpdatePlanRequestObject) (superadminapi.SaUpdatePlanResponseObject, error) {
	if req.Body == nil {
		return nil, httpx.RuleError("name", "required")
	}
	now := h.d.NowFunc()
	err := h.tx(ctx, func(q *store.Queries) error {
		cur, err := q.SuperadminLockPlan(ctx, req.PlanId)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NotFound()
		}
		if err != nil {
			return err
		}
		d, err := validatePlan(ctx, q, *req.Body, cur.ID)
		if err != nil {
			return err
		}
		if err := q.SuperadminUpdatePlan(ctx, store.SuperadminUpdatePlanParams{Slug: d.slug, Name: d.name, PriceMonthly: d.price,
			TrialDays: d.trialDays, Features: d.features, IsActive: d.active, Now: &now, ID: cur.ID}); err != nil {
			return err
		}
		// (string) значений модели: bool — "1"/"", массив — implode(', ').
		boolStr := func(b bool) string {
			if b {
				return "1"
			}
			return ""
		}
		changes := map[string]any{}
		if d.slug != cur.Slug {
			changes["slug"] = d.slug
		}
		if d.name != cur.Name {
			changes["name"] = d.name
		}
		if tenge(d.price) != tenge(cur.PriceMonthly) {
			changes["price_monthly"] = tenge(d.price)
		}
		if d.trialDays != cur.TrialDays {
			changes["trial_days"] = strconv.Itoa(int(d.trialDays))
		}
		if old := planFeatures(cur.Features); strings.Join(old, "\x00") != strings.Join(d.featList, "\x00") {
			changes["features"] = strings.Join(d.featList, ", ")
		}
		if d.active != cur.IsActive {
			changes["is_active"] = boolStr(d.active)
		}
		if len(changes) == 0 {
			return nil
		}
		if err := h.log(ctx, q, activity.PlatformEntry{Event: "superadmin.plan_updated", Key: "Изменён тариф платформы: :name",
			Params: map[string]string{"name": d.name}, SubjectType: subjectPlan, SubjectID: cur.ID, Details: changes}); err != nil {
			return err
		}
		before := planData{slug: cur.Slug, name: cur.Name, price: cur.PriceMonthly, trialDays: cur.TrialDays, featList: planFeatures(cur.Features), active: cur.IsActive}
		return h.audit(ctx, q, 0, "tariff", "saas_plan.updated", subjectPlan, cur.ID, planAudit(before), planAudit(d))
	})
	if err != nil {
		return nil, err
	}
	return superadminapi.SaUpdatePlan200JSONResponse(msg(h.d.T(ctx, "Тариф сохранён.", nil))), nil
}
