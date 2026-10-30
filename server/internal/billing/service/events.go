package service

// Журнал действий (activity_logs), события клуба (outbox) и уведомления агента после коммита.
//
// Журнал и outbox пишутся в той же транзакции, что и действие (ответ на SV-8/CSQ-5 в
// docs/open-questions/stage2-events.md): откат действия откатывает и запись. Уведомления агента
// (SessionNotifier) — только после успешного COMMIT: команда на ПК не должна уйти за операцию, которой нет.

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/aitmaganbetov/zafirogo/server/gen/agent/v1"
	"github.com/aitmaganbetov/zafirogo/server/internal/activity"
	"github.com/aitmaganbetov/zafirogo/server/internal/billing"
	"github.com/aitmaganbetov/zafirogo/server/internal/money"
	"github.com/aitmaganbetov/zafirogo/server/internal/outbox"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// SessionNotifier — что сервисы вызывают ПОСЛЕ коммита транзакции, изменившей сессию ПК или баланс клиента.
// Сигнатуры совпадают с commands.SessionNotifier (server/internal/agent/commands): *commands.Notifier
// подходит без адаптера. Ошибка доставки не откатывает операцию — сервис её только логирует (агент получит
// актуальное состояние в Welcome при следующем подключении).
type SessionNotifier interface {
	SessionStarted(ctx context.Context, clubID, computerID int64, s *agentv1.ActiveSession, issuedBy int64) error
	SessionEnded(ctx context.Context, clubID, computerID int64, e *agentv1.EndSession, issuedBy int64) error
	BalanceChanged(ctx context.Context, clubID, userID int64, b *agentv1.Balance) error
}

// SetNotifier подключает уведомления агента (nil — без уведомлений). Вызывать до начала работы сервиса.
func (s *Service) SetNotifier(n SessionNotifier) { s.notifier = n }

// ---------------------------------------------------------------------------
// Действия после коммита (для транзакций, которыми владеет вызывающий)
// ---------------------------------------------------------------------------

// CommitHooks — действия, отложенные до успешного COMMIT транзакции вызывающего. Методы кассы
// (cash/service) работают в транзакции HTTP-слоя и регистрируют уведомления через AfterCommit; HTTP-слой
// создаёт CommitHooks на запрос, сбрасывает их в начале каждой попытки транзакции (db.InTenantTx повторяет
// её при deadlock) и вызывает Run после успешного коммита.
type CommitHooks struct {
	mu  sync.Mutex
	fns []func(context.Context)
}

type commitHooksKey struct{}

// WithCommitHooks возвращает контекст с новой очередью действий после коммита.
func WithCommitHooks(ctx context.Context) (context.Context, *CommitHooks) {
	h := &CommitHooks{}
	return context.WithValue(ctx, commitHooksKey{}, h), h
}

// Reset очищает очередь (начало новой попытки транзакции).
func (h *CommitHooks) Reset() {
	h.mu.Lock()
	h.fns = nil
	h.mu.Unlock()
}

// Run выполняет отложенные действия по порядку и очищает очередь. Вызывать только после успешного COMMIT.
func (h *CommitHooks) Run(ctx context.Context) {
	h.mu.Lock()
	fns := h.fns
	h.fns = nil
	h.mu.Unlock()
	for _, fn := range fns {
		fn(ctx)
	}
}

// AfterCommit откладывает fn до коммита транзакции вызывающего. false — в контексте нет CommitHooks
// (вызывающий не просил уведомлений): fn не выполняется.
func AfterCommit(ctx context.Context, fn func(context.Context)) bool {
	h, ok := ctx.Value(commitHooksKey{}).(*CommitHooks)
	if !ok || h == nil {
		return false
	}
	h.mu.Lock()
	h.fns = append(h.fns, fn)
	h.mu.Unlock()
	return true
}

// ---------------------------------------------------------------------------
// Уведомления агента
// ---------------------------------------------------------------------------

// NotifyBalance — BalanceChanged с балансами после операции (тиыны). Ошибка — в лог.
func NotifyBalance(ctx context.Context, n SessionNotifier, clubID, userID int64, main, bonus money.Tiyn, currency string) {
	if n == nil || userID <= 0 {
		return
	}
	if err := n.BalanceChanged(ctx, clubID, userID, &agentv1.Balance{
		MainTiyn: int64(main), BonusTiyn: int64(bonus), Currency: currency,
	}); err != nil {
		slog.WarnContext(ctx, "notify balance changed", "club_id", clubID, "user_id", userID, "err", err)
	}
}

// NotifySessionStarted — StartSession на ПК (новая или продлённая сессия). Ошибка — в лог.
func NotifySessionStarted(ctx context.Context, n SessionNotifier, clubID, computerID int64, sessionID string, userID int64,
	guest bool, startedAt, endsAt time.Time, issuedBy int64) {
	if n == nil {
		return
	}
	s := &agentv1.ActiveSession{SessionId: sessionID, UserId: userID, Guest: guest, EndsAt: timestamppb.New(endsAt)}
	if !startedAt.IsZero() {
		s.StartedAt = timestamppb.New(startedAt)
	}
	if err := n.SessionStarted(ctx, clubID, computerID, s, issuedBy); err != nil {
		slog.WarnContext(ctx, "notify session started", "club_id", clubID, "computer_id", computerID, "err", err)
	}
}

