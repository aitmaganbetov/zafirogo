package httpserver

// Тарифы (чтение), сессии ПК со стороны сотрудника (гостевая сессия, завершение, пересадка) и карта клуба.
// Laravel: TariffController::index(), GuestSessionController, ClubComputerSessions::end(), SeatTransfers::transfer(),
// ClubZoneController::state().

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/aitmaganbetov/zafirogo/server/internal/auth/permissions"
	billingsvc "github.com/aitmaganbetov/zafirogo/server/internal/billing/service"
	"github.com/aitmaganbetov/zafirogo/server/internal/cash"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/api"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

const (
	// GuestSessionController::store() — 403, если гостевой режим выключен (settings.guests.guest_mode_enabled).
	msgGuestModeOff = "Гостевой режим отключён в настройках клуба."
	// ClubZoneController::computerStates() — подписи сессии без аккаунта.
	msgGuestNoAccount = "Гость без аккаунта"
	// GuestSessionController::sell(): computer_ids max:50, minutes min:1|max:720.
	maxGuestComputers = 50
	maxGuestMinutes   = 720
)

func (h *handlers) billing() (*billingsvc.Service, error) {
	if h.opts.Billing == nil {
		return nil, errAdminNotConfigured
	}
	return h.opts.Billing, nil
}

