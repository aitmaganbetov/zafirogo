package legacy

// Брони ПК в гостевом приложении (mobileclient, «Настройки клуба → Бронь из приложения»): список своих броней,
// ПК и тарифы для брони, свободные ПК на время, бронь с оплатой с баланса, отмена с возвратом.
// Laravel: app/Http/Controllers/Api/GuestBookingController.php (index(), computers(), tariffs(), options(), store(),
// cancel()); сервис — server/internal/reservations (GuestBookings, ComputerReservations). Времена приходят и уходят
// в ISO 8601 (приложение показывает их в поясе телефона); деньги — строки «900.00», как отдавал старый API.
// Клубы, ещё живущие на Laravel, обслуживает он: токен другого сервера уходит туда (authenticate → forward).

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
	"github.com/aitmaganbetov/zafirogo/server/internal/reservations"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

type bookingJSON struct {
	ID        int64    `json:"id"`
	Code      *string  `json:"code"`
	Computer  string   `json:"computer"`
	Computers []string `json:"computers"`
	Zone      *string  `json:"zone"`
	Tariff    *string  `json:"tariff"`
	StartsAt  string   `json:"starts_at"`
	EndsAt    string   `json:"ends_at"`
	State     string   `json:"state"`
	Paid      string   `json:"paid"`
	PaidBonus string   `json:"paid_bonus"`
	CanCancel bool     `json:"can_cancel"`
}

// bookingOf — GuestBookings::present(): времена — $at->utc()->toIso8601String(), суммы — formatCents().
func bookingOf(b reservations.AppBooking) bookingJSON {
	return bookingJSON{
		ID: b.ID, Code: b.Code, Computer: strings.Join(b.Computers, ", "), Computers: b.Computers, Zone: b.Zone, Tariff: b.Tariff,
		StartsAt: b.StartsAt.UTC().Format("2006-01-02T15:04:05-07:00"), EndsAt: b.EndsAt.UTC().Format("2006-01-02T15:04:05-07:00"),
		State: b.State, Paid: moneyStr(int64(b.Paid)), PaidBonus: moneyStr(int64(b.PaidBonus)), CanCancel: b.CanCancel,
	}
}

// bookingEnv — окружение операции гостя: журнал и ledger — от его имени; агенту — баланс после коммита.
func (a *API) bookingEnv(r *http.Request) reservations.Env {
	env := reservations.Env{Now: a.d.NowFunc(), Actor: guestActor(r), T: func(key string, params map[string]string) string {
		return a.t(r, key, params)
	}}
	if a.d.Cash != nil && a.d.Cash.Notifier != nil {
		env.Notifier = a.d.Cash.Notifier
	}
	return env
}

// bookingError — ошибки сервиса броней в формате Laravel: выключено в клубе — 403, правило — 422 с полем.
// model — модель для 404 (ModelNotFoundException), id — её ключ ("" — без ключа).
func (a *API) bookingError(w http.ResponseWriter, r *http.Request, err error, model, id string) {
	var be *reservations.Error
	switch {
	case errors.Is(err, reservations.ErrAppDisabled):
		writeMessage(w, http.StatusForbidden, a.t(r, reservations.MsgAppDisabled, nil))
	case errors.As(err, &be):
		writeValidation(w, be.Field, a.t(r, be.Message, be.Params))
	case errors.Is(err, reservations.ErrNotFound):
		if id == "" {
			writeMessage(w, http.StatusNotFound, "No query results for model [App\\Models\\"+model+"].")
			return
		}
		modelNotFound(w, model, id)
	default:
		a.writeError(w, r, err)
	}
}

// bookingsIndex — GET /api/guest/bookings: брони гостя и что разрешает клуб. От подписки клуба не зависит.
func (a *API) bookingsIndex(w http.ResponseWriter, r *http.Request) {
	p := httpx.Principal(r.Context())
	var idx reservations.AppIndex
	if err := a.d.Tx(r.Context(), func(ctx context.Context, q *store.Queries) error {
		var err error
		idx, err = reservations.ListForGuest(ctx, q, p.ClubID, p.UserID, a.d.NowFunc())
		return err
	}); err != nil {
		a.fail(w, r, "bookings", err)
		return
	}
	bookings := make([]bookingJSON, 0, len(idx.Bookings))
	for _, b := range idx.Bookings {
		bookings = append(bookings, bookingOf(b))
	}
	writeJSON(w, http.StatusOK, struct {
		Enabled      bool          `json:"enabled"`
		CanCancel    bool          `json:"can_cancel"`
		GapMinutes   int64         `json:"gap_minutes"`
		Currency     string        `json:"currency"`
		MinMinutes   int           `json:"min_minutes"`
		MaxMinutes   int           `json:"max_minutes"`
		MaxDaysAhead int           `json:"max_days_ahead"`
		Bookings     []bookingJSON `json:"bookings"`
	}{idx.Enabled, idx.CanCancel, idx.GapMinutes, parseClubSetting(idx.Settings).currency(),
		reservations.MinMinutes, reservations.MaxMinutes, reservations.MaxDaysAhead, bookings})
}

