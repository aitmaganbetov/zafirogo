package clubmap

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/aitmaganbetov/zafirogo/server/internal/activity"
	"github.com/aitmaganbetov/zafirogo/server/internal/outbox"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// Error — ошибка правила карты: Status 422 (поле формы) / 409 (пересечение) / 404. Message — ключ перевода
// (русский текст старого кода), Params — подстановки; Rule — правило валидатора Laravel (текст даёт HTTP-слой).
type Error struct {
	Status  int
	Field   string
	Message string
	Params  map[string]string
	Rule    string
	// RuleParam — значение :min/:max правила.
	RuleParam string
}

func (e *Error) Error() string {
	if e.Rule != "" {
		return "clubmap: " + e.Field + ": " + e.Rule
	}
	return "clubmap: " + e.Field + ": " + e.Message
}

// ErrNotFound — зона или ПК не найдены в клубе (404).
var ErrNotFound = errors.New("clubmap: not found")

func invalid(field, msg string, params map[string]string) *Error {
	return &Error{Status: http.StatusUnprocessableEntity, Field: field, Message: msg, Params: params}
}

func rule(field, r, param string) *Error {
	return &Error{Status: http.StatusUnprocessableEntity, Field: field, Rule: r, RuleParam: param}
}

func notFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// Тексты старого кода (ключи перевода).
const (
	MsgZoneAdded          = "Зона добавлена."                                                    // store()
	MsgZoneUpdated        = "Зона обновлена."                                                    // update()
	MsgZoneDeleted        = "Зона удалена."                                                      // destroy()
	MsgZonesOverlap       = "Карточки залов не могут пересекаться."                              // updateLayout() 409
	MsgZoneHasSessions    = "На ПК зоны идёт сессия: удалить зону можно после её окончания."     // Q-018 (новый)
	MsgZoneCreated        = "Создана зона: :zone"                                                // журнал zone.created
	MsgZoneChanged        = "Изменена зона: :zone"                                               // журнал zone.updated
	MsgZoneRemoved        = "Удалена зона: :zone"                                                // журнал zone.deleted
	MsgTariffsDisabled    = "Тарифы без зон выключены: :names"                                   // Q-018 (новый)
	MsgZoneCapacity       = "Достигнута вместимость зоны."                                       // ClubComputerController
	MsgComputerAdded      = "Компьютер добавлен в зону."                                         // store()
	MsgComputerUpdated    = "Данные компьютера обновлены."                                       // update()
	MsgComputerDeleted    = "ПК :computer удалён."                                               // destroy()
	MsgComputerBusy       = "На ПК :computer идёт сессия: удалить его можно после её окончания." // destroy()
	MsgZoneNoRoom         = "В выбранной зоне нет свободного места."                             // update()
	MsgCellTaken          = "Эта ячейка уже занята другим компьютером."                          // __invoke() 409
	MsgNumberTaken        = "Номер уже используется в этой зоне."                                // UpdateClubComputerRequest
	MsgNameTaken          = "Это имя уже используется в клубе."                                  // UpdateClubComputerRequest
	MsgMacFormat          = "Введите MAC-адрес в формате AA:BB:CC:DD:EE:FF."                     // UpdateClubComputerRequest
	MsgMacTaken           = "Этот MAC-адрес уже привязан к другому ПК клуба."                    // UpdateClubComputerRequest
	MsgComputerCreatedLog = "Добавлен ПК :computer в зону :zone"                                 // журнал computer.created
	MsgComputerChangedLog = "Изменён компьютер: :computer"                                       // журнал computer.updated
	MsgComputerRemovedLog = "Удалён ПК :computer"                                                // журнал computer.deleted
)

// SubjectZone / SubjectComputer — морф-классы предметов журнала (как в старом activity_logs.subject_type).
const (
	SubjectZone     = `App\Models\ClubZone`
	SubjectComputer = `App\Models\ClubComputer`
)

