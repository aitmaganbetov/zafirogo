// Package notifications — REST-раздел: колокольчик уведомлений панели клуба (api/notifications.yaml).
//
// Laravel: app/Http/Controllers/NotificationController.php, app/Services/ClubNotifications.php for().
// Уведомления выводятся из данных: журнал (события колокольчика), подписка, приглашения, чат, заказы, низкий
// остаток, ПК не на связи; новое (Этап 7, S7-H-11) — открытые проблемы здоровья ПК. Обновление — событие notifications.changed (вместо опроса раз в 60 с).
// «Новая версия агента» — релиз из platform_settings 'agent_release' (AgentUpdate::status()).
package notifications

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/aitmaganbetov/zafirogo/server/internal/activity"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/health"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/permissions"
	"github.com/aitmaganbetov/zafirogo/server/internal/club"
	"github.com/aitmaganbetov/zafirogo/server/internal/clubmap"
	notificationsapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/notifications"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// Маршруты notifications.* — club-role персонала (не право club-area): проверка в обработчике.
var access = httpx.Access{Public: []string{"ListNotifications", "MarkNotificationsSeen"}}

type handlers struct {
	d *httpx.Deps
}

var _ notificationsapi.StrictServerInterface = (*handlers)(nil)

// Section — уведомления. Регистрация — server/internal/http/sections.go.
func Section() httpx.Section {
	return httpx.Section{Name: "notifications", Mount: mount}
}

func mount(r chi.Router, d *httpx.Deps) {
	si := notificationsapi.NewStrictHandlerWithOptions(&handlers{d: d},
		httpx.Middlewares[notificationsapi.StrictMiddlewareFunc](access),
		notificationsapi.StrictHTTPServerOptions{RequestErrorHandlerFunc: d.RequestError, ResponseErrorHandlerFunc: d.ResponseError()})
	notificationsapi.HandlerWithOptions(si, notificationsapi.ChiServerOptions{BaseRouter: r, ErrorHandlerFunc: d.ParamError})
}

const (
	// lowStockBelow — ClubNotifications::LOW_STOCK_BELOW.
	lowStockBelow = 5
	// activityDays — ClubNotifications::ACTIVITY_DAYS.
	activityDays = 7
	// heartbeatTimeout — ClubComputer::HEARTBEAT_TIMEOUT_SECONDS.
	heartbeatTimeout = 90 * time.Second
	// healthComputers — новое (S7-H-11): не больше стольких ПК с проблемами здоровья (свежие); все — на карте клуба.
	healthComputers = 10
)

var managers = []string{permissions.RoleOwner, permissions.RoleAdmin, permissions.RoleManager}

// staff — club-role:owner,admin,manager,cashier,operator (RequireClubRole: 403 без текста); запись — не в режиме
// просмотра суперадмина.
func staff(ctx context.Context, method string) error {
	p := httpx.Principal(ctx)
	if p.UserID == 0 {
		return httpx.NewAPIError(http.StatusUnauthorized, permissions.CodeUnauthenticated, permissions.MsgUnauthenticated)
	}
	if status, code, msg, ok := permissions.CheckClub(p, method); !ok {
		return httpx.NewAPIError(status, code, msg)
	}
	if !slices.Contains([]string{permissions.RoleOwner, permissions.RoleAdmin, permissions.RoleManager, permissions.RoleCashier, permissions.RoleOperator}, p.Role) && !p.Superadmin {
		return httpx.NewAPIError(http.StatusForbidden, permissions.CodeForbidden, "")
	}
	return nil
}

type item struct {
	notificationsapi.Notification
	at time.Time
}