// NotifySessionEnded — EndSession на ПК. Ошибка — в лог.
func NotifySessionEnded(ctx context.Context, n SessionNotifier, clubID, computerID int64, e *agentv1.EndSession, issuedBy int64) {
	if n == nil {
		return
	}
	if err := n.SessionEnded(ctx, clubID, computerID, e, issuedBy); err != nil {
		slog.WarnContext(ctx, "notify session ended", "club_id", clubID, "computer_id", computerID, "err", err)
	}
}

// ---------------------------------------------------------------------------
// Журнал действий
// ---------------------------------------------------------------------------

// Морф-классы предметов журнала — как subject_type в перенесённых строках Laravel (getMorphClass()).
const (
	SubjectUser            = `App\Models\User`
	SubjectCashTransaction = `App\Models\CashTransaction`
	SubjectCashShift       = `App\Models\CashShift`
	SubjectProduct         = `App\Models\Product`
	SubjectTariff          = `App\Models\Tariff`
	SubjectZone            = `App\Models\ClubZone`
	SubjectComputer        = `App\Models\ClubComputer`
)

// RecordActivity пишет запись журнала в транзакции q. Без данных запроса в контексте (activity.WithRequestMeta
// ставит HTTP/gRPC-middleware) или без пользователя запись не делается: фоновые задачи журнал не писали
// и в старом коде (open-questions/stage2-events.md EV-13).
func RecordActivity(ctx context.Context, q *store.Queries, e activity.Entry) error {
	if e.UserID <= 0 {
		return nil
	}
	if _, ok := activity.RequestMetaFromContext(ctx); !ok && e.Meta == nil {
		return nil
	}
	_, err := activity.Record(ctx, q, e)
	return err
}

// AddEvent пишет событие клуба в outbox в транзакции q (docs/realtime.md).
func AddEvent(ctx context.Context, q *store.Queries, clubID int64, typ string, data any) error {
	_, err := outbox.Add(ctx, q, outbox.Event{ClubID: clubID, Type: typ, Data: data})
	return err
}

// BalanceEvent — data события balance.changed (тиыны).
func BalanceEvent(userID int64, main, bonus money.Tiyn) map[string]any {
	return map[string]any{"user_id": userID, "balance": int64(main), "bonus_balance": int64(bonus)}
}

// PHPFloat — число так, как его писал json_encode() PHP для float (serialize_precision = -1): кратчайшая
// запись, у целых — ".0" ("1500.0", "1500.5"). Так в details журнала старого кода лежат суммы в тенге,
// посчитанные во float ((float) $amount, $total).
func PHPFloat(t money.Tiyn) json.Number {
	s := strconv.FormatFloat(t.Float(), 'f', -1, 64)
	if !strings.Contains(s, ".") {
		s += ".0"
	}
	return json.Number(s)
}

// ISO8601 — Carbon::toIso8601String() в UTC ("2026-10-15T11:30:00+00:00").
func ISO8601(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05-07:00") }

// computerEvent — data события computer.state (дифф одного ПК): сессия после действия. Статус связи
// (online/offline) знает только присутствие агента — его поля сюда не входят (клиент сливает дифф).
func computerEvent(computerID int64, sessionID string, userID int64, guest bool, endsAt *time.Time) map[string]any {
	ev := map[string]any{"computer_id": computerID, "session": nil}
	if sessionID != "" && endsAt != nil {
		var user any
		if userID > 0 {
			user = userID
		}
		ev["session"] = map[string]any{"id": sessionID, "user_id": user, "guest": guest, "ends_at": endsAt.UTC().Format(time.RFC3339)}
		ev["status"] = "busy"
	}
	return ev
}

// clubCurrency — data_get(settings, 'finances.currency', 'KZT').
func clubCurrency(settings []byte) string {
	var root struct {
		Finances struct {
			Currency any `json:"currency"`
		} `json:"finances"`
	}
	if json.Unmarshal(settings, &root) != nil {
		return "KZT"
	}
	if c, ok := root.Finances.Currency.(string); ok && c != "" {
		return c
	}
	return "KZT"
}

// RecordBalanceTransferred — журнал balance.transferred и события balance.changed обоих клиентов после
// billing.FriendTransfer / billing.ConfirmFriendTransferCode, в той же транзакции q (вызывает слой гостя).
// Laravel: app/Services/Friends.php transfer() (стр. 143–151): предмет — друг (перевод виден в истории обоих).
func RecordBalanceTransferred(ctx context.Context, q *store.Queries, clubID, userID, friendshipID int64, res billing.FriendTransferResult) error {
	names, err := q.ListClubUserNames(ctx, store.ListClubUserNamesParams{ClubID: clubID, Ids: []int64{userID, res.FriendID}})
	if err != nil {
		return err
	}
	byID := make(map[int64]string, len(names))
	for _, n := range names {
		byID[n.ID] = n.Name
	}
	amount := res.Amount.Tenge()
	if err := RecordActivity(ctx, q, activity.Entry{
		ClubID: clubID, Event: "balance.transferred",
		Description: byID[userID] + " перевёл другу " + byID[res.FriendID] + " " + amount,
		UserID:      userID, SubjectType: SubjectUser, SubjectID: res.FriendID,
		Details: map[string]any{"amount": amount, "to_user_id": res.FriendID, "friendship_id": friendshipID},
	}); err != nil {
		return err
	}
	for _, id := range []int64{userID, res.FriendID} {
		mb, err := q.GetMemberBalance(ctx, store.GetMemberBalanceParams{ClubID: clubID, UserID: id})
		if err != nil {
			return err
		}
		if err := AddEvent(ctx, q, clubID, "balance.changed", BalanceEvent(id, money.Tiyn(mb.Balance), money.Tiyn(mb.BonusBalance))); err != nil {
			return err
		}
	}
	return nil
}
