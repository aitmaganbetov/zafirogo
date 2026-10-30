package clubmap

// Управление ПК с карты: питание, техрежим, флаг брони, завершение сессий, уведомление.
// Laravel: ClubComputerController::control(), endSessions(), notify(); ComputerPower.

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"

	billingsvc "github.com/aitmaganbetov/zafirogo/server/internal/billing/service"
	"github.com/aitmaganbetov/zafirogo/server/internal/clubmap"
	clubmapapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/clubmap"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

func (h *handlers) presence() clubmap.Presence {
	if h.d.Presence == nil {
		return nil
	}
	return h.d.Presence
}

func (h *handlers) sender() clubmap.CommandSender {
	if h.d.AgentCommands == nil {
		return nil
	}
	return h.d.AgentCommands
}

// ControlComputers — ClubComputerController::control().
func (h *handlers) ControlComputers(ctx context.Context, req clubmapapi.ControlComputersRequestObject) (clubmapapi.ControlComputersResponseObject, error) {
	if req.Body == nil {
		return nil, httpx.RuleError("computer_ids", "required")
	}
	b := req.Body
	if err := clubmap.ValidateIDs(b.ComputerIds); err != nil {
		return nil, err
	}
	if !slices.Contains(clubmap.Actions, b.Action) {
		return nil, httpx.RuleError("action", "in")
	}
	if b.Action != clubmap.ActionReserve && h.d.AgentCommands == nil {
		return nil, httpx.ErrNotConfigured
	}
	env := h.env(ctx)
	var (
		plan   clubmap.ControlPlan
		latest string
	)
	if err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		rows, err := clubmap.Selected(ctx, q, env.ClubID, b.ComputerIds, true)
		if err != nil {
			return err
		}
		latest = latestAgentVersion(ctx, q, env.ClubID)
		plan, err = clubmap.ControlTx(ctx, q, env, rows, b.Action)
		return err
	}); err != nil {
		return nil, err
	}
	if b.Action == clubmap.ActionReserve {
		reserved := plan.Reserved
		return clubmapapi.ControlComputers200JSONResponse{Updated: plan.Updated, Reserved: &reserved}, nil
	}
	counts := clubmap.SendControl(ctx, h.sender(), h.presence(), env.ClubID, env.UserID, plan, h.d.Log())
	msg := clubmap.ControlMessage(b.Action, counts, latest, env.T)
	out := clubmapapi.ControlComputers200JSONResponse{Updated: int64(len(plan.IDs)), Message: &msg}
	if plan.Status != "" {
		s := plan.Status
		out.Status = &s
	}
	return out, nil
}

// EndComputerSessions — ClubComputerController::endSessions(): сессии выбранных ПК сотрудником (возврат
// неиспользованного времени — billing.EndSession, у каждого ПК своя транзакция).
func (h *handlers) EndComputerSessions(ctx context.Context, req clubmapapi.EndComputerSessionsRequestObject) (clubmapapi.EndComputerSessionsResponseObject, error) {
	if req.Body == nil {
		return nil, httpx.RuleError("computer_ids", "required")
	}
	if err := clubmap.ValidateIDs(req.Body.ComputerIds); err != nil {
		return nil, err
	}
	if h.d.Billing == nil {
		return nil, httpx.ErrNotConfigured
	}
	clubID := httpx.ClubID(ctx)
	var rows []store.ClubMapComputersWithSessionRow
	if err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		var err error
		rows, err = q.ClubMapComputersWithSession(ctx, store.ClubMapComputersWithSessionParams{ClubID: clubID, Ids: req.Body.ComputerIds})
		return err
	}); err != nil {
		return nil, err
	}
	actor := httpx.Actor(ctx)
	ended := 0
	for _, r := range rows {
		if r.CurrentSessionID == nil {
			continue
		}
		_, err := h.d.Billing.EndSession(ctx, billingsvc.EndSessionRequest{
			ClubID: clubID, ComputerID: r.ID, SessionID: *r.CurrentSessionID, Actor: actor, IsStaff: true,
		})
		if errors.Is(err, billingsvc.ErrNoSuchSession) {
			continue // сессию уже закончили (агент, воркер истечения, другая вкладка)
		}
		if err != nil {
			return nil, err
		}
		ended++
	}
	msg := h.d.T(ctx, clubmap.MsgNoSessions, nil)
	if ended > 0 {
		msg = h.d.T(ctx, clubmap.MsgSessionsEnded, map[string]string{"count": strconv.Itoa(ended)})
	}
	return clubmapapi.EndComputerSessions200JSONResponse{Ended: int64(ended), Message: msg}, nil
}

// NotifyComputers — ClubComputerController::notify().
func (h *handlers) NotifyComputers(ctx context.Context, req clubmapapi.NotifyComputersRequestObject) (clubmapapi.NotifyComputersResponseObject, error) {
	if req.Body == nil {
		return nil, httpx.RuleError("computer_ids", "required")
	}
	if err := clubmap.ValidateIDs(req.Body.ComputerIds); err != nil {
		return nil, err
	}
	text, err := clubmap.ValidateMessage(req.Body.Message)
	if err != nil {
		return nil, err
	}
	if h.d.AgentCommands == nil {
		return nil, httpx.ErrNotConfigured
	}
	env := h.env(ctx)
	var (
		rows   []store.ClubMapComputersByIDsRow
		latest string
	)
	if err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		var err error
		if rows, err = clubmap.Selected(ctx, q, env.ClubID, req.Body.ComputerIds, false); err != nil {
			return err
		}
		latest = latestAgentVersion(ctx, q, env.ClubID)
		return clubmap.NotifyTx(ctx, q, env, rows, text)
	}); err != nil {
		return nil, err
	}
	counts := clubmap.SendNotify(ctx, h.sender(), h.presence(), env.ClubID, env.UserID, rows, text, h.d.Log())
	return clubmapapi.NotifyComputers200JSONResponse{
		Sent: int64(counts.Queued - counts.Outdated), Message: clubmap.NotifyMessage(counts, latest, env.T),
	}, nil
}

// mapError — ошибки домена карты → ответы (тексты — ключи перевода с подстановками, F1-5).
func mapError(err error) *httpx.APIError {
	var e *clubmap.Error
	switch {
	case errors.As(err, &e):
		if e.Rule != "" {
			if e.RuleParam != "" {
				return httpx.RuleErrorWith(e.Field, e.Rule, e.RuleParam)
			}
			return httpx.RuleError(e.Field, e.Rule)
		}
		if e.Status == http.StatusConflict {
			return httpx.ErrorMsg(http.StatusConflict, "conflict", e.Message, e.Params)
		}
		return httpx.ValidationMsg(e.Field, e.Message, e.Params)
	case errors.Is(err, clubmap.ErrNotFound):
		return httpx.NotFound()
	}
	return nil
}
