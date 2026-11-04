package sessions

// Сессии ПК со стороны сотрудника: гостевая сессия, завершение. Laravel: GuestSessionController,
// ClubComputerSessions::end().

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/aitmaganbetov/zafirogo/server/internal/auth/permissions"
	billingsvc "github.com/aitmaganbetov/zafirogo/server/internal/billing/service"
	"github.com/aitmaganbetov/zafirogo/server/internal/cash"
	sessionsapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/sessions"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

const (
	// GuestSessionController::store() — 403, если гостевой режим выключен (settings.guests.guest_mode_enabled).
	msgGuestModeOff = "Гостевой режим отключён в настройках клуба."
	// GuestSessionController::sell(): computer_ids max:50, minutes min:1|max:720.
	maxGuestComputers = 50
	maxGuestMinutes   = 720
)

func (h *handlers) billing() (*billingsvc.Service, error) {
	if h.d.Billing == nil {
		return nil, httpx.ErrNotConfigured
	}
	return h.d.Billing, nil
}

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
func (h *handlers) SellGuestSession(ctx context.Context, req sessionsapi.SellGuestSessionRequestObject) (sessionsapi.SellGuestSessionResponseObject, error) {
	svc, err := h.billing()
	if err != nil {
		return nil, err
	}
	b := req.Body
	clubID := httpx.ClubID(ctx)
	var enabled bool
	if err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		raw, err := q.GetClubSettingsForCash(ctx, clubID)
		enabled = guestModeEnabled(raw)
		return err
	}); err != nil {
		return nil, err
	}
	if !enabled {
		return nil, httpx.NewAPIError(http.StatusForbidden, permissions.CodeForbidden, msgGuestModeOff)
	}
	// Правила формы по порядку полей: computer_ids, tariff_id, minutes, payment_method.
	switch {
	case len(b.ComputerIds) == 0:
		return nil, httpx.RuleError("computer_ids", cash.RuleRequired)
	case len(b.ComputerIds) > maxGuestComputers:
		return nil, httpx.RuleError("computer_ids", cash.RuleMax)
	}
	seen := map[int64]bool{}
	for i, id := range b.ComputerIds {
		if seen[id] {
			return nil, httpx.RuleError("computer_ids."+httpx.Itoa(int64(i)), cash.RuleDistinct)
		}
		seen[id] = true
	}
	var minutes int64
	if b.TariffId == nil {
		switch {
		case b.Minutes == nil:
			return nil, httpx.Validation("tariff_id", "The tariff id field is required when minutes is not present.")
		case *b.Minutes < 1:
			return nil, httpx.RuleError("minutes", cash.RuleMin)
		case *b.Minutes > maxGuestMinutes:
			return nil, httpx.RuleError("minutes", cash.RuleMax)
		}
		minutes = *b.Minutes
	}
	if !slices.Contains(billingsvc.GuestPaymentMethods, b.PaymentMethod) {
		return nil, httpx.RuleError("payment_method", cash.RuleIn)
	}
	actor := httpx.Actor(ctx)
	res, err := svc.SellGuestSession(ctx, billingsvc.GuestSessionRequest{
		ClubID: clubID, ComputerIDs: b.ComputerIds, TariffID: b.TariffId, Minutes: minutes, PaymentMethod: b.PaymentMethod,
		CashierID: actor.UserID, IdempotencyKey: req.Params.IdempotencyKey, Actor: actor,
	})
	if err != nil {
		return nil, err
	}
	out := sessionsapi.GuestSessionResult{
		AmountTiyn: int64(res.Amount), Minutes: res.Minutes, Description: res.Description,
		CashTransactionId: res.CashTransactionID, Computers: make([]sessionsapi.GuestSessionComputer, 0, len(res.Computers)),
	}
	for _, c := range res.Computers {
		out.Computers = append(out.Computers, sessionsapi.GuestSessionComputer{
			ComputerId: c.ComputerID, Name: c.Name, SessionId: c.SessionID, SessionEndsAt: c.SessionEndsAt,
			Extends: c.Extends, PriceTiyn: int64(c.Price),
		})
	}
	return sessionsapi.SellGuestSession201JSONResponse(out), nil
}

// EndComputerSession — ClubComputerSessions::end() сотрудником. Сервис ведёт свою транзакцию, поэтому
// идемпотентность — сохранённым ответом: повтор с тем же ключом возвращает первый ответ.
func (h *handlers) EndComputerSession(ctx context.Context, req sessionsapi.EndComputerSessionRequestObject) (sessionsapi.EndComputerSessionResponseObject, error) {
	svc, err := h.billing()
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.Body.SessionId) == "" {
		return nil, httpx.RuleError("session_id", cash.RuleRequired)
	}
	clubID, key := httpx.ClubID(ctx), req.Params.IdempotencyKey
	request := map[string]any{"computer_id": req.ComputerId, "session_id": req.Body.SessionId}
	const op = "http:end_session"

	// Повтор: ответ уже сохранён (тот же отпечаток тела — ответ, другой — 409).
	var prev sessionsapi.EndSessionResult
	replayed := false
	if err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		_, err := q.GetBillingRequest(ctx, store.GetBillingRequestParams{ClubID: clubID, IdempotencyKey: key})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		replayed, err = billingsvc.ClaimRequest(ctx, q, clubID, key, op, request, &prev, h.d.NowFunc())
		return err
	}); err != nil {
		return nil, err
	}
	if replayed {
		return sessionsapi.EndComputerSession200JSONResponse(prev), nil
	}

	actor := httpx.Actor(ctx)
	res, err := svc.EndSession(ctx, billingsvc.EndSessionRequest{
		ClubID: clubID, ComputerID: req.ComputerId, SessionID: req.Body.SessionId, Actor: actor, IsStaff: true,
	})
	if err != nil {
		return nil, err
	}
	out := sessionsapi.EndSessionResult{RefundMainTiyn: int64(res.RefundMain), RefundBonusTiyn: int64(res.RefundBonus), KeptMinutes: res.KeptMinutes}
	// Запомнить ответ (параллельный повтор мог успеть раньше — тогда ключ уже занят, это не ошибка).
	if err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		var dummy sessionsapi.EndSessionResult
		replayed, err := billingsvc.ClaimRequest(ctx, q, clubID, key, op, request, &dummy, h.d.NowFunc())
		if err != nil || replayed {
			return err
		}
		return billingsvc.RememberRequest(ctx, q, clubID, key, out)
	}); err != nil && !errors.Is(err, billingsvc.ErrIdempotencyConflict) {
		return nil, err
	}
	return sessionsapi.EndComputerSession200JSONResponse(out), nil
}