type bookingComputerJSON struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Available *bool  `json:"available,omitempty"`
}

type bookingZoneJSON struct {
	ID           int64                 `json:"id"`
	Name         string                `json:"name"`
	PricePerHour *string               `json:"price_per_hour"`
	Price        *string               `json:"price,omitempty"`
	Computers    []bookingComputerJSON `json:"computers"`
}

func bookingZones(zones []reservations.AppZone, options bool) []bookingZoneJSON {
	out := make([]bookingZoneJSON, 0, len(zones))
	for _, z := range zones {
		j := bookingZoneJSON{ID: z.ID, Name: z.Name, Computers: make([]bookingComputerJSON, 0, len(z.Computers))}
		if z.PricePerHour != nil {
			j.PricePerHour = ptr(moneyStr(int64(*z.PricePerHour)))
		}
		if options {
			j.Price = ptr(moneyStr(int64(z.Price)))
		}
		for _, c := range z.Computers {
			pc := bookingComputerJSON{ID: c.ID, Name: c.Name}
			if options {
				pc.Available = ptr(c.Available)
			}
			j.Computers = append(j.Computers, pc)
		}
		out = append(out, j)
	}
	return out
}

// bookingsComputers — GET /api/guest/bookings/computers: ПК клуба по зонам — гость сначала выбирает ПК.
func (a *API) bookingsComputers(w http.ResponseWriter, r *http.Request) {
	p := httpx.Principal(r.Context())
	var zones []reservations.AppZone
	if err := a.d.Tx(r.Context(), func(ctx context.Context, q *store.Queries) error {
		var err error
		zones, err = reservations.AppComputers(ctx, q, p.ClubID, p.UserID)
		return err
	}); err != nil {
		a.bookingError(w, r, err, "ClubMembership", "")
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Zones []bookingZoneJSON `json:"zones"`
	}{bookingZones(zones, false)})
}

// bookingEnabled — ensureEnabled() до проверки запроса (контроллер вызывает его первым): false — ответ записан.
func (a *API) bookingEnabled(w http.ResponseWriter, r *http.Request) bool {
	cs, err := a.settings(r.Context())
	if err != nil {
		a.fail(w, r, "settings", err)
		return false
	}
	if !cs.flag("club", "app_booking_enabled", false) {
		writeMessage(w, http.StatusForbidden, a.t(r, reservations.MsgAppDisabled, nil))
		return false
	}
	return true
}

// dateLayouts — что присылает приложение (DateTime.toUtc().toIso8601String()) и что ещё понимает правило 'date'
// старого кода из разумного: ISO 8601 с поясом и без (без пояса — UTC, как у запросов api/*).
var dateLayouts = []string{
	time.RFC3339Nano, "2006-01-02T15:04:05.999999999", "2006-01-02T15:04", "2006-01-02 15:04:05.999999999", "2006-01-02 15:04",
	"2006-01-02T15:04:05.999999999Z0700", "2006-01-02T15:04Z07:00", "2006-01-02",
}

// msgRequiredWithout — текст правила required_without в старом проекте: перевода «validation.required_without» в
// его каталоге нет, и Laravel отдаёт сам ключ (так отвечает прод; снято со стенда — testdata/bookings.laravel.json).
const msgRequiredWithout = "validation.required_without"

// ruleErrors — ошибки правил запроса в порядке правил ($request->validate([...])): Laravel отдаёт errors в этом
// порядке, а message — первая ошибка (+ «(and N more errors)»).
type ruleErrors struct {
	order []string
	fe    httpx.FormErrors
}

func (e *ruleErrors) add(field, msg string) {
	if !e.fe.Has(field) {
		e.order = append(e.order, field)
	}
	e.fe.Add(field, msg)
}

// orderedErrors — errors ответа 422 с порядком полей, как в Laravel (encoding/json сортирует ключи map).
type orderedErrors struct {
	order []string
	byKey map[string][]string
}