// Значения правил StoreClubZoneRequest.
var (
	ZoneIcons    = []string{"crown", "desktop", "gamepad", "mug-hot", "couch", "dice", "door-open", "bong", "smoking"}
	ZoneThemes   = []string{"green", "neutral", "yellow", "purple", "blue"}
	ZoneStatuses = []string{"available", "busy", "service"}
)

// ZoneInput — поля формы зоны (StoreClubZoneRequest). Цена часа — тиыны (1…100 000 000, «decimal:0,2»
// проверяет разбор суммы на фронте и общий парсер; Q-002).
type ZoneInput struct {
	Name         string
	Description  *string
	Capacity     *int64
	PricePerHour *int64
	Icon         string
	Theme        string
	Status       string
	IsFeatured   bool
	SortOrder    int64
	TariffIDs    []int64
}

// Translator — перевод текстов журнала на язык клуба (httpx.Deps.T).
type Translator func(key string, params map[string]string) string

// Env — общее для операций: клуб, кто, часы, перевод текстов журнала.
type Env struct {
	ClubID int64
	UserID int64
	Now    time.Time
	T      Translator
}

func (e Env) t(key string, params map[string]string) string {
	if e.T != nil {
		return e.T(key, params)
	}
	return substitute(key, params)
}

func substitute(s string, params map[string]string) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b string) int { return len(b) - len(a) })
	for _, k := range keys {
		s = strings.ReplaceAll(s, ":"+k, params[k])
	}
	return s
}

// validateZone — правила StoreClubZoneRequest по порядку полей (первая ошибка).
func validateZone(ctx context.Context, q *store.Queries, clubID int64, in *ZoneInput) error {
	in.Name = strings.TrimSpace(in.Name)
	switch {
	case in.Name == "":
		return rule("name", "required", "")
	case utf8.RuneCountInString(in.Name) > 100:
		return rule("name", "max_string", "100")
	}
	if in.Description != nil {
		d := strings.TrimSpace(*in.Description)
		if d == "" {
			in.Description = nil // ConvertEmptyStringsToNull
		} else {
			if utf8.RuneCountInString(d) > 180 {
				return rule("description", "max_string", "180")
			}
			in.Description = &d
		}
	}
	if in.Capacity != nil {
		switch {
		case *in.Capacity < 0:
			return rule("capacity", "min", "0")
		case *in.Capacity > 10000:
			return rule("capacity", "max", "10000")
		}
	}
	if in.PricePerHour != nil {
		switch {
		case *in.PricePerHour < 1:
			return rule("price_per_hour", "min", "0.01")
		case *in.PricePerHour > 100_000_000:
			return rule("price_per_hour", "max", "1000000")
		}
	}
	switch {
	case !slices.Contains(ZoneIcons, in.Icon):
		return rule("icon", "in", "")
	case !slices.Contains(ZoneThemes, in.Theme):
		return rule("theme", "in", "")
	case !slices.Contains(ZoneStatuses, in.Status):
		return rule("status", "in", "")
	case in.SortOrder < 0:
		return rule("sort_order", "min", "0")
	case in.SortOrder > 10000:
		return rule("sort_order", "max", "10000")
	case len(in.TariffIDs) > 100:
		return rule("tariff_ids", "max", "100")
	}
	seen := map[int64]bool{}
	for i, id := range in.TariffIDs {
		if seen[id] {
			return rule("tariff_ids."+strconv.Itoa(i), "distinct", "")
		}
		seen[id] = true
	}
	if len(in.TariffIDs) > 0 {
		n, err := q.ClubMapCountTariffs(ctx, store.ClubMapCountTariffsParams{ClubID: clubID, Ids: in.TariffIDs})
		if err != nil {
			return err
		}
		if n != int64(len(in.TariffIDs)) {
			return rule("tariff_ids.0", "exists", "")
		}
	}
	return nil
}

