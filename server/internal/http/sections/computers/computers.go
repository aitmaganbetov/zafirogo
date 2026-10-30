// Package computers — REST-раздел: список ПК и подключение ПК к клубу (api/computers.yaml).
// Laravel: ClubComputerListController, AgentUpdate::status(); коды, ключи образа и ожидающие ПК — сервис
// server/internal/agent/enroll (решение Q-037).
package computers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/netip"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/aitmaganbetov/zafirogo/server/internal/agent/enroll"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/permissions"
	"github.com/aitmaganbetov/zafirogo/server/internal/auth/principal"
	"github.com/aitmaganbetov/zafirogo/server/internal/clubmap"
	computersapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/computers"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// access — список ПК — право club_map.view (club-computers.index). Подключение ПК — новые маршруты без
// записи в RequireClubArea: роли проверяет сервис enroll (CanRegister / CanManageImageKeys).
var access = httpx.Access{
	Routes: map[string]string{"ListClubComputers": "club-computers.index"},
	Public: []string{"GetAgentEnrollment", "CreateEnrollmentCode", "CancelEnrollmentCode", "CreateImageKey",
		"RevokeImageKey", "ApprovePendingComputer", "RejectPendingComputer", "ReregisterComputer", "RevokeComputer"},
}

// perPage — paginate(50).
const perPage = 50

// Тексты ответов (новые экраны — новые ключи перевода).
const (
	msgCodeCancelled = "Код отменён."
	msgKeyRevoked    = "Ключ образа отозван."
	msgApproved      = "ПК подтверждён."
	msgRejected      = "ПК отклонён."
)

type handlers struct{ d *httpx.Deps }

var _ computersapi.StrictServerInterface = (*handlers)(nil)

// Section — список ПК и подключение ПК.
func Section() httpx.Section { return httpx.Section{Name: "computers", Mount: mount} }

func mount(r chi.Router, d *httpx.Deps) {
	si := computersapi.NewStrictHandlerWithOptions(&handlers{d: d},
		httpx.Middlewares[computersapi.StrictMiddlewareFunc](access),
		computersapi.StrictHTTPServerOptions{RequestErrorHandlerFunc: d.RequestError, ResponseErrorHandlerFunc: d.ResponseError(mapEnrollError)})
	computersapi.HandlerWithOptions(si, computersapi.ChiServerOptions{BaseRouter: r, ErrorHandlerFunc: d.ParamError})
}

// ListClubComputers — ClubComputerListController.
func (h *handlers) ListClubComputers(ctx context.Context, req computersapi.ListClubComputersRequestObject) (computersapi.ListClubComputersResponseObject, error) {
	clubID := httpx.ClubID(ctx)
	page := int64(1)
	if req.Params.Page != nil && *req.Params.Page > 1 {
		page = min(*req.Params.Page, 1_000_000)
	}
	var (
		rows     []store.ClubComputersPageRow
		total    int64
		versions []*string
		release  json.RawMessage
	)
	if err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		var err error
		if total, err = q.ClubComputersCount(ctx, clubID); err != nil {
			return err
		}
		if rows, err = q.ClubComputersPage(ctx, store.ClubComputersPageParams{ClubID: clubID, PageSize: perPage, PageOffset: int32((page - 1) * perPage)}); err != nil { // #nosec G115 -- page ≤ 1e6
			return err
		}
		if versions, err = q.ClubComputerAgentVersions(ctx, clubID); err != nil {
			return err
		}
		release, _ = q.AgentReleaseSetting(ctx, clubID)
		return nil
	}); err != nil {
		return nil, err
	}
	latest, download := parseRelease(release)
	online := map[int64]bool{}
	if h.d.Presence != nil {
		states, err := h.d.Presence.List(ctx, clubID)
		if err != nil {
			h.d.Log().WarnContext(ctx, "computers: presence", "err", err)
		}
		for id := range clubmap.PresenceByID(states) {
			online[id] = true
		}
	}
	out := computersapi.ComputerList{Items: make([]computersapi.ListedComputer, 0, len(rows)), Page: page, PerPage: perPage, Total: total}
	for _, r := range rows {
		conn := computersapi.Offline
		if online[r.ID] {
			conn = computersapi.Online
		}
		var cores *int64
		if r.CpuCores != nil {
			c := int64(*r.CpuCores)
			cores = &c
		}
		out.Items = append(out.Items, computersapi.ListedComputer{
			Id: r.ID, Number: r.Number, Name: clubmap.DisplayName(r.DisplayName, r.DeviceName, r.Number), ZoneId: r.ClubZoneID,
			ZoneName: r.ZoneName, Connection: conn, Status: r.Status, IsReserved: r.IsReserved, PositionX: r.PositionX,
			PositionY: r.PositionY, AgentState: r.AgentState, MacAddress: r.MacAddress, LocalIp: r.LocalIp, RemoteIp: r.RemoteIp,
			CpuModel: r.CpuModel, CpuCores: cores, RamTotalMb: r.RamTotalMb, GpuModel: r.GpuModel, OsVersion: r.OsVersion,
			DeviceId: r.DeviceID, CurrentSessionId: r.CurrentSessionID, AgentVersion: r.AgentVersion,
			AgentOutdated: clubmap.IsOutdated(r.AgentVersion, latest), LastSeenAt: r.LastSeenAt,
			HardwareReportedAt: r.HardwareReportedAt, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
		})
	}
	// AgentUpdate::status(): блок, только если есть устаревшие ПК.
	if latest != "" {
		var outdated int64
		for _, v := range versions {
			if clubmap.IsOutdated(v, latest) {
				outdated++
			}
		}
		if outdated > 0 {
			out.AgentUpdate = &computersapi.AgentUpdate{Version: latest, DownloadUrl: download, Outdated: outdated, Total: int64(len(versions))}
		}
	}
	return computersapi.ListClubComputers200JSONResponse(out), nil
}