// ListNotifications — index(): уведомления по роли, новые сверху, непрочитано — после notifications_seen_at.
func (h *handlers) ListNotifications(ctx context.Context, _ notificationsapi.ListNotificationsRequestObject) (notificationsapi.ListNotificationsResponseObject, error) {
	if err := staff(ctx, http.MethodGet); err != nil {
		return nil, err
	}
	p := httpx.Principal(ctx)
	now := h.d.NowFunc()
	locale := h.d.Locale(ctx)
	var items []item
	var seenAt *time.Time
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		info, err := club.Load(ctx, q, p.ClubID)
		if err != nil {
			return err
		}
		role := p.Role
		if p.Superadmin && p.ReadOnly {
			role = permissions.RoleOwner // просмотр клуба суперадмином — как владелец
		}
		if seenAt, err = q.NotificationsSeenAt(ctx, store.NotificationsSeenAtParams{ClubID: p.ClubID, UserID: p.UserID}); err != nil {
			seenAt = nil // членства нет (просмотр суперадмином) — всё непрочитано
		}
		isManager := slices.Contains(managers, role)
		if isManager {
			if items, err = h.activity(ctx, q, now, items); err != nil {
				return err
			}
			if items, err = h.subscription(ctx, q, info, now, items); err != nil {
				return err
			}
			if items, err = h.invitations(ctx, q, info, now, items); err != nil {
				return err
			}
			if items, err = h.agentUpdate(ctx, q, p.ClubID, now, items); err != nil {
				return err
			}
		}
		if items, err = h.chat(ctx, q, p.ClubID, items); err != nil {
			return err
		}
		if items, err = h.orders(ctx, q, p.ClubID, items); err != nil {
			return err
		}
		if isManager || role == permissions.RoleCashier {
			if items, err = h.lowStock(ctx, q, p.ClubID, now, items); err != nil {
				return err
			}
		}
		if isManager || role == permissions.RoleOperator {
			if items, err = h.offline(ctx, q, p.ClubID, now, items); err != nil {
				return err
			}
			if items, err = h.computerHealth(ctx, q, p.ClubID, items); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	slices.SortStableFunc(items, func(a, b item) int { return b.at.Compare(a.at) })
	out := notificationsapi.NotificationList{Items: make([]notificationsapi.Notification, 0, len(items))}
	for _, it := range items {
		n := it.Notification
		n.Unread = seenAt == nil || it.at.After(*seenAt)
		n.At = it.at.Format(time.RFC3339)
		n.AtHuman = diffForHumans(it.at, now, locale)
		if n.Unread {
			out.UnreadCount++
		}
		out.Items = append(out.Items, n)
	}
	return notificationsapi.ListNotifications200JSONResponse(out), nil
}

// MarkNotificationsSeen — markSeen().
func (h *handlers) MarkNotificationsSeen(ctx context.Context, _ notificationsapi.MarkNotificationsSeenRequestObject) (notificationsapi.MarkNotificationsSeenResponseObject, error) {
	if err := staff(ctx, http.MethodPost); err != nil {
		return nil, err
	}
	p := httpx.Principal(ctx)
	now := h.d.NowFunc()
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		return q.MarkNotificationsSeen(ctx, store.MarkNotificationsSeenParams{ClubID: p.ClubID, UserID: p.UserID, Now: &now})
	})
	if err != nil {
		return nil, err
	}
	return notificationsapi.MarkNotificationsSeen200JSONResponse{UnreadCount: 0}, nil
}

func (h *handlers) activity(ctx context.Context, q *store.Queries, now time.Time, items []item) ([]item, error) {
	pr := httpx.Principal(ctx)
	events := make([]string, 0, len(activity.BellEvents))
	for e := range activity.BellEvents {
		events = append(events, e)
	}
	slices.Sort(events)
	rows, err := q.NotificationActivity(ctx, store.NotificationActivityParams{
		ClubID: pr.ClubID, Since: now.Add(-activityDays * 24 * time.Hour), Events: events, UserID: pr.UserID,
	})
	if err != nil {
		return items, err
	}
	for _, r := range rows {
		label, _ := activity.Label(r.Event)
		desc := r.Description
		if t := activity.TextOf(r.Details); t != nil {
			desc = h.d.T(ctx, t.Key, t.Params)
		}
		body := desc
		if r.UserName != nil && !strings.Contains(desc, *r.UserName) {
			body = desc + " · " + *r.UserName
		}
		items = append(items, item{at: r.CreatedAt, Notification: notificationsapi.Notification{
			Id: "activity-" + strconv.FormatInt(r.ID, 10), Icon: activity.BellEvents[r.Event], Title: h.d.T(ctx, label, nil),
			Body: body, Url: "/activity-logs?event=" + r.Event,
		}})
	}
	return items, nil
}