func dash(v any) string {
	switch x := v.(type) {
	case nil:
		return "—"
	case *string:
		if x == nil {
			return "—"
		}
		return *x
	case *int64:
		if x == nil {
			return "—"
		}
		return strconv.FormatInt(*x, 10)
	case string:
		return x
	case int64:
		return strconv.FormatInt(x, 10)
	case bool:
		if x {
			return "1"
		}
		return "0"
	}
	return "—"
}

// tenge — сумма decimal(…,2) старой колонки: тиыны → «1000.00» (журнал «было → стало»).
func tenge(t *int64) any {
	if t == nil {
		return nil
	}
	v := *t
	sign := ""
	if v < 0 {
		sign, v = "-", -v
	}
	frac := strconv.FormatInt(v%100, 10)
	if len(frac) < 2 {
		frac = "0" + frac
	}
	return sign + strconv.FormatInt(v/100, 10) + "." + frac
}

func idsList(ids []int64) string {
	s := make([]string, len(ids))
	for i, id := range ids {
		s[i] = strconv.FormatInt(id, 10)
	}
	return strings.Join(s, ", ")
}

// CreateZone — ClubZoneController::store(): зона, тарифы зоны, журнал zone.created.
func CreateZone(ctx context.Context, q *store.Queries, env Env, in ZoneInput) (int64, error) {
	if err := validateZone(ctx, q, env.ClubID, &in); err != nil {
		return 0, err
	}
	n, err := q.ClubMapCountZones(ctx, env.ClubID)
	if err != nil {
		return 0, err
	}
	offset := n * 30
	now := env.Now
	id, err := q.ClubMapInsertZone(ctx, store.ClubMapInsertZoneParams{
		ClubID: env.ClubID, Name: in.Name, Description: in.Description, Capacity: in.Capacity,
		PricePerHour: in.PricePerHour, Icon: in.Icon, Theme: in.Theme, Status: in.Status, IsFeatured: in.IsFeatured,
		SortOrder: in.SortOrder, PositionX: 20 + offset%240, PositionY: 20 + offset%180, Now: &now,
	})
	if err != nil {
		return 0, err
	}
	if len(in.TariffIDs) > 0 {
		if err := q.ClubMapAttachZoneTariffs(ctx, store.ClubMapAttachZoneTariffsParams{ZoneID: id, ClubID: env.ClubID, TariffIds: in.TariffIDs}); err != nil {
			return 0, err
		}
	}
	var capacity any = "—"
	if in.Capacity != nil {
		capacity = *in.Capacity
	}
	tariffs := "—"
	if len(in.TariffIDs) > 0 {
		tariffs = idsList(in.TariffIDs)
	}
	_, err = activity.Record(ctx, q, activity.Entry{
		ClubID: env.ClubID, Event: "zone.created", Description: env.t(MsgZoneCreated, map[string]string{"zone": in.Name}),
		UserID: env.UserID, SubjectType: SubjectZone, SubjectID: id, At: now,
		Details: map[string]any{"capacity": capacity, "icon": in.Icon, "tariff_ids": tariffs},
	})
	return id, err
}