// MarshalJSON пишет поля в порядке order.
func (o orderedErrors) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, k := range o.order {
		if i > 0 {
			buf.WriteByte(',')
		}
		key, err := json.Marshal(k)
		if err != nil {
			return nil, err
		}
		val := &bytes.Buffer{}
		enc := json.NewEncoder(val)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(o.byKey[k]); err != nil {
			return nil, err
		}
		buf.Write(key)
		buf.WriteByte(':')
		buf.Write(bytes.TrimSpace(val.Bytes()))
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// writeRuleErrors — ответ 422, если ошибки есть (true). Тексты правил переводит общий слой (язык клуба), как у
// остальных маршрутов; порядок полей — порядок правил.
func (a *API) writeRuleErrors(w http.ResponseWriter, r *http.Request, e *ruleErrors) bool {
	err := e.fe.Err()
	if err == nil {
		return false
	}
	rec := &bufferWriter{h: http.Header{}}
	a.d.ResponseError()(rec, r, err)
	var body struct {
		Errors map[string][]string `json:"errors"`
	}
	_ = json.Unmarshal(rec.buf.Bytes(), &body)
	if rec.status != http.StatusUnprocessableEntity || len(body.Errors) == 0 || len(e.order) == 0 {
		a.writeError(w, r, err)
		return true
	}
	first := ""
	if list := body.Errors[e.order[0]]; len(list) > 0 {
		first = list[0]
	}
	writeJSON(w, http.StatusUnprocessableEntity, struct {
		Message string        `json:"message"`
		Errors  orderedErrors `json:"errors"`
	}{summarize(first, body.Errors), orderedErrors{order: e.order, byKey: body.Errors}})
	return true
}

// bookingStart — start(): starts_at required|date; Carbon::parse(...)->utc()->startOfMinute(). false — ответ записан.
func (a *API) bookingStart(w http.ResponseWriter, r *http.Request, b body) (time.Time, bool) {
	if !b.filled("starts_at") {
		a.writeError(w, r, httpx.RuleError("starts_at", "required"))
		return time.Time{}, false
	}
	s, _ := b.str("starts_at")
	s = strings.TrimSpace(s)
	if b.isString("starts_at") {
		for _, layout := range dateLayouts {
			if t, err := time.ParseInLocation(layout, s, time.UTC); err == nil {
				return t.UTC().Truncate(time.Minute), true
			}
		}
	}
	a.writeError(w, r, httpx.RuleError("starts_at", "date"))
	return time.Time{}, false
}

// bookingPeriod — period(): tariff_id (nullable|integer) и duration_minutes (required_without:tariff_id, integer,
// min:30, max:720), затем starts_at. false — ответ записан.
func (a *API) bookingPeriod(w http.ResponseWriter, r *http.Request, b body) (startsAt time.Time, minutes, tariffID *int64, ok bool) {
	var fe ruleErrors
	if b.filled("tariff_id") {
		if n, isInt := b.integer("tariff_id"); isInt {
			tariffID = &n
		} else {
			fe.add("tariff_id", httpx.RuleMessage("tariff_id", "integer", ""))
		}
	}
	switch {
	case !b.filled("duration_minutes"):
		if !b.filled("tariff_id") {
			fe.add("duration_minutes", msgRequiredWithout)
		}
	default:
		n, isInt := b.integer("duration_minutes")
		switch {
		case !isInt:
			fe.add("duration_minutes", httpx.RuleMessage("duration_minutes", "integer", ""))
		case n < reservations.MinMinutes:
			fe.add("duration_minutes", httpx.RuleMessage("duration_minutes", "min", strconv.Itoa(reservations.MinMinutes)))
		case n > reservations.MaxMinutes:
			fe.add("duration_minutes", httpx.RuleMessage("duration_minutes", "max", strconv.Itoa(reservations.MaxMinutes)))
		default:
			minutes = &n
		}
	}
	if a.writeRuleErrors(w, r, &fe) {
		return time.Time{}, nil, nil, false
	}
	// $tariffId === null ? (int) $data['duration_minutes'] : null.
	if tariffID != nil {
		minutes = nil
	}
	startsAt, ok = a.bookingStart(w, r, b)
	return startsAt, minutes, tariffID, ok
}

// queryIDs — массив из строки запроса: computer_ids[]=1&computer_ids[]=2 или computer_ids[0]=1. present — параметр
// есть; ok = false — элемент не целое.
func queryIDs(r *http.Request, name string) (ids []int64, present, ok bool) {
	q := r.URL.Query()
	for key, vals := range q {
		if key != name+"[]" && (!strings.HasPrefix(key, name+"[") || !strings.HasSuffix(key, "]")) {
			continue
		}
		present = true
		for _, v := range vals {
			n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
			if err != nil {
				return nil, true, false
			}
			ids = append(ids, n)
		}
	}
	return ids, present, true
}

// bookingsTariffs — GET /api/guest/bookings/tariffs?starts_at=&computer_id=&computer_ids[]=: тарифы, которые гость
// может забронировать на это начало (действующие в зонах выбранных ПК).
func (a *API) bookingsTariffs(w http.ResponseWriter, r *http.Request) {
	if !a.bookingEnabled(w, r) {
		return
	}
	b := readBody(r)
	startsAt, ok := a.bookingStart(w, r, b)
	if !ok {
		return
	}
	// computer_id: nullable|integer; computer_ids: nullable|array|max:10; computer_ids.*: integer.
	ids, present, valid := queryIDs(r, "computer_ids")
	if !present {
		if arr, isArr := b["computer_ids"]; isArr && arr != nil {
			present = true
			ids, valid = b.ids("computer_ids")
			if _, isList := arr.([]any); !isList {
				a.writeError(w, r, httpx.RuleError("computer_ids", "array"))
				return
			}
		}
	}
	switch {
	case !valid:
		a.writeError(w, r, httpx.RuleError("computer_ids.0", "integer"))
		return
	case len(ids) > reservations.MaxComputers:
		a.writeError(w, r, httpx.RuleErrorWith("computer_ids", "max_array", strconv.Itoa(reservations.MaxComputers)))
		return
	}
	if !present && b.filled("computer_id") {
		n, isInt := b.integer("computer_id")
		if !isInt {
			a.writeError(w, r, httpx.RuleError("computer_id", "integer"))
			return
		}
		if n != 0 { // array_filter([$data['computer_id'] ?? null])
			ids = []int64{n}
		}
	}
	p := httpx.Principal(r.Context())
	var tariffs []reservations.AppTariff
	if err := a.d.Tx(r.Context(), func(ctx context.Context, q *store.Queries) error {
		var err error
		tariffs, err = reservations.AppTariffs(ctx, q, p.ClubID, p.UserID, startsAt, ids, a.d.NowFunc())
		return err
	}); err != nil {
		a.bookingError(w, r, err, "ClubMembership", "")
		return
	}
	type tariffJSON struct {
		ID          int64   `json:"id"`
		Name        string  `json:"name"`
		Description *string `json:"description"`
		Minutes     int64   `json:"minutes"`
		Price       string  `json:"price"`
		ZoneIDs     []int64 `json:"zone_ids"`
	}
	out := make([]tariffJSON, 0, len(tariffs))
	for _, t := range tariffs {
		out = append(out, tariffJSON{t.ID, t.Name, t.Description, t.Minutes, moneyStr(int64(t.Price)), t.ZoneIDs})
	}
	writeJSON(w, http.StatusOK, struct {
		Tariffs []tariffJSON `json:"tariffs"`
	}{out})
}

// bookingsOptions — GET /api/guest/bookings/options?starts_at=&duration_minutes=|tariff_id=: зоны и ПК, свободные на
// это время (по часам или тарифом), с ценой.
func (a *API) bookingsOptions(w http.ResponseWriter, r *http.Request) {
	if !a.bookingEnabled(w, r) {
		return
	}
	startsAt, minutes, tariffID, ok := a.bookingPeriod(w, r, readBody(r))
	if !ok {
		return
	}
	p := httpx.Principal(r.Context())
	var zones []reservations.AppZone
	var planned int64
	if err := a.d.Tx(r.Context(), func(ctx context.Context, q *store.Queries) error {
		var err error
		planned, zones, err = reservations.AppOptions(ctx, q, p.ClubID, p.UserID, startsAt, minutes, tariffID, a.d.NowFunc())
		return err
	}); err != nil {
		a.bookingError(w, r, err, "ClubMembership", "")
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Minutes int64             `json:"minutes"`
		Zones   []bookingZoneJSON `json:"zones"`
	}{planned, bookingZones(zones, true)})
}

