package clubmap

// Карта клуба: экран, состояние ПК, зоны, карточки ПК. Laravel: ClubZoneController, ClubComputerController.

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/agent/presence"
	"github.com/aitmaganbetov/zafirogo/server/internal/billing/pricing"
	"github.com/aitmaganbetov/zafirogo/server/internal/clubmap"
	clubmapapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/clubmap"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// Тексты computerStates (ключи перевода).
const (
	msgGuestNoAccount = "Гость без аккаунта"     // сессия без аккаунта
	msgPaidAtCash     = "Оплачено на кассе"      // гостевая сессия без покупок
	msgPerMinute      = "Поминутно: :count мин." // часть сессии kind = minutes
	// maxStateIDs — GET /club-map/state?ids=… (досылка после событий).
	maxStateIDs = 500
	// defaultGapMinutes — ComputerReservations::DEFAULT_GAP_MINUTES.
	defaultGapMinutes = 30
)

// clubSettings — нужное карте из clubs.settings (data_get старого кода).
type clubSettings struct {
	Club struct {
		BookingInterval any `json:"booking_interval"`
	} `json:"club"`
	Guests struct {
		GuestModeEnabled any `json:"guest_mode_enabled"`
	} `json:"guests"`
}

func parseSettings(raw []byte) clubSettings {
	var s clubSettings
	_ = json.Unmarshal(raw, &s)
	return s
}

// filterBool — filter_var($v ?? true, FILTER_VALIDATE_BOOLEAN).
func filterBool(v any, def bool) bool {
	switch x := v.(type) {
	case nil:
		return def
	case bool:
		return x
	case float64:
		return x == 1
	case string:
		switch strings.ToLower(strings.TrimSpace(x)) {
		case "1", "true", "on", "yes":
			return true
		}
	}
	return false
}

// gapMinutes — ComputerReservations::gapMinutes(): booking_interval (числом) или 30, не меньше 0.
func gapMinutes(v any) int64 {
	switch x := v.(type) {
	case float64:
		return max(0, int64(x+0.5))
	case string:
		if f, err := strconv.ParseFloat(strings.TrimSpace(x), 64); err == nil {
			return max(0, int64(f+0.5))
		}
	}
	return defaultGapMinutes
}

// mapData — всё, из чего строятся ПК карты.
type mapData struct {
	rows      []store.ClubMapComputersRow
	purchases map[string]string
	held      map[int64]bool
	latest    string
	settings  store.ClubMapSettingsRow
}

// load — ПК клуба (все или ids), покупки идущих сессий, ПК с бронью сейчас, объявленная версия агента.
func (h *handlers) load(ctx context.Context, q *store.Queries, clubID int64, ids map[int64]bool, now time.Time) (mapData, error) {
	var d mapData
	var err error
	if d.settings, err = q.ClubMapSettings(ctx, clubID); err != nil {
		return d, err
	}
	var after int64
	for {
		rows, err := q.ClubMapComputers(ctx, store.ClubMapComputersParams{ClubID: clubID, AfterID: after, PageSize: 500})
		if err != nil {
			return d, err
		}
		for _, r := range rows {
			if ids == nil || ids[r.ID] {
				d.rows = append(d.rows, r)
			}
		}
		if len(rows) < 500 {
			break
		}
		after = rows[len(rows)-1].ID
	}
	var sessions []string
	for _, r := range d.rows {
		if r.CurrentSessionID != nil {
			sessions = append(sessions, *r.CurrentSessionID)
		}
	}
	d.purchases = map[string]string{}
	if len(sessions) > 0 {
		parts, err := q.ClubMapSessionPurchases(ctx, store.ClubMapSessionPurchasesParams{ClubID: clubID, SessionIds: sessions})
		if err != nil {
			return d, err
		}
		for _, p := range parts {
			name := p.Name
			if p.Kind != "tariff" {
				name = h.d.T(ctx, msgPerMinute, map[string]string{"count": strconv.FormatInt(p.Minutes, 10)})
			}
			if prev, ok := d.purchases[p.SessionID]; ok {
				name = prev + ", " + name
			}
			d.purchases[p.SessionID] = name
		}
	}
	gap := gapMinutes(parseSettings(d.settings.Settings).Club.BookingInterval)
	held, err := q.ClubMapHeldComputerIDs(ctx, store.ClubMapHeldComputerIDsParams{
		ClubID: clubID, FromAt: now, ToAt: now.Add(time.Duration(gap) * time.Minute)})
	if err != nil {
		return d, err
	}
	d.held = make(map[int64]bool, len(held))
	for _, id := range held {
		d.held[id] = true
	}
	d.latest = latestAgentVersion(ctx, q, clubID)
	return d, nil
}

