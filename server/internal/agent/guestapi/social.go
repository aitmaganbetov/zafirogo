package guestapi

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
	"github.com/aitmaganbetov/zafirogo/server/internal/agent/identity"
	"github.com/aitmaganbetov/zafirogo/server/internal/billing"
	billingsvc "github.com/aitmaganbetov/zafirogo/server/internal/billing/service"
	"github.com/aitmaganbetov/zafirogo/server/internal/chat"
	"github.com/aitmaganbetov/zafirogo/server/internal/db"
	"github.com/aitmaganbetov/zafirogo/server/internal/friends"
	"github.com/aitmaganbetov/zafirogo/server/internal/money"
	"github.com/aitmaganbetov/zafirogo/server/internal/outbox"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// Тексты Friends / GuestFriendController.
const (
	msgFriendNotFound   = friends.MsgUserNotFound
	msgRequestNotFound  = friends.MsgRequestNotFound
	msgTransferPassword = "Введите пароль от аккаунта, чтобы подтвердить перевод." // #nosec G101 -- текст для пользователя, не секрет
	msgTransferWrong    = "Неверный пароль. Перевод не выполнен."
	msgCodeFormat       = "Код — 6 цифр из сообщения в Telegram."
	msgChatEmpty        = "Введите сообщение."
	msgChatTooLong      = "Сообщение — не длиннее 1000 символов."
)

// chatMaxLength — ChatMessage::MAX_LENGTH.
const chatMaxLength = 1000

var transferCodeRe = regexp.MustCompile(`^\d{6}$`)

type socialService struct {
	agentv1.UnimplementedSocialServiceServer
	s *Server
}

// overview — Friends::overview() (friends.List) в виде ответа агента.
func overview(ctx context.Context, q *store.Queries, clubID, userID int64) (*agentv1.ListFriendsResponse, error) {
	o, err := friends.List(ctx, q, clubID, userID)
	if err != nil {
		return nil, err
	}
	conv := func(in []friends.Friend) []*agentv1.Friend {
		out := make([]*agentv1.Friend, 0, len(in))
		for _, f := range in {
			out = append(out, &agentv1.Friend{FriendshipId: f.FriendshipID, Name: f.Name, MaskedPhone: f.MaskedPhone})
		}
		return out
	}
	return &agentv1.ListFriendsResponse{Friends: conv(o.Friends), Incoming: conv(o.Incoming), Outgoing: conv(o.Outgoing)}, nil
}

// friendsError — ошибки friends → статусы gRPC (как было до выноса в пакет friends).
func friendsError(err error) error {
	var fe *friends.Error
	switch {
	case errors.Is(err, friends.ErrNotFound):
		return rpcError(codes.NotFound, agentv1.ErrorCode_ERROR_CODE_FRIENDSHIP_NOT_FOUND, msgRequestNotFound, nil, "friendship")
	case errors.As(err, &fe) && fe.Code == friends.CodeRequestNotFound:
		return rpcError(codes.NotFound, agentv1.ErrorCode_ERROR_CODE_FRIENDSHIP_NOT_FOUND, msgRequestNotFound, nil, "friendship")
	case errors.As(err, &fe) && fe.Code == friends.CodeUserNotFound:
		return rpcError(codes.NotFound, agentv1.ErrorCode_ERROR_CODE_USER_NOT_FOUND, msgFriendNotFound, nil, "phone")
	case errors.As(err, &fe):
		return validation(fe.Field, fe.Message)
	}
	return err
}

// ListFriends — GET /api/guest/friends (GuestFriendController::index()).
func (ss *socialService) ListFriends(ctx context.Context, _ *agentv1.ListFriendsRequest) (*agentv1.ListFriendsResponse, error) {
	s := ss.s
	ctx, id, g, err := s.guestCall(ctx, "api.guest.friends.index")
	if err != nil {
		return nil, err
	}
	var out *agentv1.ListFriendsResponse
	err = db.InTenantTx(ctx, s.o.Pool, id.ClubID, func(q *store.Queries) error {
		out, err = overview(ctx, q, id.ClubID, g.UserID)
		return err
	})
	if err != nil {
		return nil, s.fail(ctx, "ListFriends", err)
	}
	return out, nil
}

