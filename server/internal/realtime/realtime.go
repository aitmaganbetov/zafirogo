// Package realtime — WebSocket-хаб админки: события клуба в браузер вместо опроса (polling).
//
// Заменяет 5 мест опроса старой админки (docs/parts/10-screens.md, «Polling → WebSocket»): уведомления,
// карта клуба, чат, to-do, заказы из агента; плюс касса и балансы. Схема сообщений — docs/realtime.md.
//
// Как устроено:
//   - NewHandler возвращает http.Handler для /ws. Пользователь — из principal.FromContext (middleware
//     аутентификации админки ставит его до хендлера); клуб — только из Principal, никогда из запроса;
//   - на инстансе один ordered consumer JetStream на все события (club.>, только новые); событие
//     раздаётся подключённым сотрудникам своего клуба на этом инстансе, у которых есть право раздела
//     (SectionRights / Options.Authorize);
//   - у каждого соединения ограниченный буфер: медленный клиент, у которого буфер переполнен, отключается
//     (код 1008), а не тормозит раздачу остальным; клиент переподключается и перечитывает состояние;
//   - ping каждые PingInterval, нет pong за PingTimeout — соединение закрывается;
//   - не больше MaxConnsPerUser соединений одного пользователя на инстанс (вкладки);
//   - событие staff.access_changed {user_id} отключает соединения этого сотрудника в клубе с кодом 4001
//     (переподключиться с новым токеном: права изменились), клиенту не пересылается.
//
// Доставка в браузер — «не более одного раза» и только пока соединение открыто: после переподключения
// клиент перечитывает состояние экрана по REST (как при первой загрузке).
package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/aitmaganbetov/zafirogo/server/internal/auth/principal"
	"github.com/aitmaganbetov/zafirogo/server/internal/outbox"
)

// Коды закрытия (кроме стандартных 1001 — сервер останавливается, 1008 — медленный клиент).
const (
	// CloseReauth — переподключиться с новым токеном (права изменились или истёк срок соединения).
	CloseReauth websocket.StatusCode = 4001
)

// AccessChangedEvent — служебное событие: права/членство сотрудника изменились (data: {"user_id": N}).
const AccessChangedEvent = "staff.access_changed"

// LiveSubjects — живые обновления (Этап 7: загрузка и здоровье ПК, computer.load): core NATS, без outbox и
// JetStream — их много (раз в 15 с на ПК при заметном изменении), терять их не страшно (следующее придёт само, после
// переподключения клиент перечитывает экран). Не пересекается с потоком событий club.> (иначе JetStream их сохранял бы).
const LiveSubjects = "live.club.>"

// LiveSubject — subject живого обновления клуба.
func LiveSubject(clubID int64, typ string) string {
	return "live.club." + strconv.FormatInt(clubID, 10) + "." + typ
}

// Options — параметры хаба.
type Options struct {
	// JS — JetStream (источник событий). Без него хаб раздаёт только Dispatch (тесты, один инстанс).
	JS jetstream.JetStream
	// Live — NATS для живых обновлений (LiveSubjects, outbox.Envelope в теле). nil — не подписываться.
	Live *nats.Conn
	// Stream — поток событий (пусто — outbox.StreamName).
	Stream string
	// MaxConnsPerUser — соединений одного пользователя на инстансе (0 — 5).
	MaxConnsPerUser int
	// SendBuffer — сообщений в очереди соединения; переполнение — отключение (0 — 64).
	SendBuffer int
	// PingInterval (0 — 25 с), PingTimeout (0 — 10 с), WriteTimeout (0 — 10 с).
	PingInterval, PingTimeout, WriteTimeout time.Duration
	// MaxLifetime — сколько живёт соединение, потом 4001 и переподключение с новым токеном (0 — без предела).
	MaxLifetime time.Duration
	// OriginPatterns — разрешённые Origin кроме своего хоста (по умолчанию — только тот же хост).
	OriginPatterns []string
	// Authorize — фильтр событий по правам (nil — Allowed).
	Authorize func(p principal.Principal, eventType string) bool
	// Log — журнал (nil — slog.Default()).
	Log *slog.Logger
}

// Handler — хаб и http.Handler для /ws.
type Handler struct {
	o     Options
	mu    sync.RWMutex
	clubs map[int64]map[*client]struct{}
	users map[int64]int
	ready chan struct{}
	once  sync.Once
}

var _ http.Handler = (*Handler)(nil)

