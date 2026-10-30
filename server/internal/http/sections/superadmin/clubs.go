package superadmin

import (
	"context"
	"errors"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aitmaganbetov/zafirogo/server/internal/activity"
	superadminapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/superadmin"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// Laravel: app/Http/Controllers/Superadmin/ClubController.php, app/Services/Monitoring/ClubAttention.php.
const (
	clubsPage   = 25
	membersPage = 30
	// loadMinutes — LOAD_MINUTES: запросы клуба в минуту — среднее за 15 минут.
	loadMinutes = 15
	// ClubAttention.
	offlineMinOnline  = 3
	webhookFailures   = 3
	subscriptionDays  = 3
	subjectSubscribed = `App\Models\ClubSubscription`
)

// sorts — SORTS: ключ → направление по умолчанию.
var sorts = map[string]string{
	"name": "asc", "online": "desc", "busy": "desc", "agent_requests": "desc", "api_requests": "desc",
	"last_login": "desc", "sessions": "desc", "webhooks": "desc", "ends": "asc",
}

// badge — superadmin/clubs/subscription-badge.blade.php: подпись и активна ли подписка (ClubSubscription::isActive()).
func badge(status *string, trialEnds, periodEnds *time.Time, now time.Time) superadminapi.SubscriptionBadge {
	if status == nil {
		return superadminapi.SubscriptionBadge{Label: "Нет подписки"}
	}
	active := (*status == "active" && (periodEnds == nil || periodEnds.After(now))) ||
		(*status == "trialing" && trialEnds != nil && trialEnds.After(now))
	pastPaid := (*status == "active" || *status == "expired") && periodEnds != nil && periodEnds.Before(now)
	switch {
	case *status == "trialing" && active:
		return superadminapi.SubscriptionBadge{Label: "Пробный период", Active: true}
	case active:
		return superadminapi.SubscriptionBadge{Label: "Активна", Active: true}
	case *status == "suspended":
		return superadminapi.SubscriptionBadge{Label: "Приостановлена"}
	case pastPaid:
		return superadminapi.SubscriptionBadge{Label: "Истекла"}
	}
	return superadminapi.SubscriptionBadge{Label: "Неактивна"}
}

// SaListClubs — ClubController::index().
func (h *handlers) SaListClubs(ctx context.Context, req superadminapi.SaListClubsRequestObject) (superadminapi.SaListClubsResponseObject, error) {
	p := req.Params
	search := ""
	if p.Search != nil {
		if len([]rune(*p.Search)) > 100 {
			return nil, httpx.RuleErrorWith("search", "max_string", "100")
		}
		search = strings.TrimSpace(*p.Search)
	}
	subscription := ""
	if p.Subscription != nil {
		subscription = string(*p.Subscription)
		if subscription != "active" && subscription != "inactive" {
			return nil, httpx.RuleError("subscription", "in")
		}
	}
	sort := "name"
	if p.Sort != nil {
		if _, ok := sorts[string(*p.Sort)]; !ok {
			return nil, httpx.RuleError("sort", "in")
		}
		sort = string(*p.Sort)
	}
	direction := sorts[sort]
	if p.Direction != nil {
		if *p.Direction != "asc" && *p.Direction != "desc" {
			return nil, httpx.RuleError("direction", "in")
		}
		direction = string(*p.Direction)
	}
	pg := page(p.Page)
	now := h.d.NowFunc()
	online := h.online(ctx)
	ids := sortedKeys(online)
	counts := make([]int64, len(ids))
	for i, id := range ids {
		counts[i] = online[id]
	}
	locale := h.d.Locale(ctx)
	out := superadminapi.ClubList{Items: []superadminapi.ClubRow{}, Page: pg, PerPage: clubsPage, Sort: sort, Direction: direction,
		LoadMinutes: loadMinutes, Issues: []superadminapi.Issue{}}
	err := h.tx(ctx, func(q *store.Queries) error {
		total, err := q.SuperadminCountClubs(ctx, store.SuperadminCountClubsParams{Search: search, Subscription: subscription, Now: &now})
		if err != nil {
			return err
		}
		out.Total = total
		rows, err := q.SuperadminClubs(ctx, store.SuperadminClubsParams{
			Subscription: subscription, Sort: sort, Direction: direction, PageOffset: int64(pg-1) * clubsPage, PageSize: clubsPage,
			OnlineClubIds: ids, OnlineCounts: counts, Now: &now, LoadFrom: now.Unix()/60 - loadMinutes,
			DayAgo: now.Add(-24 * time.Hour), Search: search,
		})
		if err != nil {
			return err
		}
		rel, err := readRelease(ctx, q)
		if err != nil {
			return err
		}
		latest := rel.latestVersion()
		if latest != "" {
			out.LatestAgent = &latest
		}
		outdated := map[int64]int64{}
		if latest != "" && len(rows) > 0 {
			clubIDs := make([]int64, len(rows))
			for i, r := range rows {
				clubIDs[i] = r.ID
			}
			vs, err := q.SuperadminClubAgentVersions(ctx, clubIDs)
			if err != nil {
				return err
			}
			for _, v := range vs {
				if isOutdated(v.AgentVersion, latest) {
					outdated[v.ClubID] += v.Aggregate
				}
			}
		}
		for _, r := range rows {
			row := superadminapi.ClubRow{Id: r.ID, Name: r.Name, Slug: r.Slug, ComputersCount: r.ComputersCount,
				OnlineCount: r.OnlineCount, BusyCount: r.BusyCount, SessionsToday: r.SessionsToday, WebhookFailures: r.WebhookFailures,
				AgentPerMinute: phpNum(float64(r.AgentRequests)/loadMinutes, 1), ApiPerMinute: phpNum(float64(r.ApiRequests)/loadMinutes, 1),
				Badge: badge(r.SubscriptionStatus, r.TrialEndsAt, r.CurrentPeriodEndsAt, now)}
			if r.OwnerName != "" {
				name := r.OwnerName
				row.OwnerName = &name
			}
			if r.ComputersCount > 0 {
				pct := intRound(float64(r.BusyCount) * 100 / float64(r.ComputersCount))
				row.BusyPercent = &pct
			}
			if r.LastLoginAt.Year() > 1 {
				human := diffForHumans(r.LastLoginAt, now, locale)
				row.LastLoginHuman = &human
			}
			if latest != "" {
				n := outdated[r.ID]
				row.OutdatedAgents = &n
			}
			// $endsAt?->isFuture() ? (int) ceil(now()->diffInHours($endsAt) / 24) : null
			if r.SubscriptionEndsAt.Year() > 1 && r.SubscriptionEndsAt.After(now) {
				hours := math.Floor(r.SubscriptionEndsAt.Sub(now).Hours())
				days := int64(math.Ceil(hours / 24))
				row.DaysLeft = &days
			}
			out.Items = append(out.Items, row)
		}
		totals, err := q.SuperadminClubTotals(ctx, &now)
		if err != nil {
			return err
		}
		out.Totals.Clubs, out.Totals.Active, out.Totals.Trial = totals.Clubs, totals.Active, totals.Trial
		out.Issues, err = h.issues(ctx, q, now, online)
		return err
	})
	if err != nil {
		return nil, err
	}
	return superadminapi.SaListClubs200JSONResponse(out), nil
}

// issues — ClubAttention::issues(): ПК внезапно ушли офлайн, недоставленные вебхуки, подписка скоро кончится.
func (h *handlers) issues(ctx context.Context, q *store.Queries, now time.Time, online map[int64]int64) ([]superadminapi.Issue, error) {
	out := []superadminapi.Issue{}
	active, err := q.SuperadminActiveSubscriptionClubs(ctx, &now)
	if err != nil {
		return nil, err
	}
	subscribed := map[int64]bool{}
	for _, id := range active {
		subscribed[id] = true
	}
	before, err := q.SuperadminLoadSnapshotsOnline(ctx, store.SuperadminLoadSnapshotsOnlineParams{FromAt: now.Add(-20 * time.Minute), ToAt: now.Add(-10 * time.Minute)})
	if err != nil {
		return nil, err
	}
	for _, b := range before {
		is := online[b.ClubID]
		if b.Online >= offlineMinOnline && is*2 < b.Online && subscribed[b.ClubID] {
			out = append(out, superadminapi.Issue{ClubId: b.ClubID, Kind: superadminapi.IssueKindOffline,
				Text: h.d.T(ctx, "ПК в сети: :now, 15 минут назад было :was. Возможно, у клуба пропал интернет.",
					map[string]string{"now": strconv.FormatInt(is, 10), "was": strconv.FormatInt(b.Online, 10)})})
		}
	}
	fails, err := q.SuperadminWebhookFailures(ctx, store.SuperadminWebhookFailuresParams{Since: ptr(now.Add(-24 * time.Hour)), MinFailures: webhookFailures})
	if err != nil {
		return nil, err
	}
	for _, f := range fails {
		out = append(out, superadminapi.Issue{ClubId: f.ClubID, Kind: superadminapi.IssueKindWebhooks,
			Text: h.d.T(ctx, "Не доставлено вебхуков за сутки: :count", map[string]string{"count": strconv.FormatInt(f.Failures, 10)})})
	}
	until := now.AddDate(0, 0, subscriptionDays)
	ending, err := q.SuperadminEndingSubscriptions(ctx, store.SuperadminEndingSubscriptionsParams{Now: &now, Until: &until})
	if err != nil {
		return nil, err
	}
	for _, s := range ending {
		ends := s.CurrentPeriodEndsAt
		if s.Status == "trialing" {
			ends = s.TrialEndsAt
		}
		date := ""
		if ends != nil {
			date = ends.UTC().Format("02.01.2006 15:04")
		}
		out = append(out, superadminapi.Issue{ClubId: s.ClubID, Kind: superadminapi.IssueKindSubscription,
			Text: h.d.T(ctx, "Подписка заканчивается :date", map[string]string{"date": date})})
	}
	if len(out) == 0 {
		return out, nil
	}
	ids := make([]int64, len(out))
	for i, o := range out {
		ids[i] = o.ClubId
	}
	names, err := q.SuperadminClubNames(ctx, ids)
	if err != nil {
		return nil, err
	}
	byID := map[int64]string{}
	for _, n := range names {
		byID[n.ID] = n.Name
	}
	for i := range out {
		if n, ok := byID[out[i].ClubId]; ok {
			out[i].Club = n
		} else {
			out[i].Club = "#" + strconv.FormatInt(out[i].ClubId, 10)
		}
	}
	return out, nil
}

func ptr[T any](v T) *T { return &v }

// SaGetClub — ClubController::show().
func (h *handlers) SaGetClub(ctx context.Context, req superadminapi.SaGetClubRequestObject) (superadminapi.SaGetClubResponseObject, error) {
	now := h.d.NowFunc()
	pg := page(req.Params.Page)
	var out superadminapi.ClubCard
	err := h.tx(ctx, func(q *store.Queries) error {
		c, err := q.SuperadminClub(ctx, req.ClubId)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NotFound()
		}
		if err != nil {
			return err
		}
		out = superadminapi.ClubCard{Id: c.ID, Name: c.Name, Slug: c.Slug, Timezone: c.Timezone, Page: pg, PerPage: membersPage,
			Badge: badge(c.SubscriptionStatus, c.TrialEndsAt, c.CurrentPeriodEndsAt, now)}
		if c.CreatedAt != nil {
			out.Created = c.CreatedAt.UTC().Format("02.01.2006")
		}
		if c.SubscriptionStatus != nil && c.SaasPlanID != nil {
			s := &struct {
				Active     bool    `json:"active"`
				PaidUntil  *string `json:"paid_until"`
				PlanName   *string `json:"plan_name"`
				SaasPlanId int64   `json:"saas_plan_id"`
				Status     string  `json:"status"`
				TrialUntil *string `json:"trial_until"`
			}{Active: out.Badge.Active, SaasPlanId: *c.SaasPlanID, Status: *c.SubscriptionStatus}
			if c.PlanName != "" {
				s.PlanName = ptr(c.PlanName)
			}
			if c.CurrentPeriodEndsAt != nil {
				s.PaidUntil = ptr(c.CurrentPeriodEndsAt.UTC().Format("02.01.2006"))
			}
			if *c.SubscriptionStatus == "trialing" && c.TrialEndsAt != nil {
				s.TrialUntil = ptr(c.TrialEndsAt.UTC().Format("02.01.2006"))
			}
			out.Subscription = s
		}
		if out.Total, err = q.SuperadminCountClubMembers(ctx, c.ID); err != nil {
			return err
		}
		members, err := q.SuperadminClubMembers(ctx, store.SuperadminClubMembersParams{ClubID: c.ID, PageSize: membersPage, PageOffset: int64(pg-1) * membersPage})
		if err != nil {
			return err
		}
		out.Members = []superadminapi.Member{}
		for _, m := range members {
			out.Members = append(out.Members, superadminapi.Member{Id: m.ID, Name: m.Name, Phone: m.Phone, Role: m.Role, Blocked: m.BlockedAt != nil})
		}
		if out.Computers.Total, err = q.SuperadminClubComputerTotal(ctx, c.ID); err != nil {
			return err
		}
		out.Computers.Online = h.online(ctx)[c.ID]
		heat, err := q.SuperadminClubHeatmap(ctx, store.SuperadminClubHeatmapParams{ClubID: c.ID, Since: now.AddDate(0, 0, -28)})
		if err != nil {
			return err
		}
		out.Heatmap = []superadminapi.HeatCell{}
		for _, cell := range heat {
			out.Heatmap = append(out.Heatmap, superadminapi.HeatCell{Weekday: int(cell.Weekday), Hour: int(cell.Hour),
				Busy: intRound(cell.Busy), Online: phpNum(cell.Online, 1)})
		}
		plans, err := q.SuperadminPlans(ctx)
		if err != nil {
			return err
		}
		out.Plans = []superadminapi.PlanOption{}
		for _, p := range plans {
			out.Plans = append(out.Plans, superadminapi.PlanOption{Id: p.ID, Name: p.Name, IsActive: p.IsActive})
		}
		act, err := q.SuperadminClubActivity(ctx, c.ID)
		if err != nil {
			return err
		}
		out.Activity = h.rows(ctx, clubActivityRows(act), false)
		id := c.ID
		logs, err := q.SuperadminPlatformLogs(ctx, store.SuperadminPlatformLogsParams{ClubID: &id, PageSize: 15})
		out.PlatformLogs = h.rows(ctx, platformRows(logs), true)
		return err
	})
	if err != nil {
		return nil, err
	}
	return superadminapi.SaGetClub200JSONResponse(out), nil
}