// latestAgentVersion — AgentUpdate::latestVersion() из platform_settings 'agent_release' ("" — не объявлена).
func latestAgentVersion(ctx context.Context, q *store.Queries, clubID int64) string {
	raw, err := q.AgentReleaseSetting(ctx, clubID)
	if err != nil {
		return ""
	}
	var rel struct {
		Latest any `json:"latest_version"`
	}
	if json.Unmarshal(raw, &rel) != nil {
		return ""
	}
	if v, ok := rel.Latest.(string); ok && clubmap.ValidLatest(v) {
		return v
	}
	return ""
}

// presenceByID — присутствие клуба; ошибка Valkey — карта без присутствия (все ПК не в сети), а не ошибка.
func (h *handlers) presenceByID(ctx context.Context, clubID int64) map[int64]presence.State {
	if h.d.Presence == nil {
		return nil
	}
	states, err := h.d.Presence.List(ctx, clubID)
	if err != nil {
		h.d.Log().WarnContext(ctx, "club map: presence", "err", err)
	}
	return clubmap.PresenceByID(states)
}

// state — элемент ClubZoneController::computerStates() (классы подсветки — поле light).
func (h *handlers) state(ctx context.Context, r store.ClubMapComputersRow, d mapData, pres map[int64]presence.State, now time.Time) clubmapapi.ComputerState {
	st, online := pres[r.ID]
	pc := clubmap.Computer{
		ID: r.ID, Status: r.Status, IsReserved: r.IsReserved, BookedNow: d.held[r.ID], LastSeenAt: r.LastSeenAt,
		SessionID: r.CurrentSessionID, SessionUserID: r.CurrentSessionUserID, SessionGuest: r.SessionGuest,
		SessionEndsAt: r.SessionEndsAt, StaffUntil: r.StaffUntil, Online: online,
	}
	v := clubmapapi.ComputerState{
		Id: r.ID, ZoneId: r.ClubZoneID, Number: r.Number, Name: clubmap.DisplayName(r.DisplayName, r.DeviceName, r.Number),
		Status: pc.CurrentStatus(), Light: pc.Light(now), Connection: clubmapapi.ComputerStateConnection(pc.Connection()),
		IsReserved: r.IsReserved, HasSession: r.CurrentSessionID != nil, SessionId: r.CurrentSessionID,
		SessionEndsAt: r.SessionEndsAt, SessionGuest: r.SessionGuest, SessionUserId: r.CurrentSessionUserID,
		LastSeen: r.LastSeenAt,
	}
	version := r.AgentVersion
	if r.AgentState != nil {
		v.AgentState = *r.AgentState
	}
	if online {
		if st.AgentState != "" {
			v.AgentState = st.AgentState
		}
		if st.AgentVersion != "" {
			av := st.AgentVersion
			version = &av
		}
		if !st.LastSeenAt.IsZero() {
			ls := st.LastSeenAt
			v.LastSeen = &ls
		}
		// Этап 7: загрузка и проблемы здоровья (только ПК в сети с новым агентом — иначе полей нет).
		v.Load = loadOf(st)
		if st.Health != nil || len(st.Problems) > 0 {
			p := problemsOf(st.Problems)
			v.Problems = &p
		}
		v.RttMs = nonZero(st.RTTMillis)
	}
	if version != nil {
		v.AgentVersion = *version
	}
	v.AgentOutdated = clubmap.IsOutdated(version, d.latest)
	if v.HasSession {
		if r.SessionGuest {
			v.SessionUser = h.d.T(ctx, msgGuestNoAccount, nil)
		} else {
			var parts []string
			if r.SessionUserName != nil && *r.SessionUserName != "" {
				parts = append(parts, *r.SessionUserName)
			}
			if r.SessionUserPhone != nil && *r.SessionUserPhone != "" {
				parts = append(parts, *r.SessionUserPhone)
			}
			v.SessionUser = strings.Join(parts, " · ")
		}
		if bought, ok := d.purchases[*r.CurrentSessionID]; ok {
			v.SessionTariff = bought
		} else if r.SessionGuest {
			v.SessionTariff = h.d.T(ctx, msgPaidAtCash, nil)
		}
	}
	if pc.HasStaffVisit(now) {
		v.StaffUntil = r.StaffUntil
		name := ""
		if r.StaffName != nil {
			name = *r.StaffName
		}
		v.StaffName = &name
		if r.StaffKind != nil {
			v.StaffKind = *r.StaffKind
		}
	}
	return v
}

