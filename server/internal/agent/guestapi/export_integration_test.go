//go:build integration

package guestapi

import (
	"context"
	"fmt"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/agent/identity"
	"github.com/aitmaganbetov/zafirogo/server/internal/money"
)

// Для теста сравнения пакетного чтения автобиллинга с поштучным (S7-AB-2).

// ABData — данные решения автобиллинга по ПК (непрозрачные; сравнивать reflect.DeepEqual).
type ABData = abData

// ABLoadOne — поштучное чтение (эталон).
func (s *Server) ABLoadOne(ctx context.Context, club, pc, user int64) (ABData, error) {
	return s.abLoad(ctx, identity.Identity{ClubID: club, ComputerID: pc}, user)
}

// ABLoadClub — пакетное чтение ПК клуба: pairs — пары {ПК, гость}.
func (s *Server) ABLoadClub(ctx context.Context, club int64, pairs [][2]int64) (map[int64]ABData, error) {
	reqs := make([]abReq, len(pairs))
	for i, p := range pairs {
		reqs[i] = abReq{pc: p[0], user: p[1]}
	}
	return s.abLoadClub(ctx, club, reqs)
}

// ABDecide — решение по данным и состоянию ПК (режим pause|after, начало паузы; нулевое — не начата) на момент now,
// строкой: идёт ли сессия (и до когда), «истекла», включён ли автобиллинг, решение (ждать / конец паузы / минуты).
func ABDecide(d ABData, user int64, mode string, pauseStart, now time.Time) string {
	st := abState{user: user, mode: mode}
	if !pauseStart.IsZero() {
		st.pause = pauseStart.UnixMilli()
	}
	v := d.view(user, now)
	dec := abDecide(st, v, now)
	session := "-"
	if v.session != nil {
		session = v.session.GetSessionId() + "@" + v.session.GetEndsAt().AsTime().UTC().Format(time.RFC3339)
	}
	return fmt.Sprintf("session=%s expired=%v enabled=%v main=%d price=%d waiting=%v pause_ends=%d buy=%d",
		session, v.expired, v.offer.AutoBilling && v.offer.Available, v.main, v.offer.PricePerHour,
		dec.Waiting, dec.PauseEndsAt.UnixMilli(), dec.BuyMinutes)
}

// ABWithMain — те же данные с другим основным балансом (устаревшие данные пакета).
func ABWithMain(d ABData, main money.Tiyn) ABData {
	d.main = main
	return d
}

// ABProcessWith — шаг автобиллинга ПК с данными «из пакета», прочитанными для клуба club и гостя user.
func (s *Server) ABProcessWith(ctx context.Context, pc, club, user int64, d ABData) error {
	return s.abProcess(ctx, pc, &abPre{club: club, user: user, data: d})
}

// AutoBillingRecheckAt — срок пересмотра ПК клуба, которому автобиллинг не положен.
func AutoBillingRecheckAt(club int64, now time.Time) time.Time { return abRecheckAt(club, now) }