// AddFriend — заявка по телефону (POST /api/guest/friends, Friends::request()); встречная заявка принимается.
func (ss *socialService) AddFriend(ctx context.Context, req *agentv1.AddFriendRequest) (*agentv1.AddFriendResponse, error) {
	s := ss.s
	ctx, id, g, err := s.guestCall(ctx, "api.guest.friends.store")
	if err != nil {
		return nil, err
	}
	if err := s.requireFeature(ctx, id, featureFriends); err != nil {
		return nil, err
	}
	phone := NormalizePhone(req.GetPhone())
	if !registerPhoneRe.MatchString(phone) {
		return nil, validation("phone", msgPhoneDigits)
	}
	now := s.o.Now()
	out := &agentv1.AddFriendResponse{}
	err = db.InTenantTx(ctx, s.o.Pool, id.ClubID, func(q *store.Queries) error {
		accepted, err := friends.Request(ctx, q, s.record, id.ClubID, g.UserID, phone, now)
		if err != nil {
			return friendsError(err)
		}
		out.Accepted = accepted
		out.Friends, err = overview(ctx, q, id.ClubID, g.UserID)
		return err
	})
	if err != nil {
		return nil, s.fail(ctx, "AddFriend", err)
	}
	return out, nil
}

// AcceptFriend — POST /api/guest/friends/{friendship}/accept (Friends::accept()).
func (ss *socialService) AcceptFriend(ctx context.Context, req *agentv1.AcceptFriendRequest) (*agentv1.AcceptFriendResponse, error) {
	s := ss.s
	ctx, id, g, err := s.guestCall(ctx, "api.guest.friends.accept")
	if err != nil {
		return nil, err
	}
	if err := s.requireFeature(ctx, id, featureFriends); err != nil {
		return nil, err
	}
	out := &agentv1.AcceptFriendResponse{}
	err = db.InTenantTx(ctx, s.o.Pool, id.ClubID, func(q *store.Queries) error {
		if err := friends.Accept(ctx, q, s.record, id.ClubID, g.UserID, req.GetFriendshipId(), s.o.Now()); err != nil {
			return friendsError(err)
		}
		var err error
		out.Friends, err = overview(ctx, q, id.ClubID, g.UserID)
		return err
	})
	if err != nil {
		return nil, s.fail(ctx, "AcceptFriend", err)
	}
	return out, nil
}

// RemoveFriend — отклонить/отозвать заявку или удалить друга (POST /api/guest/friends/{friendship}/remove).
func (ss *socialService) RemoveFriend(ctx context.Context, req *agentv1.RemoveFriendRequest) (*agentv1.RemoveFriendResponse, error) {
	s := ss.s
	ctx, id, g, err := s.guestCall(ctx, "api.guest.friends.remove")
	if err != nil {
		return nil, err
	}
	out := &agentv1.RemoveFriendResponse{}
	err = db.InTenantTx(ctx, s.o.Pool, id.ClubID, func(q *store.Queries) error {
		if err := friends.Remove(ctx, q, s.record, id.ClubID, g.UserID, req.GetFriendshipId()); err != nil {
			return friendsError(err)
		}
		var err error
		out.Friends, err = overview(ctx, q, id.ClubID, g.UserID)
		return err
	})
	if err != nil {
		return nil, s.fail(ctx, "RemoveFriend", err)
	}
	return out, nil
}

// RequestTransferCode — telegram.go (Этап 4, H2).

// TransferBalance — перевод другу с основного баланса (POST /api/guest/friends/{friendship}/transfer с паролем,
// .../transfer/confirm с кодом из Telegram; Friends::transfer(), FriendTransferCodes::confirm()).
func (ss *socialService) TransferBalance(ctx context.Context, req *agentv1.TransferBalanceRequest) (*agentv1.TransferBalanceResponse, error) {
	s := ss.s
	rule := "api.guest.friends.transfer"
	if req.GetTelegramCode() != "" {
		rule = "api.guest.friends.transfer.confirm"
	}
	ctx, id, g, err := s.guestCall(ctx, rule)
	if err != nil {
		return nil, err
	}
	return once(ctx, s.idem, id.ComputerID, "TransferBalance", req.GetIdempotencyKey(), req, &agentv1.TransferBalanceResponse{},
		func() (*agentv1.TransferBalanceResponse, error) {
			if err := s.requireFeature(ctx, id, featureBalanceTransfer); err != nil {
				return nil, err
			}
			switch c := req.GetConfirmation().(type) {
			case *agentv1.TransferBalanceRequest_TelegramCode:
				if !transferCodeRe.MatchString(c.TelegramCode) {
					return nil, rpcError(codes.InvalidArgument, agentv1.ErrorCode_ERROR_CODE_CONFIRMATION_CODE_INVALID, msgCodeFormat, nil, "code")
				}
				return s.transferByCode(ctx, id, g, req, c.TelegramCode)
			case *agentv1.TransferBalanceRequest_Password:
				if c.Password == "" || len(c.Password) > 256 {
					return nil, validation("password", msgTransferPassword)
				}
				// Деньги уходят только с паролем аккаунта: не тем, кто сел за оставленный ПК.
				if err := s.verifyUserPassword(ctx, id, g.UserID, c.Password); err != nil {
					return nil, err
				}
			default:
				return nil, validation("password", msgTransferPassword)
			}
			var res billing.FriendTransferResult
			err := db.InTenantTx(ctx, s.o.Pool, id.ClubID, func(q *store.Queries) error {
				var err error
				res, err = billing.FriendTransfer(ctx, q, billing.FriendTransferParams{
					ClubID: id.ClubID, UserID: g.UserID, FriendshipID: req.GetFriendshipId(), Amount: money.Tiyn(req.GetAmountTiyn()),
					IdempotencyKey: serviceKey(id.ComputerID, req.GetIdempotencyKey()), Actor: actor(ctx, g),
				})
				if err != nil {
					return err
				}
				return s.afterTransfer(ctx, q, id.ClubID, g.UserID, req.GetFriendshipId(), res)
			})
			if err != nil {
				return nil, s.fail(ctx, "TransferBalance", err)
			}
			return s.transferResponse(ctx, id, g, res)
		})
}