// GetClubMapState — ClubZoneController::state(): ПК из БД + присутствие агента; ids — только эти ПК.
func (h *handlers) GetClubMapState(ctx context.Context, req clubmapapi.GetClubMapStateRequestObject) (clubmapapi.GetClubMapStateResponseObject, error) {
	clubID, now := httpx.ClubID(ctx), h.d.NowFunc()
	var ids map[int64]bool
	if req.Params.Ids != nil {
		ids = map[int64]bool{}
		for _, s := range strings.Split(*req.Params.Ids, ",") {
			if id, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64); err == nil && id > 0 {
				ids[id] = true
			}
			if len(ids) > maxStateIDs {
				return nil, httpx.RuleErrorWith("ids", "max", strconv.Itoa(maxStateIDs))
			}
		}
	}
	var d mapData
	if err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		var err error
		d, err = h.load(ctx, q, clubID, ids, now)
		return err
	}); err != nil {
		return nil, err
	}
	pres := h.presenceByID(ctx, clubID)
	out := clubmapapi.ClubMapState{Computers: make([]clubmapapi.ComputerState, 0, len(d.rows))}
	for _, r := range d.rows {
		out.Computers = append(out.Computers, h.state(ctx, r, d, pres, now))
	}
	return clubmapapi.GetClubMapState200JSONResponse(out), nil
}

// GetClubMap — ClubZoneController::index().
func (h *handlers) GetClubMap(ctx context.Context, _ clubmapapi.GetClubMapRequestObject) (clubmapapi.GetClubMapResponseObject, error) {
	clubID, now := httpx.ClubID(ctx), h.d.NowFunc()
	var (
		d       mapData
		zones   []store.ClubMapZonesRow
		links   []store.ClubMapZoneTariffsRow
		tariffs []store.ClubMapTariffsRow
	)
	if err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		var err error
		if d, err = h.load(ctx, q, clubID, nil, now); err != nil {
			return err
		}
		if zones, err = q.ClubMapZones(ctx, clubID); err != nil {
			return err
		}
		if links, err = q.ClubMapZoneTariffs(ctx, clubID); err != nil {
			return err
		}
		tariffs, err = q.ClubMapTariffs(ctx, clubID)
		return err
	}); err != nil {
		return nil, err
	}
	loc, err := time.LoadLocation(d.settings.Timezone)
	if err != nil {
		loc = time.UTC
	}
	settings := parseSettings(d.settings.Settings)
	local := now.In(loc)
	start := time.Date(local.Year(), local.Month(), local.Day(), local.Hour(), 0, 0, 0, loc).Add(time.Hour)
	out := clubmapapi.ClubMapPage{
		Zones: make([]clubmapapi.Zone, 0, len(zones)), Computers: make([]clubmapapi.ComputerCard, 0, len(d.rows)),
		States: make([]clubmapapi.ComputerState, 0, len(d.rows)), Tariffs: make([]clubmapapi.MapTariff, 0, len(tariffs)),
		GuestMode:    filterBool(settings.Guests.GuestModeEnabled, true),
		BookingStart: start.Format("2006-01-02T15:04"),
	}
	byZone := map[int64][]int64{}
	for _, l := range links {
		byZone[l.ClubZoneID] = append(byZone[l.ClubZoneID], l.TariffID)
	}
	for _, z := range zones {
		ids := byZone[z.ID]
		if ids == nil {
			ids = []int64{}
		}
		out.Zones = append(out.Zones, clubmapapi.Zone{
			Id: z.ID, Name: z.Name, Description: z.Description, Capacity: z.Capacity, PricePerHourTiyn: z.PricePerHour,
			Icon: z.Icon, Theme: z.Theme, Status: z.Status, IsFeatured: z.IsFeatured, SortOrder: z.SortOrder,
			PositionX: z.PositionX, PositionY: z.PositionY, Width: z.Width, Height: z.Height, Rotation: int64(z.Rotation),
			TariffIds: ids,
		})
	}
	pres := h.presenceByID(ctx, clubID)
	for _, r := range d.rows {
		out.Computers = append(out.Computers, clubmapapi.ComputerCard{
			Id: r.ID, ZoneId: r.ClubZoneID, Number: r.Number, Name: clubmap.DisplayName(r.DisplayName, r.DeviceName, r.Number),
			DisplayName: r.DisplayName, MacAddress: r.MacAddress, PositionX: r.PositionX, PositionY: r.PositionY,
			Details: clubmap.Details(clubmap.Hardware{
				MAC: r.MacAddress, LocalIP: r.LocalIp, RemoteIP: r.RemoteIp, CPUModel: r.CpuModel, GPUModel: r.GpuModel,
				OSVersion: r.OsVersion, CPUCores: r.CpuCores, RAMTotalMB: r.RamTotalMb,
			}),
		})
		out.States = append(out.States, h.state(ctx, r, d, pres, now))
	}
	for _, t := range tariffs {
		sellable := t.IsActive
		if sellable {
			sale, err1 := pricing.ParseWeeklyPeriods(t.SalePeriods)
			validity, err2 := pricing.ParseWeeklyPeriods(t.ValidityPeriods)
			pt := pricing.Tariff{SalePeriods: sale, ValidityPeriods: validity}
			sellable = err1 == nil && err2 == nil && pt.OnSale(now, loc) && pt.InValidityPeriod(now, loc)
		}
		out.Tariffs = append(out.Tariffs, clubmapapi.MapTariff{
			Id: t.ID, Name: t.Name, DurationMinutes: t.DurationMinutes, PriceTiyn: t.Price, Currency: t.Currency,
			IsActive: t.IsActive, GuestSellable: sellable,
		})
	}
	return clubmapapi.GetClubMap200JSONResponse(out), nil
}