// UpdateZone — ClubZoneController::update(): поля, синхронизация тарифов, журнал zone.updated (изменённые поля
// «было → стало» и tariff_ids, если состав изменился).
func UpdateZone(ctx context.Context, q *store.Queries, env Env, zoneID int64, in ZoneInput) error {
	cur, err := q.ClubMapLockZone(ctx, store.ClubMapLockZoneParams{ClubID: env.ClubID, ID: zoneID})
	if err != nil {
		return notFound(err)
	}
	if err := validateZone(ctx, q, env.ClubID, &in); err != nil {
		return err
	}
	now := env.Now
	if err := q.ClubMapUpdateZone(ctx, store.ClubMapUpdateZoneParams{
		Name: in.Name, Description: in.Description, Capacity: in.Capacity, PricePerHour: in.PricePerHour,
		Icon: in.Icon, Theme: in.Theme, Status: in.Status, IsFeatured: in.IsFeatured, SortOrder: in.SortOrder,
		Now: &now, ClubID: env.ClubID, ID: zoneID,
	}); err != nil {
		return err
	}
	before, err := q.ClubMapZoneTariffIDs(ctx, store.ClubMapZoneTariffIDsParams{ClubID: env.ClubID, ZoneID: zoneID})
	if err != nil {
		return err
	}
	keep := in.TariffIDs
	if keep == nil {
		keep = []int64{}
	}
	if err := q.ClubMapDetachZoneTariffs(ctx, store.ClubMapDetachZoneTariffsParams{ClubID: env.ClubID, ZoneID: zoneID, KeepIds: keep}); err != nil {
		return err
	}
	if len(keep) > 0 {
		if err := q.ClubMapAttachZoneTariffs(ctx, store.ClubMapAttachZoneTariffsParams{ZoneID: zoneID, ClubID: env.ClubID, TariffIds: keep}); err != nil {
			return err
		}
	}
	changes := map[string]any{}
	diff := func(field string, was, now any) {
		if dash(was) != dash(now) {
			changes[field] = dash(was) + " → " + dash(now)
		}
	}
	diff("name", cur.Name, in.Name)
	diff("description", cur.Description, in.Description)
	diff("capacity", cur.Capacity, in.Capacity)
	diff("price_per_hour", tenge(cur.PricePerHour), tenge(in.PricePerHour))
	diff("icon", cur.Icon, in.Icon)
	diff("theme", cur.Theme, in.Theme)
	diff("status", cur.Status, in.Status)
	diff("is_featured", cur.IsFeatured, in.IsFeatured)
	diff("sort_order", cur.SortOrder, in.SortOrder)
	sortedBefore, sortedAfter := slices.Clone(before), slices.Clone(keep)
	slices.Sort(sortedBefore)
	slices.Sort(sortedAfter)
	if !slices.Equal(slices.Compact(sortedBefore), slices.Compact(sortedAfter)) {
		changes["tariff_ids"] = "—"
		if len(keep) > 0 {
			changes["tariff_ids"] = idsList(keep)
		}
	}
	if len(changes) == 0 {
		return nil
	}
	_, err = activity.Record(ctx, q, activity.Entry{
		ClubID: env.ClubID, Event: "zone.updated", Description: env.t(MsgZoneChanged, map[string]string{"zone": in.Name}),
		UserID: env.UserID, SubjectType: SubjectZone, SubjectID: zoneID, At: now, Details: changes,
	})
	return err
}

// DeleteZone — ClubZoneController::destroy() с решением Q-018: при идущей сессии на ПК зоны — 422; тарифы
// зоны, у которых не останется других зон, выключаются (иначе тариф «только этой зоны» стал бы продаваться
// во всех зонах). ПК зоны удаляются каскадом (их брони — тоже, сертификаты отзывает триггер). Возвращает
// имена выключенных тарифов.
func DeleteZone(ctx context.Context, q *store.Queries, env Env, zoneID int64) ([]string, error) {
	zone, err := q.ClubMapLockZone(ctx, store.ClubMapLockZoneParams{ClubID: env.ClubID, ID: zoneID})
	if err != nil {
		return nil, notFound(err)
	}
	now := env.Now
	running, err := q.ClubMapZoneRunningSessions(ctx, store.ClubMapZoneRunningSessionsParams{ClubID: env.ClubID, ZoneID: zoneID, Now: &now})
	if err != nil {
		return nil, err
	}
	if running > 0 {
		return nil, invalid("zone", MsgZoneHasSessions, nil)
	}
	count, err := q.ClubMapZoneComputerCount(ctx, store.ClubMapZoneComputerCountParams{ClubID: env.ClubID, ZoneID: zoneID})
	if err != nil {
		return nil, err
	}
	disabled, err := q.ClubMapDisableOrphanTariffs(ctx, store.ClubMapDisableOrphanTariffsParams{Now: &now, ClubID: env.ClubID, ZoneID: zoneID})
	if err != nil {
		return nil, err
	}
	if err := q.ClubMapDeleteZone(ctx, store.ClubMapDeleteZoneParams{ClubID: env.ClubID, ID: zoneID}); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(disabled))
	ids := make([]int64, 0, len(disabled))
	for _, t := range disabled {
		names = append(names, t.Name)
		ids = append(ids, t.ID)
	}
	details := map[string]any{"computers_removed": count}
	if len(ids) > 0 {
		details["tariffs_disabled"] = idsList(ids)
	}
	if _, err := activity.Record(ctx, q, activity.Entry{
		ClubID: env.ClubID, Event: "zone.deleted", Description: env.t(MsgZoneRemoved, map[string]string{"zone": zone.Name}),
		UserID: env.UserID, SubjectType: SubjectZone, SubjectID: zoneID, At: now, Details: details,
	}); err != nil {
		return nil, err
	}
	if count > 0 {
		// Карта и список ПК других вкладок перечитываются.
		if _, err := outbox.Add(ctx, q, outbox.Event{ClubID: env.ClubID, Type: "computer.state",
			Data: map[string]any{"zone_id": zoneID, "removed": true}}); err != nil {
			return nil, err
		}
	}
	return names, nil
}

