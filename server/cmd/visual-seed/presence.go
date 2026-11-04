package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/valkey-io/valkey-go"

	"github.com/aitmaganbetov/zafirogo/server/internal/agent/presence"
)

// heartbeatTimeout — ClubComputer::HEARTBEAT_TIMEOUT_SECONDS: старый код считал ПК в сети, если last_seen_at не
// старше 90 с от «сейчас» демо-данных.
const heartbeatTimeout = 90 * time.Second

// seedPresence — присутствие агентов в Valkey для ПК, которые старый код показывает «в сети» (новый сервер берёт
// связь с агентом из присутствия, а не из last_seen_at). Адрес — VISUAL_VALKEY_ADDR (пусто — шаг пропускается).
// LastSeenAt снимка — реальное время + 12 ч: снимок не устаревает, пока идут снимки страниц (часы сервера
// фиксированы только для денег и обработчиков, а свежесть присутствия считается по реальному времени).
func (s *seeder) presence(ctx context.Context) error {
	addr := os.Getenv("VISUAL_VALKEY_ADDR")
	if addr == "" {
		return nil
	}
	c, err := valkey.NewClient(valkey.ClientOption{InitAddress: []string{addr}, Password: os.Getenv("VISUAL_VALKEY_PASSWORD"), DisableCache: true})
	if err != nil {
		return fmt.Errorf("valkey: %w", err)
	}
	defer c.Close()
	seen := time.Now().UTC().Add(12 * time.Hour)
	for _, pc := range s.f.Computers {
		last, err := timePtr(pc.LastSeenAt)
		if err != nil {
			return err
		}
		key := presence.DefaultPrefix + "club:" + strconv.FormatInt(pc.ClubID, 10)
		field := strconv.FormatInt(pc.ID, 10)
		if last == nil || s.now.Sub(*last) > heartbeatTimeout {
			if err := c.Do(ctx, c.B().Hdel().Key(key).Field(field).Build()).Error(); err != nil {
				return err
			}
			if err := c.Do(ctx, c.B().Zrem().Key(presence.DefaultPrefix+"online").Member(strconv.FormatInt(pc.ClubID, 10)+":"+field).Build()).Error(); err != nil {
				return err
			}
			continue
		}
		st := presence.State{ClubID: pc.ClubID, ComputerID: pc.ID, Online: true, ConnectedAt: seen, LastSeenAt: seen}
		if pc.AgentState != nil {
			st.AgentState = *pc.AgentState
		}
		if pc.AgentVersion != nil {
			st.AgentVersion = *pc.AgentVersion
		}
		if pc.CurrentSessionID != nil {
			st.SessionID = *pc.CurrentSessionID
		}
		b, err := json.Marshal(st)
		if err != nil {
			return err
		}
		if err := c.Do(ctx, c.B().Hset().Key(key).FieldValue().FieldValue(field, string(b)).Build()).Error(); err != nil {
			return err
		}
		// Набор «на связи» всей платформы (presence.Store.OnlineByClub — кабинет суперадмина, снимки загрузки).
		member := strconv.FormatInt(pc.ClubID, 10) + ":" + field
		if err := c.Do(ctx, c.B().Zadd().Key(presence.DefaultPrefix+"online").ScoreMember().ScoreMember(float64(seen.UnixMilli()), member).Build()).Error(); err != nil {
			return err
		}
	}
	return nil
}
