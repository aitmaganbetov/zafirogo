package guestapi

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/money"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// historyPage — GuestHistory::PAGE.
const historyPage = 30

// Журнал гостя (app/Services/GuestHistory.php): свои события и события над аккаунтом (сотрудник, друг).
var (
	historyOwnEvents      = []string{"tariff.purchased", "time.purchased", "shop.purchased", "time.refunded", "balance.transferred", "session.transferred", "auth.api_login", "auth.qr_login"}
	historyReceivedEvents = []string{"user.balance_topped_up", "user.balance_refunded", "user.balance_written_off", "balance.transferred", "shop.order_cancelled", "time.refunded"} //nolint:misspell // код события старого журнала
	// GuestHistory::FILTERS: [свои, полученные].
	historyFilters = map[agentv1.HistoryFilter][2][]string{
		agentv1.HistoryFilter_HISTORY_FILTER_PURCHASES: {{"tariff.purchased", "time.purchased", "shop.purchased", "time.refunded"}, {"shop.order_cancelled", "time.refunded"}}, //nolint:misspell // код события старого журнала
		agentv1.HistoryFilter_HISTORY_FILTER_TOP_UPS:   {{}, {"user.balance_topped_up", "user.balance_refunded", "user.balance_written_off"}},
		agentv1.HistoryFilter_HISTORY_FILTER_TRANSFERS: {{"balance.transferred"}, {"balance.transferred"}},
		agentv1.HistoryFilter_HISTORY_FILTER_VISITS:    {{"session.transferred", "auth.api_login", "auth.qr_login"}, {}},
	}
)

type historyRow struct {
	ID        int64
	UserID    int64
	Event     string
	SubjectID *int64
	Details   map[string]any
	CreatedAt *timestamppb.Timestamp
}

func tsPtr(t *time.Time) *timestamppb.Timestamp {
	if t == nil {
		return nil
	}
	return timestamppb.New(*t)
}

// ListHistory — журнал гостя, новые сверху, по 30 (GET /api/guest/history, GuestHistory::page()).
func (gs *guestService) ListHistory(ctx context.Context, req *agentv1.ListHistoryRequest) (*agentv1.ListHistoryResponse, error) {
	s := gs.s
	ctx, id, g, err := s.guestCall(ctx, "api.guest.history")
	if err != nil {
		return nil, err
	}
	before := int64(1<<63 - 1)
	if t := req.GetPageToken(); t != "" {
		if before, err = strconv.ParseInt(t, 10, 64); err != nil || before <= 0 {
			return nil, validation("page_token", "Неверный курсор страницы.")
		}
	}
	own, received := historyOwnEvents, historyReceivedEvents
	if f, ok := historyFilters[req.GetFilter()]; ok {
		own, received = f[0], f[1]
	} else if req.GetFilter() != agentv1.HistoryFilter_HISTORY_FILTER_UNSPECIFIED {
		return nil, validation("filter", "Неизвестный фильтр.")
	}
	out := &agentv1.ListHistoryResponse{}
	err = db.InTenantTx(ctx, s.o.Pool, id.ClubID, func(q *store.Queries) error {
		var logs []historyRow
		seen := map[int64]bool{}
		add := func(r historyRow) {
			if !seen[r.ID] {
				seen[r.ID] = true
				logs = append(logs, r)
			}
		}
		if len(own) > 0 {
			rows, err := q.GuestHistoryOwn(ctx, store.GuestHistoryOwnParams{ClubID: id.ClubID, UserID: g.UserID, Events: own,
				BeforeID: before, PageSize: historyPage + 1})
			if err != nil {
				return err
			}
			for _, r := range rows {
				add(historyRow{ID: r.ID, UserID: r.UserID, Event: r.Event, SubjectID: r.SubjectID, Details: details(r.Details), CreatedAt: timestamppb.New(r.CreatedAt)})
			}
		}
		if len(received) > 0 {
			st, uid := subjectUser, g.UserID
			rows, err := q.GuestHistoryReceived(ctx, store.GuestHistoryReceivedParams{ClubID: id.ClubID, SubjectType: &st, UserID: &uid,
				Events: received, BeforeID: before, PageSize: historyPage + 1})
			if err != nil {
				return err
			}
			for _, r := range rows {
				add(historyRow{ID: r.ID, UserID: r.UserID, Event: r.Event, SubjectID: r.SubjectID, Details: details(r.Details), CreatedAt: timestamppb.New(r.CreatedAt)})
			}
		}
		slices.SortFunc(logs, func(a, b historyRow) int { return int(b.ID - a.ID) })
		page := logs[:min(len(logs), historyPage)]
		if len(logs) > historyPage {
			out.NextPageToken = strconv.FormatInt(page[len(page)-1].ID, 10)
		}
		tariffs, users, err := historyNames(ctx, q, id.ClubID, page)
		if err != nil {
			return err
		}
		for _, l := range page {
			out.Entries = append(out.Entries, historyEntry(l, g.UserID, tariffs, users))
		}
		return nil
	})
	if err != nil {
		return nil, s.fail(ctx, "ListHistory", err)
	}
	return out, nil
}