// bookingComputerIDs — правила store(): club_computer_ids (required_without:club_computer_id, array, min:1, max:10;
// элементы — integer) или club_computer_id (required_without:club_computer_ids, integer) — старые версии приложения.
func (a *API) bookingComputerIDs(w http.ResponseWriter, r *http.Request, b body) ([]int64, bool) {
	list, hasList := b["club_computer_ids"]
	arr, isArr := list.([]any)
	listFilled := hasList && list != nil && (!isArr || len(arr) > 0)
	if s, isStr := list.(string); isStr && strings.TrimSpace(s) == "" {
		listFilled = false
	}
	oneFilled := b.filled("club_computer_id")
	var fe ruleErrors
	var ids []int64
	switch {
	case !listFilled && !oneFilled:
		fe.add("club_computer_ids", msgRequiredWithout)
	case listFilled && !isArr:
		fe.add("club_computer_ids", httpx.RuleMessage("club_computer_ids", "array", ""))
	case listFilled && len(arr) > reservations.MaxComputers:
		fe.add("club_computer_ids", httpx.RuleMessage("club_computer_ids", "max_array", strconv.Itoa(reservations.MaxComputers)))
	case listFilled:
		for i, v := range arr {
			n, isInt := (body{"v": v}).integer("v")
			if !isInt {
				field := "club_computer_ids." + strconv.Itoa(i)
				fe.add(field, httpx.RuleMessage(field, "integer", ""))
				continue
			}
			ids = append(ids, n)
		}
	}
	switch {
	case !oneFilled && !listFilled:
		fe.add("club_computer_id", msgRequiredWithout)
	case oneFilled:
		n, isInt := b.integer("club_computer_id")
		if !isInt {
			fe.add("club_computer_id", httpx.RuleMessage("club_computer_id", "integer", ""))
		} else if !listFilled {
			ids = []int64{n} // $data['club_computer_ids'] ?? [$data['club_computer_id']]
		}
	}
	if a.writeRuleErrors(w, r, &fe) {
		return nil, false
	}
	return ids, true
}

