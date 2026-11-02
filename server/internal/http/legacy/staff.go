package legacy

// Приложение сотрудника (routes/api_staff.php, 24 маршрута; группа auth:sanctum, current-club, subscription,
// club-role:STAFF, throttle:staff-app). Laravel: app/Http/Controllers/Api/Staff/* (деньги — строки "1500.00",
// время — UTC ISO 8601 «Z», StaffApiController), ClubComputerController::control()/endSession(),
// GuestSessionController::store(), ChatController::conversationsJson()/poll()/reply(), NotificationController.
// Всё — через сервисы нового кода: cash/service, billing/service, clubmap, stock, shop, chat.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aitmaganbetov/zafirogo/server/internal/auth/permissions"
	"github.com/aitmaganbetov/zafirogo/server/internal/billing/bonus"
	billingsvc "github.com/aitmaganbetov/zafirogo/server/internal/billing/service"
	"github.com/aitmaganbetov/zafirogo/server/internal/cash"
	cashsvc "github.com/aitmaganbetov/zafirogo/server/internal/cash/service"
	chatsvc "github.com/aitmaganbetov/zafirogo/server/internal/chat"
	"github.com/aitmaganbetov/zafirogo/server/internal/club"
	"github.com/aitmaganbetov/zafirogo/server/internal/clubmap"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
	"github.com/aitmaganbetov/zafirogo/server/internal/money"
	"github.com/aitmaganbetov/zafirogo/server/internal/shop"
	"github.com/aitmaganbetov/zafirogo/server/internal/stock"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// Тексты (русский текст = ключ перевода).
const (
	msgGuestModeOff    = "Гостевой режим отключён в настройках клуба."
	msgGuestSold       = "Тариф подключён: :count ПК, к оплате :amount."
	msgTopUpDone       = "Баланс пользователя пополнен."
	msgTopUpBonusDone  = "Баланс пользователя пополнен. Бонус за пополнение: :amount."
	msgNoAnswerRight   = "Нет права отвечать в чате. Его даёт владелец клуба в «Настройки → Права»."
	defaultGapMinutes  = 30 // ComputerReservations::DEFAULT_GAP_MINUTES
	shiftTransactions  = 20
	recentTransactions = 10
	customersPage      = 25
	productsPage       = 50
	ordersPage         = 30
)

// idempotencyKey — ключ денежной операции: заголовок Idempotency-Key, если приложение его прислало (старое не
// присылает), иначе новый на каждый запрос — как в старом API (повтор запроса = новая операция).
func idempotencyKey(r *http.Request) string {
	if k := strings.TrimSpace(r.Header.Get("Idempotency-Key")); k != "" && len(k) <= 100 {
		return "mobile:" + k
	}
	return "mobile:" + uuid.NewString()
}

// meta — StaffApiController::meta(): {current_page, last_page}.
type pageMeta struct {
	CurrentPage int64 `json:"current_page"`
	LastPage    int64 `json:"last_page"`
}

func page(r *http.Request) int64 {
	n, err := strconv.ParseInt(r.URL.Query().Get("page"), 10, 64)
	if err != nil || n < 1 {
		return 1
	}
	return n
}

// offset — OFFSET страницы (номер страницы ограничен, переполнения int32 нет).
func offset(cur, size int64) int32 {
	v := min(max(cur-1, 0)*size, 1<<30)
	return int32(v) // #nosec G115 -- v в [0, 2^30]
}

func metaOf(current, total, size int64) pageMeta {
	last := max(1, (total+size-1)/size)
	return pageMeta{CurrentPage: current, LastPage: last}
}

// likePattern — '%'.addcslashes($v, '\\%_').'%'.
func likePattern(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + r.Replace(s) + "%"
}

// ---------------------------------------------------------------------------
// Me и отчёт дня (Api\Staff\MeController)
// ---------------------------------------------------------------------------

type shiftJSON struct {
	ID             int64   `json:"id"`
	OpenedAt       *string `json:"opened_at"`
	OpeningBalance string  `json:"opening_balance"`
}

func shiftOf(s store.CashShift) shiftJSON {
	return shiftJSON{ID: s.ID, OpenedAt: zulu(&s.OpenedAt), OpeningBalance: moneyStr(s.OpeningBalance)}
}

func totalsOf(t cash.Totals) map[string]string {
	return map[string]string{"income": moneyStr(int64(t.Income)), "expense": moneyStr(int64(t.Expense)), "balance": moneyStr(int64(t.Balance()))}
}

// staffMe — GET /api/staff/me.
func (a *API) staffMe(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := httpx.Principal(ctx)
	type counters struct {
		OrdersNew           int64 `json:"orders_new"`
		ChatUnread          int64 `json:"chat_unread"`
		NotificationsUnread int64 `json:"notifications_unread"`
	}
	type meShift struct {
		ID       int64   `json:"id"`
		OpenedAt *string `json:"opened_at"`
		Balance  string  `json:"balance"`
	}
	var out struct {
		User struct {
			ID    int64  `json:"id"`
			Name  string `json:"name"`
			Phone string `json:"phone"`
		} `json:"user"`
		Club struct {
			ID       int64  `json:"id"`
			Name     string `json:"name"`
			Currency string `json:"currency"`
			Timezone string `json:"timezone"`
		} `json:"club"`
		Role        string               `json:"role"`
		Permissions permissions.StaffApp `json:"permissions"`
		Counters    counters             `json:"counters"`
		Shift       *meShift             `json:"shift"`
	}
	err := a.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		prof, err := q.MobileMemberProfile(ctx, store.MobileMemberProfileParams{ClubID: p.ClubID, UserID: p.UserID})
		if err != nil {
			return err
		}
		cs := parseClubSetting(prof.Settings)
		out.User.ID, out.User.Name, out.User.Phone = p.UserID, prof.UserName, prof.UserPhone
		out.Club.ID, out.Club.Name, out.Club.Currency, out.Club.Timezone = p.ClubID, prof.ClubName, cs.currency(), prof.Timezone
		out.Role = prof.Role
		mem := &permissions.Membership{Role: prof.Role, Given: permissions.ParseGiven(prof.Permissions)}
		out.Permissions = permissions.StaffAppFor(mem, cs.guestMode())
		if out.Counters.OrdersNew, err = q.MobileStaffNewOrders(ctx, p.ClubID); err != nil {
			return err
		}
		if out.Counters.ChatUnread, err = q.ChatUnreadTotal(ctx, p.ClubID); err != nil {
			return err
		}
		if out.Permissions.Cash && a.d.Cash != nil {
			shift, totals, err := a.d.Cash.CurrentShift(ctx, q, p.ClubID)
			if err != nil {
				return err
			}
			if shift != nil {
				out.Shift = &meShift{ID: shift.ID, OpenedAt: zulu(&shift.OpenedAt), Balance: moneyStr(int64(totals.Balance()))}
			}
		}
		return nil
	})
	if err != nil {
		a.fail(w, r, "staff me", err)
		return
	}
	// ClubNotifications::for() собирает раздел notifications — число непрочитанных берётся из него.
	var list struct {
		UnreadCount int64 `json:"unread_count"`
	}
	if status, raw := a.innerCall(r, http.MethodGet, "/api/notifications"); status == http.StatusOK {
		_ = json.Unmarshal(raw, &list)
	}
	out.Counters.NotificationsUnread = list.UnreadCount
	writeJSON(w, http.StatusOK, out)
}