// ZoneLayout — UpdateClubZoneLayoutRequest.
type ZoneLayout struct {
	X, Y, Width, Height int64
	Rotation            *int64
}

// UpdateZoneLayout — ClubZoneController::updateLayout(): правила размеров (диван — меньше), пересечение
// с другой зоной (зазор 20) — 409.
func UpdateZoneLayout(ctx context.Context, q *store.Queries, env Env, zoneID int64, l ZoneLayout) error {
	zone, err := q.ClubMapLockZone(ctx, store.ClubMapLockZoneParams{ClubID: env.ClubID, ID: zoneID})
	if err != nil {
		return notFound(err)
	}
	minW, minH := int64(180), int64(130)
	if zone.Icon == "couch" {
		minW, minH = 80, 60
	}
	switch {
	case l.X < 0:
		return rule("position_x", "min", "0")
	case l.X > 5000:
		return rule("position_x", "max", "5000")
	case l.Y < 0:
		return rule("position_y", "min", "0")
	case l.Y > 5000:
		return rule("position_y", "max", "5000")
	case l.Width < minW:
		return rule("width", "min", strconv.FormatInt(minW, 10))
	case l.Width > 1600:
		return rule("width", "max", "1600")
	case l.Height < minH:
		return rule("height", "min", strconv.FormatInt(minH, 10))
	case l.Height > 1200:
		return rule("height", "max", "1200")
	case l.Rotation != nil && *l.Rotation != 0 && *l.Rotation != 90:
		return rule("rotation", "in", "")
	}
	others, err := q.ClubMapOtherZoneRects(ctx, store.ClubMapOtherZoneRectsParams{ClubID: env.ClubID, ID: zoneID})
	if err != nil {
		return err
	}
	const gap = 20
	for _, z := range others {
		if l.X < z.PositionX+z.Width+gap && z.PositionX < l.X+l.Width+gap &&
			l.Y < z.PositionY+z.Height+gap && z.PositionY < l.Y+l.Height+gap {
			return &Error{Status: http.StatusConflict, Message: MsgZonesOverlap}
		}
	}
	var rot *int16
	if l.Rotation != nil {
		r := int16(0)
		if *l.Rotation == 90 { // 0 или 90 (проверено выше)
			r = 90
		}
		rot = &r
	}
	now := env.Now
	return q.ClubMapUpdateZoneLayout(ctx, store.ClubMapUpdateZoneLayoutParams{
		PositionX: l.X, PositionY: l.Y, Width: l.Width, Height: l.Height, Rotation: rot, Now: &now,
		ClubID: env.ClubID, ID: zoneID,
	})
}