// subscriptionAudit — состояние подписки для audit_log.
func subscriptionAudit(planID int64, status string, ends *time.Time) map[string]any {
	m := map[string]any{"saas_plan_id": planID, "status": status}
	if ends != nil {
		m["current_period_ends_at"] = ends.UTC().Format(time.RFC3339)
	}
	return m
}

// SaExtendSubscription — ClubController::extend(): N дней активной подписки (от сегодня, если срок прошёл).
func (h *handlers) SaExtendSubscription(ctx context.Context, req superadminapi.SaExtendSubscriptionRequestObject) (superadminapi.SaExtendSubscriptionResponseObject, error) {
	if req.Body == nil {
		return nil, httpx.RuleError("days", "required")
	}
	days := req.Body.Days
	switch {
	case days < 1:
		return nil, httpx.RuleErrorWith("days", "min", "1")
	case days > 3650:
		return nil, httpx.RuleErrorWith("days", "max", "3650")
	}
	now := h.d.NowFunc()
	err := h.tx(ctx, func(q *store.Queries) error {
		name, err := q.SuperadminClubExists(ctx, req.ClubId)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NotFound()
		}
		if err != nil {
			return err
		}
		if req.Body.SaasPlanId != nil {
			if _, err := q.SuperadminPlanExists(ctx, *req.Body.SaasPlanId); errors.Is(err, pgx.ErrNoRows) {
				return httpx.RuleError("saas_plan_id", "exists")
			} else if err != nil {
				return err
			}
		}
		cur, err := q.SuperadminLockSubscription(ctx, req.ClubId)
		exists := err == nil
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		from := now
		if exists && cur.CurrentPeriodEndsAt != nil && cur.CurrentPeriodEndsAt.After(now) {
			from = *cur.CurrentPeriodEndsAt
		}
		var planID int64
		switch {
		case req.Body.SaasPlanId != nil:
			planID = *req.Body.SaasPlanId
		case exists:
			planID = cur.SaasPlanID
		default:
			if planID, err = q.SuperadminCheapestActivePlan(ctx); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return httpx.RuleError("saas_plan_id", "required")
				}
				return err
			}
		}
		ends := from.AddDate(0, 0, days)
		var subID int64
		var before any
		if exists {
			subID = cur.ID
			before = subscriptionAudit(cur.SaasPlanID, cur.Status, cur.CurrentPeriodEndsAt)
			if err := q.SuperadminExtendSubscription(ctx, store.SuperadminExtendSubscriptionParams{SaasPlanID: planID, EndsAt: &ends, Now: &now, ClubID: req.ClubId}); err != nil {
				return err
			}
		} else if subID, err = q.SuperadminInsertSubscription(ctx, store.SuperadminInsertSubscriptionParams{ClubID: req.ClubId, SaasPlanID: planID, Now: now, EndsAt: &ends}); err != nil {
			return err
		}
		if err := h.log(ctx, q, activity.PlatformEntry{Event: "superadmin.subscription_extended",
			Key:    "Подписка клуба :club продлена на :days дн. (до :date)",
			Params: map[string]string{"club": name, "days": strconv.Itoa(days), "date": ends.UTC().Format("02.01.2006")},
			ClubID: req.ClubId, SubjectType: subjectSubscribed, SubjectID: subID,
			Details: map[string]any{"days": days, "until": ends.UTC().Format(time.RFC3339)}}); err != nil {
			return err
		}
		return h.audit(ctx, q, req.ClubId, "settings", "subscription.extended", subjectSubscribed, subID, before, subscriptionAudit(planID, "active", &ends))
	})
	if err != nil {
		return nil, err
	}
	return superadminapi.SaExtendSubscription200JSONResponse(msg(h.d.T(ctx, "Подписка продлена.", nil))), nil
}