func (h *handlers) chat(ctx context.Context, q *store.Queries, clubID int64, items []item) ([]item, error) {
	r, err := q.NotificationChat(ctx, clubID)
	if err != nil || r.Unread == 0 {
		return items, err
	}
	return append(items, item{at: r.LastAt, Notification: notificationsapi.Notification{
		Id: "chat-" + strconv.FormatInt(r.LastID, 10), Icon: "fa-comments",
		Title: h.d.T(ctx, "Новые сообщения в чате: :count", map[string]string{"count": strconv.FormatInt(r.Unread, 10)}),
		Body:  strimwidth(r.LastBody, 120), Url: "/chat",
	}}), nil
}

func (h *handlers) orders(ctx context.Context, q *store.Queries, clubID int64, items []item) ([]item, error) {
	r, err := q.NotificationOrders(ctx, clubID)
	if err != nil || r.NewCount == 0 {
		return items, err
	}
	return append(items, item{at: r.LastAt, Notification: notificationsapi.Notification{
		Id: "order-" + strconv.FormatInt(r.LastID, 10), Icon: "fa-bag-shopping",
		Title: h.d.T(ctx, "Новые заказы: :count", map[string]string{"count": strconv.FormatInt(r.NewCount, 10)}),
		Body:  h.d.T(ctx, "Последний — на ПК :computer", map[string]string{"computer": r.LastComputer}), Url: "/shop-orders",
	}}), nil
}

func (h *handlers) lowStock(ctx context.Context, q *store.Queries, clubID int64, now time.Time, items []item) ([]item, error) {
	rows, err := q.NotificationLowStock(ctx, store.NotificationLowStockParams{ClubID: clubID, Below: lowStockBelow})
	if err != nil || len(rows) == 0 {
		return items, err
	}
	var names []string
	var at *time.Time
	for i, r := range rows {
		if i < 3 {
			names = append(names, h.d.T(ctx, ":product (:quantity шт.)", map[string]string{"product": r.Name, "quantity": strconv.FormatInt(r.Quantity, 10)}))
		}
		if r.UpdatedAt != nil && (at == nil || r.UpdatedAt.After(*at)) {
			at = r.UpdatedAt
		}
	}
	when := now
	if at != nil {
		when = *at
	}
	count := strconv.Itoa(len(rows))
	return append(items, item{at: when, Notification: notificationsapi.Notification{
		Id: "low-stock", Icon: "fa-boxes-stacked",
		Title: h.d.T(ctx, "Низкий остаток: :count товар|Низкий остаток: :count товара|Низкий остаток: :count товаров", map[string]string{"count": count}),
		Body:  h.listWithMore(ctx, strings.Join(names, ", "), len(rows)-3), Url: "/warehouse",
	}}), nil
}

// offline — ПК, которые должны быть на связи (status <> offline), но не отвечают дольше HEARTBEAT_TIMEOUT. Связь
// агента в новом сервере — присутствие в Valkey: ПК, чей агент сейчас в сети, не считается.
func (h *handlers) offline(ctx context.Context, q *store.Queries, clubID int64, now time.Time, items []item) ([]item, error) {
	rows, err := q.NotificationOfflineComputers(ctx, store.NotificationOfflineComputersParams{ClubID: clubID, SeenBefore: ptr(now.Add(-heartbeatTimeout))})
	if err != nil || len(rows) == 0 {
		return items, err
	}
	online := map[int64]bool{}
	if h.d.Presence != nil {
		if states, err := h.d.Presence.List(ctx, clubID); err == nil {
			for _, s := range states {
				if s.Online {
					online[s.ComputerID] = true
				}
			}
		}
	}
	var names []string
	var latest time.Time
	n := 0
	for _, r := range rows {
		if online[r.ID] {
			continue
		}
		n++
		if len(names) < 5 {
			zone := ""
			if r.ZoneName != nil {
				zone = *r.ZoneName
			}
			names = append(names, club.ComputerName(r.DisplayName, r.DeviceName, r.Number)+" ("+zone+")")
		}
		if r.LastSeenAt != nil && r.LastSeenAt.After(latest) {
			latest = *r.LastSeenAt
		}
	}
	if n == 0 {
		return items, nil
	}
	return append(items, item{at: latest.Add(heartbeatTimeout), Notification: notificationsapi.Notification{
		Id: "computers-offline", Icon: "fa-plug-circle-xmark",
		Title: h.d.T(ctx, "Не на связи: :count ПК", map[string]string{"count": strconv.Itoa(n)}),
		Body:  h.listWithMore(ctx, strings.Join(names, ", "), n-5), Url: "/club-computers",
	}}), nil
}