func details(raw []byte) map[string]any {
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return m
}

// historyNames — GuestHistory::names(): тарифы покупок (withTrashed) и друзья переводов.
func historyNames(ctx context.Context, q *store.Queries, clubID int64, page []historyRow) (map[int64]string, map[int64]string, error) {
	var tariffIDs, userIDs []int64
	for _, l := range page {
		switch l.Event {
		case "tariff.purchased":
			if l.SubjectID != nil {
				tariffIDs = append(tariffIDs, *l.SubjectID)
			}
		case "balance.transferred":
			userIDs = append(userIDs, l.UserID)
			if to := int64Of(l.Details["to_user_id"]); to > 0 {
				userIDs = append(userIDs, to)
			}
		}
	}
	tariffs, users := map[int64]string{}, map[int64]string{}
	if len(tariffIDs) > 0 {
		rows, err := q.GuestTariffNames(ctx, store.GuestTariffNamesParams{ClubID: clubID, Ids: tariffIDs})
		if err != nil {
			return nil, nil, err
		}
		for _, r := range rows {
			tariffs[r.ID] = r.Name
		}
	}
	if len(userIDs) > 0 {
		rows, err := q.GuestUserNames(ctx, store.GuestUserNamesParams{ClubID: clubID, Ids: userIDs})
		if err != nil {
			return nil, nil, err
		}
		for _, r := range rows {
			users[r.ID] = r.Name
		}
	}
	return tariffs, users, nil
}

func int64Of(v any) int64 {
	switch x := v.(type) {
	case float64:
		return int64(x)
	case string:
		n, _ := strconv.ParseInt(x, 10, 64)
		return n
	}
	return 0
}

// amountOf — сумма из details (тенге строкой или числом): is_numeric && > 0, иначе 0 (GuestHistory::negative/positive).
func amountOf(v any) money.Tiyn {
	var s string
	switch x := v.(type) {
	case float64:
		s = strconv.FormatFloat(x, 'f', -1, 64)
	case string:
		s = strings.TrimSpace(x)
	default:
		return 0
	}
	if !money.PHPIsNumeric(s) {
		return 0
	}
	t, err := money.ParseTengeRounded(s)
	if err != nil || t <= 0 {
		return 0
	}
	return t
}