// bookingsStore — POST /api/guest/bookings: бронь одного ПК (club_computer_id) или нескольких вместе
// (club_computer_ids) с оплатой с балансов; 201 {message, booking}. Повтор запроса с тем же Idempotency-Key
// (приложение его не шлёт; если пришлёт) возвращает первую бронь и не списывает второй раз.
func (a *API) bookingsStore(w http.ResponseWriter, r *http.Request) {
	if !a.bookingEnabled(w, r) {
		return
	}
	b := readBody(r)
	ids, ok := a.bookingComputerIDs(w, r, b)
	if !ok {
		return
	}
	startsAt, minutes, tariffID, ok := a.bookingPeriod(w, r, b)
	if !ok {
		return
	}
	p := httpx.Principal(r.Context())
	req := reservations.BookRequest{ClubID: p.ClubID, UserID: p.UserID, ComputerIDs: ids, StartsAt: startsAt, Minutes: minutes, TariffID: tariffID}
	if k := strings.TrimSpace(r.Header.Get("Idempotency-Key")); k != "" && len(k) <= 100 {
		req.IdempotencyKey = "mobile:booking:" + strconv.FormatInt(p.UserID, 10) + ":" + k
	}
	var booking reservations.AppBooking
	var code string
	if err := a.d.Tx(r.Context(), func(ctx context.Context, q *store.Queries) error {
		res, err := reservations.Book(ctx, q, req, a.bookingEnv(r))
		if err != nil {
			return err
		}
		code = res.Code
		booking, err = reservations.Group(ctx, q, p.ClubID, p.UserID, res.ReservationIDs[0], a.d.NowFunc())
		return err
	}); err != nil {
		a.bookingError(w, r, err, "MemberBalance", "")
		return
	}
	msg := a.t(r, reservations.MsgBookedOne, nil)
	if len(ids) > 1 {
		msg = a.t(r, reservations.MsgBookedMany, map[string]string{"code": code})
	}
	writeJSON(w, http.StatusCreated, struct {
		Message string      `json:"message"`
		Booking bookingJSON `json:"booking"`
	}{msg, bookingOf(booking)})
}

// bookingsCancel — POST /api/guest/bookings/{reservation}/cancel: гость отменяет свою бронь (все её ПК) до начала —
// деньги возвращаются. Чужая бронь — 404. От подписки клуба не зависит.
func (a *API) bookingsCancel(w http.ResponseWriter, r *http.Request) {
	raw := chi.URLParam(r, "reservation")
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		modelNotFound(w, "ComputerReservation", raw)
		return
	}
	p := httpx.Principal(r.Context())
	if err := a.d.Tx(r.Context(), func(ctx context.Context, q *store.Queries) error {
		return reservations.CancelByGuest(ctx, q, p.ClubID, p.UserID, id, a.bookingEnv(r))
	}); err != nil {
		a.bookingError(w, r, err, "ComputerReservation", raw)
		return
	}
	writeMessage(w, http.StatusOK, a.t(r, reservations.MsgGuestCancelled, nil))
}