// problemLabels — подписи проблем здоровья (те же ключи, что web/src/features/clubmap/healthText.ts problemLabel()).
var problemLabels = map[string]string{
	health.KindCPUHot:      "Перегрев CPU",
	health.KindGPUHot:      "Перегрев GPU",
	health.KindDiskLow:     "Мало места на диске",
	health.KindDiskSmart:   "Диск неисправен (SMART)",
	health.KindMouse:       "Мышь не подключена",
	health.KindKeyboard:    "Клавиатура не подключена",
	health.KindMonitor:     "Монитор не подключён",
	health.KindNetworkSlow: "Медленная сеть",
	health.KindUnstable:    "Нестабильная работа: сбои за 7 суток",
}

// computerHealth — новое (Этап 7, S7-H-11): пункт на ПК с открытыми проблемами здоровья (computer_health_events):
// «Проблемы с ПК 7» — «Перегрев CPU (92 °C), Мышь не подключена», переход — карта клуба с карточкой этого ПК. Время
// пункта — последнее появление проблемы: снятая и снова появившаяся проблема — снова непрочитана.
func (h *handlers) computerHealth(ctx context.Context, q *store.Queries, clubID int64, items []item) ([]item, error) {
	rows, err := q.NotificationComputerHealth(ctx, store.NotificationComputerHealthParams{ClubID: clubID, PageSize: healthComputers})
	if err != nil {
		return items, err
	}
	for _, r := range rows {
		var details map[string]struct {
			TempDeci *int64 `json:"temp_deci"`
		}
		_ = json.Unmarshal(r.Details, &details)
		problems := make([]string, 0, len(r.Kinds))
		for _, kind := range r.Kinds {
			label := kind // вид новее сервера — как problemLabel() фронта
			if key, ok := problemLabels[kind]; ok {
				label = h.d.T(ctx, key, nil)
			}
			if d := details[kind].TempDeci; d != nil && (kind == health.KindCPUHot || kind == health.KindGPUHot) {
				// Десятые °C → целые, как deg() карты (Math.round(deci / 10)).
				label = h.d.T(ctx, ":problem (:value °C)", map[string]string{"problem": label, "value": strconv.FormatInt(int64(math.Round(float64(*d)/10)), 10)})
			}
			problems = append(problems, label)
		}
		id := strconv.FormatInt(r.ComputerID, 10)
		// id меняется с каждой новой проблемой ПК: звук колокольчика помнит уже прозвучавшие id (как chat-/order-).
		items = append(items, item{at: r.LastRaisedAt, Notification: notificationsapi.Notification{
			Id: "computer-health-" + id + "-" + strconv.FormatInt(r.LastRaisedAt.UnixMicro(), 10), Icon: "fa-triangle-exclamation",
			Title: h.d.T(ctx, "Проблемы с ПК :computer", map[string]string{"computer": club.ComputerName(r.DisplayName, r.DeviceName, r.Number)}),
			Body:  strings.Join(problems, ", "), Url: "/club-map?computer=" + id,
		}})
	}
	return items, nil
}

func (h *handlers) subscription(ctx context.Context, q *store.Queries, info club.Info, now time.Time, items []item) ([]item, error) {
	s, err := q.NotificationSubscription(ctx, info.ID)
	if err != nil {
		return items, nil //nolint:nilerr // подписки нет — уведомления нет
	}
	var ends *time.Time
	switch s.Status {
	case "trialing":
		ends = s.TrialEndsAt
	case "active", "expired":
		ends = s.CurrentPeriodEndsAt
	}
	if ends == nil || ends.After(now.Add(72*time.Hour)) {
		return items, nil
	}
	past := ends.Before(now)
	var title string
	switch {
	case s.Status == "trialing" && past:
		title = "Пробный период закончился"
	case s.Status == "trialing":
		title = "Пробный период заканчивается"
	case past:
		title = "Подписка закончилась"
	default:
		title = "Подписка заканчивается"
	}
	date := ends.In(info.Loc).Format("02.01.2006 15:04")
	body := h.d.T(ctx, "Действует до :date", map[string]string{"date": date})
	if past {
		body = h.d.T(ctx, "Окончание: :date", map[string]string{"date": date})
	}
	return append(items, item{at: ends.Add(-72 * time.Hour), Notification: notificationsapi.Notification{
		Id: "subscription", Icon: "fa-credit-card", Title: h.d.T(ctx, title, nil), Body: body, Url: "/billing",
	}}), nil
}