func firstAmount(d map[string]any, keys ...string) money.Tiyn {
	for _, k := range keys {
		if v, ok := d[k]; ok && v != nil {
			return amountOf(v)
		}
	}
	return 0
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

// historyEntry — GuestHistory::entry(): заголовок и изменения балансов по событию.
func historyEntry(l historyRow, userID int64, tariffs, users map[int64]string) *agentv1.HistoryEntry {
	d := l.Details
	e := &agentv1.HistoryEntry{EntryId: l.ID, Event: l.Event, OccurredAt: l.CreatedAt}
	switch l.Event {
	case "tariff.purchased":
		name := "—"
		if l.SubjectID != nil && tariffs[*l.SubjectID] != "" {
			name = tariffs[*l.SubjectID]
		}
		e.Title = "Покупка тарифа «" + name + "»"
		e.MainDeltaTiyn = -int64(firstAmount(d, "main_balance_amount", "amount"))
		e.BonusDeltaTiyn = -int64(firstAmount(d, "bonus_balance_amount"))
	case "time.purchased":
		e.Title = "Поминутная оплата: " + strconv.FormatInt(int64Of(d["duration_minutes"]), 10) + " мин."
		e.MainDeltaTiyn = -int64(firstAmount(d, "main_balance_amount", "amount"))
		e.BonusDeltaTiyn = -int64(firstAmount(d, "bonus_balance_amount"))
	case "shop.purchased":
		e.Title = "Заказ в магазине клуба"
		e.MainDeltaTiyn = -int64(amountOf(d["amount"]))
	case "shop.order_cancelled": //nolint:misspell // код события старого журнала
		e.Title = "Возврат за отменённый заказ"
		e.MainDeltaTiyn = int64(amountOf(d["amount"]))
	case "time.refunded":
		e.Title = "Возврат за неиспользованное время"
		e.MainDeltaTiyn = int64(firstAmount(d, "main_balance_amount"))
		e.BonusDeltaTiyn = int64(firstAmount(d, "bonus_balance_amount"))
	case "user.balance_topped_up":
		e.Title = "Пополнение на кассе"
		bt := strings.ToLower(str(d["balance_type"]))
		if strings.Contains(bt, "бонус") || strings.Contains(bt, "bonus") {
			e.BonusDeltaTiyn = int64(amountOf(d["amount"]))
		} else {
			e.MainDeltaTiyn = int64(amountOf(d["amount"]))
			e.BonusDeltaTiyn = int64(amountOf(d["bonus_amount"]))
		}
	case "user.balance_refunded":
		e.Title = "Возврат пополнения на кассе"
		if str(d["balance_field"]) == "bonus_balance" {
			e.BonusDeltaTiyn = -int64(amountOf(d["amount"]))
		} else {
			e.MainDeltaTiyn = -int64(amountOf(d["amount"]))
			e.BonusDeltaTiyn = -int64(amountOf(d["bonus_taken_back"]))
		}
	case "user.balance_written_off":
		e.Title = "Списание с баланса администрацией клуба"
		if str(d["balance_field"]) == "bonus_balance" {
			e.BonusDeltaTiyn = -int64(amountOf(d["amount"]))
		} else {
			e.MainDeltaTiyn = -int64(amountOf(d["amount"]))
		}
	case "balance.transferred":
		if l.UserID == userID {
			e.Title = "Перевод другу " + nameOr(users, int64Of(d["to_user_id"]))
			e.MainDeltaTiyn = -int64(amountOf(d["amount"]))
		} else {
			e.Title = "Перевод от друга " + nameOr(users, l.UserID)
			e.MainDeltaTiyn = int64(amountOf(d["amount"]))
		}
	case "session.transferred":
		to := str(d["to"])
		if to == "" {
			to = "—"
		}
		e.Title = "Пересадка на ПК " + to
	case "auth.qr_login":
		e.Title = "Вход на ПК по QR-коду"
	default:
		e.Title = "Вход в аккаунт"
	}
	return e
}

func nameOr(names map[int64]string, id int64) string {
	if n := names[id]; n != "" {
		return n
	}
	return "—"
}

// newsLimit — GuestNewsController::LIMIT.
const newsLimit = 10

// ListNews — новости Zafiro и клуба, до 10 каждого вида (GET /api/guest/news, GuestNewsController).
func (gs *guestService) ListNews(ctx context.Context, _ *agentv1.ListNewsRequest) (*agentv1.ListNewsResponse, error) {
	s := gs.s
	ctx, id, _, err := s.guestCall(ctx, "api.guest.news")
	if err != nil {
		return nil, err
	}
	out := &agentv1.ListNewsResponse{}
	item := func(nid int64, title, body string, image *string, at *timestamppb.Timestamp) *agentv1.NewsItem {
		n := &agentv1.NewsItem{NewsId: nid, Title: title, Body: body}
		if image != nil && *image != "" { // News::imageUrl(): route('news-images.show', [$news, basename($path)])
			n.ImageUrl = s.o.PublicBaseURL + "/news-images/" + strconv.FormatInt(nid, 10) + "/" + basename(*image)
		}
		n.PublishedAt = at
		return n
	}
	err = db.InTenantTx(ctx, s.o.Pool, id.ClubID, func(q *store.Queries) error {
		platform, err := q.GuestListPlatformNews(ctx, newsLimit)
		if err != nil {
			return err
		}
		club := id.ClubID
		own, err := q.GuestListClubNews(ctx, store.GuestListClubNewsParams{ClubID: &club, PageSize: newsLimit})
		if err != nil {
			return err
		}
		out.Platform, out.Club = []*agentv1.NewsItem{}, []*agentv1.NewsItem{}
		for _, n := range platform {
			out.Platform = append(out.Platform, item(n.ID, n.Title, n.Body, n.ImagePath, tsPtr(n.CreatedAt)))
		}
		for _, n := range own {
			out.Club = append(out.Club, item(n.ID, n.Title, n.Body, n.ImagePath, tsPtr(n.CreatedAt)))
		}
		return nil
	})
	if err != nil {
		return nil, s.fail(ctx, "ListNews", err)
	}
	return out, nil
}
