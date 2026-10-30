// Package chat — REST-раздел: чат сотрудников с гостями ПК (api/chat.yaml). Конвенции — api/README.md.
//
// Laravel: app/Http/Controllers/ChatController.php. Отвечают сотрудники с правом chat.create (по умолчанию
// кассир и оператор, BR-141); остальные читают. Открытие диалога отвечающим помечает сообщения гостя
// прочитанными. Опрос /chat/poll раз в 5 с заменён событиями chat.message / chat.unread (WebSocket).
package chat

import (
	"context"
	"errors"
	"slices"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/aitmaganbetov/zafirogo/server/internal/auth/permissions"
	billingsvc "github.com/aitmaganbetov/zafirogo/server/internal/billing/service"
	chatsvc "github.com/aitmaganbetov/zafirogo/server/internal/chat"
	"github.com/aitmaganbetov/zafirogo/server/internal/club"
	chatapi "github.com/aitmaganbetov/zafirogo/server/internal/http/api/chat"
	"github.com/aitmaganbetov/zafirogo/server/internal/http/httpx"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

var access = httpx.Access{
	Routes: map[string]string{
		"GetChat":         "chat.index",
		"GetChatMessages": "chat.poll",
		"ReplyChat":       "chat.reply",
	},
	// reply(): abort_unless($this->answers(), 403, …) — свой текст отказа.
	Messages: map[string]string{"ReplyChat": msgNoAnswerRight},
}

const msgNoAnswerRight = "Нет права отвечать в чате. Его даёт владелец клуба в «Настройки → Права»."

type handlers struct {
	d *httpx.Deps
}

var _ chatapi.StrictServerInterface = (*handlers)(nil)

// Section — чат с клиентами. Регистрация — server/internal/http/sections.go.
func Section() httpx.Section {
	return httpx.Section{Name: "chat", Mount: mount, Limits: map[string]string{
		"POST /api/chat/{customerId}/messages": "chat.reply", // throttle:60,1
	}}
}

func mount(r chi.Router, d *httpx.Deps) {
	si := chatapi.NewStrictHandlerWithOptions(&handlers{d: d},
		httpx.Middlewares[chatapi.StrictMiddlewareFunc](access),
		chatapi.StrictHTTPServerOptions{RequestErrorHandlerFunc: d.RequestError, ResponseErrorHandlerFunc: d.ResponseError()})
	chatapi.HandlerWithOptions(si, chatapi.ChiServerOptions{BaseRouter: r, ErrorHandlerFunc: d.ParamError})
}

// answers — право chat.create (ChatController::answers()); в режиме просмотра суперадмина — нет (только чтение).
func answers(ctx context.Context) bool {
	p := httpx.Principal(ctx)
	return p.Can("chat.create") && !p.ReadOnly
}

func messageView(r store.ListChatThreadRow) chatapi.ChatMessage {
	m := chatapi.ChatMessage{Id: r.ID, FromStaff: r.FromStaff, Body: r.Body, CreatedAt: r.CreatedAt}
	if r.FromStaff { // 'author' => from_staff ? author?->name : null
		m.Author = r.AuthorName
	}
	if r.ComputerNumber != nil {
		name := club.ComputerName(r.ComputerDisplayName, r.ComputerDeviceName, *r.ComputerNumber)
		m.Computer = &name
	}
	return m
}

// thread — сообщения диалога после after по возрастанию id (latest('id')->limit(100)->reverse()).
func thread(ctx context.Context, q *store.Queries, clubID, customerID, after int64) ([]chatapi.ChatMessage, error) {
	rows, err := q.ListChatThread(ctx, store.ListChatThreadParams{ClubID: clubID, CustomerID: customerID, AfterID: after})
	if err != nil {
		return nil, err
	}
	slices.Reverse(rows)
	out := make([]chatapi.ChatMessage, 0, len(rows))
	for _, r := range rows {
		out = append(out, messageView(r))
	}
	return out, nil
}

// GetChat — index(): диалоги, открытый диалог (по умолчанию — первый), сообщения.
func (h *handlers) GetChat(ctx context.Context, req chatapi.GetChatRequestObject) (chatapi.GetChatResponseObject, error) {
	p := httpx.Principal(ctx)
	now := h.d.NowFunc()
	out := chatapi.ChatPage{Conversations: []chatapi.ChatConversation{}, Messages: []chatapi.ChatMessage{}, CanAnswer: answers(ctx)}
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		var customerID int64
		if req.Params.Customer != nil && *req.Params.Customer > 0 {
			customerID = *req.Params.Customer
		} else {
			convs, err := q.ListChatConversations(ctx, p.ClubID)
			if err != nil {
				return err
			}
			if len(convs) > 0 {
				customerID = convs[0].CustomerID
			}
		}
		if customerID != 0 {
			c, err := q.ChatCustomer(ctx, store.ChatCustomerParams{ClubID: p.ClubID, CustomerID: customerID})
			switch {
			case errors.Is(err, pgx.ErrNoRows):
			case err != nil:
				return err
			default:
				phone := c.Phone
				out.Customer = &chatapi.ChatCustomer{Id: c.ID, Name: c.Name, Phone: &phone}
				if out.CanAnswer {
					if err := chatsvc.MarkRead(ctx, q, p.ClubID, c.ID, now); err != nil {
						return err
					}
				}
				if out.Messages, err = thread(ctx, q, p.ClubID, c.ID, 0); err != nil {
					return err
				}
			}
		}
		// Список диалогов — после отметки прочитанного (как conversations() во view).
		convs, err := q.ListChatConversations(ctx, p.ClubID)
		if err != nil {
			return err
		}
		for _, c := range convs {
			body, fromStaff := c.LastBody, c.LastFromStaff
			out.Conversations = append(out.Conversations, chatapi.ChatConversation{
				CustomerId: c.CustomerID, CustomerName: c.CustomerName, Unread: c.Unread, LastBody: &body, LastFromStaff: &fromStaff,
			})
		}
		out.Unread, err = q.ChatUnreadTotal(ctx, p.ClubID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return chatapi.GetChat200JSONResponse(out), nil
}

// GetChatMessages — poll(): новые сообщения диалога и общее число непрочитанных.
func (h *handlers) GetChatMessages(ctx context.Context, req chatapi.GetChatMessagesRequestObject) (chatapi.GetChatMessagesResponseObject, error) {
	p := httpx.Principal(ctx)
	now := h.d.NowFunc()
	var after int64
	if req.Params.After != nil {
		after = *req.Params.After
	}
	out := chatapi.ChatPoll{Messages: []chatapi.ChatMessage{}}
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		if answers(ctx) {
			if err := chatsvc.MarkRead(ctx, q, p.ClubID, req.CustomerId, now); err != nil {
				return err
			}
		}
		msgs, err := thread(ctx, q, p.ClubID, req.CustomerId, after)
		if err != nil {
			return err
		}
		out.Messages = msgs
		out.Unread, err = q.ChatUnreadTotal(ctx, p.ClubID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return chatapi.GetChatMessages200JSONResponse(out), nil
}

// ReplyChat — reply(): ответ клиенту; нет диалога — 404; ответ уходит на ПК гостя после коммита.
func (h *handlers) ReplyChat(ctx context.Context, req chatapi.ReplyChatRequestObject) (chatapi.ReplyChatResponseObject, error) {
	p := httpx.Principal(ctx)
	if !answers(ctx) {
		return nil, httpx.NewAPIError(403, permissions.CodeForbidden, msgNoAnswerRight)
	}
	now := h.d.NowFunc()
	var out chatapi.ChatMessage
	err := h.d.Tx(ctx, func(ctx context.Context, q *store.Queries) error {
		if _, err := q.ChatCustomer(ctx, store.ChatCustomerParams{ClubID: p.ClubID, CustomerID: req.CustomerId}); errors.Is(err, pgx.ErrNoRows) {
			return httpx.NotFound()
		} else if err != nil {
			return err
		}
		raw := ""
		if req.Body != nil {
			raw = req.Body.Body
		}
		body, rule := chatsvc.ValidBody(raw)
		switch rule {
		case "required":
			return httpx.RuleError("body", "required")
		case "max":
			return httpx.Validation("body", httpx.RuleMessage("body", "max_string", "1000"))
		}
		id, err := chatsvc.Reply(ctx, q, p.ClubID, req.CustomerId, p.UserID, body, now)
		if err != nil {
			return err
		}
		rows, err := q.ListChatThread(ctx, store.ListChatThreadParams{ClubID: p.ClubID, CustomerID: req.CustomerId, AfterID: id - 1})
		if err != nil {
			return err
		}
		for _, r := range rows {
			if r.ID == id {
				out = messageView(r)
			}
		}
		author := ""
		if out.Author != nil {
			author = *out.Author
		}
		if h.d.ChatToGuest != nil {
			clubID, customer := p.ClubID, req.CustomerId
			billingsvc.AfterCommit(ctx, func(ctx context.Context) { h.d.ChatToGuest(ctx, clubID, customer, id, author, body, now) })
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return chatapi.ReplyChat201JSONResponse{Message: out}, nil
}