// innerCall — запрос к новому REST этого сервера от имени того же сотрудника (Principal в контексте; вход панели
// не нужен). Ответ — статус и тело.
func (a *API) innerCall(r *http.Request, method, path string) (int, []byte) {
	// Новый маршрутный контекст chi: внешний (маршрут старого контракта) не должен влиять на разбор пути.
	ctx := context.WithValue(Internal(r.Context()), chi.RouteCtxKey, (*chi.Context)(nil))
	req, err := http.NewRequestWithContext(ctx, method, path, nil)
	if err != nil {
		return http.StatusInternalServerError, nil
	}
	req.RemoteAddr = r.RemoteAddr
	req.Host = r.Host
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", r.UserAgent())
	if method != http.MethodGet {
		req.Header.Set("Idempotency-Key", uuid.NewString())
		req.Header.Set("Content-Type", "application/json")
	}
	rec := &bufferWriter{h: http.Header{}}
	a.inner.ServeHTTP(rec, req)
	if rec.status == 0 {
		rec.status = http.StatusOK
	}
	return rec.status, rec.buf.Bytes()
}

// passthrough — ответ нового REST как есть (формат совпадает со старым: уведомления).
func (a *API) passthrough(method, path string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		status, raw := a.innerCall(r, method, path)
		if status != http.StatusOK {
			var e struct {
				Message string `json:"message"`
			}
			_ = json.Unmarshal(raw, &e)
			if status >= 500 {
				e.Message = "Server Error"
			}
			writeMessage(w, status, e.Message)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-cache, private")
		w.WriteHeader(status)
		_, _ = w.Write(raw)
	}
}

// reportToday — GET /api/staff/reports/today: сегодня и 7 местных дней клуба, ПК, мало на складе, последние операции.
func (a *API) reportToday(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := httpx.Principal(ctx)
	now := a.d.NowFunc()
	type dayTotals struct {
		Income       string `json:"income"`
		Expense      string `json:"expense"`
		Transactions int64  `json:"transactions"`
	}
	type weekDay struct {
		Date    string `json:"date"`
		Income  string `json:"income"`
		Expense string `json:"expense"`
	}
	type lowStock struct {
		ID       int64  `json:"id"`
		Name     string `json:"name"`
		Quantity int64  `json:"quantity"`
	}
	var out struct {
		Currency  string    `json:"currency"`
		Today     dayTotals `json:"today"`
		Week      []weekDay `json:"week"`
		Computers struct {
			Total    int `json:"total"`
			Online   int `json:"online"`
			Busy     int `json:"busy"`
			Reserved int `json:"reserved"`
		} `json:"computers"`
		LowStock           []lowStock        `json:"low_stock"`
		RecentTransactions []transactionJSON `json:"recent_transactions"`
	}
	out.Week, out.LowStock, out.RecentTransactions = []weekDay{}, []lowStock{}, []transactionJSON{}
	pres := a.presence(ctx, p.ClubID)
	err := a.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		prof, err := q.MobileMemberProfile(ctx, store.MobileMemberProfileParams{ClubID: p.ClubID, UserID: p.UserID})
		if err != nil {
			return err
		}
		out.Currency = parseClubSetting(prof.Settings).currency()
		// now($club->timezone ?: app.timezone)->startOfDay(): 7 дней до сегодня включительно.
		loc := time.UTC
		if prof.Timezone != "" {
			if l, err := time.LoadLocation(prof.Timezone); err == nil {
				loc = l
			}
		}
		local := now.In(loc)
		today := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
		starts := make([]time.Time, 7)
		for i := range starts {
			starts[i] = today.AddDate(0, 0, i-6)
		}
		rows, err := q.MobileStaffCashDays(ctx, store.MobileStaffCashDaysParams{Starts: starts, ClubID: p.ClubID})
		if err != nil {
			return err
		}
		type sums struct{ income, expense, count int64 }
		days := make([]sums, 7)
		for _, row := range rows {
			i := int(row.Day) - 1 // WITH ORDINALITY — с 1
			if i < 0 || i > 6 {
				continue
			}
			if row.Type == string(cash.Income) {
				days[i].income = row.Total
			} else {
				days[i].expense = row.Total
			}
			days[i].count += row.Count
		}
		out.Today = dayTotals{Income: moneyStr(days[6].income), Expense: moneyStr(days[6].expense), Transactions: days[6].count}
		for i, d := range days {
			out.Week = append(out.Week, weekDay{Date: starts[i].Format("2006-01-02"), Income: moneyStr(d.income), Expense: moneyStr(d.expense)})
		}
		comps, err := a.computers(ctx, q, p.ClubID, now)
		if err != nil {
			return err
		}
		for _, c := range comps {
			pc := c.pc(pres, now)
			out.Computers.Total++
			if pc.Connection() == "online" {
				out.Computers.Online++
			}
			if pc.HasPaidSession(now) {
				out.Computers.Busy++
			}
			if pc.CurrentStatus() == "reserved" {
				out.Computers.Reserved++
			}
		}
		low, err := q.MobileStaffLowStock(ctx, p.ClubID)
		if err != nil {
			return err
		}
		for _, l := range low {
			out.LowStock = append(out.LowStock, lowStock{ID: l.ID, Name: l.Name, Quantity: l.Quantity})
		}
		recent, err := q.MobileStaffRecentTransactions(ctx, store.MobileStaffRecentTransactionsParams{ClubID: p.ClubID, PageSize: recentTransactions})
		if err != nil {
			return err
		}
		for _, t := range recent {
			out.RecentTransactions = append(out.RecentTransactions, transactionOf(t.ID, t.Type, t.Amount, t.Description, t.CreatedAt))
		}
		return nil
	})
	if err != nil {
		a.fail(w, r, "report today", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// ---------------------------------------------------------------------------
// Карта клуба (Api\Staff\ComputerController, ClubComputerController::control/endSession, GuestSessionController)
// ---------------------------------------------------------------------------

type mapComputer struct {
	row  store.ClubMapComputersRow
	held bool
}

func (c mapComputer) pc(pres map[int64]bool, _ time.Time) clubmap.Computer {
	r := c.row
	return clubmap.Computer{ID: r.ID, Status: r.Status, IsReserved: r.IsReserved, BookedNow: c.held, LastSeenAt: r.LastSeenAt,
		SessionID: r.CurrentSessionID, SessionUserID: r.CurrentSessionUserID, SessionGuest: r.SessionGuest,
		SessionEndsAt: r.SessionEndsAt, StaffUntil: r.StaffUntil, Online: pres[r.ID]}
}

// presence — ПК клуба в сети (присутствие агента); ошибка — все не в сети.
func (a *API) presence(ctx context.Context, clubID int64) map[int64]bool {
	out := map[int64]bool{}
	if a.d.Presence == nil {
		return out
	}
	states, err := a.d.Presence.List(ctx, clubID)
	if err != nil {
		a.o.Log.WarnContext(ctx, "legacy presence", "err", err)
	}
	for id := range clubmap.PresenceByID(states) {
		out[id] = true
	}
	return out
}

// computers — ПК клуба (zone, number) и брони сейчас (ComputerReservations::heldComputerIds()).
func (a *API) computers(ctx context.Context, q *store.Queries, clubID int64, now time.Time) ([]mapComputer, error) {
	settings, err := q.ClubMapSettings(ctx, clubID)
	if err != nil {
		return nil, err
	}
	gap := int64(defaultGapMinutes)
	switch v := parseClubSetting(settings.Settings).value("club", "booking_interval").(type) {
	case float64:
		gap = max(0, int64(v+0.5))
	case string:
		if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
			gap = max(0, int64(f+0.5))
		}
	}
	held, err := q.ClubMapHeldComputerIDs(ctx, store.ClubMapHeldComputerIDsParams{ClubID: clubID, FromAt: now, ToAt: now.Add(time.Duration(gap) * time.Minute)})
	if err != nil {
		return nil, err
	}
	var out []mapComputer
	var after int64
	for {
		rows, err := q.ClubMapComputers(ctx, store.ClubMapComputersParams{ClubID: clubID, AfterID: after, PageSize: 500})
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			out = append(out, mapComputer{row: r, held: slices.Contains(held, r.ID)})
		}
		if len(rows) < 500 {
			break
		}
		after = rows[len(rows)-1].ID
	}
	// orderBy('club_zone_id')->orderBy('number')
	slices.SortStableFunc(out, func(x, y mapComputer) int {
		if x.row.ClubZoneID != y.row.ClubZoneID {
			return cmpInt(x.row.ClubZoneID, y.row.ClubZoneID)
		}
		return cmpInt(x.row.Number, y.row.Number)
	})
	return out, nil
}

