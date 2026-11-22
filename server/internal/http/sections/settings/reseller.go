package settings

// Карточка «Реселлер» на «Настройке клуба» (тема Д, docs/upstream-2026-11.md): клуб один раз вводит код реселлера.
// Laravel: SettingsController::reseller() (settings.reseller, throttle:10,1), settings/club.blade.php (#reseller).
// Правила и запись — internal/reseller.

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	settingsapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/settings"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
	"github.com/aitmaganbetov/zafirogo/server/internal/reseller"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

const (
	msgResellerAttached = "Клуб привязан к реселлеру :name."
	logResellerAttached = "Клуб привязан к реселлеру :code"
)

// GetClubReseller — реселлер клуба для карточки (пул клуба: RLS показывает только реселлера своего клуба).
//
//	@if($currentClub->reseller) … @if($currentClub->reseller->is_active && $currentClub->reseller->discount_percent > 0)
//
// Laravel: resources/views/settings/club.blade.php.
func (h *handlers) GetClubReseller(ctx context.Context, _ settingsapi.GetClubResellerRequestObject) (settingsapi.GetClubResellerResponseObject, error) {
	p := httpx.Principal(ctx)
	var out settingsapi.ClubResellerState
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		r, err := q.ResellerOfClub(ctx, p.ClubID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		out.Reseller = &settingsapi.ClubReseller{Name: r.Name, Code: r.Code}
		// Скидка остановленного реселлера не действует — текст о ней не показывается.
		if r.IsActive {
			out.Reseller.DiscountPercent = int(r.DiscountPercent)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return settingsapi.GetClubReseller200JSONResponse(out), nil
}

// AttachClubReseller — SettingsController::reseller(): клуб вводит код своего реселлера один раз. Привязку делает
// пул платформы (clubs.reseller_id пул клуба не меняет — триггер clubs_reseller_guard, 0034); запись журнала клуба
// и audit_log — в той же транзакции, чтобы привязка без следа в журнале была невозможна.
func (h *handlers) AttachClubReseller(ctx context.Context, req settingsapi.AttachClubResellerRequestObject) (settingsapi.AttachClubResellerResponseObject, error) {
	if h.d.PlatformPool == nil {
		return nil, httpx.ErrNotConfigured
	}
	code := ""
	if req.Body != nil && req.Body.ResellerCode != nil {
		code = *req.Body.ResellerCode
	}
	p := httpx.Principal(ctx)
	now := h.d.NowFunc()
	var attached reseller.Attached
	err := db.InPlatformTx(ctx, h.d.PlatformPool, func(q *store.Queries) error {
		var err error
		attached, err = reseller.Attach(ctx, q, p.ClubID, code, now)
		switch {
		case errors.Is(err, reseller.ErrAlreadyAttached):
			return httpx.ValidationMsg("reseller_code", reseller.MsgAlreadyAttached, nil)
		case errors.Is(err, reseller.ErrCodeNotFound):
			return httpx.ValidationMsg("reseller_code", reseller.MsgCodeNotFound, nil)
		case errors.Is(err, reseller.ErrNotFound):
			return httpx.NotFound()
		case err != nil:
			return err
		}
		if err := logClub(ctx, q, now, "club.reseller_attached", nil, logResellerAttached, map[string]string{"code": attached.Code}); err != nil {
			return err
		}
		// Привязка меняет цену подписки клуба (скидка реселлера) — аудит (CLAUDE.md «Безопасность»).
		return httpx.Audit(ctx, q, p.ClubID, "settings", "reseller.attached", subjectClub, p.ClubID, map[string]any{"reseller_id": nil},
			map[string]any{"reseller_id": attached.ID, "code": attached.Code})
	})
	if err != nil {
		return nil, err
	}
	return settingsapi.AttachClubReseller200JSONResponse{Message: h.d.T(ctx, msgResellerAttached, map[string]string{"name": attached.Name})}, nil
}
