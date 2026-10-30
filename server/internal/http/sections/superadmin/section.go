// Package superadmin — кабинет суперадмина (api/superadmin.yaml, Этап 3, группа 6): сервер и релиз агента,
// клубы и подписки, пользователи платформы, тарифы платформы, журнал, новости платформы, языки и переводы,
// Telegram. Laravel: app/Http/Controllers/Superadmin/*, routes/web.php (~стр. 52–106).
//
// Доступ — RequireSuperadmin (флаг суперадмина; заблокированный Principal не получает вовсе). Хост
// SUPERADMIN_DOMAIN проверяет общий middleware роутера (Q-069). Данные — пул zafiro_platform (по всем клубам).
// Каждое изменение пишется в platform_logs (PD-15) и audit_log (category по смыслу действия).
package superadmin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/netip"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/aitmaganbetov/zafirogo/server/internal/activity"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/permissions"
	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	superadminapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/superadmin"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// access — все операции «публичные» для проверки прав клуба: доступ решает RequireSuperadmin на маршрутах.
var access = httpx.Access{Public: []string{
	"SaDashboard", "SaSetAgentRelease", "SaClearAgentRelease",
	"SaListClubs", "SaGetClub", "SaExtendSubscription", "SaSuspendSubscription", "SaChangeClubPlan",
	"SaListAccounts", "SaBlockAccount", "SaUnblockAccount", "SaGrantSuperadmin", "SaRevokeSuperadmin",
	"SaListPlans", "SaCreatePlan", "SaUpdatePlan", "SaListLogs",
	"SaListPlatformNews", "SaCreatePlatformNews", "SaUpdatePlatformNews", "SaDeletePlatformNews",
	"SaListLanguages", "SaCreateLanguage", "SaUpdateLanguage", "SaDeleteLanguage", "SaExportTranslations",
	"SaImportTranslations", "SaListTranslations", "SaSaveTranslations",
	"SaTelegram", "SaSetTelegramGateway", "SaClearTelegramGateway", "SaSetTelegramAlerts", "SaClearTelegramAlerts",
	"SaTestTelegramAlerts",
}}

// limits — throttle: маршрутов кабинета (routes/web.php).
var limits = map[string]string{
	"POST /api/superadmin/telegram/alerts/test": "superadmin.telegram.alerts.test", // throttle:5,1
	"POST /api/superadmin/languages/import":     "languages.import",                // throttle:20,1
}

// Online — ПК на связи по клубам (*presence.Store); нет — все ПК «не в сети».
type Online interface {
	OnlineByClub(ctx context.Context, now time.Time) (map[int64]int64, error)
}

type handlers struct {
	d *httpx.Deps
}

var _ superadminapi.StrictServerInterface = (*handlers)(nil)

// Section — кабинет суперадмина. Регистрация — server/internal/http/sections.go.
func Section() httpx.Section {
	return httpx.Section{Name: "superadmin", Mount: mount, Limits: limits}
}

func mount(r chi.Router, d *httpx.Deps) {
	h := &handlers{d: d}
	si := superadminapi.NewStrictHandlerWithOptions(h,
		httpx.Middlewares[superadminapi.StrictMiddlewareFunc](access),
		superadminapi.StrictHTTPServerOptions{RequestErrorHandlerFunc: d.RequestError, ResponseErrorHandlerFunc: d.ResponseError()})
	superadminapi.HandlerWithOptions(si, superadminapi.ChiServerOptions{
		BaseRouter: r, ErrorHandlerFunc: d.ParamError,
		Middlewares: []superadminapi.MiddlewareFunc{permissions.RequireSuperadmin},
	})
}

// tx — транзакция пула zafiro_platform (кабинет работает по всем клубам).
func (h *handlers) tx(ctx context.Context, fn func(q *store.Queries) error) error {
	if h.d.PlatformPool == nil {
		return httpx.ErrNotConfigured
	}
	return db.InPlatformTx(ctx, h.d.PlatformPool, fn)
}

func (h *handlers) online(ctx context.Context) map[int64]int64 {
	if o, ok := h.d.Presence.(Online); ok && o != nil {
		m, err := o.OnlineByClub(ctx, time.Now()) // присутствие — по часам Valkey, не по dev.fixed_now
		if err == nil {
			return m
		}
		h.d.Log().WarnContext(ctx, "superadmin: presence", "err", err)
	}
	return map[int64]int64{}
}

// log — запись журнала платформы от суперадмина запроса.
func (h *handlers) log(ctx context.Context, q *store.Queries, e activity.PlatformEntry) error {
	e.UserID = httpx.Principal(ctx).UserID
	if e.At.IsZero() {
		e.At = h.d.NowFunc()
	}
	_, err := activity.RecordPlatform(ctx, q, e)
	return err
}

// audit — запись audit_log (club_id NULL — событие платформы).
func (h *handlers) audit(ctx context.Context, q *store.Queries, clubID int64, category, action, subjectType string, subjectID int64, before, after any) error {
	p := httpx.Principal(ctx)
	uid := p.UserID
	params := store.InsertAuditLogParams{ActorUserID: &uid, ActorKind: "superadmin", Category: category, Action: action}
	if clubID > 0 {
		params.ClubID = &clubID
	}
	if subjectType != "" {
		params.SubjectType, params.SubjectID = &subjectType, &subjectID
	}
	params.Before, params.After = auditJSON(before), auditJSON(after)
	if _, r := httpx.HTTPFrom(ctx); r != nil {
		if ip, err := netip.ParseAddr(httpx.HostOnly(r.RemoteAddr)); err == nil {
			addr := ip.Unmap()
			params.Ip = &addr
		}
	}
	if id := middleware.GetReqID(ctx); id != "" {
		params.RequestID = &id
	}
	return q.InsertAuditLog(ctx, params)
}

func auditJSON(v any) json.RawMessage {
	switch x := v.(type) {
	case nil:
		return nil
	case json.RawMessage:
		if len(x) == 0 {
			return nil
		}
		return x
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return b
}

func msg(text string) superadminapi.Message { return superadminapi.Message{Message: text} }

// page — номер страницы из запроса (с 1).
func page(p *int) int {
	if p != nil && *p > 1 {
		return *p
	}
	return 1
}

var _ = http.StatusOK