// ListTariffs — TariffController::index(): тарифы клуба и цены часа зон.
func (h *handlers) ListTariffs(ctx context.Context, _ api.ListTariffsRequestObject) (api.ListTariffsResponseObject, error) {
	out := api.TariffList{Tariffs: []api.Tariff{}, Zones: []api.Zone{}}
	err := h.tx(ctx, func(ctx context.Context, q *store.Queries) error {
		clubID := h.clubID(ctx)
		rows, err := q.AdminListTariffs(ctx, clubID)
		if err != nil {
			return err
		}
		for _, t := range rows {
			v := api.Tariff{
				Id: t.ID, Name: t.Name, Description: t.Description, DurationMinutes: t.DurationMinutes, PriceTiyn: t.Price,
				Currency: t.Currency, IsActive: t.IsActive, PaymentMethod: api.TariffPaymentMethod(t.PaymentMethod),
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
			out.Zones = append(out.Zones, api.Zone{Id: z.ID, Name: z.Name, PricePerHourTiyn: z.PricePerHour})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return api.ListTariffs200JSONResponse(out), nil
}

// guestModeEnabled — filter_var(settings()['guests']['guest_mode_enabled'] ?? true, FILTER_VALIDATE_BOOLEAN).
func guestModeEnabled(settings []byte) bool {
	var root struct {
		Guests struct {
			Enabled any `json:"guest_mode_enabled"`
		} `json:"guests"`
	}
	if json.Unmarshal(settings, &root) != nil || root.Guests.Enabled == nil {
		return true
	}
	switch v := root.Guests.Enabled.(type) {
	case bool:
		return v
	case float64:
		return v == 1
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "1", "true", "on", "yes":
			return true
		}
	}
	return false
}

// SellGuestSession — GuestSessionController::store() / sell().
func (h *handlers) SellGuestSession(ctx context.Context, req api.SellGuestSessionRequestObject) (api.SellGuestSessionResponseObject, error) {
	svc, err := h.billing()
	if err != nil {
		return nil, err
	}
	b := req.Body
	clubID := h.clubID(ctx)
	var enabled bool
	if err := h.tx(ctx, func(ctx context.Context, q *store.Queries) error {
		raw, err := q.GetClubSettingsForCash(ctx, clubID)
		enabled = guestModeEnabled(raw)
		return err
	}); err != nil {
		return nil, err
	}
	if !enabled {
		return nil, newAPIError(http.StatusForbidden, permissions.CodeForbidden, msgGuestModeOff)
	}
	// Правила формы по порядку полей: computer_ids, tariff_id, minutes, payment_method.
	switch {
	case len(b.ComputerIds) == 0:
		return nil, ruleError("computer_ids", cash.RuleRequired)
	case len(b.ComputerIds) > maxGuestComputers:
		return nil, ruleError("computer_ids", cash.RuleMax)
	}
	seen := map[int64]bool{}
	for i, id := range b.ComputerIds {
		if seen[id] {
			return nil, ruleError("computer_ids."+itoa(int64(i)), cash.RuleDistinct)
		}
		seen[id] = true
	}
	var minutes int64
	if b.TariffId == nil {
		switch {
		case b.Minutes == nil:
			return nil, validation("tariff_id", "The tariff id field is required when minutes is not present.")
		case *b.Minutes < 1:
			return nil, ruleError("minutes", cash.RuleMin)
		case *b.Minutes > maxGuestMinutes:
			return nil, ruleError("minutes", cash.RuleMax)
		}
		minutes = *b.Minutes
	}
	if !slices.Contains(billingsvc.GuestPaymentMethods, b.PaymentMethod) {
		return nil, ruleError("payment_method", cash.RuleIn)
	}
	actor := h.actor(ctx)
	res, err := svc.SellGuestSession(ctx, billingsvc.GuestSessionRequest{
		ClubID: clubID, ComputerIDs: b.ComputerIds, TariffID: b.TariffId, Minutes: minutes, PaymentMethod: b.PaymentMethod,
		CashierID: actor.UserID, IdempotencyKey: req.Params.IdempotencyKey, Actor: actor,
	})
	if err != nil {
		return nil, err
	}
	out := api.GuestSessionResult{
		AmountTiyn: int64(res.Amount), Minutes: res.Minutes, Description: res.Description,
		CashTransactionId: res.CashTransactionID, Computers: make([]api.GuestSessionComputer, 0, len(res.Computers)),
	}
	for _, c := range res.Computers {
		out.Computers = append(out.Computers, api.GuestSessionComputer{
			ComputerId: c.ComputerID, Name: c.Name, SessionId: c.SessionID, SessionEndsAt: c.SessionEndsAt,
			Extends: c.Extends, PriceTiyn: int64(c.Price),
		})
	}
	return api.SellGuestSession201JSONResponse(out), nil
}

// EndComputerSession — ClubComputerSessions::end() сотрудником. Сервис ведёт свою транзакцию, поэтому
// идемпотентность — сохранённым ответом: повтор с тем же ключом возвращает первый ответ.
func (h *handlers) EndComputerSession(ctx context.Context, req api.EndComputerSessionRequestObject) (api.EndComputerSessionResponseObject, error) {
	svc, err := h.billing()
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.Body.SessionId) == "" {
		return nil, ruleError("session_id", cash.RuleRequired)
	}
	clubID, key := h.clubID(ctx), req.Params.IdempotencyKey
	request := map[string]any{"computer_id": req.ComputerId, "session_id": req.Body.SessionId}
	const op = "http:end_session"

	// Повтор: ответ уже сохранён (тот же отпечаток тела — ответ, другой — 409).
	var prev api.EndSessionResult
	replayed := false
	if err := h.tx(ctx, func(ctx context.Context, q *store.Queries) error {
		_, err := q.GetBillingRequest(ctx, store.GetBillingRequestParams{ClubID: clubID, IdempotencyKey: key})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		replayed, err = billingsvc.ClaimRequest(ctx, q, clubID, key, op, request, &prev, h.now())
		return err
	}); err != nil {
		return nil, err
	}
	if replayed {
		return api.EndComputerSession200JSONResponse(prev), nil
	}

	actor := h.actor(ctx)
	res, err := svc.EndSession(ctx, billingsvc.EndSessionRequest{
		ClubID: clubID, ComputerID: req.ComputerId, SessionID: req.Body.SessionId, Actor: actor, IsStaff: true,
	})
	if err != nil {
		return nil, err
	}
	out := api.EndSessionResult{RefundMainTiyn: int64(res.RefundMain), RefundBonusTiyn: int64(res.RefundBonus), KeptMinutes: res.KeptMinutes}
	// Запомнить ответ (параллельный повтор мог успеть раньше — тогда ключ уже занят, это не ошибка).
	if err := h.tx(ctx, func(ctx context.Context, q *store.Queries) error {
		var dummy api.EndSessionResult
		replayed, err := billingsvc.ClaimRequest(ctx, q, clubID, key, op, request, &dummy, h.now())
		if err != nil || replayed {
			return err
		}
		return billingsvc.RememberRequest(ctx, q, clubID, key, out)
	}); err != nil && !errors.Is(err, billingsvc.ErrIdempotencyConflict) {
		return nil, err
	}
	return api.EndComputerSession200JSONResponse(out), nil
}

// GetClubMapState — ClubZoneController::state(): ПК из БД + присутствие агента.
func (h *handlers) GetClubMapState(ctx context.Context, _ api.GetClubMapStateRequestObject) (api.GetClubMapStateResponseObject, error) {
	clubID := h.clubID(ctx)
	out := api.ClubMapState{Computers: []api.ComputerState{}}
	err := h.tx(ctx, func(ctx context.Context, q *store.Queries) error {
		var after int64
		for {
			rows, err := q.AdminListComputerStates(ctx, store.AdminListComputerStatesParams{ClubID: clubID, AfterID: after, PageSize: 500})
			if err != nil {
				return err
			}
			for _, r := range rows {
				out.Computers = append(out.Computers, computerStateView(r))
			}
			if len(rows) < 500 {
				return nil
			}
			after = rows[len(rows)-1].ID
		}
	})
	if err != nil {
		return nil, err
	}
	if h.opts.Presence != nil {
		states, err := h.opts.Presence.List(ctx, clubID)
		if err != nil {
			// Карта без присутствия лучше, чем ошибка: все ПК офлайн.
			h.opts.Logger.WarnContext(ctx, "club map: presence", "err", err)
		}
		byID := make(map[int64]int, len(out.Computers))
		for i, c := range out.Computers {
			byID[c.Id] = i
		}
		for _, st := range states {
			i, ok := byID[st.ComputerID]
			if !ok || !st.Online {
				continue
			}
			c := &out.Computers[i]
			c.Connection = api.Online
			if c.Status == string(api.Offline) {
				c.Status = string(api.Online)
			}
			if st.AgentState != "" {
				c.AgentState = st.AgentState
			}
			if st.AgentVersion != "" {
				c.AgentVersion = st.AgentVersion
			}
			if !st.LastSeenAt.IsZero() {
				ls := st.LastSeenAt
				c.LastSeen = &ls
			}
		}
	}
	return api.GetClubMapState200JSONResponse(out), nil
}

// computerStateView — элемент computerStates старого кода (без классов подсветки — их строит фронт).
func computerStateView(r store.AdminListComputerStatesRow) api.ComputerState {
	name := ""
	switch {
	case r.DisplayName != nil && *r.DisplayName != "":
		name = *r.DisplayName
	case r.DeviceName != nil && *r.DeviceName != "":
		name = *r.DeviceName
	default:
		name = "PC-" + pad2(r.Number)
	}
	status := "offline"
	if r.Status == "restarting" {
		status = "maintenance"
	}
	v := api.ComputerState{
		Id: r.ID, ZoneId: r.ClubZoneID, Number: r.Number, Name: name, Status: status, Connection: api.Offline,
		IsReserved: r.IsReserved, HasSession: r.CurrentSessionID != nil, SessionId: r.CurrentSessionID,
		SessionEndsAt: r.SessionEndsAt, SessionGuest: r.SessionGuest, SessionUserId: r.CurrentSessionUserID,
		LastSeen: r.LastSeenAt, StaffUntil: r.StaffUntil, StaffName: r.StaffName,
	}
	if r.AgentState != nil {
		v.AgentState = *r.AgentState
	}
	if r.AgentVersion != nil {
		v.AgentVersion = *r.AgentVersion
	}
	if v.HasSession {
		user := msgGuestNoAccount
		if !r.SessionGuest {
			parts := []string{}
			if r.SessionUserName != nil && *r.SessionUserName != "" {
				parts = append(parts, *r.SessionUserName)
			}
			if r.SessionUserPhone != nil && *r.SessionUserPhone != "" {
				parts = append(parts, *r.SessionUserPhone)
			}
			user = strings.Join(parts, " · ")
		}
		v.SessionUser = &user
	}
	return v
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }

// pad2 — sprintf('%02d', $n).
func pad2(n int64) string {
	s := itoa(n)
	if n >= 0 && n < 10 {
		return "0" + s
	}
	return s
}
