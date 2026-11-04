package reservations

// Брони ПК. Laravel: app/Http/Controllers/ComputerReservationController.php index(), store(), storeMany(),
// customers(), cancel(), arrive(); app/Http/Requests/StoreComputerReservationRequest.php.

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/aitmaganbetov/zafirogo/server/internal/club"
	reservationsapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/reservations"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
	booking "github.com/aitmaganbetov/zafirogo/server/internal/reservations"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// Тексты контроллера (ключи перевода).
const (
	msgCreated   = "ПК забронирован для клиента :name: в это время он сможет купить тариф на этом ПК."
	msgMany      = "Забронировано ПК: :count, для :name на :from – :to."
	msgCancelled = "Бронь отменена."
	msgArrived   = "Отмечено: клиент пришёл. ПК свободен для его сессии."
	// perPage — paginate(50).
	perPage = 50
	// localLayout — date_format:Y-m-d\TH:i (datetime-local).
	localLayout = "2006-01-02T15:04"
)

var nonDigits = regexp.MustCompile(`\D+`)

// ListReservations — index(): брони локального дня клуба, ПК для формы, интервал, начало по умолчанию.
func (h *handlers) ListReservations(ctx context.Context, req reservationsapi.ListReservationsRequestObject) (reservationsapi.ListReservationsResponseObject, error) {
	p := httpx.Principal(ctx)
	now := h.d.NowFunc()
	page := 1
	if req.Params.Page != nil && *req.Params.Page > 1 {
		page = *req.Params.Page
	}
	out := reservationsapi.ReservationsPage{Items: []reservationsapi.Reservation{}, Computers: []reservationsapi.ReservationComputer{},
		Page: page, PerPage: perPage}
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		info, err := club.Load(ctx, q, p.ClubID)
		if err != nil {
			return err
		}
		day := info.StartOfDay(now)
		if req.Params.Date != nil && *req.Params.Date != "" {
			d, ok := info.ParseDay(*req.Params.Date)
			if !ok {
				return httpx.RuleErrorWith("date", "date_format", "Y-m-d")
			}
			day = d
		}
		next := day.AddDate(0, 0, 1)
		out.Date = day.Format("2006-01-02")
		out.GapMinutes = info.BookingGapMinutes()
		// Carbon::now($tz)->addHour()->startOfHour().
		start := now.In(info.Loc).Add(time.Hour).Truncate(time.Minute)
		start = start.Add(-time.Duration(start.Minute()) * time.Minute)
		out.DefaultStart = start.Format(localLayout)
		if out.Total, err = q.CountDayReservations(ctx, store.CountDayReservationsParams{ClubID: p.ClubID, FromAt: day, ToAt: next}); err != nil {
			return err
		}
		rows, err := q.ListDayReservations(ctx, store.ListDayReservationsParams{
			ClubID: p.ClubID, FromAt: day, ToAt: next, PageSize: perPage, PageOffset: int64(page-1) * perPage,
		})
		if err != nil {
			return err
		}
		for _, r := range rows {
			item := reservationsapi.Reservation{
				Id: r.ID, ComputerId: r.ClubComputerID, ZoneName: r.ZoneName, Note: r.Note,
				StartsAt: r.StartsAt, EndsAt: r.EndsAt,
				State:         reservationsapi.ReservationState(booking.State(r.StartsAt, r.EndsAt, r.ArrivedAt, r.CancelledAt, now)),
				CustomerLabel: booking.CustomerLabel(r.UserName, r.UserPhone, r.CustomerName, r.CustomerPhone),
			}
			if r.Note != nil && *r.Note == "" {
				item.Note = nil
			}
			if r.ComputerNumber != nil {
				name := club.ComputerName(r.ComputerDisplayName, r.ComputerDeviceName, *r.ComputerNumber)
				item.ComputerName = &name
			}
			out.Items = append(out.Items, item)
		}
		pcs, err := q.ListReservationComputers(ctx, p.ClubID)
		if err != nil {
			return err
		}
		for _, c := range pcs {
			out.Computers = append(out.Computers, reservationsapi.ReservationComputer{
				Id: c.ID, Number: c.Number, Name: club.ComputerName(c.DisplayName, c.DeviceName, c.Number), ZoneName: c.ZoneName,
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return reservationsapi.ListReservations200JSONResponse(out), nil
}

// form — проверенные поля брони.
type form struct {
	startsAt time.Time
	minutes  int64
	note     *string
	customer store.ReservationClubCustomerRow
}

// validateCommon — user_id, starts_at, duration_minutes, note (StoreComputerReservationRequest по порядку),
// затем «не в прошлом» и клиент.
func validateCommon(ctx context.Context, q *store.Queries, info club.Info, fe *httpx.FormErrors, userID *int64,
	startsAt string, duration *int64, note *string, now time.Time) (form, error) {
	var f form
	var customer *store.ReservationClubCustomerRow
	if userID == nil {
		fe.Add("user_id", booking.MsgPickClient)
	} else {
		row, err := q.ReservationClubCustomer(ctx, store.ReservationClubCustomerParams{ClubID: info.ID, UserID: *userID})
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			fe.Add("user_id", booking.MsgPickClient)
		case err != nil:
			return f, err
		default:
			customer = &row
		}
	}
	if startsAt == "" {
		fe.Add("starts_at", httpx.RuleMessage("starts_at", "required", ""))
	} else if t, err := time.ParseInLocation(localLayout, startsAt, info.Loc); err != nil {
		fe.Add("starts_at", httpx.RuleMessage("starts_at", "date_format", ""))
	} else {
		f.startsAt = t
	}
	switch {
	case duration == nil:
		fe.Add("duration_minutes", httpx.RuleMessage("duration_minutes", "required", ""))
	case *duration < 15:
		fe.Add("duration_minutes", httpx.RuleMessage("duration_minutes", "min", "15"))
	case *duration > 1440:
		fe.Add("duration_minutes", httpx.RuleMessage("duration_minutes", "max", "1440"))
	default:
		f.minutes = *duration
	}
	if note != nil && utf8.RuneCountInString(*note) > 500 {
		fe.Add("note", httpx.RuleMessage("note", "max_string", "500"))
	}
	if note != nil && strings.TrimSpace(*note) != "" {
		n := strings.TrimSpace(*note) // TrimStrings + ConvertEmptyStringsToNull
		f.note = &n
	}
	if err := fe.Err(); err != nil {
		return f, err
	}
	// $startsAt->lt(now()->subMinutes(5)).
	if f.startsAt.Before(now.Add(-5 * time.Minute)) {
		return f, httpx.ValidationMsg("starts_at", booking.MsgPast, nil)
	}
	f.customer = *customer
	return f, nil
}

// CreateReservation — store(): одна бронь из формы экрана «Брони».
func (h *handlers) CreateReservation(ctx context.Context, req reservationsapi.CreateReservationRequestObject) (reservationsapi.CreateReservationResponseObject, error) {
	if req.Body == nil {
		return nil, httpx.RuleError("club_computer_id", "required")
	}
	in := *req.Body
	p := httpx.Principal(ctx)
	now := h.d.NowFunc()
	var out reservationsapi.ReservationCreated
	var name string
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		info, err := club.Load(ctx, q, p.ClubID)
		if err != nil {
			return err
		}
		var fe httpx.FormErrors
		if in.ClubComputerId == nil {
			fe.Add("club_computer_id", httpx.RuleMessage("club_computer_id", "required", ""))
		} else if n, err := q.ReservationComputersExist(ctx, store.ReservationComputersExistParams{ClubID: p.ClubID, Ids: []int64{*in.ClubComputerId}}); err != nil {
			return err
		} else if n == 0 {
			fe.Add("club_computer_id", httpx.RuleMessage("club_computer_id", "exists", ""))
		}
		f, err := validateCommon(ctx, q, info, &fe, in.UserId, in.StartsAt, in.DurationMinutes, in.Note, now)
		if err != nil {
			return err
		}
		if _, err := booking.Create(ctx, q, info, p.UserID, booking.Booking{
			ComputerID: *in.ClubComputerId, UserID: f.customer.ID, CustomerName: f.customer.Name, CustomerPhone: &f.customer.Phone,
			Note: f.note, StartsAt: f.startsAt, EndsAt: f.startsAt.Add(time.Duration(f.minutes) * time.Minute),
		}, now); err != nil {
			return err
		}
		name = f.customer.Name
		out.Date = f.startsAt.Format("2006-01-02")
		return nil
	})
	if err != nil {
		return nil, err
	}
	out.Message = h.d.T(ctx, msgCreated, map[string]string{"name": name})
	return reservationsapi.CreateReservation201JSONResponse(out), nil
}