func cmpInt(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// latestAgent — AgentUpdate::latestVersion() (platform_settings agent_release).
func latestAgent(ctx context.Context, q *store.Queries, clubID int64) string {
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

// staffComputers — GET /api/staff/computers: zones, computers, guest_mode, tariffs.
func (a *API) staffComputers(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	clubID := httpx.ClubID(ctx)
	now := a.d.NowFunc()
	type zoneJSON struct {
		ID          int64   `json:"id"`
		Name        string  `json:"name"`
		PricePerHou *string `json:"price_per_hour"`
	}
	type userRef struct {
		ID   int64   `json:"id"`
		Name *string `json:"name"`
	}
	type sessionJSON struct {
		ID     *string  `json:"id"`
		EndsAt *string  `json:"ends_at"`
		Guest  bool     `json:"guest"`
		User   *userRef `json:"user"`
	}
	type visitJSON struct {
		Until *string `json:"until"`
		Name  *string `json:"name"`
	}
	type computerJSON struct {
		ID            int64        `json:"id"`
		Number        int64        `json:"number"`
		Name          string       `json:"name"`
		ZoneID        int64        `json:"zone_id"`
		Status        string       `json:"status"`
		Connection    string       `json:"connection"`
		AgentState    *string      `json:"agent_state"`
		AgentVersion  *string      `json:"agent_version"`
		AgentOutdated bool         `json:"agent_outdated"`
		Reserved      bool         `json:"reserved"`
		BookedNow     bool         `json:"booked_now"`
		Session       *sessionJSON `json:"session"`
		StaffVisit    *visitJSON   `json:"staff_visit"`
	}
	type tariffJSON struct {
		ID      int64   `json:"id"`
		Name    string  `json:"name"`
		Minutes int64   `json:"minutes"`
		Price   string  `json:"price"`
		ZoneIDs []int64 `json:"zone_ids"`
	}
	out := struct {
		Zones     []zoneJSON     `json:"zones"`
		Computers []computerJSON `json:"computers"`
		GuestMode bool           `json:"guest_mode"`
		Tariffs   []tariffJSON   `json:"tariffs"`
	}{Zones: []zoneJSON{}, Computers: []computerJSON{}, Tariffs: []tariffJSON{}}
	pres := map[int64]bool{}
	var states = map[int64]struct{ state, version string }{}
	if a.d.Presence != nil {
		list, err := a.d.Presence.List(ctx, clubID)
		if err != nil {
			a.o.Log.WarnContext(ctx, "legacy presence", "err", err)
		}
		for id, st := range clubmap.PresenceByID(list) {
			pres[id] = true
			states[id] = struct{ state, version string }{st.AgentState, st.AgentVersion}
		}
	}
	err := a.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		zones, err := q.MobileStaffZones(ctx, clubID)
		if err != nil {
			return err
		}
		for _, z := range zones {
			zj := zoneJSON{ID: z.ID, Name: z.Name}
			if z.PricePerHour != nil {
				zj.PricePerHou = ptr(moneyStr(*z.PricePerHour))
			}
			out.Zones = append(out.Zones, zj)
		}
		comps, err := a.computers(ctx, q, clubID, now)
		if err != nil {
			return err
		}
		latest := latestAgent(ctx, q, clubID)
		for _, c := range comps {
			pc, row := c.pc(pres, now), c.row
			cj := computerJSON{ID: row.ID, Number: row.Number, Name: clubmap.DisplayName(row.DisplayName, row.DeviceName, row.Number),
				ZoneID: row.ClubZoneID, Status: pc.CurrentStatus(), Connection: pc.Connection(), AgentState: row.AgentState,
				AgentVersion: row.AgentVersion, Reserved: row.IsReserved, BookedNow: c.held}
			if st, ok := states[row.ID]; ok {
				if st.state != "" {
					cj.AgentState = ptr(st.state)
				}
				if st.version != "" {
					cj.AgentVersion = ptr(st.version)
				}
			}
			cj.AgentOutdated = clubmap.IsOutdated(cj.AgentVersion, latest)
			if pc.HasPaidSession(now) {
				s := &sessionJSON{ID: row.CurrentSessionID, EndsAt: zulu(row.SessionEndsAt), Guest: row.SessionGuest}
				if row.CurrentSessionUserID != nil {
					s.User = &userRef{ID: *row.CurrentSessionUserID, Name: row.SessionUserName}
				}
				cj.Session = s
			}
			if pc.HasStaffVisit(now) {
				cj.StaffVisit = &visitJSON{Until: zulu(row.StaffUntil), Name: row.StaffName}
			}
			out.Computers = append(out.Computers, cj)
		}
		raw, err := q.GetClubSettingsForCash(ctx, clubID)
		if err != nil {
			return err
		}
		out.GuestMode = parseClubSetting(raw).guestMode()
		tariffs, err := q.MobileStaffTariffs(ctx, clubID)
		if err != nil {
			return err
		}
		for _, t := range tariffs {
			// AgentTariffPurchaseService::effectiveMinutes(): с лимитом использования — min(длительность, лимит).
			minutes := t.DurationMinutes
			if t.UsageLimitEnabled && t.UsageLimitMinutes != nil {
				minutes = min(minutes, *t.UsageLimitMinutes)
			}
			ids := t.ZoneIds
			if ids == nil {
				ids = []int64{}
			}
			out.Tariffs = append(out.Tariffs, tariffJSON{ID: t.ID, Name: t.Name, Minutes: minutes, Price: moneyStr(t.Price), ZoneIDs: ids})
		}
		return nil
	})
	if err != nil {
		a.fail(w, r, "staff computers", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// clubmapError — ошибки домена карты (clubmap.Error) в ответ.
func clubmapError(err error) *httpx.APIError {
	var e *clubmap.Error
	if errors.As(err, &e) {
		if e.Rule != "" {
			if e.RuleParam != "" {
				return httpx.RuleErrorWith(e.Field, e.Rule, e.RuleParam)
			}
			return httpx.RuleError(e.Field, e.Rule)
		}
		return httpx.ValidationMsg(e.Field, e.Message, e.Params)
	}
	return nil
}

// errorWith — writeError с мапперами доменных ошибок раздела.
func (a *API) errorWith(w http.ResponseWriter, r *http.Request, err error, extra ...func(error) *httpx.APIError) {
	for _, m := range extra {
		if ae := m(err); ae != nil {
			a.writeError(w, r, ae)
			return
		}
	}
	a.writeError(w, r, err)
}

func (a *API) env(ctx context.Context) clubmap.Env {
	p := httpx.Principal(ctx)
	return clubmap.Env{ClubID: p.ClubID, UserID: p.UserID, Now: a.d.NowFunc(),
		T: func(key string, params map[string]string) string { return a.d.T(ctx, key, params) }}
}

// control — POST /api/staff/computers/control {computer_ids, action}: ClubComputerController::control().
func (a *API) control(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	b := readBody(r)
	ids, ok := b.ids("computer_ids")
	if !ok {
		a.writeError(w, r, httpx.RuleError("computer_ids", "required"))
		return
	}
	if err := clubmap.ValidateIDs(ids); err != nil {
		a.errorWith(w, r, err, clubmapError)
		return
	}
	action, _ := b.str("action")
	if !slices.Contains(clubmap.Actions, action) {
		a.writeError(w, r, httpx.RuleError("action", "in"))
		return
	}
	if action != clubmap.ActionReserve && a.d.AgentCommands == nil {
		a.fail(w, r, "control", httpx.ErrNotConfigured)
		return
	}
	env := a.env(ctx)
	var (
		plan     clubmap.ControlPlan
		latest   string
		statuses []map[string]any
	)
	err := a.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		rows, err := clubmap.Selected(ctx, q, env.ClubID, ids, true)
		if err != nil {
			return err
		}
		latest = latestAgent(ctx, q, env.ClubID)
		if plan, err = clubmap.ControlTx(ctx, q, env, rows, action); err != nil {
			return err
		}
		if action != clubmap.ActionReserve {
			return nil
		}
		// ['id' => …, 'status' => $computer->currentStatus()] после смены флага.
		comps, err := a.computers(ctx, q, env.ClubID, env.Now)
		if err != nil {
			return err
		}
		pres := a.presence(ctx, env.ClubID)
		for _, c := range comps {
			if slices.Contains(ids, c.row.ID) {
				statuses = append(statuses, map[string]any{"id": c.row.ID, "status": c.pc(pres, env.Now).CurrentStatus()})
			}
		}
		return nil
	})
	if err != nil {
		a.errorWith(w, r, err, clubmapError)
		return
	}
	if action == clubmap.ActionReserve {
		if statuses == nil {
			statuses = []map[string]any{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"updated": plan.Updated, "reserved": plan.Reserved, "computers": statuses})
		return
	}
	var pres clubmap.Presence
	if a.d.Presence != nil {
		pres = a.d.Presence
	}
	counts := clubmap.SendControl(ctx, a.d.AgentCommands, pres, env.ClubID, env.UserID, plan, a.o.Log)
	var status any
	if plan.Status != "" {
		status = plan.Status
	}
	out := map[string]any{"updated": len(plan.IDs), "status": status, "message": clubmap.ControlMessage(action, counts, latest, env.T)}
	if action == clubmap.ActionPowerOn {
		out["woken"], out["without_mac"], out["relays"], out["old_relays"] = counts.Woken, counts.WithoutMAC, counts.Relays, counts.OldRelays
	} else {
		out["queued"], out["outdated"] = counts.Queued, counts.Outdated
	}
	writeJSON(w, http.StatusOK, out)
}

// endSession — POST /api/staff/computers/{computer}/end-session {session_id}: ClubComputerController::endSession().
func (a *API) endSession(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	s := chi.URLParam(r, "computer")
	computerID, err := strconv.ParseInt(s, 10, 64)
	if err != nil || computerID <= 0 {
		modelNotFound(w, "ClubComputer", s)
		return
	}
	b := readBody(r)
	sessionID, _ := b.str("session_id")
	switch {
	case !b.filled("session_id"):
		a.writeError(w, r, httpx.RuleError("session_id", "required"))
		return
	case !b.isString("session_id"):
		a.writeError(w, r, httpx.RuleError("session_id", "string"))
		return
	case utf8.RuneCountInString(sessionID) > 255:
		a.writeError(w, r, httpx.RuleErrorWith("session_id", "max_string", "255"))
		return
	}
	if a.d.Billing == nil {
		a.fail(w, r, "end session", httpx.ErrNotConfigured)
		return
	}
	res, err := a.d.Billing.EndSession(ctx, billingsvc.EndSessionRequest{ClubID: httpx.ClubID(ctx), ComputerID: computerID,
		SessionID: sessionID, Actor: httpx.Actor(ctx), IsStaff: true})
	if errors.Is(err, billingsvc.ErrNotFound) {
		modelNotFound(w, "ClubComputer", s)
		return
	}
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Success       bool   `json:"success"`
		Session       any    `json:"session"`
		Refunded      string `json:"refunded"`
		RefundedBonus string `json:"refunded_bonus"`
		KeptMinutes   int64  `json:"kept_minutes"`
	}{true, nil, moneyStr(int64(res.RefundMain)), moneyStr(int64(res.RefundBonus)), res.KeptMinutes})
}

