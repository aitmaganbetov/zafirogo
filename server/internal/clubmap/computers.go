package clubmap

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/aitmaganbetov/zafirogo/server/internal/activity"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/enroll"
	"github.com/aitmaganbetov/zafirogo/server/internal/outbox"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// Сетка ПК в зоне (ClubZoneComputerService::add(), ClubComputerController::update(), JS карты): шаг 47×42,
// отступ 5, 6 ПК в ряд при добавлении.
const (
	GridX     = 47
	GridY     = 42
	GridStart = 5
)

// AddComputer — ClubZoneComputerService::add(): ПК в зону desktop (иначе 404); вместимость достигнута — 0
// без ошибки; номер = max + 1, device_name PC-NN, первая свободная ячейка 6 в ряд. Журнал computer.created.
func AddComputer(ctx context.Context, q *store.Queries, env Env, zoneID int64) (int64, error) {
	zone, err := q.ClubMapLockZone(ctx, store.ClubMapLockZoneParams{ClubID: env.ClubID, ID: zoneID})
	if err != nil {
		return 0, notFound(err)
	}
	if zone.Icon != "desktop" {
		return 0, ErrNotFound
	}
	pcs, err := q.ClubMapZoneComputersForUpdate(ctx, store.ClubMapZoneComputersForUpdateParams{ClubID: env.ClubID, ZoneID: zoneID})
	if err != nil {
		return 0, err
	}
	if zone.Capacity != nil && int64(len(pcs)) >= *zone.Capacity {
		return 0, nil
	}
	var number int64
	occupied := map[[2]int64]bool{}
	for _, pc := range pcs {
		number = max(number, pc.Number)
		occupied[[2]int64{pc.PositionX, pc.PositionY}] = true
	}
	number++
	var x, y int64
	for i := int64(0); ; i++ {
		x, y = GridStart+(i%6)*GridX, GridStart+(i/6)*GridY
		if !occupied[[2]int64{x, y}] {
			break
		}
	}
	name := fmt.Sprintf("PC-%02d", number)
	now := env.Now
	id, err := q.ClubMapInsertComputer(ctx, store.ClubMapInsertComputerParams{
		ClubID: env.ClubID, ZoneID: zoneID, Number: number, DeviceName: &name, PositionX: x, PositionY: y, Now: &now,
	})
	if err != nil {
		return 0, err
	}
	if _, err := activity.Record(ctx, q, activity.Entry{
		ClubID: env.ClubID, Event: "computer.created",
		Description: env.t(MsgComputerCreatedLog, map[string]string{"computer": name, "zone": zone.Name}),
		UserID:      env.UserID, SubjectType: SubjectComputer, SubjectID: id, At: now,
		Details: map[string]any{"zone": zone.Name, "number": number},
	}); err != nil {
		return 0, err
	}
	return id, stateChanged(ctx, q, env.ClubID, id)
}

// stateChanged — событие computer.state «перечитать ПК» (карта и список других вкладок).
func stateChanged(ctx context.Context, q *store.Queries, clubID, computerID int64) error {
	_, err := outbox.Add(ctx, q, outbox.Event{ClubID: clubID, Type: "computer.state", Data: map[string]any{"computer_id": computerID}})
	return err
}

// ComputerInput — UpdateClubComputerRequest.
type ComputerInput struct {
	Number      int64
	DisplayName *string
	// MACSet — поле mac_address пришло ('sometimes'); MAC — значение (nil/"" — убрать MAC).
	MACSet bool
	MAC    *string
	ZoneID int64
}

var macRe = regexp.MustCompile(`^(?:[0-9A-F]{2}:){5}[0-9A-F]{2}$`)