// env — клуб, сотрудник, часы и перевод текстов журнала на язык клуба.
func (h *handlers) env(ctx context.Context) clubmap.Env {
	p := httpx.Principal(ctx)
	return clubmap.Env{ClubID: p.ClubID, UserID: p.UserID, Now: h.d.NowFunc(),
		T: func(key string, params map[string]string) string { return h.d.T(ctx, key, params) }}
}

func zoneInput(b clubmapapi.ZoneInput) clubmap.ZoneInput {
	return clubmap.ZoneInput{
		Name: b.Name, Description: b.Description, Capacity: b.Capacity, PricePerHour: b.PricePerHourTiyn,
		Icon: b.Icon, Theme: b.Theme, Status: b.Status, IsFeatured: b.IsFeatured, SortOrder: b.SortOrder,
		TariffIDs: deref(b.TariffIds),
	}
}

func deref[T any](p *[]T) []T {
	if p == nil {
		return nil
	}
	return *p
}

// CreateZone — ClubZoneController::store().
func (h *handlers) CreateZone(ctx context.Context, req clubmapapi.CreateZoneRequestObject) (clubmapapi.CreateZoneResponseObject, error) {
	if req.Body == nil {
		return nil, httpx.RuleError("name", "required")
	}
	var id int64
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		var err error
		id, err = clubmap.CreateZone(ctx, q, h.env(ctx), zoneInput(*req.Body))
		return err
	})
	if err != nil {
		return nil, err
	}
	return clubmapapi.CreateZone201JSONResponse{Id: id, Message: h.d.T(ctx, clubmap.MsgZoneAdded, nil)}, nil
}

// UpdateZone — ClubZoneController::update().
func (h *handlers) UpdateZone(ctx context.Context, req clubmapapi.UpdateZoneRequestObject) (clubmapapi.UpdateZoneResponseObject, error) {
	if req.Body == nil {
		return nil, httpx.RuleError("name", "required")
	}
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		return clubmap.UpdateZone(ctx, q, h.env(ctx), req.ZoneId, zoneInput(*req.Body))
	})
	if err != nil {
		return nil, err
	}
	return clubmapapi.UpdateZone200JSONResponse{Id: req.ZoneId, Message: h.d.T(ctx, clubmap.MsgZoneUpdated, nil)}, nil
}