// guestSession — POST /api/staff/computers/guest-session: GuestSessionController::store() (GuestSessions::sell()).
func (a *API) guestSession(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	cs, err := a.settings(ctx)
	if err != nil {
		a.fail(w, r, "settings", err)
		return
	}
	if !cs.guestMode() {
		writeMessage(w, http.StatusForbidden, a.t(r, msgGuestModeOff, nil))
		return
	}
	b := readBody(r)
	ids, ok := b.ids("computer_ids")
	switch {
	case !ok || len(ids) == 0:
		a.writeError(w, r, httpx.RuleError("computer_ids", "required"))
		return
	case len(ids) > 50:
		a.writeError(w, r, httpx.RuleError("computer_ids", cash.RuleMax))
		return
	}
	seen := map[int64]bool{}
	for i, id := range ids {
		if seen[id] {
			a.writeError(w, r, httpx.RuleError("computer_ids."+strconv.Itoa(i), cash.RuleDistinct))
			return
		}
		seen[id] = true
	}
	var tariffID *int64
	var minutes int64
	if b.filled("tariff_id") {
		n, ok := b.integer("tariff_id")
		if !ok {
			a.writeError(w, r, httpx.RuleError("tariff_id", "integer"))
			return
		}
		tariffID = &n
	} else {
		n, ok := b.integer("minutes")
		switch {
		case !b.filled("minutes"):
			a.writeError(w, r, httpx.Validation("tariff_id", "The tariff id field is required when minutes is not present."))
			return
		case !ok:
			a.writeError(w, r, httpx.RuleError("minutes", "integer"))
			return
		case n < 1:
			a.writeError(w, r, httpx.RuleError("minutes", cash.RuleMin))
			return
		case n > 720:
			a.writeError(w, r, httpx.RuleError("minutes", cash.RuleMax))
			return
		}
		minutes = n
	}
	method, _ := b.str("payment_method")
	if !slices.Contains(billingsvc.GuestPaymentMethods, method) {
		a.writeError(w, r, httpx.RuleError("payment_method", cash.RuleIn))
		return
	}
	if a.d.Billing == nil {
		a.fail(w, r, "guest session", httpx.ErrNotConfigured)
		return
	}
	actor := httpx.Actor(ctx)
	res, err := a.d.Billing.SellGuestSession(ctx, billingsvc.GuestSessionRequest{ClubID: httpx.ClubID(ctx), ComputerIDs: ids,
		TariffID: tariffID, Minutes: minutes, PaymentMethod: method, CashierID: actor.UserID,
		IdempotencyKey: idempotencyKey(r), Actor: actor})
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	type pcJSON struct {
		ID     int64  `json:"id"`
		EndsAt string `json:"ends_at"`
	}
	pcs := make([]pcJSON, 0, len(res.Computers))
	for _, c := range res.Computers {
		pcs = append(pcs, pcJSON{ID: c.ComputerID, EndsAt: c.SessionEndsAt.UTC().Format("15:04")})
	}
	writeJSON(w, http.StatusOK, struct {
		Message   string   `json:"message"`
		Amount    string   `json:"amount"`
		Computers []pcJSON `json:"computers"`
	}{a.t(r, msgGuestSold, map[string]string{"count": strconv.Itoa(len(res.Computers)), "amount": moneyComma(int64(res.Amount))}),
		moneyStr(int64(res.Amount)), pcs})
}

// ---------------------------------------------------------------------------
// Касса (Api\Staff\CashController)
// ---------------------------------------------------------------------------

type transactionJSON struct {
	ID          int64   `json:"id"`
	Type        string  `json:"type"`
	Amount      string  `json:"amount"`
	Description string  `json:"description"`
	At          *string `json:"at"`
}