// CreateReservationsMany — storeMany(): выбранные ПК для одного клиента и периода — все или ни одного.
func (h *handlers) CreateReservationsMany(ctx context.Context, req reservationsapi.CreateReservationsManyRequestObject) (reservationsapi.CreateReservationsManyResponseObject, error) {
	if req.Body == nil {
		return nil, httpx.RuleError("computer_ids", "required")
	}
	in := *req.Body
	p := httpx.Principal(ctx)
	now := h.d.NowFunc()
	var params map[string]string
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		info, err := club.Load(ctx, q, p.ClubID)
		if err != nil {
			return err
		}
		var fe httpx.FormErrors
		switch {
		case len(in.ComputerIds) == 0:
			fe.Add("computer_ids", httpx.RuleMessage("computer_ids", "required", ""))
		case len(in.ComputerIds) > 50:
			fe.Add("computer_ids", httpx.RuleMessage("computer_ids", "max", "50"))
		default:
			seen := map[int64]int{}
			for _, id := range in.ComputerIds {
				seen[id]++
			}
			for i, id := range in.ComputerIds {
				field := "computer_ids." + strconv.Itoa(i)
				if seen[id] > 1 {
					fe.Add(field, httpx.RuleMessage(field, "distinct", ""))
				}
				n, err := q.ReservationComputersExist(ctx, store.ReservationComputersExistParams{ClubID: p.ClubID, Ids: []int64{id}})
				if err != nil {
					return err
				}
				if n == 0 {
					fe.Add(field, httpx.RuleMessage(field, "exists", ""))
				}
			}
		}
		f, err := validateCommon(ctx, q, info, &fe, in.UserId, in.StartsAt, in.DurationMinutes, in.Note, now)
		if err != nil {
			return err
		}
		end := f.startsAt.Add(time.Duration(f.minutes) * time.Minute)
		for _, id := range in.ComputerIds {
			if _, err := booking.Create(ctx, q, info, p.UserID, booking.Booking{
				ComputerID: id, UserID: f.customer.ID, CustomerName: f.customer.Name, CustomerPhone: &f.customer.Phone,
				Note: f.note, StartsAt: f.startsAt, EndsAt: end,
			}, now); err != nil {
				return err
			}
		}
		params = map[string]string{
			"count": strconv.Itoa(len(in.ComputerIds)), "name": f.customer.Name,
			"from": f.startsAt.Format("02.01 15:04"), "to": end.In(info.Loc).Format("15:04"),
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return reservationsapi.CreateReservationsMany201JSONResponse{Message: h.d.T(ctx, msgMany, params)}, nil
}

// SearchReservationCustomers — customers(): пользователи клуба по имени или телефону, не больше 10.
func (h *handlers) SearchReservationCustomers(ctx context.Context, req reservationsapi.SearchReservationCustomersRequestObject) (reservationsapi.SearchReservationCustomersResponseObject, error) {
	out := reservationsapi.ReservationCustomers{Data: []reservationsapi.ReservationCustomer{}}
	query := ""
	if req.Params.Q != nil {
		if utf8.RuneCountInString(*req.Params.Q) > 50 {
			return nil, httpx.Validation("q", httpx.RuleMessage("q", "max_string", "50"))
		}
		query = strings.TrimSpace(*req.Params.Q)
	}
	if utf8.RuneCountInString(query) < 2 {
		return reservationsapi.SearchReservationCustomers200JSONResponse(out), nil
	}
	// Телефоны хранятся как +7XXXXXXXXXX: номер, набранный с 8, ищется с 7.
	digits := nonDigits.ReplaceAllString(query, "")
	if strings.HasPrefix(digits, "8") {
		digits = "7" + digits[1:]
	}
	phoneLike := ""
	if len(digits) >= 3 {
		phoneLike = httpx.LikePattern(&digits)
	}
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		rows, err := q.SearchReservationCustomers(ctx, store.SearchReservationCustomersParams{
			ClubID: httpx.ClubID(ctx), NameLike: httpx.LikePattern(&query), PhoneLike: phoneLike,
		})
		for _, r := range rows {
			out.Data = append(out.Data, reservationsapi.ReservationCustomer{Id: r.ID, Name: r.Name, Phone: &r.Phone})
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return reservationsapi.SearchReservationCustomers200JSONResponse(out), nil
}

// CancelReservation — cancel().
func (h *handlers) CancelReservation(ctx context.Context, req reservationsapi.CancelReservationRequestObject) (reservationsapi.CancelReservationResponseObject, error) {
	if err := h.close(ctx, req.ReservationId, false); err != nil {
		return nil, err
	}
	return reservationsapi.CancelReservation200JSONResponse{Message: h.d.T(ctx, msgCancelled, nil)}, nil
}

// ArriveReservation — arrive().
func (h *handlers) ArriveReservation(ctx context.Context, req reservationsapi.ArriveReservationRequestObject) (reservationsapi.ArriveReservationResponseObject, error) {
	if err := h.close(ctx, req.ReservationId, true); err != nil {
		return nil, err
	}
	return reservationsapi.ArriveReservation200JSONResponse{Message: h.d.T(ctx, msgArrived, nil)}, nil
}

func (h *handlers) close(ctx context.Context, id int64, arrive bool) error {
	p := httpx.Principal(ctx)
	now := h.d.NowFunc()
	return h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		info, err := club.Load(ctx, q, p.ClubID)
		if err != nil {
			return err
		}
		if arrive {
			return booking.Arrive(ctx, q, info, p.UserID, id, now)
		}
		return booking.Cancel(ctx, q, info, p.UserID, id, now)
	})
}
