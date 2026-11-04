// Package friends — друзья среди клиентов клуба (Laravel: app/Services/Friends.php): список, заявка по телефону,
// принятие, удаление. Общий код для агента ПК (agent/guestapi, SocialService) и мобильного приложения клиента
// (http/legacy, /api/guest/friends*). Переводы балансов между друзьями — billing.FriendTransfer.
//
// Функции работают в тенантной транзакции вызывающего (q с app.club_id); журнал действий — через rec.
// Ошибки правил — *Error (в Laravel — ValidationException: 422 с полем), остальные — ошибки БД.
package friends

import (
	"context"
	"errors"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aitmaganbetov/zafirogo/server/internal/activity"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// Коды ошибок (для отображения в ErrorCode агента).
const (
	CodeUserNotFound    = "user_not_found"
	CodeSelf            = "self"
	CodeAlreadyFriends  = "already_friends"
	CodeAlreadyRequest  = "already_requested"
	CodeRequestNotFound = "request_not_found"
)

// Тексты Friends::request(), accept(), remove() (русский текст = ключ перевода).
const (
	MsgUserNotFound    = "Клиент с таким номером в клубе не найден."
	MsgSelf            = "Нельзя добавить в друзья самого себя."
	MsgAlreadyFriends  = "Этот клиент уже у вас в друзьях."
	MsgAlreadyRequest  = "Заявка этому клиенту уже отправлена."
	MsgRequestNotFound = "Заявка не найдена."
)

// SubjectFriendship — subject_type журнала (morph-класс Laravel).
const SubjectFriendship = `App\Models\Friendship`

// Error — ошибка правила (ValidationException::withMessages([Field => Message])).
type Error struct {
	Code    string
	Field   string
	Message string
}

func (e *Error) Error() string { return e.Field + ": " + e.Message }

func ruleError(code, field, msg string) *Error { return &Error{Code: code, Field: field, Message: msg} }

// Recorder — запись журнала действий в транзакции q (ActivityLogger::record()).
type Recorder func(ctx context.Context, q *store.Queries, e activity.Entry) error

// Friend — строка списка: id дружбы (по нему принимают, удаляют и переводят), имя и замаскированный телефон.
type Friend struct {
	FriendshipID int64
	Name         string
	MaskedPhone  string
}

// Overview — Friends::overview(): друзья, входящие и исходящие заявки (до 300 строк, принятые сначала).
type Overview struct {
	Friends  []Friend
	Incoming []Friend
	Outgoing []Friend
}

var kzPhoneRe = regexp.MustCompile(`^\+7\d{10}$`)

// MaskPhone — Friends::maskPhone(): "+7 701 ••• •• 12"; не казахстанский формат — "•••"; пусто — пусто.
func MaskPhone(phone string) string {
	if !kzPhoneRe.MatchString(phone) {
		if phone == "" {
			return ""
		}
		return "•••"
	}
	return "+7 " + phone[2:5] + " ••• •• " + phone[10:12]
}

// List — Friends::overview().
func List(ctx context.Context, q *store.Queries, clubID, userID int64) (Overview, error) {
	rows, err := q.GuestListFriendships(ctx, store.GuestListFriendshipsParams{UserID: userID, ClubID: clubID})
	if err != nil {
		return Overview{}, err
	}
	out := Overview{Friends: []Friend{}, Incoming: []Friend{}, Outgoing: []Friend{}}
	for _, r := range rows {
		f := Friend{FriendshipID: r.ID, Name: r.OtherName, MaskedPhone: MaskPhone(r.OtherPhone)}
		switch {
		case r.AcceptedAt != nil:
			out.Friends = append(out.Friends, f)
		case r.AddresseeID == userID:
			out.Incoming = append(out.Incoming, f)
		default:
			out.Outgoing = append(out.Outgoing, f)
		}
	}
	return out, nil
}

// Request — Friends::request(): заявка клиенту клуба с этим телефоном (+7XXXXXXXXXX, уже проверен вызывающим);
// встречная заявка от него принимается. accepted — стали друзьями (ответ «Вы теперь друзья.»).
func Request(ctx context.Context, q *store.Queries, rec Recorder, clubID, userID int64, phone string, now time.Time) (accepted bool, err error) {
	friend, err := q.GuestFindClubUserByPhone(ctx, store.GuestFindClubUserByPhoneParams{ClubID: clubID, Phone: phone})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, ruleError(CodeUserNotFound, "phone", MsgUserNotFound)
	}
	if err != nil {
		return false, err
	}
	if friend.ID == userID {
		return false, ruleError(CodeSelf, "phone", MsgSelf)
	}
	me, err := q.GuestUserInClub(ctx, store.GuestUserInClubParams{ClubID: clubID, UserID: userID})
	if err != nil {
		return false, err
	}
	existing, err := q.GuestLockFriendshipPair(ctx, store.GuestLockFriendshipPairParams{ClubID: clubID, UserA: userID, UserB: friend.ID})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		fid, err := q.GuestInsertFriendship(ctx, store.GuestInsertFriendshipParams{ClubID: clubID, RequesterID: userID, AddresseeID: friend.ID, Now: &now})
		if err != nil {
			return false, err
		}
		return false, rec(ctx, q, activity.Entry{ClubID: clubID, Event: "friend.requested",
			Description: me.Name + " отправил заявку в друзья " + friend.Name, UserID: userID,
			SubjectType: SubjectFriendship, SubjectID: fid})
	case err != nil:
		return false, err
	case existing.AcceptedAt != nil:
		return false, ruleError(CodeAlreadyFriends, "phone", MsgAlreadyFriends)
	case existing.RequesterID == userID:
		return false, ruleError(CodeAlreadyRequest, "phone", MsgAlreadyRequest)
	}
	return true, acceptLocked(ctx, q, rec, clubID, existing.ID, userID, me.Name, now)
}