func transactionOf(id int64, typ string, amount int64, desc string, at *time.Time) transactionJSON {
	return transactionJSON{ID: id, Type: typ, Amount: moneyStr(amount), Description: desc, At: zulu(at)}
}

type reportJSON struct {
	Kinds      []map[string]any `json:"kinds"`
	Items      []map[string]any `json:"items"`
	ItemsTotal string           `json:"items_total"`
	Discount   string           `json:"discount"`
	Staff      []map[string]any `json:"staff"`
}

// report — CashController::report(): ShiftReport::for() в формате приложения.
func (a *API) report(ctx context.Context, q *store.Queries, typ cash.ReportType, shiftID int64) (reportJSON, error) {
	p := httpx.Principal(ctx)
	res, err := a.d.Cash.Report(ctx, q, cashsvc.ReportParams{ClubID: p.ClubID, Type: typ, ShiftID: shiftID})
	if err != nil {
		return reportJSON{}, err
	}
	rep := res.Report
	out := reportJSON{Kinds: []map[string]any{}, Items: []map[string]any{}, Staff: []map[string]any{},
		ItemsTotal: moneyStr(int64(rep.ItemsTotal)), Discount: moneyStr(int64(rep.Discount))}
	for _, k := range rep.Kinds {
		out.Kinds = append(out.Kinds, map[string]any{"label": a.d.T(ctx, k.Label, nil), "count": k.Count, "amount": moneyStr(int64(k.Amount))})
	}
	for _, it := range rep.Items {
		out.Items = append(out.Items, map[string]any{"name": it.Name, "quantity": it.Quantity, "amount": moneyStr(int64(it.Amount))})
	}
	for _, s := range rep.Staff {
		out.Staff = append(out.Staff, map[string]any{"name": s.Name, "count": s.Count, "income": moneyStr(int64(s.Income)), "expense": moneyStr(int64(s.Expense))})
	}
	return out, nil
}

func (a *API) cashReady(w http.ResponseWriter, r *http.Request) bool {
	if a.d.Cash == nil {
		a.fail(w, r, "cash", httpx.ErrNotConfigured)
		return false
	}
	return true
}

// cashShow — GET /api/staff/cash: {currency, shift, totals, report, transactions}.
func (a *API) cashShow(w http.ResponseWriter, r *http.Request) {
	if !a.cashReady(w, r) {
		return
	}
	ctx := r.Context()
	p := httpx.Principal(ctx)
	out := map[string]any{"shift": nil, "totals": nil, "report": nil, "transactions": []transactionJSON{}}
	err := a.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		raw, err := q.GetClubSettingsForCash(ctx, p.ClubID)
		if err != nil {
			return err
		}
		out["currency"] = parseClubSetting(raw).currency()
		shift, totals, err := a.d.Cash.CurrentShift(ctx, q, p.ClubID)
		if err != nil || shift == nil {
			return err
		}
		out["shift"], out["totals"] = shiftOf(*shift), totalsOf(totals)
		if out["report"], err = a.report(ctx, q, cash.ReportX, shift.ID); err != nil {
			return err
		}
		rows, err := q.MobileStaffShiftTransactions(ctx, store.MobileStaffShiftTransactionsParams{ClubID: p.ClubID, ShiftID: shift.ID, PageSize: shiftTransactions})
		if err != nil {
			return err
		}
		list := make([]transactionJSON, 0, len(rows))
		for _, t := range rows {
			list = append(list, transactionOf(t.ID, t.Type, t.Amount, t.Description, t.CreatedAt))
		}
		out["transactions"] = list
		return nil
	})
	if err != nil {
		a.fail(w, r, "cash", err)
		return
	}
	writeJSON(w, http.StatusOK, orderedCash(out))
}

// orderedCash — порядок ключей ответа show() (currency, shift, totals, report, transactions).
func orderedCash(m map[string]any) any {
	return struct {
		Currency     any `json:"currency"`
		Shift        any `json:"shift"`
		Totals       any `json:"totals"`
		Report       any `json:"report"`
		Transactions any `json:"transactions"`
	}{m["currency"], m["shift"], m["totals"], m["report"], m["transactions"]}
}

// amountArg — сумма из тела как строка для cash.Parse*: число — его литерал, строка — как есть; нет — "".
func amountArg(b body, k string) string {
	s, _ := b.str(k)
	return s
}

// cashOpen — POST /api/staff/cash/shifts {opening_balance}: 201 {shift}.
func (a *API) cashOpen(w http.ResponseWriter, r *http.Request) {
	if !a.cashReady(w, r) {
		return
	}
	ctx := r.Context()
	balance, err := cash.ParseOpeningBalance(amountArg(readBody(r), "opening_balance"))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	var out shiftJSON
	err = a.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		res, err := a.d.Cash.OpenShift(ctx, q, cashsvc.OpenShiftParams{ClubID: httpx.ClubID(ctx), OpeningBalance: balance, Actor: httpx.Actor(ctx)})
		out = shiftOf(res.Shift)
		return err
	})
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"shift": out})
}