// SaSuspendSubscription — ClubController::suspend(): клуб теряет доступ до продления.
func (h *handlers) SaSuspendSubscription(ctx context.Context, req superadminapi.SaSuspendSubscriptionRequestObject) (superadminapi.SaSuspendSubscriptionResponseObject, error) {
	now := h.d.NowFunc()
	err := h.tx(ctx, func(q *store.Queries) error {
		name, err := q.SuperadminClubExists(ctx, req.ClubId)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NotFound()
		}
		if err != nil {
			return err
		}
		cur, err := q.SuperadminLockSubscription(ctx, req.ClubId)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NotFound() // abort_if($subscription === null, 404)
		}
		if err != nil {
			return err
		}
		if err := q.SuperadminSuspendSubscription(ctx, store.SuperadminSuspendSubscriptionParams{Now: &now, ClubID: req.ClubId}); err != nil {
			return err
		}
		if err := h.log(ctx, q, activity.PlatformEntry{Event: "superadmin.subscription_suspended", Key: "Подписка клуба :club приостановлена",
			Params: map[string]string{"club": name}, ClubID: req.ClubId, SubjectType: subjectSubscribed, SubjectID: cur.ID}); err != nil {
			return err
		}
		return h.audit(ctx, q, req.ClubId, "settings", "subscription.suspended", subjectSubscribed, cur.ID,
			subscriptionAudit(cur.SaasPlanID, cur.Status, cur.CurrentPeriodEndsAt), subscriptionAudit(cur.SaasPlanID, "suspended", cur.CurrentPeriodEndsAt))
	})
	if err != nil {
		return nil, err
	}
	return superadminapi.SaSuspendSubscription200JSONResponse(msg(h.d.T(ctx, "Подписка приостановлена.", nil))), nil
}