// NewHandler создаёт хаб. Источник событий запускает Run (оркестратор — в той же группе, что HTTP-сервер).
func NewHandler(o Options) (*Handler, error) {
	if o.Stream == "" {
		o.Stream = outbox.StreamName
	}
	if o.MaxConnsPerUser <= 0 {
		o.MaxConnsPerUser = 5
	}
	if o.SendBuffer <= 0 {
		o.SendBuffer = 64
	}
	if o.PingInterval <= 0 {
		o.PingInterval = 25 * time.Second
	}
	if o.PingTimeout <= 0 {
		o.PingTimeout = 10 * time.Second
	}
	if o.WriteTimeout <= 0 {
		o.WriteTimeout = 10 * time.Second
	}
	if o.Authorize == nil {
		o.Authorize = Allowed
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	return &Handler{
		o:     o,
		clubs: map[int64]map[*client]struct{}{},
		users: map[int64]int{},
		ready: make(chan struct{}),
	}, nil
}

// Ready закрывается, когда Run подписался на поток (события, опубликованные после, дойдут).
func (h *Handler) Ready() <-chan struct{} { return h.ready }

// Run читает события из JetStream и раздаёт их до отмены ctx; затем закрывает все соединения (1001).
func (h *Handler) Run(ctx context.Context) error {
	defer h.closeAll(websocket.StatusGoingAway, "server shutdown")
	if h.o.Live != nil {
		sub, err := h.o.Live.Subscribe(LiveSubjects, func(m *nats.Msg) {
			env, err := outbox.DecodeEnvelope(m.Data)
			if err != nil || env.Type == AccessChangedEvent {
				h.o.Log.Warn("realtime: bad live event", "subject", m.Subject, "err", err)
				return
			}
			h.Dispatch(env)
		})
		if err != nil {
			return fmt.Errorf("realtime: live subscribe: %w", err)
		}
		// Медленная раздача не должна копить память без предела: лишнее отбрасывается (следующее обновление придёт).
		_ = sub.SetPendingLimits(65536, 64<<20)
		defer func() { _ = sub.Unsubscribe() }()
	}
	if h.o.JS == nil {
		h.once.Do(func() { close(h.ready) })
		<-ctx.Done()
		return nil
	}
	cons, err := h.o.JS.OrderedConsumer(ctx, h.o.Stream, jetstream.OrderedConsumerConfig{
		FilterSubjects: []string{outbox.AllSubjects},
		DeliverPolicy:  jetstream.DeliverNewPolicy,
	})
	if err != nil {
		return fmt.Errorf("realtime: consumer: %w", err)
	}
	cc, err := cons.Consume(func(msg jetstream.Msg) {
		env, err := outbox.DecodeEnvelope(msg.Data())
		if err != nil {
			h.o.Log.Warn("realtime: bad event", "subject", msg.Subject(), "err", err)
			return
		}
		h.Dispatch(env)
	})
	if err != nil {
		return fmt.Errorf("realtime: consume: %w", err)
	}
	h.once.Do(func() { close(h.ready) })
	<-ctx.Done()
	cc.Stop()
	return nil
}

// Message — сообщение клиенту (docs/realtime.md).
type Message struct {
	ID   string          `json:"id,omitempty"`
	Type string          `json:"type"`
	At   time.Time       `json:"at"`
	Data json.RawMessage `json:"data"`
}

// Dispatch раздаёт событие сотрудникам его клуба на этом инстансе.
func (h *Handler) Dispatch(env outbox.Envelope) {
	if env.Type == AccessChangedEvent {
		var d struct {
			UserID int64 `json:"user_id"`
		}
		if json.Unmarshal(env.Data, &d) == nil && d.UserID > 0 {
			h.kickUser(env.ClubID, d.UserID)
		}
		return
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	conns := h.clubs[env.ClubID]
	if len(conns) == 0 {
		return
	}
	var msg []byte
	for c := range conns {
		if !h.o.Authorize(c.p, env.Type) {
			continue
		}
		if msg == nil {
			b, err := json.Marshal(Message{ID: env.ID, Type: env.Type, At: env.CreatedAt.UTC(), Data: env.Data})
			if err != nil {
				return
			}
			msg = b
		}
		c.enqueue(msg)
	}
}

// Connections — число открытых соединений клуба на инстансе (метрики, тесты).
func (h *Handler) Connections(clubID int64) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clubs[clubID])
}

