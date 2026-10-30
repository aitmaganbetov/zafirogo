package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/valkey-io/valkey-go"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/presence"
	"github.com/aitmaganbetov/zafirogo/server/internal/db"
)

// DefaultSubjectPrefix — префикс subject NATS: "<prefix>.inst.<instance>".
const DefaultSubjectPrefix = "zafiro.agent"

// Ограничения содержимого (как в старом агенте: StaffCommands.MaxMessageLength, MaxWakeTargets).
const (
	MaxMessageRunes = 300
	MaxWakeTargets  = 50
)

// Ошибки отправки.
var (
	ErrInvalidCommand = errors.New("commands: invalid command")
)

var instanceRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// ValidInstance — id инстанса годится для subject NATS (без точек, пробелов и * >).
func ValidInstance(id string) bool { return instanceRe.MatchString(id) }

// InstanceSubject — subject, на который подписан инстанс.
func InstanceSubject(prefix, instance string) string {
	if prefix == "" {
		prefix = DefaultSubjectPrefix
	}
	return prefix + ".inst." + instance
}

// Типы конверта.
const (
	EnvelopeCommand = "cmd"
	EnvelopeKick    = "kick"
)

// Envelope — сообщение между инстансами через NATS.
type Envelope struct {
	Type       string `json:"t"`
	ClubID     int64  `json:"club"`
	ComputerID int64  `json:"pc"`
	// Gen — для kick: закрыть соединения ПК с поколением меньше Gen (0 — любое: отзыв сертификата).
	Gen int64 `json:"gen,omitempty"`
	// Command — Command в двоичном protobuf.
	Command []byte `json:"cmd,omitempty"`
	// Code — gRPC-код закрытия, ErrorCode — agentv1.ErrorCode, Reason — текст статуса (для kick).
	Code      uint32 `json:"code,omitempty"`
	ErrorCode int32  `json:"err,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

// Sender — Commands.Send для остального сервера.
type Sender struct {
	queue    *Queue
	presence *presence.Store
	nc       *nats.Conn
	prefix   string
	now      func() time.Time
	log      *slog.Logger
}

// SenderOptions — зависимости отправителя.
type SenderOptions struct {
	// Pool — пул логина zafiro_app.
	Pool     db.Beginner
	Presence *presence.Store
	NATS     *nats.Conn
	// SubjectPrefix — префикс subject (пусто — DefaultSubjectPrefix).
	SubjectPrefix string
	Now           func() time.Time
	Log           *slog.Logger
}

// NewSender создаёт отправителя.
func NewSender(o SenderOptions) *Sender {
	s := &Sender{queue: NewQueue(o.Pool), presence: o.Presence, nc: o.NATS, prefix: o.SubjectPrefix, now: o.Now, log: o.Log}
	if s.prefix == "" {
		s.prefix = DefaultSubjectPrefix
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.log == nil {
		s.log = slog.Default()
	}
	return s
}

// Queue — очередь в Postgres (для стрима).
func (s *Sender) Queue() *Queue { return s.queue }

// Validate проверяет команду до отправки.
func Validate(cmd *agentv1.Command) error {
	if cmd == nil || KindOf(cmd) == "" {
		return fmt.Errorf("%w: kind is required", ErrInvalidCommand)
	}
	switch k := cmd.GetKind().(type) {
	case *agentv1.Command_ShowMessage:
		if n := utf8.RuneCountInString(k.ShowMessage.GetText()); n == 0 || n > MaxMessageRunes {
			return fmt.Errorf("%w: show_message.text must be 1..%d characters", ErrInvalidCommand, MaxMessageRunes)
		}
	case *agentv1.Command_WakeOnLan:
		if n := len(k.WakeOnLan.GetMacAddresses()); n == 0 || n > MaxWakeTargets {
			return fmt.Errorf("%w: wake_on_lan needs 1..%d MAC addresses", ErrInvalidCommand, MaxWakeTargets)
		}
	case *agentv1.Command_StartSession:
		if k.StartSession.GetSession().GetSessionId() == "" {
			return fmt.Errorf("%w: start_session.session.session_id is required", ErrInvalidCommand)
		}
	}
	return nil
}

func (s *Sender) coalesceKey(computerID int64, k Kind) string {
	return s.presence.Prefix() + "last:" + strconv.FormatInt(computerID, 10) + ":" + string(k)
}

// Send отправляет команду ПК клуба и возвращает её command_id (UUID v7). Поля command_id, seq, issued_at
// заполняет сервер; expires_at — по виду команды, если не задан. Долговременная команда сохраняется в
// Postgres до отправки: после успешного Send она дойдёт до ПК (сейчас или после reconnect) или истечёт.
// Мгновенная до ПК не в сети не доходит (кроме «заменяемых последним», см. PolicyFor).
//
// Вызывать ПОСЛЕ коммита транзакции, изменившей данные (иначе агент может узнать о том, чего нет).
func (s *Sender) Send(ctx context.Context, clubID, computerID int64, cmd *agentv1.Command) (string, error) {
	if err := Validate(cmd); err != nil {
		return "", err
	}
	if clubID <= 0 || computerID <= 0 {
		return "", fmt.Errorf("%w: club and computer are required", ErrInvalidCommand)
	}
	c, ok := proto.Clone(cmd).(*agentv1.Command)
	if !ok {
		return "", ErrInvalidCommand
	}
	kind := KindOf(c)
	pol := PolicyFor(kind)
	id, err := uuid.NewV7()
	if err != nil {
		return "", fmt.Errorf("commands: uuid: %w", err)
	}
	now := s.now().UTC()
	c.CommandId = id.String()
	c.Seq = 0
	c.IssuedAt = timestamppb.New(now)
	if c.ExpiresAt == nil {
		switch {
		case pol.UntilSessionEnd && c.GetStartSession().GetSession().GetEndsAt() != nil:
			c.ExpiresAt = c.GetStartSession().GetSession().GetEndsAt()
		case pol.TTL > 0 && !pol.Coalesce:
			c.ExpiresAt = timestamppb.New(now.Add(pol.TTL))
		}
	}
	if pol.Durable {
		if err := s.queue.Enqueue(ctx, clubID, computerID, c); err != nil {
			return "", err
		}
	}
	payload, err := proto.Marshal(c)
	if err != nil {
		return "", fmt.Errorf("commands: encode: %w", err)
	}
	if pol.Coalesce {
		vc := s.presence.Client()
		if err := vc.Do(ctx, vc.B().Set().Key(s.coalesceKey(computerID, kind)).Value(valkey.BinaryString(payload)).Px(pol.TTL).Build()).Error(); err != nil {
			s.log.Warn("agent command not coalesced", "kind", kind, "computer_id", computerID, "err", err)
		}
	}
	// Владельца смотрим ПОСЛЕ сохранения: новое соединение берёт владение раньше, чем читает очередь,
	// поэтому команда либо уйдёт ему через NATS, либо будет прочитана им из Postgres/Valkey.
	if err := s.route(ctx, clubID, computerID, payload); err != nil {
		if pol.Durable {
			// Команда сохранена — дойдёт после reconnect.
			s.log.Warn("agent command not routed", "kind", kind, "club_id", clubID, "computer_id", computerID, "err", err)
			return c.CommandId, nil
		}
		return c.CommandId, err
	}
	return c.CommandId, nil
}

func (s *Sender) route(ctx context.Context, clubID, computerID int64, payload []byte) error {
	own, ok, err := s.presence.OwnerOf(ctx, computerID)
	if err != nil || !ok {
		return err
	}
	b, err := json.Marshal(Envelope{Type: EnvelopeCommand, ClubID: clubID, ComputerID: computerID, Command: payload})
	if err != nil {
		return err
	}
	if err := s.nc.Publish(InstanceSubject(s.prefix, own.Instance), b); err != nil {
		return fmt.Errorf("commands: publish: %w", err)
	}
	return nil
}

// Coalesced — последние «заменяемые» мгновенные команды для ПК (досылка после Welcome).
func (s *Sender) Coalesced(ctx context.Context, computerID int64) ([]*agentv1.Command, error) {
	vc := s.presence.Client()
	keys := make([]string, len(CoalescedKinds))
	for i, k := range CoalescedKinds {
		keys[i] = s.coalesceKey(computerID, k)
	}
	vals, err := vc.Do(ctx, vc.B().Mget().Key(keys...).Build()).ToArray()
	if err != nil {
		return nil, fmt.Errorf("commands: coalesced: %w", err)
	}
	var out []*agentv1.Command
	for _, v := range vals {
		b, err := v.AsBytes()
		if err != nil {
			continue // nil — нет значения
		}
		c := &agentv1.Command{}
		if proto.Unmarshal(b, c) == nil && KindOf(c) != "" {
			out = append(out, c)
		}
	}
	return out, nil
}