// parseRelease — AgentUpdate::latestVersion() и downloadUrl() (только https-ссылка).
func parseRelease(raw json.RawMessage) (string, *string) {
	var rel struct {
		Latest   any `json:"latest_version"`
		Download any `json:"download_url"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &rel) != nil {
		return "", nil
	}
	latest, _ := rel.Latest.(string)
	if !clubmap.ValidLatest(latest) {
		return "", nil
	}
	var download *string
	if u, ok := rel.Download.(string); ok && strings.HasPrefix(u, "https://") {
		if p, err := url.Parse(u); err == nil && p.Host != "" {
			download = &u
		}
	}
	return latest, download
}

// staff — вошедший сотрудник с выбранным клубом (401/403 как у разделов с правами).
func staff(ctx context.Context) (principal.Principal, error) {
	p, ok := principal.FromContext(ctx)
	switch {
	case !ok:
		return p, httpx.NewAPIError(http.StatusUnauthorized, "unauthenticated", "Unauthenticated.")
	case p.ClubID <= 0 || p.Role == "":
		return p, httpx.NewAPIError(http.StatusForbidden, permissions.CodeForbidden, permissions.MsgNoClubs)
	}
	return p, nil
}

func (h *handlers) svc() (*enroll.Service, error) {
	if h.d.Enroll == nil {
		return nil, httpx.ErrNotConfigured
	}
	return h.d.Enroll, nil
}

func clientIP(ctx context.Context) netip.Addr {
	_, r := httpx.HTTPFrom(ctx)
	if r == nil {
		return netip.Addr{}
	}
	a, _ := netip.ParseAddr(httpx.HostOnly(r.RemoteAddr))
	return a
}

// GetAgentEnrollment — коды, ключи образа, ожидающие ПК и зоны (для выбора зоны новой карточки).
func (h *handlers) GetAgentEnrollment(ctx context.Context, _ computersapi.GetAgentEnrollmentRequestObject) (computersapi.GetAgentEnrollmentResponseObject, error) {
	p, err := staff(ctx)
	if err != nil {
		return nil, err
	}
	svc, err := h.svc()
	if err != nil {
		return nil, err
	}
	out := computersapi.Enrollment{
		CanRegister: enroll.CanRegister(p), CanManageImageKeys: enroll.CanManageImageKeys(p),
		Zones: []computersapi.EnrollmentZone{}, Codes: []computersapi.EnrollmentCode{},
		ImageKeys: []computersapi.ImageKey{}, Pending: []computersapi.PendingComputer{},
	}
	if err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		zones, err := q.ClubMapZones(ctx, p.ClubID)
		for _, z := range zones {
			out.Zones = append(out.Zones, computersapi.EnrollmentZone{Id: z.ID, Name: z.Name})
		}
		if err != nil || !out.CanRegister {
			return err
		}
		return enrollmentComputers(ctx, q, p.ClubID, &out)
	}); err != nil {
		return nil, err
	}
	if out.CanRegister {
		codes, err := svc.ListCodes(ctx, p)
		if err != nil {
			return nil, err
		}
		for _, c := range codes {
			out.Codes = append(out.Codes, computersapi.EnrollmentCode{Id: c.ID, ZoneId: c.ClubZoneID, ComputerId: c.ClubComputerID, CreatedAt: c.CreatedAt, ExpiresAt: c.ExpiresAt})
		}
		pending, err := svc.ListPending(ctx, p)
		if err != nil {
			return nil, err
		}
		for _, pc := range pending {
			item := computersapi.PendingComputer{Id: pc.ID, MacAddress: pc.MacAddress, Hostname: pc.Hostname, CreatedAt: pc.CreatedAt, LastSeenAt: pc.LastSeenAt, Attempts: int64(pc.Attempts)}
			if pc.Ip != nil {
				s := pc.Ip.String()
				item.Ip = &s
			}
			out.Pending = append(out.Pending, item)
		}
	}
	if out.CanManageImageKeys {
		keys, err := svc.ListImageKeys(ctx, p)
		if err != nil {
			return nil, err
		}
		for _, k := range keys {
			subnets := make([]string, len(k.Subnets))
			for i, s := range k.Subnets {
				subnets[i] = s.String()
			}
			out.ImageKeys = append(out.ImageKeys, computersapi.ImageKey{Id: k.ID, Prefix: k.KeyPrefix, Name: k.Name, Subnets: subnets,
				CreatedAt: k.CreatedAt, ExpiresAt: k.ExpiresAt, RevokedAt: k.RevokedAt, LastUsedAt: k.LastUsedAt})
		}
	}
	return computersapi.GetAgentEnrollment200JSONResponse(out), nil
}

// CreateEnrollmentCode — «Подключить ПК»: одноразовый код (показывается один раз).
func (h *handlers) CreateEnrollmentCode(ctx context.Context, req computersapi.CreateEnrollmentCodeRequestObject) (computersapi.CreateEnrollmentCodeResponseObject, error) {
	p, err := staff(ctx)
	if err != nil {
		return nil, err
	}
	svc, err := h.svc()
	if err != nil {
		return nil, err
	}
	var zone *int64
	if req.Body != nil {
		zone = req.Body.ZoneId
	}
	c, err := svc.CreateCode(ctx, p, enroll.CodeRequest{ZoneID: zone, IP: clientIP(ctx)})
	if err != nil {
		return nil, err
	}
	return computersapi.CreateEnrollmentCode201JSONResponse{Id: c.ID, Code: c.Code, ExpiresAt: c.ExpiresAt}, nil
}

// CancelEnrollmentCode — отменить неиспользованный код.
func (h *handlers) CancelEnrollmentCode(ctx context.Context, req computersapi.CancelEnrollmentCodeRequestObject) (computersapi.CancelEnrollmentCodeResponseObject, error) {
	p, err := staff(ctx)
	if err != nil {
		return nil, err
	}
	svc, err := h.svc()
	if err != nil {
		return nil, err
	}
	if err := svc.CancelCode(ctx, p, req.CodeId); err != nil {
		return nil, err
	}
	return computersapi.CancelEnrollmentCode200JSONResponse{Message: h.d.T(ctx, msgCodeCancelled, nil)}, nil
}

// CreateImageKey — ключ образа для бездисковых ПК (показывается один раз).
func (h *handlers) CreateImageKey(ctx context.Context, req computersapi.CreateImageKeyRequestObject) (computersapi.CreateImageKeyResponseObject, error) {
	p, err := staff(ctx)
	if err != nil {
		return nil, err
	}
	svc, err := h.svc()
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, httpx.Validation("name", "Укажите название ключа.")
	}
	subnets := make([]netip.Prefix, 0, len(req.Body.Subnets))
	for _, s := range req.Body.Subnets {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		pfx, err := netip.ParsePrefix(s)
		if err != nil {
			if a, err2 := netip.ParseAddr(s); err2 == nil {
				pfx = netip.PrefixFrom(a, a.BitLen())
			} else {
				return nil, httpx.Validation("subnets", "Подсеть должна ограничивать адреса клуба.")
			}
		}
		subnets = append(subnets, pfx)
	}
	k, err := svc.CreateImageKey(ctx, p, enroll.ImageKeyRequest{Name: req.Body.Name, Subnets: subnets, ExpiresAt: req.Body.ExpiresAt, IP: clientIP(ctx)})
	if err != nil {
		return nil, err
	}
	return computersapi.CreateImageKey201JSONResponse{Id: k.ID, Key: k.Key, Prefix: k.Prefix, Name: k.Name}, nil
}

// RevokeImageKey — отозвать ключ образа.
func (h *handlers) RevokeImageKey(ctx context.Context, req computersapi.RevokeImageKeyRequestObject) (computersapi.RevokeImageKeyResponseObject, error) {
	p, err := staff(ctx)
	if err != nil {
		return nil, err
	}
	svc, err := h.svc()
	if err != nil {
		return nil, err
	}
	if _, err := svc.RevokeImageKey(ctx, p, req.KeyId, clientIP(ctx)); err != nil {
		return nil, err
	}
	return computersapi.RevokeImageKey200JSONResponse{Message: h.d.T(ctx, msgKeyRevoked, nil)}, nil
}

// ApprovePendingComputer — подтвердить ПК, ожидающий по ключу образа.
func (h *handlers) ApprovePendingComputer(ctx context.Context, req computersapi.ApprovePendingComputerRequestObject) (computersapi.ApprovePendingComputerResponseObject, error) {
	p, err := staff(ctx)
	if err != nil {
		return nil, err
	}
	svc, err := h.svc()
	if err != nil {
		return nil, err
	}
	var zone *int64
	if req.Body != nil {
		zone = req.Body.ZoneId
	}
	if _, err := svc.ApprovePending(ctx, p, enroll.ApproveRequest{PendingID: req.PendingId, ZoneID: zone, IP: clientIP(ctx)}); err != nil {
		return nil, err
	}
	return computersapi.ApprovePendingComputer200JSONResponse{Message: h.d.T(ctx, msgApproved, nil)}, nil
}

// RejectPendingComputer — отклонить ПК.
func (h *handlers) RejectPendingComputer(ctx context.Context, req computersapi.RejectPendingComputerRequestObject) (computersapi.RejectPendingComputerResponseObject, error) {
	p, err := staff(ctx)
	if err != nil {
		return nil, err
	}
	svc, err := h.svc()
	if err != nil {
		return nil, err
	}
	if err := svc.RejectPending(ctx, p, req.PendingId, clientIP(ctx)); err != nil {
		return nil, err
	}
	return computersapi.RejectPendingComputer200JSONResponse{Message: h.d.T(ctx, msgRejected, nil)}, nil
}

// mapEnrollError — enroll.Error → ответ (Kind → HTTP-код), как REST-слой панели, описанный в enroll/errors.go.
func mapEnrollError(err error) *httpx.APIError {
	var e *enroll.Error
	if !errors.As(err, &e) {
		return nil
	}
	switch e.Kind {
	case enroll.KindInvalid, enroll.KindPrecondition:
		field := e.Field
		if field == "" {
			field = "computer"
		}
		return httpx.Validation(field, e.Message)
	case enroll.KindForbidden:
		return httpx.NewAPIError(http.StatusForbidden, permissions.CodeForbidden, e.Message)
	case enroll.KindNotFound:
		if e.Field != "" {
			return httpx.Validation(e.Field, e.Message)
		}
		return httpx.NewAPIError(http.StatusNotFound, "not_found", e.Message)
	case enroll.KindRateLimited:
		return httpx.NewAPIError(http.StatusTooManyRequests, "too_many_requests", e.Message)
	}
	return httpx.NewAPIError(http.StatusUnprocessableEntity, "validation_failed", e.Message)
}