// cashClose — POST /api/staff/cash/shifts/{shift}/close: {shift, totals, report} (Z-отчёт; без отметки печати).
func (a *API) cashClose(w http.ResponseWriter, r *http.Request) {
	if !a.cashReady(w, r) {
		return
	}
	ctx := r.Context()
	s := chi.URLParam(r, "shift")
	shiftID, err := strconv.ParseInt(s, 10, 64)
	if err != nil || shiftID <= 0 {
		modelNotFound(w, "CashShift", s)
		return
	}
	var out struct {
		Shift struct {
			ID             int64   `json:"id"`
			ClosedAt       *string `json:"closed_at"`
			ClosingBalance string  `json:"closing_balance"`
		} `json:"shift"`
		Totals map[string]string `json:"totals"`
		Report reportJSON        `json:"report"`
	}
	err = a.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		res, err := a.d.Cash.CloseShift(ctx, q, cashsvc.CloseShiftParams{ClubID: httpx.ClubID(ctx), ShiftID: shiftID, Actor: httpx.Actor(ctx)})
		if err != nil {
			return err
		}
		out.Shift.ID, out.Shift.ClosedAt = res.Shift.ID, zulu(res.Shift.ClosedAt)
		if res.Shift.ClosingBalance != nil {
			out.Shift.ClosingBalance = moneyStr(*res.Shift.ClosingBalance)
		} else {
			out.Shift.ClosingBalance = moneyStr(int64(res.Totals.Balance()))
		}
		out.Totals = totalsOf(res.Totals)
		out.Report, err = a.report(ctx, q, cash.ReportZ, shiftID)
		return err
	})
	if errors.Is(err, cashsvc.ErrNotFound) {
		modelNotFound(w, "CashShift", s)
		return
	}
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// cashTopUp — POST /api/staff/cash/top-ups {user_id, amount, balance_type, payment_method}:
// {message, bonus, balance, bonus_balance}.
func (a *API) cashTopUp(w http.ResponseWriter, r *http.Request) {
	if !a.cashReady(w, r) {
		return
	}
	ctx := r.Context()
	clubID := httpx.ClubID(ctx)
	b := readBody(r)
	userID, _ := b.integer("user_id")
	balanceType, _ := b.str("balance_type")
	method, _ := b.str("payment_method")
	key := idempotencyKey(r)
	var out struct {
		Message      string `json:"message"`
		Bonus        string `json:"bonus"`
		Balance      string `json:"balance"`
		BonusBalance string `json:"bonus_balance"`
	}
	err := a.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		// StoreBalanceTopUpRequest: все ошибки полей сразу.
		var form httpx.FormErrors
		if !b.filled("user_id") {
			form.Add("user_id", httpx.RuleMessage("user_id", cash.RuleRequired, ""))
		} else {
			names, err := q.ListClubUserNames(ctx, store.ListClubUserNamesParams{ClubID: clubID, Ids: []int64{userID}})
			if err != nil {
				return err
			}
			if len(names) == 0 {
				form.Add("user_id", httpx.RuleMessage("user_id", cash.RuleIn, ""))
			}
		}
		amount, perr := cash.ParseTopUpAmount(amountArg(b, "amount"))
		if perr != nil && !form.AddErr(perr) {
			return perr
		}
		if !cash.BalanceField(balanceType).Valid() {
			form.Add("balance_type", httpx.RuleMessage("balance_type", cash.RuleIn, ""))
		}
		if !cash.PaymentMethod(method).Valid() {
			form.Add("payment_method", httpx.RuleMessage("payment_method", cash.RuleIn, ""))
		}
		if err := form.Err(); err != nil {
			return err
		}
		res, err := a.d.Cash.TopUp(ctx, q, cashsvc.TopUpParams{ClubID: clubID, UserID: userID, Field: cash.BalanceField(balanceType),
			Amount: amount, PaymentMethod: cash.PaymentMethod(method), IdempotencyKey: key, Actor: httpx.Actor(ctx)})
		if err != nil {
			return err
		}
		mb, err := q.GetMemberBalance(ctx, store.GetMemberBalanceParams{ClubID: clubID, UserID: userID})
		if err != nil {
			return err
		}
		out.Message = a.d.T(ctx, msgTopUpDone, nil)
		if res.Bonus > 0 {
			out.Message = a.d.T(ctx, msgTopUpBonusDone, map[string]string{"amount": moneySpaced(int64(res.Bonus))})
		}
		out.Bonus, out.Balance, out.BonusBalance = moneyStr(int64(res.Bonus)), moneyStr(mb.Balance), moneyStr(mb.BonusBalance)
		return nil
	})
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// cashSale — POST /api/staff/cash/sales {items: [{product_id, quantity}]}: 201 {id, amount, items}.
func (a *API) cashSale(w http.ResponseWriter, r *http.Request) {
	if !a.cashReady(w, r) {
		return
	}
	ctx := r.Context()
	b := readBody(r)
	raw, _ := b["items"].([]any)
	items := make([]cash.SaleItem, 0, len(raw))
	for _, it := range raw {
		m, _ := it.(map[string]any)
		row := body(m)
		pid, _ := row.integer("product_id")
		sid, _ := row.integer("service_id")
		qty, _ := row.integer("quantity")
		items = append(items, cash.SaleItem{ProductID: pid, ServiceID: sid, Quantity: qty})
	}
	type lineJSON struct {
		Name     string `json:"name"`
		Quantity int64  `json:"quantity"`
		Price    string `json:"price"`
		Total    string `json:"total"`
	}
	var out struct {
		ID     int64      `json:"id"`
		Amount string     `json:"amount"`
		Items  []lineJSON `json:"items"`
	}
	key := idempotencyKey(r)
	err := a.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		res, err := a.d.Cash.Sale(ctx, q, cashsvc.SaleParams{ClubID: httpx.ClubID(ctx), Items: items, IdempotencyKey: key, Actor: httpx.Actor(ctx)})
		if err != nil {
			return err
		}
		out.ID, out.Amount, out.Items = res.Transaction.ID, moneyStr(res.Transaction.Amount), []lineJSON{}
		for _, l := range res.Sale.Lines {
			out.Items = append(out.Items, lineJSON{Name: l.Name, Quantity: l.Quantity, Price: moneyStr(int64(l.Price)), Total: moneyStr(int64(l.Total))})
		}
		return nil
	})
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

// cashTransaction — POST /api/staff/cash/transactions {type, amount, description}: 201 transaction.
func (a *API) cashTransaction(w http.ResponseWriter, r *http.Request) {
	if !a.cashReady(w, r) {
		return
	}
	ctx := r.Context()
	b := readBody(r)
	s, _ := b.str("type")
	typ := cash.Type(s)
	if typ != cash.Income && typ != cash.Expense {
		a.writeError(w, r, httpx.RuleError("type", cash.RuleIn))
		return
	}
	amount, err := cash.ParseTransactionAmount(amountArg(b, "amount"))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	desc, _ := b.str("description")
	switch {
	case strings.TrimSpace(desc) == "":
		a.writeError(w, r, httpx.RuleError("description", cash.RuleRequired))
		return
	case utf8.RuneCountInString(desc) > cash.MaxDescription:
		a.writeError(w, r, httpx.RuleError("description", cash.RuleMax))
		return
	}
	key := idempotencyKey(r)
	var out transactionJSON
	err = a.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		res, err := a.d.Cash.Transact(ctx, q, cashsvc.TransactParams{ClubID: httpx.ClubID(ctx), Type: typ, Amount: amount,
			Description: desc, IdempotencyKey: key, Actor: httpx.Actor(ctx)})
		t := res.Transaction
		out = transactionOf(t.ID, t.Type, t.Amount, t.Description, t.CreatedAt)
		return err
	})
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

// ---------------------------------------------------------------------------
// Клиенты, товары, склад
// ---------------------------------------------------------------------------

