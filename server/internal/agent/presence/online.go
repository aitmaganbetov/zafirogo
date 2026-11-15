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

// onlineChunk — сколько ПК проверяется одной командой ZMSCORE.
const onlineChunk = 1000

// OnlineAmong сообщает по каждому ПК из pcs (клуб, ПК), на связи ли он: член zset «online» с неистёкшим сроком —
// то же правило, что OnlineComputers, но только для заданных ПК (каталог клубов гостевого приложения: «свободные
// ПК» клубов города; в старом коде — last_seen_at >= now − HEARTBEAT_TIMEOUT_SECONDS в базе).
func (s *Store) OnlineAmong(ctx context.Context, pcs [][2]int64, now time.Time) ([]bool, error) {
	out := make([]bool, len(pcs))
	nowms := float64(now.UnixMilli())
	for from := 0; from < len(pcs); from += onlineChunk {
		to := min(from+onlineChunk, len(pcs))
		members := make([]string, 0, to-from)
		for _, p := range pcs[from:to] {
			members = append(members, member(p[0], p[1]))
		}
		scores, err := s.c.Do(ctx, s.c.B().Zmscore().Key(s.onlineKey()).Member(members...).Build()).ToArray()
		if err != nil {
			return nil, fmt.Errorf("presence: online among: %w", err)
		}
		for i, sc := range scores {
			v, err := sc.AsFloat64()
			if err != nil {
				continue // nil — ПК нет в zset
			}
			out[from+i] = v > nowms
		}
	}
	return out, nil
}