func (h *handlers) invitations(ctx context.Context, q *store.Queries, info club.Info, now time.Time, items []item) ([]item, error) {
	rows, err := q.NotificationInvitations(ctx, store.NotificationInvitationsParams{ClubID: info.ID, Now: now, Until: now.Add(24 * time.Hour)})
	if err != nil {
		return items, err
	}
	for _, r := range rows {
		date := r.ExpiresAt.In(info.Loc).Format("02.01 15:04")
		body := h.d.T(ctx, "Роль :role · до :date", map[string]string{"role": r.Role, "date": date})
		if r.Email != nil && *r.Email != "" {
			body = h.d.T(ctx, "Роль :role, :email · до :date", map[string]string{"role": r.Role, "email": *r.Email, "date": date})
		}
		items = append(items, item{at: r.ExpiresAt.Add(-24 * time.Hour), Notification: notificationsapi.Notification{
			Id: "invitation-" + strconv.FormatInt(r.ID, 10), Icon: "fa-envelope-open-text",
			Title: h.d.T(ctx, "Приглашение скоро истечёт", nil), Body: body, Url: "/users",
		}})
	}
	return items, nil
}

// agentUpdate — AgentUpdate::status(): объявлен релиз агента (platform_settings 'agent_release') и в клубе есть ПК
// со старой версией.
func (h *handlers) agentUpdate(ctx context.Context, q *store.Queries, clubID int64, now time.Time, items []item) ([]item, error) {
	raw, err := q.AgentReleaseSetting(ctx, clubID)
	if err != nil {
		return items, nil //nolint:nilerr // релиз не объявлен — уведомления нет
	}
	var rel struct {
		Latest     any `json:"latest_version"`
		ReleasedAt any `json:"released_at"`
	}
	if json.Unmarshal(raw, &rel) != nil {
		return items, nil
	}
	latest, _ := rel.Latest.(string)
	if !clubmap.ValidLatest(latest) {
		return items, nil
	}
	versions, err := q.ClubComputerAgentVersions(ctx, clubID)
	if err != nil {
		return items, err
	}
	var outdated int
	for _, v := range versions {
		if clubmap.IsOutdated(v, latest) {
			outdated++
		}
	}
	if outdated == 0 {
		return items, nil
	}
	released, _ := rel.ReleasedAt.(string)
	return append(items, item{at: releasedAt(released, now), Notification: notificationsapi.Notification{
		Id: "agent-update-" + latest, Icon: "fa-download",
		Title: h.d.T(ctx, "Новая версия агента :version", map[string]string{"version": latest}),
		Body: h.d.T(ctx, "Устарела на :count ПК из :total. Бездисковая система: обновите агент один раз в образе на сервере загрузки.",
			map[string]string{"count": strconv.Itoa(outdated), "total": strconv.Itoa(len(versions))}),
		Url: "/club-computers",
	}}), nil
}

// releasedAt — AgentUpdate::releasedAt(): Carbon::parse(released_at) (пусто — сейчас), ошибка — начало дня.
func releasedAt(s string, now time.Time) time.Time {
	if strings.TrimSpace(s) == "" {
		return now
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02T15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	y, m, d := now.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, now.Location())
}

// listWithMore — ':list и ещё :count'.
func (h *handlers) listWithMore(ctx context.Context, list string, more int) string {
	if more <= 0 {
		return list
	}
	return h.d.T(ctx, ":list и ещё :count", map[string]string{"list": list, "count": strconv.Itoa(more)})
}

// strimwidth — mb_strimwidth($s, 0, 120, '…'): ширина символов (восточноазиатские широкие — 2) с многоточием.
func strimwidth(s string, width int) string {
	if utf8.RuneCountInString(s) <= width {
		return s
	}
	r := []rune(s)
	return string(r[:width-1]) + "…"
}

func ptr[T any](v T) *T { return &v }
