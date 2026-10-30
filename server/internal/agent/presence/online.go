package presence

import (
	"context"
	"fmt"
	"strconv"
	"time"
)

// OnlineByClub — сколько ПК каждого клуба сейчас на связи (по всей платформе): члены zset «online» с неистёкшим
// сроком. Для суперадминки (ClubController::index, ClubAttention) и снимков загрузки (SnapshotClubLoad), где в
// старом коде был COUNT по last_seen_at >= now − HEARTBEAT_TIMEOUT_SECONDS. ПК, чей стрим закрылся, считается
// на связи до конца окна переподключения (как last_seen_at в старом — ещё 90 с после последнего пинга).
func (s *Store) OnlineByClub(ctx context.Context, now time.Time) (map[int64]int64, error) {
	pcs, err := s.OnlineComputers(ctx, now)
	if err != nil {
		return nil, err
	}
	out := map[int64]int64{}
	for _, p := range pcs {
		out[p[0]]++
	}
	return out, nil
}

// OnlineComputers — пары (клуб, ПК) на связи по всей платформе (рассылка команд всем ПК: TranslationsChanged).
func (s *Store) OnlineComputers(ctx context.Context, now time.Time) ([][2]int64, error) {
	members, err := s.c.Do(ctx, s.c.B().Zrange().Key(s.onlineKey()).
		Min("("+strconv.FormatInt(now.UnixMilli(), 10)).Max("+inf").Byscore().Build()).AsStrSlice()
	if err != nil {
		return nil, fmt.Errorf("presence: online computers: %w", err)
	}
	out := make([][2]int64, 0, len(members))
	for _, m := range members {
		if club, pc, ok := parseMember(m); ok {
			out = append(out, [2]int64{club, pc})
		}
	}
	return out, nil
}
