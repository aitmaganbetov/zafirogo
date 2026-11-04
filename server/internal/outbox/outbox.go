// Package outbox — события домена через transactional outbox: запись в outbox_events в той же транзакции,
// что и действие (Add), и публикация в NATS JetStream фоновым публикатором (Publisher).
//
// Зачем: событие уходит наружу (админка по WebSocket, вебхуки интеграций, другие инстансы) тогда и только
// тогда, когда действие закоммичено — как WebhookDispatcher::dispatch() + DeliverWebhook::dispatch()->afterCommit()
// в Laravel (app/Services/Integrations/WebhookDispatcher.php ~стр. 24–51), но без потери событий при падении
// процесса между COMMIT и отправкой: источник правды — Postgres.
//
// Гарантии: at-least-once от outbox до JetStream; дубликаты публикации (падение публикатора после отправки,
// но до пометки published_at) отсекает JetStream по Nats-Msg-Id = event_id в окне дедупликации потока.
// Получатели, которым нужна строгая однократность за пределами окна (вебхуки), дополнительно дедуплицируют
// по id события.
package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// StreamName — поток JetStream событий клубов.
const StreamName = "zafiro-events"

// SubjectPrefix — subject события: club.<club_id>.<type>; все события — "club.>".
const SubjectPrefix = "club."

// AllSubjects — фильтр всех событий потока.
const AllSubjects = "club.>"

// MaxTypeLength — outbox_events.type varchar(64).
const MaxTypeLength = 64

// typeRe — тип события: слова через точку, как в CHECK outbox_events_type_check ("computer.state").
var typeRe = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$`)

// ErrInvalidEvent — неверный клуб, тип или данные события.
var ErrInvalidEvent = errors.New("outbox: invalid event")

// Event — событие для записи в outbox.
type Event struct {
	// ClubID — клуб события (= клуб транзакции; RLS не даст записать в чужой).
	ClubID int64
	// Type — тип ("computer.state", "chat.message", "sale.completed"; каталог — docs/realtime.md).
	Type string
	// Data — данные события: JSON-объект (структура или map). Без секретов; для типов, которые уходят
	// во внешние вебхуки, — без персональных данных (комментарий WebhookDispatcher::dispatch()).
	Data any
}

// Added — что записано.
type Added struct {
	ID        int64
	EventID   string
	CreatedAt time.Time
}

// Add записывает событие в outbox_events в транзакции q (db.InTenantTx). Публикатор отправит его в
// JetStream после COMMIT; откат транзакции — события нет.
func Add(ctx context.Context, q *store.Queries, ev Event) (Added, error) {
	if err := Validate(ev.ClubID, ev.Type); err != nil {
		return Added{}, err
	}
	data, err := marshalData(ev.Data)
	if err != nil {
		return Added{}, err
	}
	row, err := q.InsertOutboxEvent(ctx, store.InsertOutboxEventParams{ClubID: ev.ClubID, Type: ev.Type, Payload: data})
	if err != nil {
		return Added{}, fmt.Errorf("outbox: insert %s: %w", ev.Type, err)
	}
	return Added{ID: row.ID, EventID: uuid.UUID(row.EventID.Bytes).String(), CreatedAt: row.CreatedAt}, nil
}

// Validate проверяет клуб и тип события.
func Validate(clubID int64, typ string) error {
	if clubID <= 0 {
		return fmt.Errorf("%w: club id is required", ErrInvalidEvent)
	}
	if len(typ) > MaxTypeLength || !typeRe.MatchString(typ) {
		return fmt.Errorf("%w: type %q", ErrInvalidEvent, typ)
	}
	return nil
}

func marshalData(v any) (json.RawMessage, error) {
	if v == nil {
		return json.RawMessage(`{}`), nil
	}
	var b []byte
	switch t := v.(type) {
	case json.RawMessage:
		b = t
	case []byte:
		b = t
	default:
		var err error
		if b, err = json.Marshal(v); err != nil {
			return nil, fmt.Errorf("%w: data: %w", ErrInvalidEvent, err)
		}
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(b, &obj); err != nil || obj == nil {
		return nil, fmt.Errorf("%w: data must be a JSON object", ErrInvalidEvent)
	}
	return b, nil
}

// Subject — subject события в JetStream: club.<club_id>.<type>.
func Subject(clubID int64, typ string) string {
	return SubjectPrefix + strconv.FormatInt(clubID, 10) + "." + typ
}

// ClubSubjects — все события клуба: club.<club_id>.>.
func ClubSubjects(clubID int64) string {
	return SubjectPrefix + strconv.FormatInt(clubID, 10) + ".>"
}

// Envelope — тело сообщения в JetStream (и основа сообщения WebSocket, docs/realtime.md).
type Envelope struct {
	// ID — id события (uuid), он же Nats-Msg-Id.
	ID     string `json:"id"`
	Type   string `json:"type"`
	ClubID int64  `json:"club_id"`
	// CreatedAt — время записи в outbox (= время действия).
	CreatedAt time.Time       `json:"created_at"`
	Data      json.RawMessage `json:"data"`
}

// DecodeEnvelope разбирает тело сообщения и проверяет обязательные поля.
func DecodeEnvelope(b []byte) (Envelope, error) {
	var env Envelope
	if err := json.Unmarshal(b, &env); err != nil {
		return Envelope{}, fmt.Errorf("outbox: decode envelope: %w", err)
	}
	if env.ID == "" {
		return Envelope{}, fmt.Errorf("%w: envelope without id", ErrInvalidEvent)
	}
	if err := Validate(env.ClubID, env.Type); err != nil {
		return Envelope{}, err
	}
	if len(env.Data) == 0 {
		env.Data = json.RawMessage(`{}`)
	}
	return env, nil
}