func (s *Server) verifyUserPassword(ctx context.Context, id identity.Identity, userID int64, plain string) error {
	var hash string
	err := db.InTenantTx(ctx, s.o.Pool, id.ClubID, func(q *store.Queries) error {
		u, err := q.GuestUserInClub(ctx, store.GuestUserInClubParams{ClubID: id.ClubID, UserID: userID})
		hash = u.Password
		return err
	})
	if err != nil {
		return s.fail(ctx, "verifyUserPassword", err)
	}
	if _, err := s.o.Passwords.Verify(ctx, plain, hash); err != nil {
		return rpcError(codes.PermissionDenied, agentv1.ErrorCode_ERROR_CODE_WRONG_PASSWORD, msgTransferWrong, nil, "password")
	}
	return nil
}

// transferByCode — FriendTransferCodes::confirm(): неверный код засчитывает попытку отдельной транзакцией.
func (s *Server) transferByCode(ctx context.Context, id identity.Identity, g Guest, req *agentv1.TransferBalanceRequest, code string) (*agentv1.TransferBalanceResponse, error) {
	var res billing.FriendTransferResult
	err := db.InTenantTxWith(ctx, s.o.Pool, id.ClubID, db.Options{UserID: g.UserID}, func(q *store.Queries) error {
		var err error
		res, err = billing.ConfirmFriendTransferCode(ctx, q, billing.ConfirmTransferCodeParams{
			ClubID: id.ClubID, UserID: g.UserID, FriendshipID: req.GetFriendshipId(),
			AmountInput: money.Tiyn(req.GetAmountTiyn()).Tenge(), Code: code, Now: s.o.Now(),
			VerifyCode: func(hash, code string) bool { _, err := s.o.Passwords.Verify(ctx, code, hash); return err == nil },
			Actor:      actor(ctx, g),
		})
		if err != nil {
			return err
		}
		return s.afterTransfer(ctx, q, id.ClubID, g.UserID, req.GetFriendshipId(), res)
	})
	var mismatch *billing.TransferCodeMismatchError
	if errors.As(err, &mismatch) {
		if e := db.InTenantTxWith(ctx, s.o.Pool, id.ClubID, db.Options{UserID: g.UserID}, func(q *store.Queries) error {
			return billing.RecordTransferCodeAttempt(ctx, q, id.ClubID, mismatch.CodeID)
		}); e != nil {
			s.o.Log.WarnContext(ctx, "transfer code attempt not recorded", "err", e)
		}
	}
	if err != nil {
		return nil, s.fail(ctx, "TransferBalance", err)
	}
	return s.transferResponse(ctx, id, g, res)
}

// afterTransfer — журнал balance.transferred (Friends::transfer() стр. 141–150; предмет — друг) и события
// balance.changed обоих: общий хелпер billing/service.RecordBalanceTransferred (B1). Повтор — без записей.
func (s *Server) afterTransfer(ctx context.Context, q *store.Queries, clubID, userID, friendshipID int64, res billing.FriendTransferResult) error {
	if res.Ledger.Replayed {
		return nil
	}
	return billingsvc.RecordBalanceTransferred(ctx, q, clubID, userID, friendshipID, res)
}