// DeleteZone — ClubZoneController::destroy() + решение Q-018.
func (h *handlers) DeleteZone(ctx context.Context, req clubmapapi.DeleteZoneRequestObject) (clubmapapi.DeleteZoneResponseObject, error) {
	var disabled []string
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		var err error
		disabled, err = clubmap.DeleteZone(ctx, q, h.env(ctx), req.ZoneId)
		return err
	})
	if err != nil {
		return nil, err
	}
	msg := h.d.T(ctx, clubmap.MsgZoneDeleted, nil)
	if len(disabled) > 0 {
		msg += " " + h.d.T(ctx, clubmap.MsgTariffsDisabled, map[string]string{"names": strings.Join(disabled, ", ")})
	} else {
		disabled = []string{}
	}
	return clubmapapi.DeleteZone200JSONResponse{Message: msg, DisabledTariffs: disabled}, nil
}

// UpdateZoneLayout — ClubZoneController::updateLayout().
func (h *handlers) UpdateZoneLayout(ctx context.Context, req clubmapapi.UpdateZoneLayoutRequestObject) (clubmapapi.UpdateZoneLayoutResponseObject, error) {
	if req.Body == nil {
		return nil, httpx.RuleError("position_x", "required")
	}
	b := req.Body
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		return clubmap.UpdateZoneLayout(ctx, q, h.env(ctx), req.ZoneId, clubmap.ZoneLayout{X: b.PositionX, Y: b.PositionY, Width: b.Width, Height: b.Height, Rotation: b.Rotation})
	})
	if err != nil {
		return nil, err
	}
	return clubmapapi.UpdateZoneLayout200JSONResponse{Id: req.ZoneId, Message: h.d.T(ctx, "Расположение сохранено", nil)}, nil
}

// AddZoneComputer — ClubComputerController::store().
func (h *handlers) AddZoneComputer(ctx context.Context, req clubmapapi.AddZoneComputerRequestObject) (clubmapapi.AddZoneComputerResponseObject, error) {
	var id int64
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		var err error
		id, err = clubmap.AddComputer(ctx, q, h.env(ctx), req.ZoneId)
		return err
	})
	if err != nil {
		return nil, err
	}
	if id == 0 {
		return clubmapapi.AddZoneComputer200JSONResponse{Added: false, Message: h.d.T(ctx, clubmap.MsgZoneCapacity, nil)}, nil
	}
	return clubmapapi.AddZoneComputer200JSONResponse{Added: true, Id: &id, Message: h.d.T(ctx, clubmap.MsgComputerAdded, nil)}, nil
}

// UpdateComputer — ClubComputerController::update().
func (h *handlers) UpdateComputer(ctx context.Context, req clubmapapi.UpdateComputerRequestObject) (clubmapapi.UpdateComputerResponseObject, error) {
	if req.Body == nil {
		return nil, httpx.RuleError("number", "required")
	}
	b := req.Body
	in := clubmap.ComputerInput{Number: b.Number, DisplayName: b.DisplayName, MACSet: true, MAC: b.MacAddress, ZoneID: b.ClubZoneId}
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		return clubmap.UpdateComputer(ctx, q, h.env(ctx), req.ComputerId, in)
	})
	if err != nil {
		return nil, err
	}
	return clubmapapi.UpdateComputer200JSONResponse{Id: req.ComputerId, Message: h.d.T(ctx, clubmap.MsgComputerUpdated, nil)}, nil
}

// DeleteComputer — ClubComputerController::destroy().
func (h *handlers) DeleteComputer(ctx context.Context, req clubmapapi.DeleteComputerRequestObject) (clubmapapi.DeleteComputerResponseObject, error) {
	var name string
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		var err error
		name, err = clubmap.DeleteComputer(ctx, q, h.env(ctx), req.ComputerId)
		return err
	})
	if err != nil {
		return nil, err
	}
	return clubmapapi.DeleteComputer200JSONResponse{Message: h.d.T(ctx, clubmap.MsgComputerDeleted, map[string]string{"computer": name})}, nil
}

// UpdateComputerLayout — ClubComputerController::__invoke().
func (h *handlers) UpdateComputerLayout(ctx context.Context, req clubmapapi.UpdateComputerLayoutRequestObject) (clubmapapi.UpdateComputerLayoutResponseObject, error) {
	if req.Body == nil {
		return nil, httpx.RuleError("position_x", "required")
	}
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		return clubmap.UpdateComputerLayout(ctx, q, h.env(ctx), req.ComputerId, req.Body.PositionX, req.Body.PositionY)
	})
	if err != nil {
		return nil, err
	}
	return clubmapapi.UpdateComputerLayout200JSONResponse{Id: req.ComputerId, Message: h.d.T(ctx, "Место ПК сохранено", nil)}, nil
}