// SaChangeClubPlan — ClubController::changePlan().
func (h *handlers) SaChangeClubPlan(ctx context.Context, req superadminapi.SaChangeClubPlanRequestObject) (superadminapi.SaChangeClubPlanResponseObject, error) {
	if req.Body == nil || req.Body.SaasPlanId <= 0 {
		return nil, httpx.RuleError("saas_plan_id", "required")
	}
	now := h.d.NowFunc()
	err := h.tx(ctx, func(q *store.Queries) error {
		c, err := q.SuperadminClub(ctx, req.ClubId)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NotFound()
		}
		if err != nil {
			return err
		}
		to, err := q.SuperadminPlanExists(ctx, req.Body.SaasPlanId)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.RuleError("saas_plan_id", "exists")
		}
		if err != nil {
			return err
		}
		cur, err := q.SuperadminLockSubscription(ctx, req.ClubId)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NotFound()
		}
		if err != nil {
			return err
		}
		if err := q.SuperadminChangeSubscriptionPlan(ctx, store.SuperadminChangeSubscriptionPlanParams{SaasPlanID: req.Body.SaasPlanId, Now: &now, ClubID: req.ClubId}); err != nil {
			return err
		}
		from := c.PlanName
		if from == "" {
			from = "—"
		}
		if err := h.log(ctx, q, activity.PlatformEntry{Event: "superadmin.subscription_plan_changed", Key: "Тариф клуба :club: :from → :to",
			Params: map[string]string{"club": c.Name, "from": from, "to": to}, ClubID: req.ClubId, SubjectType: subjectSubscribed, SubjectID: cur.ID}); err != nil {
			return err
		}
		return h.audit(ctx, q, req.ClubId, "tariff", "subscription.plan_changed", subjectSubscribed, cur.ID,
			map[string]any{"saas_plan_id": cur.SaasPlanID}, map[string]any{"saas_plan_id": req.Body.SaasPlanId})
	})
	if err != nil {
		return nil, err
	}
	return superadminapi.SaChangeClubPlan200JSONResponse(msg(h.d.T(ctx, "Тариф клуба изменён.", nil))), nil
}
