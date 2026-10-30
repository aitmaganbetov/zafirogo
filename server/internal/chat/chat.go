// Package chat — чат гостей ПК с клубом: ответ сотрудника, счётчик непрочитанных, события для панели и доставка
// ответа на ПК гостя. Laravel: app/Http/Controllers/ChatController.php reply(), markRead(), unreadTotal(),
// app/Models/ChatMessage.php. Сторона гостя (агент) — internal/agent/guestapi (SendChatMessage, ListChatMessages).
package chat

import (
	"context"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/presence"
	"github.com/aitmaganbetov/zafirogo/server/internal/outbox"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// MaxLength — ChatMessage::MAX_LENGTH.
const MaxLength = 1000

// Sender — отправка команды агенту ПК (*commands.Sender).
type Sender interface {
	Send(ctx context.Context, clubID, computerID int64, cmd *agentv1.Command) (string, error)
}

// Presence — присутствие агентов клуба (*presence.Store).
type Presence interface {
	List(ctx context.Context, clubID int64) ([]presence.State, error)
}

// Reply — ответ сотрудника: сообщения гостя в диалоге прочитаны, вставлено сообщение from_staff, события
// chat.message и chat.unread (бейдж) и notifications.changed (колокольчик). Тело уже проверено вызывающим.
func Reply(ctx context.Context, q *store.Queries, clubID, customerID, authorID int64, body string, now time.Time) (int64, error) {
	if _, err := q.MarkChatRead(ctx, store.MarkChatReadParams{ClubID: clubID, CustomerID: customerID, Now: &now}); err != nil {
		return 0, err
	}
	id, err := q.InsertStaffChatMessage(ctx, store.InsertStaffChatMessageParams{
		ClubID: clubID, CustomerID: customerID, AuthorID: &authorID, Body: body, Now: &now,
	})
	if err != nil {
		return 0, err
	}
	if err := Message(ctx, q, clubID, id, customerID, true, now); err != nil {
		return 0, err
	}
	return id, Unread(ctx, q, clubID)
}

// MarkRead — markRead(): сообщения гостя прочитаны; если что-то изменилось — chat.unread и notifications.changed.
func MarkRead(ctx context.Context, q *store.Queries, clubID, customerID int64, now time.Time) error {
	n, err := q.MarkChatRead(ctx, store.MarkChatReadParams{ClubID: clubID, CustomerID: customerID, Now: &now})
	if err != nil || n == 0 {
		return err
	}
	return Unread(ctx, q, clubID)
}

// Message — событие chat.message {message_id, customer_id, from_staff, created_at} (текст — по REST).
func Message(ctx context.Context, q *store.Queries, clubID, messageID, customerID int64, fromStaff bool, at time.Time) error {
	_, err := outbox.Add(ctx, q, outbox.Event{ClubID: clubID, Type: "chat.message", Data: map[string]any{
		"message_id": messageID, "customer_id": customerID, "from_staff": fromStaff, "created_at": at.UTC().Format(time.RFC3339),
	}})
	return err
}

// Unread — chat.unread {unread} (живой бейдж «Чат», Q-063) и notifications.changed (колокольчик).
func Unread(ctx context.Context, q *store.Queries, clubID int64) error {
	n, err := q.ChatUnreadTotal(ctx, clubID)
	if err != nil {
		return err
	}
	if _, err := outbox.Add(ctx, q, outbox.Event{ClubID: clubID, Type: "chat.unread", Data: map[string]any{"unread": n}}); err != nil {
		return err
	}
	_, err = outbox.Add(ctx, q, outbox.Event{ClubID: clubID, Type: "notifications.changed", Data: map[string]any{}})
	return err
}

// Deliver — ответ на ПК гостя: ChatMessageReceived на каждый ПК клуба, где гость сейчас вошёл (как
// BalanceChanged). Вызывается после коммита; ошибка доставки только логируется — агент получит сообщение
// списком (ListChatMessages) при следующем открытии чата.
func Deliver(ctx context.Context, log *slog.Logger, s Sender, p Presence, clubID, customerID int64, msg *agentv1.ChatMessage) {
	if s == nil || p == nil {
		return
	}
	pcs, err := p.List(ctx, clubID)
	if err != nil {
		log.WarnContext(ctx, "chat: presence", "club_id", clubID, "err", err)
		return
	}
	for _, st := range pcs {
		if !st.Online || st.SignedInUserID != customerID {
			continue
		}
		if _, err := s.Send(ctx, clubID, st.ComputerID, &agentv1.Command{
			Kind: &agentv1.Command_ChatMessageReceived{ChatMessageReceived: &agentv1.ChatMessageReceived{UserId: customerID, Message: msg}},
		}); err != nil {
			log.WarnContext(ctx, "chat: deliver", "club_id", clubID, "computer_id", st.ComputerID, "err", err)
		}
	}
}

// AgentMessage — сообщение для агента (agent.proto ChatMessage).
func AgentMessage(id int64, authorName, body string, at time.Time) *agentv1.ChatMessage {
	return &agentv1.ChatMessage{MessageId: id, FromStaff: true, AuthorName: authorName, Body: body, SentAt: timestamppb.New(at)}
}

// ValidBody — body required|string|max:1000 после trim (TrimStrings): пусто — "required", длиннее — "max".
func ValidBody(raw string) (string, string) {
	body := strings.TrimSpace(raw)
	switch {
	case body == "":
		return "", "required"
	case utf8.RuneCountInString(body) > MaxLength:
		return "", "max"
	}
	return body, ""
}
