package service

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aitmaganbetov/zafirogo/server/internal/billing/session"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// Карта пересадки гостя (решение GG-6, docs/open-questions/stage2-guest-grpc.md): «в сети» — из присутствия
// агента, как в TransferSeat.

// SeatMapZone — зона на карте пересадки.
type SeatMapZone struct {
	ID, X, Y, Width, Height int64
	Name, Icon, Theme       string
}

// SeatMapSeat — ПК на карте пересадки. Available — на него можно пересесть сейчас.
type SeatMapSeat struct {
	ID, ZoneID, Number, X, Y int64
	Name, ZoneName           string
	Available                bool
}

// SeatMap — карта клуба для выбора места.
type SeatMap struct {
	Zones             []SeatMapZone
	Seats             []SeatMapSeat
	CurrentComputerID int64
}

// seatMapFreeLimit — freeSeatsFor(): ->limit(200) свободных ПК (до проверки броней).
const seatMapFreeLimit = 200

// SeatMap — карта для пересадки клиента userID с ПК fromComputerID.
//
// Laravel: app/Services/SeatTransfers.php map() (стр. 51–81) и freeSeatsFor() (стр. 24–43): зоны по sort_order, id;
// ПК по зоне и номеру (до 500); доступны только свободные ПК, где действует покупка сессии (allowedZoneIds()),
// без чужой брони до конца сессии + интервал (ComputerReservations::blocking()). Нет своей оплаченной сессии на
// исходном ПК — ensureOwnSession(): «На этом ПК нет вашей оплаченной сессии.»
func (s *Service) SeatMap(ctx context.Context, clubID, userID, fromComputerID int64) (SeatMap, error) {
	var out SeatMap
	err := s.tx(ctx, clubID, func(q *store.Queries) error {
		out = SeatMap{CurrentComputerID: fromComputerID, Zones: []SeatMapZone{}, Seats: []SeatMapSeat{}}
		now := s.clock.Now()
		club, err := loadClub(ctx, q, clubID)
		if err != nil {
			return err
		}
		fr, err := q.SeatMapComputer(ctx, store.SeatMapComputerParams{ClubID: clubID, ID: fromComputerID})
		if errors.Is(err, pgx.ErrNoRows) {
			return notFound("computer", err)
		}
		if err != nil {
			return err
		}
		from := computerRow(fr).session()
		// ensureOwnSession() (стр. 122–127).
		if !from.HasPaidSession(now) || from.SessionUserID == nil || *from.SessionUserID != userID {
			return session.ErrTransferNotOwnSession
		}
		zones, err := allowedZones(ctx, q, clubID, *from.SessionID, from.ZoneID)
		if err != nil {
			return err
		}
		// blocking($computer, $user->getKey(), $from->session_ends_at) — для всех ПК одним запросом.
		blockedIDs, err := q.SeatMapBlockedComputers(ctx, store.SeatMapBlockedComputersParams{
			ClubID: clubID, UserID: &userID, FromAt: now,
			Until: from.SessionEndsAt.Add(time.Duration(club.gapMinutes) * time.Minute),
		})
		if err != nil {
			return err
		}
		blocked := make(map[int64]bool, len(blockedIDs))
		for _, id := range blockedIDs {
			blocked[id] = true
		}
		zoneRows, err := q.ClubMapZones(ctx, clubID)
		if err != nil {
			return err
		}
		zoneNames := make(map[int64]string, len(zoneRows))
		for _, z := range zoneRows {
			zoneNames[z.ID] = z.Name
			out.Zones = append(out.Zones, SeatMapZone{ID: z.ID, Name: z.Name, Icon: z.Icon, Theme: z.Theme,
				X: z.PositionX, Y: z.PositionY, Width: z.Width, Height: z.Height})
		}
		pcs, err := q.SeatMapComputers(ctx, clubID)
		if err != nil {
			return err
		}
		free := 0
		for _, r := range pcs {
			c := computerRow{ID: r.ID, ClubZoneID: r.ClubZoneID, Number: r.Number, DisplayName: r.DisplayName,
				DeviceName: r.DeviceName, IsReserved: r.IsReserved, LastSeenAt: r.LastSeenAt,
				CurrentSessionID: r.CurrentSessionID, CurrentSessionUserID: r.CurrentSessionUserID,
				SessionGuest: r.SessionGuest, SessionEndsAt: r.SessionEndsAt, StaffUntil: r.StaffUntil, Status: r.Status}
			// freeSeatsFor(): сначала условия запроса и limit(200), затем ->reject(бронь).
			available := false
			if free < seatMapFreeLimit && session.ListedAsFree(c.session(), fromComputerID, zones, false, now) {
				free++
				available = !blocked[c.ID]
			}
			out.Seats = append(out.Seats, SeatMapSeat{ID: c.ID, ZoneID: c.ClubZoneID, Number: c.Number, Name: c.displayName(),
				ZoneName: zoneNames[c.ClubZoneID], X: r.PositionX, Y: r.PositionY, Available: available})
		}
		return nil
	})
	return out, err
}