// ServeHTTP принимает WebSocket. Нужен Principal с выбранным клубом.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p, ok := principal.FromContext(r.Context())
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, "Unauthenticated.")
		return
	}
	if p.ClubID <= 0 || p.Role == "" {
		writeJSONError(w, http.StatusForbidden, "Доступ запрещён.")
		return
	}
	c := &client{p: p, send: make(chan []byte, h.o.SendBuffer), kicked: make(chan struct{})}
	if !h.add(c) {
		writeJSONError(w, http.StatusTooManyRequests, "Слишком много подключений.")
		return
	}
	defer h.drop(c)

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: h.o.OriginPatterns})
	if err != nil {
		return // Accept уже ответил клиенту
	}
	defer func() { _ = conn.CloseNow() }()
	conn.SetReadLimit(1024) // клиент ничего не шлёт, кроме управляющих кадров
	ctx := conn.CloseRead(r.Context())
	h.serve(ctx, conn, c)
}

func (h *Handler) serve(ctx context.Context, conn *websocket.Conn, c *client) {
	hello, _ := json.Marshal(Message{Type: "hello", At: time.Now().UTC(), Data: mustJSON(map[string]any{
		"club_id": c.p.ClubID, "user_id": c.p.UserID, "ping_interval": int(h.o.PingInterval.Seconds()),
	})})
	if err := h.write(ctx, conn, hello); err != nil {
		return
	}
	ping := time.NewTicker(h.o.PingInterval)
	defer ping.Stop()
	var lifetime <-chan time.Time
	if h.o.MaxLifetime > 0 {
		t := time.NewTimer(h.o.MaxLifetime)
		defer t.Stop()
		lifetime = t.C
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.kicked:
			h.drop(c) // из раздачи — сразу; рукопожатие закрытия с медленным клиентом может занять секунды
			_ = conn.Close(c.code, c.reason)
			return
		case <-lifetime:
			h.drop(c)
			_ = conn.Close(CloseReauth, "reconnect")
			return
		case msg := <-c.send:
			if err := h.write(ctx, conn, msg); err != nil {
				return
			}
		case <-ping.C:
			pctx, cancel := context.WithTimeout(ctx, h.o.PingTimeout)
			err := conn.Ping(pctx)
			cancel()
			if err != nil {
				h.drop(c)
				_ = conn.Close(websocket.StatusPolicyViolation, "ping timeout")
				return
			}
		}
	}
}

func (h *Handler) write(ctx context.Context, conn *websocket.Conn, msg []byte) error {
	wctx, cancel := context.WithTimeout(ctx, h.o.WriteTimeout)
	defer cancel()
	return conn.Write(wctx, websocket.MessageText, msg)
}

func (h *Handler) add(c *client) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.users[c.p.UserID] >= h.o.MaxConnsPerUser {
		return false
	}
	h.users[c.p.UserID]++
	set := h.clubs[c.p.ClubID]
	if set == nil {
		set = map[*client]struct{}{}
		h.clubs[c.p.ClubID] = set
	}
	set[c] = struct{}{}
	return true
}

// drop убирает соединение из хаба (повторный вызов — ничего не делает).
func (h *Handler) drop(c *client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if c.dropped {
		return
	}
	c.dropped = true
	if set := h.clubs[c.p.ClubID]; set != nil {
		delete(set, c)
		if len(set) == 0 {
			delete(h.clubs, c.p.ClubID)
		}
	}
	if h.users[c.p.UserID]--; h.users[c.p.UserID] <= 0 {
		delete(h.users, c.p.UserID)
	}
}

func (h *Handler) kickUser(clubID, userID int64) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.clubs[clubID] {
		if c.p.UserID == userID {
			c.kick(CloseReauth, "access changed")
		}
	}
}

func (h *Handler) closeAll(code websocket.StatusCode, reason string) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, set := range h.clubs {
		for c := range set {
			c.kick(code, reason)
		}
	}
}

// client — одно соединение.
type client struct {
	p      principal.Principal
	send   chan []byte
	kicked chan struct{}
	once   sync.Once
	code   websocket.StatusCode
	reason string
	// dropped — убран из хаба (под Handler.mu).
	dropped bool
}

// enqueue — без блокировки: буфер полон — клиент слишком медленный, отключаем (backpressure).
func (c *client) enqueue(msg []byte) {
	select {
	case c.send <- msg:
	default:
		c.kick(websocket.StatusPolicyViolation, "slow consumer")
	}
}

func (c *client) kick(code websocket.StatusCode, reason string) {
	c.once.Do(func() {
		c.code, c.reason = code, reason
		close(c.kicked)
	})
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"message": message})
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(errors.Join(errors.New("realtime: marshal"), err))
	}
	return b
}