// customers — GET /api/staff/customers?search=&page=: {data, meta}.
func (a *API) customers(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	clubID := httpx.ClubID(ctx)
	search := strings.TrimSpace(r.URL.Query().Get("search"))
	if utf8.RuneCountInString(search) > 50 {
		a.writeError(w, r, httpx.RuleErrorWith("search", "max_string", "50"))
		return
	}
	cur := page(r)
	var nameLike, phoneLike string
	if utf8.RuneCountInString(search) >= 2 {
		nameLike = likePattern(search)
		digits := strings.Map(func(c rune) rune {
			if c >= '0' && c <= '9' {
				return c
			}
			return -1
		}, search)
		if strings.HasPrefix(digits, "8") { // номера хранятся как +7…: набранный с 8 ищется с 7
			digits = "7" + digits[1:]
		}
		if len(digits) >= 3 {
			phoneLike = likePattern(digits)
		}
	}
	type customerJSON struct {
		UserID             int64  `json:"user_id"`
		Name               string `json:"name"`
		Phone              string `json:"phone"`
		Balance            string `json:"balance"`
		BonusBalance       string `json:"bonus_balance"`
		Discount           string `json:"discount"`
		DiscountCumulative bool   `json:"discount_cumulative"`
		TotalSpent         string `json:"total_spent"`
	}
	out := struct {
		Data []customerJSON `json:"data"`
		Meta pageMeta       `json:"meta"`
	}{Data: []customerJSON{}}
	err := a.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		raw, err := q.GetClubSettingsForCash(ctx, clubID)
		if err != nil {
			return err
		}
		bs, err := bonus.ParseSettings(raw)
		if err != nil {
			return err
		}
		levels := bs.LoyaltyLevelsInForce()
		rows, err := q.MobileStaffCustomers(ctx, store.MobileStaffCustomersParams{ClubID: clubID, NameLike: nameLike, PhoneLike: phoneLike,
			PageSize: customersPage, PageOffset: offset(cur, customersPage)})
		if err != nil {
			return err
		}
		var total int64
		for _, c := range rows {
			total = c.Total
			cumulative := bonus.LoyaltyPercentFor(levels, money.Tiyn(c.TotalSpent))
			discount := max(c.Discount, cumulative)
			out.Data = append(out.Data, customerJSON{UserID: c.UserID, Name: c.Name, Phone: c.Phone, Balance: moneyStr(c.Balance),
				BonusBalance: moneyStr(c.BonusBalance), Discount: percentStr(discount), DiscountCumulative: cumulative > c.Discount,
				TotalSpent: moneyStr(c.TotalSpent)})
		}
		out.Meta = metaOf(cur, total, customersPage)
		return nil
	})
	if err != nil {
		a.fail(w, r, "customers", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// percentStr — number_format((float) $x, 2, '.', ”) для процента.
func percentStr(f float64) string { return strconv.FormatFloat(money.PHPRound(f, 2), 'f', 2, 64) }

// products — GET /api/staff/products?search=&page=: {data, meta}.
func (a *API) products(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	clubID := httpx.ClubID(ctx)
	search := strings.TrimSpace(r.URL.Query().Get("search"))
	if utf8.RuneCountInString(search) > 100 {
		a.writeError(w, r, httpx.RuleErrorWith("search", "max_string", "100"))
		return
	}
	like := ""
	if search != "" {
		like = likePattern(search)
	}
	cur := page(r)
	type productJSON struct {
		ID       int64   `json:"id"`
		Sku      string  `json:"sku"`
		Name     string  `json:"name"`
		Category string  `json:"category"`
		Price    string  `json:"price"`
		Quantity int64   `json:"quantity"`
		ImageURL *string `json:"image_url"`
	}
	out := struct {
		Data []productJSON `json:"data"`
		Meta pageMeta      `json:"meta"`
	}{Data: []productJSON{}}
	err := a.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		rows, err := q.MobileStaffProducts(ctx, store.MobileStaffProductsParams{ClubID: clubID, SearchLike: like,
			PageSize: productsPage, PageOffset: offset(cur, productsPage)})
		if err != nil {
			return err
		}
		var total int64
		for _, p := range rows {
			total = p.Total
			// ProductResource::imageUrl(): в схеме нового проекта картинок товаров нет — null (open-questions/stage4-mobile.md).
			out.Data = append(out.Data, productJSON{ID: p.ID, Sku: p.Sku, Name: p.Name, Category: p.Category,
				Price: moneyStr(p.Price), Quantity: p.Quantity})
		}
		out.Meta = metaOf(cur, total, productsPage)
		return nil
	})
	if err != nil {
		a.fail(w, r, "products", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// stockMove — POST /api/staff/stock/receive (только приход, до 100 000) и /stock/movements (receipt|write_off):
// {product_id, quantity} — остаток после движения.
func (a *API) stockMove(receiveOnly bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		b := readBody(r)
		var form httpx.FormErrors
		productID, ok := b.integer("product_id")
		switch {
		case !b.filled("product_id"):
			form.Add("product_id", httpx.RuleMessage("product_id", "required", ""))
		case !ok:
			form.Add("product_id", httpx.RuleMessage("product_id", "integer", ""))
		}
		typ := stock.Receipt
		if !receiveOnly {
			s, _ := b.str("type")
			typ = stock.MovementType(s)
			if typ != stock.Receipt && typ != stock.WriteOff {
				form.Add("type", httpx.RuleMessage("type", "in", ""))
			}
		}
		qty, qok := b.integer("quantity")
		switch {
		case !b.filled("quantity"):
			form.Add("quantity", httpx.RuleMessage("quantity", "required", ""))
		case !qok:
			form.Add("quantity", httpx.RuleMessage("quantity", "integer", ""))
		case qty < 1:
			form.Add("quantity", httpx.RuleMessage("quantity", "min_numeric", "1"))
		case receiveOnly && qty > stock.MaxReceiveQuantity:
			form.Add("quantity", httpx.RuleMessage("quantity", "max_numeric", "100000"))
		}
		note, _ := b.str("note")
		if utf8.RuneCountInString(note) > 255 {
			form.Add("note", httpx.RuleMessage("note", "max_string", "255"))
		}
		if err := form.Err(); err != nil {
			a.writeError(w, r, err)
			return
		}
		maxQty := int64(0)
		if receiveOnly {
			maxQty = stock.MaxReceiveQuantity
		}
		var res stock.AdjustResult
		err := a.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
			var err error
			res, err = stock.AdjustStock(ctx, q, stock.AdjustParams{ClubID: httpx.ClubID(ctx), ProductID: productID,
				UserID: httpx.Principal(ctx).UserID, Type: typ, Quantity: qty, MaxQuantity: maxQty, Note: note, Now: a.d.NowFunc()})
			return err
		})
		if errors.Is(err, stock.ErrProductNotFound) {
			// Rule::exists('products', 'id')->where('club_id', …) у receive; findOrFail у движений.
			a.writeError(w, r, httpx.RuleError("product_id", "exists"))
			return
		}
		if err != nil {
			a.writeError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]int64{"product_id": productID, "quantity": res.Product.Quantity})
	}
}

// ---------------------------------------------------------------------------
// Заказы из агента (Api\Staff\OrderController, App\Services\AgentShop)
// ---------------------------------------------------------------------------

type orderJSON struct {
	ID           int64            `json:"id"`
	Status       string           `json:"status"`
	ComputerName *string          `json:"computer_name"`
	Customer     *customerRef     `json:"customer"`
	Items        []map[string]any `json:"items"`
	Amount       string           `json:"amount"`
	CreatedAt    *string          `json:"created_at"`
	HandledBy    *string          `json:"handled_by"`
	HandledAt    *string          `json:"handled_at"`
}

type customerRef struct {
	ID    int64   `json:"id"`
	Name  *string `json:"name"`
	Phone *string `json:"phone"`
}

func orderOf(r store.MobileStaffOrderRow) orderJSON {
	o := orderJSON{ID: r.ID, Status: r.Status, ComputerName: r.ComputerName, Items: []map[string]any{}, Amount: moneyStr(r.Amount),
		CreatedAt: zulu(r.CreatedAt), HandledBy: r.HandlerName, HandledAt: zulu(r.HandledAt)}
	if r.CustomerName != nil {
		o.Customer = &customerRef{ID: r.UserID, Name: r.CustomerName, Phone: r.CustomerPhone}
	}
	var items []any
	_ = json.Unmarshal(r.Items, &items)
	for _, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		name := ""
		if v, ok := m["name"]; ok && v != nil {
			name = scalar(v)
		}
		o.Items = append(o.Items, map[string]any{"name": name, "quantity": int64Of(m["quantity"]),
			"price": itemMoney(m["price"]), "total": itemMoney(m["total"])})
	}
	return o
}

// itemMoney — self::money($item['price'] ?? 0): строка тенге или число → "350.00".
func itemMoney(v any) string {
	t, ok := tiynOf(scalar(v))
	if v == nil || !ok {
		return "0.00"
	}
	return moneyStr(int64(t))
}