// UpdateComputer — ClubComputerController::update(): правила UpdateClubComputerRequest (первая ошибка); смена
// зоны — вместимость и первая свободная ячейка (зона width − 66 × height − 166). Журнал computer.updated.
func UpdateComputer(ctx context.Context, q *store.Queries, env Env, computerID int64, in ComputerInput) error {
	cur, err := q.ClubMapLockComputer(ctx, store.ClubMapLockComputerParams{ClubID: env.ClubID, ID: computerID})
	if err != nil {
		return notFound(err)
	}
	if in.DisplayName != nil {
		if n := strings.TrimSpace(*in.DisplayName); n == "" {
			in.DisplayName = nil
		} else {
			in.DisplayName = &n
		}
	}
	mac := cur.MacAddress
	if in.MACSet {
		mac = nil
		if in.MAC != nil {
			if m := strings.TrimSpace(*in.MAC); m != "" {
				if norm := enroll.NormalizeMAC(m); norm != "" {
					m = norm
				}
				mac = &m
			}
		}
	}
	// number: required|integer|min:1|max:4294967295|unique (club, zone).
	switch {
	case in.Number < 1:
		return rule("number", "min", "1")
	case in.Number > 4294967295:
		return rule("number", "max", "4294967295")
	}
	taken, err := q.ClubMapComputerNumberTaken(ctx, store.ClubMapComputerNumberTakenParams{ClubID: env.ClubID, ZoneID: in.ZoneID, Number: in.Number, ID: computerID})
	if err != nil {
		return err
	}
	if taken {
		return invalid("number", MsgNumberTaken, nil)
	}
	if in.DisplayName != nil {
		if utf8.RuneCountInString(*in.DisplayName) > 255 {
			return rule("display_name", "max_string", "255")
		}
		taken, err := q.ClubMapComputerNameTaken(ctx, store.ClubMapComputerNameTakenParams{ClubID: env.ClubID, DisplayName: in.DisplayName, ID: computerID})
		if err != nil {
			return err
		}
		if taken {
			return invalid("display_name", MsgNameTaken, nil)
		}
	}
	if in.MACSet && mac != nil {
		if !macRe.MatchString(*mac) {
			return invalid("mac_address", MsgMacFormat, nil)
		}
		taken, err := q.ClubMapComputerMacTaken(ctx, store.ClubMapComputerMacTakenParams{ClubID: env.ClubID, MacAddress: mac, ID: computerID})
		if err != nil {
			return err
		}
		if taken {
			return invalid("mac_address", MsgMacTaken, nil)
		}
	}
	x, y := cur.PositionX, cur.PositionY
	if in.ZoneID != cur.ClubZoneID {
		zone, err := q.ClubMapLockZone(ctx, store.ClubMapLockZoneParams{ClubID: env.ClubID, ID: in.ZoneID})
		if err != nil {
			if errors.Is(notFound(err), ErrNotFound) {
				return rule("club_zone_id", "exists", "")
			}
			return err
		}
		pcs, err := q.ClubMapZoneComputersForUpdate(ctx, store.ClubMapZoneComputersForUpdateParams{ClubID: env.ClubID, ZoneID: in.ZoneID})
		if err != nil {
			return err
		}
		if zone.Capacity != nil && int64(len(pcs)) >= *zone.Capacity {
			return invalid("club_zone_id", MsgZoneCapacity, nil)
		}
		occupied := map[[2]int64]bool{}
		for _, pc := range pcs {
			if pc.ID != computerID {
				occupied[[2]int64{pc.PositionX, pc.PositionY}] = true
			}
		}
		found := false
		maxX, maxY := max(0, zone.Width-66), max(0, zone.Height-166)
		for yy := int64(GridStart); yy <= maxY && !found; yy += GridY {
			for xx := int64(GridStart); xx <= maxX; xx += GridX {
				if !occupied[[2]int64{xx, yy}] {
					x, y, found = xx, yy, true
					break
				}
			}
		}
		if !found {
			return invalid("club_zone_id", MsgZoneNoRoom, nil)
		}
	}
	now := env.Now
	if err := q.ClubMapUpdateComputer(ctx, store.ClubMapUpdateComputerParams{
		Number: in.Number, DisplayName: in.DisplayName, MacAddress: mac, ZoneID: in.ZoneID, PositionX: x, PositionY: y,
		Now: &now, ClubID: env.ClubID, ID: computerID,
	}); err != nil {
		return err
	}
	changes := map[string]any{}
	diff := func(field string, was, now any) {
		if dash(was) != dash(now) {
			changes[field] = dash(was) + " → " + dash(now)
		}
	}
	diff("number", cur.Number, in.Number)
	diff("display_name", cur.DisplayName, in.DisplayName)
	diff("mac_address", cur.MacAddress, mac)
	diff("club_zone_id", cur.ClubZoneID, in.ZoneID)
	diff("position_x", cur.PositionX, x)
	diff("position_y", cur.PositionY, y)
	if len(changes) > 0 {
		name := DisplayName(in.DisplayName, cur.DeviceName, in.Number)
		if _, err := activity.Record(ctx, q, activity.Entry{
			ClubID: env.ClubID, Event: "computer.updated", Description: env.t(MsgComputerChangedLog, map[string]string{"computer": name}),
			UserID: env.UserID, SubjectType: SubjectComputer, SubjectID: computerID, At: now, Details: changes,
		}); err != nil {
			return err
		}
	}
	return stateChanged(ctx, q, env.ClubID, computerID)
}