func (s *Server) transferResponse(ctx context.Context, id identity.Identity, g Guest, res billing.FriendTransferResult) (*agentv1.TransferBalanceResponse, error) {
	out := &agentv1.TransferBalanceResponse{AmountTiyn: int64(res.Amount)}
	var friendBal *agentv1.Balance
	err := db.InTenantTx(ctx, s.o.Pool, id.ClubID, func(q *store.Queries) error {
		ci, err := loadClub(ctx, q, id)
		if err != nil {
			return err
		}
		if out.Balance, err = balanceOf(ctx, q, id.ClubID, g.UserID, ci.settings.currency); err != nil {
			return err
		}
		if res.FriendID > 0 {
			friendBal, err = balanceOf(ctx, q, id.ClubID, res.FriendID, ci.settings.currency)
		}
		return err
	})
	if err != nil {
		return nil, s.fail(ctx, "TransferBalance", err)
	}
	if !res.Ledger.Replayed {
		s.notifyBalance(ctx, id.ClubID, g.UserID, out.GetBalance())
		if friendBal != nil {
			s.notifyBalance(ctx, id.ClubID, res.FriendID, friendBal)
		}
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Чат (GuestChatController)
// ---------------------------------------------------------------------------

// ListChatMessages — последние 100 сообщений (или после after_message_id), ответы сотрудников помечаются
// прочитанными (GET /api/guest/chat).
func (ss *socialService) ListChatMessages(ctx context.Context, req *agentv1.ListChatMessagesRequest) (*agentv1.ListChatMessagesResponse, error) {
	s := ss.s
	ctx, id, g, err := s.guestCall(ctx, "api.guest.chat")
	if err != nil {
		return nil, err
	}
	if req.GetAfterMessageId() < 0 {
		return nil, validation("after", "Неверный номер сообщения.")
	}
	now := s.o.Now()
	out := &agentv1.ListChatMessagesResponse{}
	err = db.InTenantTx(ctx, s.o.Pool, id.ClubID, func(q *store.Queries) error {
		rows, err := q.GuestListChatMessages(ctx, store.GuestListChatMessagesParams{ClubID: id.ClubID, UserID: g.UserID, AfterID: req.GetAfterMessageId()})
		if err != nil {
			return err
		}
		slices.Reverse(rows) // ->latest('id')->limit(100)->get()->reverse()
		out.Messages = make([]*agentv1.ChatMessage, 0, len(rows))
		for _, r := range rows {
			m := &agentv1.ChatMessage{MessageId: r.ID, FromStaff: r.FromStaff, Body: r.Body}
			if r.FromStaff && r.AuthorName != nil { // 'author' => from_staff ? author?->name : null
				m.AuthorName = *r.AuthorName
			}
			if r.CreatedAt != nil {
				m.SentAt = timestamppb.New(*r.CreatedAt)
			}
			if r.ReadAt != nil {
				m.ReadAt = timestamppb.New(*r.ReadAt)
			}
			out.Messages = append(out.Messages, m)
		}
		return q.GuestMarkStaffMessagesRead(ctx, store.GuestMarkStaffMessagesReadParams{Now: &now, ClubID: id.ClubID, UserID: g.UserID})
	})
	if err != nil {
		return nil, s.fail(ctx, "ListChatMessages", err)
	}
	return out, nil
}

// SendChatMessage — сообщение клубу с этого ПК (POST /api/guest/chat, GuestChatController::store()).
func (ss *socialService) SendChatMessage(ctx context.Context, req *agentv1.SendChatMessageRequest) (*agentv1.SendChatMessageResponse, error) {
	s := ss.s
	ctx, id, g, err := s.guestCall(ctx, "api.guest.chat.store")
	if err != nil {
		return nil, err
	}
	return once(ctx, s.idem, id.ComputerID, "SendChatMessage", req.GetIdempotencyKey(), req, &agentv1.SendChatMessageResponse{},
		func() (*agentv1.SendChatMessageResponse, error) {
			body := strings.TrimSpace(req.GetBody())
			switch {
			case body == "":
				return nil, validation("body", msgChatEmpty)
			case utf8.RuneCountInString(req.GetBody()) > chatMaxLength:
				return nil, validation("body", msgChatTooLong)
			}
			now := s.o.Now()
			out := &agentv1.SendChatMessageResponse{}
			err := db.InTenantTx(ctx, s.o.Pool, id.ClubID, func(q *store.Queries) error {
				pcID := id.ComputerID
				row, err := q.GuestInsertChatMessage(ctx, store.GuestInsertChatMessageParams{ClubID: id.ClubID, UserID: g.UserID,
					Body: body, ComputerID: &pcID, Now: &now})
				if err != nil {
					return err
				}
				out.Message = &agentv1.ChatMessage{MessageId: row.ID, Body: body, SentAt: timestamppb.New(now)}
				_, err = outbox.Add(ctx, q, outbox.Event{ClubID: id.ClubID, Type: "chat.message", Data: map[string]any{
					"message_id": row.ID, "customer_id": g.UserID, "from_staff": false, "created_at": now.UTC().Format("2006-01-02T15:04:05Z07:00"),
				}})
				if err != nil {
					return err
				}
				// Живой бейдж «Чат» и колокольчик панели (chat.unread, notifications.changed — Q-063).
				return chat.Unread(ctx, q, id.ClubID)
			})
			if err != nil {
				return nil, s.fail(ctx, "SendChatMessage", err)
			}
			return out, nil
		})
}