func acceptLocked(ctx context.Context, q *store.Queries, rec Recorder, clubID, friendshipID, userID int64, userName string, now time.Time) error {
	if err := q.GuestAcceptFriendship(ctx, store.GuestAcceptFriendshipParams{Now: &now, ClubID: clubID, ID: friendshipID}); err != nil {
		return err
	}
	return rec(ctx, q, activity.Entry{ClubID: clubID, Event: "friend.accepted", Description: userName + " принял заявку в друзья",
		UserID: userID, SubjectType: SubjectFriendship, SubjectID: friendshipID})
}

// ErrNotFound — дружбы с этим id нет в клубе (route model binding Laravel → 404).
var ErrNotFound = errors.New("friends: friendship not found")

// Accept — Friends::accept(): только входящая непринятая заявка. Нет дружбы в клубе — ErrNotFound.
func Accept(ctx context.Context, q *store.Queries, rec Recorder, clubID, userID, friendshipID int64, now time.Time) error {
	f, err := q.GuestLockFriendship(ctx, store.GuestLockFriendshipParams{ClubID: clubID, ID: friendshipID})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if f.AddresseeID != userID || f.AcceptedAt != nil {
		return ruleError(CodeRequestNotFound, "friendship", MsgRequestNotFound)
	}
	me, err := q.GuestUserInClub(ctx, store.GuestUserInClubParams{ClubID: clubID, UserID: userID})
	if err != nil {
		return err
	}
	return acceptLocked(ctx, q, rec, clubID, f.ID, userID, me.Name, now)
}

// Remove — Friends::remove(): отклонить или отозвать заявку, удалить друга. Нет дружбы в клубе — ErrNotFound.
func Remove(ctx context.Context, q *store.Queries, rec Recorder, clubID, userID, friendshipID int64) error {
	f, err := q.GuestLockFriendship(ctx, store.GuestLockFriendshipParams{ClubID: clubID, ID: friendshipID})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if f.RequesterID != userID && f.AddresseeID != userID {
		return ruleError(CodeRequestNotFound, "friendship", MsgRequestNotFound)
	}
	if err := q.GuestDeleteFriendship(ctx, store.GuestDeleteFriendshipParams{ClubID: clubID, ID: f.ID}); err != nil {
		return err
	}
	me, err := q.GuestUserInClub(ctx, store.GuestUserInClubParams{ClubID: clubID, UserID: userID})
	if err != nil {
		return err
	}
	return rec(ctx, q, activity.Entry{ClubID: clubID, Event: "friend.removed", Description: me.Name + " удалил дружбу или заявку",
		UserID: userID, Details: map[string]any{"friendship_id": f.ID, "accepted": f.AcceptedAt != nil}})
}