// DeleteComputer — ClubComputerController::destroy(): не при идущей сессии; журнал computer.deleted без предмета.
// Возвращает имя ПК.
func DeleteComputer(ctx context.Context, q *store.Queries, env Env, computerID int64) (string, error) {
	cur, err := q.ClubMapLockComputer(ctx, store.ClubMapLockComputerParams{ClubID: env.ClubID, ID: computerID})
	if err != nil {
		return "", notFound(err)
	}
	name := DisplayName(cur.DisplayName, cur.DeviceName, cur.Number)
	pc := Computer{SessionID: cur.CurrentSessionID, SessionUserID: cur.CurrentSessionUserID, SessionGuest: cur.SessionGuest, SessionEndsAt: cur.SessionEndsAt}
	if pc.HasPaidSession(env.Now) {
		return "", invalid("computer", MsgComputerBusy, map[string]string{"computer": name})
	}
	var mac any
	if cur.MacAddress != nil {
		mac = *cur.MacAddress
	}
	if _, err := activity.Record(ctx, q, activity.Entry{
		ClubID: env.ClubID, Event: "computer.deleted", Description: env.t(MsgComputerRemovedLog, map[string]string{"computer": name}),
		UserID: env.UserID, At: env.Now, Details: map[string]any{"computer": name, "number": cur.Number, "mac_address": mac},
	}); err != nil {
		return "", err
	}
	if err := q.ClubMapDeleteComputer(ctx, store.ClubMapDeleteComputerParams{ClubID: env.ClubID, ID: computerID}); err != nil {
		return "", err
	}
	return name, stateChanged(ctx, q, env.ClubID, computerID)
}

// UpdateComputerLayout — ClubComputerController::__invoke(): 0…5000; ячейка занята — 409.
func UpdateComputerLayout(ctx context.Context, q *store.Queries, env Env, computerID, x, y int64) error {
	cur, err := q.ClubMapLockComputer(ctx, store.ClubMapLockComputerParams{ClubID: env.ClubID, ID: computerID})
	if err != nil {
		return notFound(err)
	}
	switch {
	case x < 0:
		return rule("position_x", "min", "0")
	case x > 5000:
		return rule("position_x", "max", "5000")
	case y < 0:
		return rule("position_y", "min", "0")
	case y > 5000:
		return rule("position_y", "max", "5000")
	}
	taken, err := q.ClubMapComputerCellTaken(ctx, store.ClubMapComputerCellTakenParams{
		ClubID: env.ClubID, ZoneID: cur.ClubZoneID, ID: computerID, PositionX: x, PositionY: y})
	if err != nil {
		return err
	}
	if taken {
		return &Error{Status: http.StatusConflict, Message: MsgCellTaken}
	}
	now := env.Now
	return q.ClubMapUpdateComputerLayout(ctx, store.ClubMapUpdateComputerLayoutParams{PositionX: x, PositionY: y, Now: &now, ClubID: env.ClubID, ID: computerID})
}