// orders — GET /api/staff/orders?status=new|done|cancelled|all&page=: {data, meta, new_count}.
func (a *API) orders(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	clubID := httpx.ClubID(ctx)
	status := r.URL.Query().Get("status")
	switch status {
	case "":
		status = "new"
	case "new", "done", "cancelled", "all": //nolint:misspell // статус заказа старого кода
	default:
		a.writeError(w, r, httpx.RuleError("status", "in"))
		return
	}
	filter := status
	if filter == "all" {
		filter = ""
	}
	cur := page(r)
	out := struct {
		Data     []orderJSON `json:"data"`
		Meta     pageMeta    `json:"meta"`
		NewCount int64       `json:"new_count"`
	}{Data: []orderJSON{}}
	err := a.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		rows, err := q.MobileStaffOrders(ctx, store.MobileStaffOrdersParams{ClubID: clubID, Status: filter, PageSize: ordersPage,
			PageOffset: offset(cur, ordersPage)})
		if err != nil {
			return err
		}
		var total int64
		for _, o := range rows {
			total = o.Total
			out.Data = append(out.Data, orderOf(store.MobileStaffOrderRow{ID: o.ID, UserID: o.UserID, ComputerName: o.ComputerName,
				Items: o.Items, Amount: o.Amount, Status: o.Status, HandledAt: o.HandledAt, CreatedAt: o.CreatedAt,
				CustomerName: o.CustomerName, CustomerPhone: o.CustomerPhone, HandlerName: o.HandlerName}))
		}
		out.Meta = metaOf(cur, total, ordersPage)
		out.NewCount, err = q.MobileStaffNewOrders(ctx, clubID)
		return err
	})
	if err != nil {
		a.fail(w, r, "orders", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleOrder — POST /api/staff/orders/{order}/done|cancel: AgentShop::handOver()/cancel() (отмена — возврат
// денег через ledger и товара на склад); ответ — заказ.
func (a *API) handleOrder(fn func(context.Context, *store.Queries, shop.HandleParams) (shop.Handled, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		s := chi.URLParam(r, "order")
		id, err := strconv.ParseInt(s, 10, 64)
		if err != nil || id <= 0 {
			modelNotFound(w, "ShopOrder", s)
			return
		}
		var row store.MobileStaffOrderRow
		err = a.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
			clubID := httpx.ClubID(ctx)
			if _, err := fn(ctx, q, shop.HandleParams{ClubID: clubID, OrderID: id, Actor: httpx.Actor(ctx), Now: a.d.NowFunc()}); err != nil {
				return err
			}
			row, err = q.MobileStaffOrder(ctx, store.MobileStaffOrderParams{ClubID: clubID, ID: id})
			return err
		})
		var se *shop.Error
		switch {
		case errors.Is(err, shop.ErrOrderNotFound) || errors.Is(err, pgx.ErrNoRows):
			modelNotFound(w, "ShopOrder", s)
		case errors.As(err, &se):
			writeValidation(w, se.Field, a.t(r, se.Message, nil))
		case err != nil:
			a.writeError(w, r, err)
		default:
			writeJSON(w, http.StatusOK, orderOf(row))
		}
	}
}

// ---------------------------------------------------------------------------
// Чат (ChatController::conversationsJson/poll/reply)
// ---------------------------------------------------------------------------

type chatMessageJSON struct {
	ID        int64   `json:"id"`
	FromStaff bool    `json:"from_staff"`
	Author    *string `json:"author"`
	Computer  *string `json:"computer"`
	Body      string  `json:"body"`
	At        *string `json:"at"`
}

// present — ChatController::present(): at — created_at->format('d.m H:i') (запросы API — в UTC).
func present(r store.ListChatThreadRow) chatMessageJSON {
	m := chatMessageJSON{ID: r.ID, FromStaff: r.FromStaff, Body: r.Body}
	if r.FromStaff {
		m.Author = r.AuthorName
	}
	if r.ComputerNumber != nil {
		m.Computer = ptr(club.ComputerName(r.ComputerDisplayName, r.ComputerDeviceName, *r.ComputerNumber))
	}
	if r.CreatedAt != nil {
		m.At = ptr(r.CreatedAt.UTC().Format("02.01 15:04"))
	}
	return m
}

// chats — GET /api/staff/chats: {conversations, unread}.
func (a *API) chats(w http.ResponseWriter, r *http.Request) {
	type lastJSON struct {
		ID        int64   `json:"id"`
		Body      string  `json:"body"`
		FromStaff bool    `json:"from_staff"`
		At        *string `json:"at"`
	}
	type convJSON struct {
		Customer *customerRef `json:"customer"`
		Unread   int64        `json:"unread"`
		Last     *lastJSON    `json:"last"`
	}
	out := struct {
		Conversations []convJSON `json:"conversations"`
		Unread        int64      `json:"unread"`
	}{Conversations: []convJSON{}}
	err := a.d.Tx(r.Context(), func(ctx context.Context, q *store.Queries) error {
		clubID := httpx.ClubID(ctx)
		rows, err := q.ListChatConversations(ctx, clubID)
		if err != nil {
			return err
		}
		for _, c := range rows {
			cv := convJSON{Unread: c.Unread, Last: &lastJSON{ID: c.LastID, Body: c.LastBody, FromStaff: c.LastFromStaff, At: zulu(c.LastAt)}}
			if c.CustomerName != nil {
				cv.Customer = &customerRef{ID: c.CustomerID, Name: c.CustomerName, Phone: c.CustomerPhone}
			}
			out.Conversations = append(out.Conversations, cv)
		}
		out.Unread, err = q.ChatUnreadTotal(ctx, clubID)
		return err
	})
	if err != nil {
		a.fail(w, r, "chats", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// answers — право chat.create (ChatController::answers()).
func answers(ctx context.Context) bool { return httpx.Principal(ctx).Can("chat.create") }

// chatPoll — GET /api/staff/chat/poll?customer=&after=: {messages, unread}.
func (a *API) chatPoll(w http.ResponseWriter, r *http.Request) {
	qv := r.URL.Query()
	var customer, after int64
	for k, dst := range map[string]*int64{"customer": &customer, "after": &after} {
		if s := qv.Get(k); s != "" {
			n, err := strconv.ParseInt(s, 10, 64)
			if err != nil {
				a.writeError(w, r, httpx.RuleError(k, "integer"))
				return
			}
			*dst = n
		}
	}
	out := struct {
		Messages []chatMessageJSON `json:"messages"`
		Unread   int64             `json:"unread"`
	}{Messages: []chatMessageJSON{}}
	err := a.d.Tx(r.Context(), func(ctx context.Context, q *store.Queries) error {
		clubID := httpx.ClubID(ctx)
		if customer != 0 {
			if answers(ctx) {
				if err := chatsvc.MarkRead(ctx, q, clubID, customer, a.d.NowFunc()); err != nil {
					return err
				}
			}
			rows, err := q.ListChatThread(ctx, store.ListChatThreadParams{ClubID: clubID, CustomerID: customer, AfterID: after})
			if err != nil {
				return err
			}
			slices.Reverse(rows)
			for _, m := range rows {
				out.Messages = append(out.Messages, present(m))
			}
		}
		var err error
		out.Unread, err = q.ChatUnreadTotal(ctx, clubID)
		return err
	})
	if err != nil {
		a.fail(w, r, "chat poll", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// chatReply — POST /api/staff/chat/{customer} {body}: 201 {message}; ответ уходит на ПК гостя после коммита.
func (a *API) chatReply(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if !answers(ctx) {
		writeMessage(w, http.StatusForbidden, a.t(r, msgNoAnswerRight, nil))
		return
	}
	s := chi.URLParam(r, "customer")
	customer, err := strconv.ParseInt(s, 10, 64)
	if err != nil || customer <= 0 {
		modelNotFound(w, "User", s)
		return
	}
	raw, _ := readBody(r).str("body")
	p := httpx.Principal(ctx)
	now := a.d.NowFunc()
	var out chatMessageJSON
	notFound := false
	err = a.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		if _, err := q.ChatCustomer(ctx, store.ChatCustomerParams{ClubID: p.ClubID, CustomerID: customer}); errors.Is(err, pgx.ErrNoRows) {
			notFound = true
			return nil
		} else if err != nil {
			return err
		}
		text, rule := chatsvc.ValidBody(raw)
		switch rule {
		case "required":
			return httpx.RuleError("body", "required")
		case "max":
			return httpx.Validation("body", httpx.RuleMessage("body", "max_string", "1000"))
		}
		id, err := chatsvc.Reply(ctx, q, p.ClubID, customer, p.UserID, text, now)
		if err != nil {
			return err
		}
		rows, err := q.ListChatThread(ctx, store.ListChatThreadParams{ClubID: p.ClubID, CustomerID: customer, AfterID: id - 1})
		if err != nil {
			return err
		}
		for _, m := range rows {
			if m.ID == id {
				out = present(m)
			}
		}
		if a.d.ChatToGuest != nil {
			author := ""
			if out.Author != nil {
				author = *out.Author
			}
			clubID := p.ClubID
			billingsvc.AfterCommit(ctx, func(ctx context.Context) { a.d.ChatToGuest(ctx, clubID, customer, id, author, text, now) })
		}
		return nil
	})
	if notFound {
		writeMessage(w, http.StatusNotFound, "")
		return
	}
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"message": out})
}
