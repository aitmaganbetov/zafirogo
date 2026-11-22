package reservations

// Оплаченная бронь из гостевого приложения: запуск оплаченного времени на ПК и возврат при отмене.
// Laravel: app/Services/ComputerReservations.php startPaidSession(), startForSignedIn(), startByCode(), refund(),
// appBookingEnabled(), guestMayCancel(). Правила — docs/business-rules.md BR-178, BR-179; решения —
// docs/open-questions/upstream-bookings.md.
//
// Деньги: оплата брони уже проведена операцией ledger reservation_payment (app.go Book). Запуск времени денег не
// двигает (в часть сессии пишутся суммы для возврата при досрочном завершении, как у покупки минут); возврат
// отменённой брони — операция reservation_refund с ключом reservation_refund:<id брони>.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aitmaganbetov/zafirogo/server/internal/activity"
	"github.com/aitmaganbetov/zafirogo/server/internal/billing"
	billingsvc "github.com/aitmaganbetov/zafirogo/server/internal/billing/service"
	"github.com/aitmaganbetov/zafirogo/server/internal/billing/session"
	"github.com/aitmaganbetov/zafirogo/server/internal/club"
	"github.com/aitmaganbetov/zafirogo/server/internal/i18n"
	"github.com/aitmaganbetov/zafirogo/server/internal/ledger"
	"github.com/aitmaganbetov/zafirogo/server/internal/money"
	"github.com/aitmaganbetov/zafirogo/server/internal/store"
)

// Тексты оплаченной брони (ключи перевода).
const (
	// MsgBusyOther — close(): «Клиент пришёл» по оплаченной брони, а ПК занят другим.
	MsgBusyOther = "На ПК идёт сессия другого клиента: оплаченное время брони начнётся, когда ПК освободится."
	// Код брони на экране входа ПК — startByCode(), AgentBookingCodeController.
	MsgCodeFormat   = "Код брони — 6 цифр."
	MsgCodeOtherPC  = "Этот код брони не для этого ПК: сядьте за забронированный ПК."
	MsgCodeNotFound = "Код брони не найден или бронь уже закончилась."
	MsgCodeEarly    = "Бронь начнётся в :time. Код заработает за :count мин до начала."
	MsgCodeBusy     = "На этом ПК уже идёт сессия."
	// MsgCodeOpened — ответ AgentBookingCodeController.
	MsgCodeOpened = "Бронь открыта: приятной игры!"

	logArrivedByCode = "ПК :computer открыт по коду брони на :from"
	logRefunded      = "Возврат за отменённую бронь ПК :computer"
	// logCutRefunded — время брони, добавленное к своей сессии, упёрлось в чужую бронь (ревью 22.11.2026, в старом нет).
	logCutRefunded = "Возврат за время брони ПК :computer, не поместившееся до следующей брони"
	// partName — __('Бронь'): имя почасовой части сессии, начатой бронью.
	partName    = "Бронь"
	subjectUser = `App\Models\User`
)

var codeRe = regexp.MustCompile(`^[0-9]{6}$`)

// Env — окружение операции с бронью.
type Env struct {
	// Now — текущее время (часы вызывающего).
	Now time.Time
	// Actor — кто действует: журнал действий, ledger, аудит.
	Actor billing.BalanceActor
	// Notifier — уведомления агента после коммита (сессия на ПК, баланс гостя); nil — без уведомлений.
	// Работают, только если транзакцией владеет слой с billingsvc.CommitHooks (httpx.Deps.Tx) или вызывающий
	// сам выполнит действия после коммита.
	Notifier billingsvc.SessionNotifier
	// T — перевод текста, который сохраняется в базе как есть (имя части сессии «Бронь»); nil — русский.
	T func(key string, params map[string]string) string
	// Meta — данные запроса для журнала действий (IP); nil — из контекста (HTTP, gRPC). Фоновая задача задаёт сама.
	Meta *activity.RequestMeta
}

func (e Env) t(key string) string {
	if e.T != nil {
		return e.T(key, nil)
	}
	return i18n.Render(i18n.SourceLocale, key, nil)
}

// settingFlag — filter_var($settings['club'][$key] ?? $def, FILTER_VALIDATE_BOOLEAN).
func settingFlag(settings []byte, key string, def bool) bool {
	var root struct {
		Club map[string]any `json:"club"`
	}
	if json.Unmarshal(settings, &root) != nil {
		return def
	}
	switch v := root.Club[key].(type) {
	case nil:
		return def
	case bool:
		return v
	case float64:
		return v == 1
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "1", "true", "on", "yes":
			return true
		}
	}
	return false
}

// AppBookingEnabled — ComputerReservations::appBookingEnabled(): гости бронируют и оплачивают ПК в приложении
// (настройка клуба club.app_booking_enabled, по умолчанию выключено).
func AppBookingEnabled(settings []byte) bool {
	return settingFlag(settings, "app_booking_enabled", false)
}

// GuestMayCancel — ComputerReservations::guestMayCancel(): гость может отменить оплаченную бронь до начала и
// получить деньги назад (club.app_booking_cancel, по умолчанию включено).
func GuestMayCancel(settings []byte) bool { return settingFlag(settings, "app_booking_cancel", true) }

// IsPaid — ComputerReservation::isPaid().
func IsPaid(paidMain, paidBonus int64) bool { return paidMain+paidBonus > 0 }

// hasPaidSession — ClubComputer::hasPaidSession() (BR-030).
func hasPaidSession(pc store.LockComputerRow, now time.Time) bool {
	return session.Computer{
		SessionID: pc.CurrentSessionID, SessionUserID: pc.CurrentSessionUserID, SessionGuest: pc.SessionGuest,
		SessionEndsAt: pc.SessionEndsAt,
	}.HasPaidSession(now)
}

// Started — сессия ПК после запуска оплаченного времени брони.
type Started struct {
	ReservationID int64
	ComputerID    int64
	SessionID     string
	// UserID — гость сессии; 0 — сессия без аккаунта (по коду).
	UserID  int64
	Guest   bool
	EndsAt  time.Time
	Minutes int64
	// Extends — время добавлено к идущей сессии гостя (тот же session_id).
	Extends bool
	// CutMinutes — минуты брони, не поместившиеся до чужой брони этого ПК (только при Extends): они возвращены
	// деньгами (RefundMain / RefundBonus) или остались пакетом (KeptPackageID). Minutes — уже без них.
	CutMinutes              int64
	RefundMain, RefundBonus money.Tiyn
	KeptPackageID           int64
}

// startPaidSession — запуск оплаченного времени брони на заблокированном ПК или добавление его к своей сессии
// гостя. false — запускать нельзя (ПК занят другим или бронь закончилась).
//
// Laravel: app/Services/ComputerReservations.php startPaidSession() (BR-178):
//
//	$own = $computer->hasPaidSession();
//	if ($own && ($byCode || $computer->session_guest || $computer->current_session_user_id !== $reservation->user_id)) return false;
//	…PlanStart…  if ($minutes <= 0) return false;
//	$sessionId = $own ? $computer->current_session_id : (string) Str::uuid();
//	$endsAt = ($own ? $computer->session_ends_at->copy() : now())->addMinutes($minutes);
//
// По коду — гостевая сессия без аккаунта и без части сессии (время оплачено бронью и не возвращается, UQ-15).
// Иначе — часть сессии: бронь тарифом — вид tariff (лимит использования → expires_at), по часам — minutes с именем
// «Бронь»; суммы части — пропорционально начатому времени (PlanStart).
//
// Отличие от Laravel (ревью 22.11.2026, docs/DECISIONS.md): время, добавляемое к своей сессии гостя, не заходит на
// чужую бронь этого ПК. В старом конец сессии сдвигался без проверки (session_ends_at + минуты) и мог наехать на
// следующую бронь: G — бронь 18–19, H — 19:30–20:30, интервал 30; G в 17:00 купил 2 часа → сессия до 20:00. Предел
// тот же, что у продажи времени (AgentTariffPurchaseService::sell() → ComputerReservations::blocking()): конец
// сессии + интервал ≤ начало чужой ожидающей брони. Не поместившиеся минуты не теряются: у брони тарифом с лимитом
// использования остаются пакетом (как урезанное время тарифа в purchase(), BR-020: TimePackage + ledger
// package_kept), иначе их доля оплаты возвращается на балансы (ledger reservation_refund, ключ
// reservation_refund:<id>:cut; траты лояльности уменьшаются на основную часть, как в refund()).
func startPaidSession(ctx context.Context, q *store.Queries, info club.Info, r store.LockReservationRow, pc store.LockComputerRow,
	byCode bool, env Env) (Started, bool, error) {
	now, clubID := env.Now, info.ID
	own := hasPaidSession(pc, now)
	if own && (byCode || pc.SessionGuest || pc.CurrentSessionUserID == nil || r.UserID == nil || *pc.CurrentSessionUserID != *r.UserID) {
		return Started{}, false, nil
	}
	if !byCode && r.UserID == nil {
		// Аккаунт гостя удалён (user_id обнулён внешним ключом): сессию с частью открыть не на кого.
		return Started{}, false, nil
	}
	plan := PlanStart(r.StartsAt, r.EndsAt, now, money.Tiyn(r.PaidMainCents), money.Tiyn(r.PaidBonusCents))
	if plan.Minutes <= 0 {
		return Started{}, false, nil
	}
	add := time.Duration(plan.Minutes) * time.Minute
	out := Started{ReservationID: r.ID, ComputerID: pc.ID, Minutes: plan.Minutes}

	if byCode {
		// Someone with the code, without an account: the agent opens the PC as for time sold at the cash register.
		out.SessionID, out.Guest, out.EndsAt = billingsvc.NewSessionID(), true, session.StoredTime(now.Add(add))
		if err := q.SetComputerSession(ctx, store.SetComputerSessionParams{
			ClubID: clubID, ID: pc.ID, SessionID: &out.SessionID, SessionUserID: nil, SessionGuest: true,
			SessionEndsAt: &out.EndsAt, Now: &now,
		}); err != nil {
			return Started{}, false, err
		}
	} else {
		out.UserID = *r.UserID
		if own {
			// Чужая бронь до нового конца сессии + интервал: время добавляется только до неё.
			gap := time.Duration(info.BookingGapMinutes()) * time.Minute
			b, err := q.GetBlockingReservation(ctx, store.GetBlockingReservationParams{
				ClubID: clubID, ComputerID: pc.ID, UserID: r.UserID, FromAt: now, Until: pc.SessionEndsAt.Add(add + gap),
			})
			switch {
			case err == nil:
				var fit int64
				if limit := b.StartsAt.Add(-gap); limit.After(*pc.SessionEndsAt) {
					fit = min(plan.Minutes, int64(limit.Sub(*pc.SessionEndsAt)/time.Minute))
				}
				out.CutMinutes, out.Minutes = plan.Minutes-fit, fit
				add = time.Duration(fit) * time.Minute
			case !errors.Is(err, pgx.ErrNoRows):
				return Started{}, false, err
			}
			out.Extends, out.SessionID, out.EndsAt = true, *pc.CurrentSessionID, session.StoredTime(pc.SessionEndsAt.Add(add))
		} else {
			out.SessionID, out.EndsAt = billingsvc.NewSessionID(), session.StoredTime(now.Add(add))
		}
		// Booked as a tariff, the time is that tariff's (not refunded; a usage limit keeps the rest).
		// Источник части — booking: в Laravel запуск брони пишет только reservation.arrived, и зоны пересадки
		// (SeatTransfers::allowedZoneIds() — последняя tariff.purchased|time.purchased сессии) его не видят.
		kind, name, source := session.KindMinutes, env.t(partName), string(session.SourceBooking)
		var tariffID *int64
		var expiresAt *time.Time
		if r.TariffID != nil {
			t, err := q.AppBookingTariff(ctx, store.AppBookingTariffParams{ClubID: clubID, ID: *r.TariffID})
			switch {
			case err == nil:
				kind, name, tariffID = session.KindTariff, t.Name, &t.ID
				if t.UsageLimitEnabled && t.UsageLimitMinutes != nil {
					at := session.StoredTime(now.Add(time.Duration(*t.UsageLimitMinutes) * time.Minute))
					expiresAt = &at
				}
			case !errors.Is(err, pgx.ErrNoRows): // тариф удалён — как бронь по часам (Tariff::find() → null)
				return Started{}, false, err
			}
		}
		partMain, partBonus := plan.Main, plan.Bonus
		keep := out.CutMinutes > 0 && tariffID != nil && expiresAt != nil
		if out.CutMinutes > 0 && !keep {
			// Доля оплаты не поместившихся минут — назад; у части сессии остаётся доля начатых (вниз, как PlanStart).
			partMain = money.Tiyn(mulDiv(r.PaidMainCents, out.Minutes, plan.Duration))
			partBonus = money.Tiyn(mulDiv(r.PaidBonusCents, out.Minutes, plan.Duration))
			out.RefundMain, out.RefundBonus = plan.Main-partMain, plan.Bonus-partBonus
		}
		if out.Minutes > 0 {
			zone := pc.ClubZoneID
			if _, err := q.InsertSessionPurchase(ctx, store.InsertSessionPurchaseParams{
				ClubID: clubID, SessionID: out.SessionID, UserID: out.UserID, Kind: string(kind), TariffID: tariffID, Name: name,
				Minutes: out.Minutes, MainCents: int64(partMain), BonusCents: int64(partBonus), ExpiresAt: expiresAt,
				ZoneID: &zone, Source: &source, Now: &now,
			}); err != nil {
				return Started{}, false, err
			}
			if err := q.SetComputerSession(ctx, store.SetComputerSessionParams{
				ClubID: clubID, ID: pc.ID, SessionID: &out.SessionID, SessionUserID: &out.UserID, SessionGuest: false,
				SessionEndsAt: &out.EndsAt, Now: &now,
			}); err != nil {
				return Started{}, false, err
			}
		}
		if keep {
			if err := keepCut(ctx, q, clubID, r.ID, pc.ID, *tariffID, name, *expiresAt, env, &out); err != nil {
				return Started{}, false, err
			}
		}
	}
	if err := q.ArriveReservation(ctx, store.ArriveReservationParams{ClubID: clubID, ID: r.ID, Now: &now}); err != nil {
		return Started{}, false, err
	}
	if out.RefundMain+out.RefundBonus > 0 {
		computer := club.ComputerName(pc.DisplayName, pc.DeviceName, pc.Number)
		if err := refundAmounts(ctx, q, info, r, out.RefundMain, out.RefundBonus, ":cut", logCutRefunded,
			map[string]any{"reason": "next_booking", "cut_minutes": out.CutMinutes}, computer, env); err != nil {
			return Started{}, false, err
		}
	}
	if out.Extends && out.Minutes == 0 {
		// Сессия ПК не изменилась: ни события карты, ни команды агенту.
		return out, true, nil
	}
	// Карта клуба: сессия на ПК (docs/realtime.md); агенту — после коммита.
	ends := out.EndsAt
	if err := billingsvc.AddEvent(ctx, q, clubID, "computer.state",
		billingsvc.ComputerStateEvent(pc.ID, out.SessionID, out.UserID, out.Guest, &ends)); err != nil {
		return Started{}, false, err
	}
	if env.Notifier != nil {
		s, issuedBy := out, env.Actor.UserID
		started := now
		if s.Extends {
			started = time.Time{}
		}
		billingsvc.AfterCommit(ctx, func(ctx context.Context) {
			billingsvc.NotifySessionStarted(ctx, env.Notifier, clubID, s.ComputerID, s.SessionID, s.UserID, s.Guest, started, s.EndsAt, issuedBy)
		})
	}
	return out, true, nil
}

// keepCut — не поместившиеся минуты брони тарифом с лимитом использования остаются пакетом до конца лимита.
// Laravel: по образцу AgentTariffPurchaseService::purchase() (BR-020): TimePackage::create(['minutes_left' => $rest,
// 'expires_at' => $part['expires_at']]); ledger минут — package_kept, ключ reservation_kept:<id брони>.
func keepCut(ctx context.Context, q *store.Queries, clubID, reservationID, computerID, tariffID int64, name string,
	expiresAt time.Time, env Env, out *Started) error {
	now := env.Now
	id, err := q.InsertTimePackage(ctx, store.InsertTimePackageParams{
		ClubID: clubID, UserID: out.UserID, TariffID: &tariffID, Name: name, MinutesLeft: out.CutMinutes,
		ExpiresAt: expiresAt, Now: &now,
	})
	if err != nil {
		return err
	}
	meta, err := json.Marshal(map[string]any{
		"reason": "next_booking", "kept_minutes": out.CutMinutes, "started_minutes": out.Minutes,
		"computer_id": computerID, "session_id": out.SessionID, "reservation_id": reservationID,
	})
	if err != nil {
		return err
	}
	if _, err := ledger.Post(ctx, q, ledger.Operation{
		ClubID: clubID, Kind: ledger.KindPackageKept, IdempotencyKey: "reservation_kept:" + strconv.FormatInt(reservationID, 10),
		Minutes: []ledger.MinuteEntry{{UserID: out.UserID, TimePackageID: id, TariffID: tariffID, Minutes: out.CutMinutes}},
		Meta:    meta, ActorUserID: env.Actor.UserID, Refs: ledger.Refs{SessionID: out.SessionID, ReservationID: reservationID},
	}); err != nil {
		return err
	}
	out.KeptPackageID = id
	return nil
}

// refund — возврат оплаты отменённой брони на те балансы, с которых платили; накопительные траты уменьшаются на
// основную часть (BR-179).
//
// Laravel: app/Services/ComputerReservations.php refund():
//
//	'balance'       => formatCents(amountToCents($balance->balance)       + $reservation->paid_main_cents),
//	'bonus_balance' => formatCents(amountToCents($balance->bonus_balance) + $reservation->paid_bonus_cents),
//	$this->loyalty->subtractSpending($reservation->user_id, $reservation->paid_main_cents);
//
// В старом коде при отсутствии строки баланса возврат молча пропускался — деньги гостя терялись (GAP-043).
// Решение UQ-9 (по образцу Q-012): строка баланса создаётся (нулевая), и возврат зачисляется. Не вернуть можно
// только тому, кого в клубе больше нет (аккаунт удалён или вышел из клуба — строку баланса создать не на кого):
// как в старом, бронь отменяется без возврата.
// Ledger: reservation_refund, обратные проводки оплаты (revenue_time → member_main, bonus_fund → member_bonus),
// ключ reservation_refund:<id брони> — повторная отмена не зачислит дважды.
func refund(ctx context.Context, q *store.Queries, info club.Info, r store.LockReservationRow, computer string, env Env) error {
	return refundAmounts(ctx, q, info, r, money.Tiyn(r.PaidMainCents), money.Tiyn(r.PaidBonusCents), "", logRefunded, nil, computer, env)
}

// refundAmounts — возврат части или всей оплаты брони (refund(); keySuffix и logKey различают полный возврат
// отменённой брони и возврат не поместившегося времени — startPaidSession).
func refundAmounts(ctx context.Context, q *store.Queries, info club.Info, r store.LockReservationRow, main, bonus money.Tiyn,
	keySuffix, logKey string, extra map[string]any, computer string, env Env) error {
	if r.UserID == nil {
		return nil
	}
	userID := *r.UserID
	if _, err := q.GetMembershipForPricing(ctx, store.GetMembershipForPricingParams{ClubID: info.ID, UserID: userID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	}
	_, err := ledger.LockBalances(ctx, q, info.ID, userID)
	if errors.Is(err, ledger.ErrBalanceNotFound) {
		if err = q.EnsureMemberBalance(ctx, store.EnsureMemberBalanceParams{ClubID: info.ID, UserID: userID}); err != nil {
			return err
		}
		_, err = ledger.LockBalances(ctx, q, info.ID, userID)
	}
	if err != nil {
		return err
	}
	// Операция оплаты — у первой брони группы ПК (у броней, перенесённых из Laravel, её нет).
	first := r.ID
	if r.Code != nil {
		if first, err = q.AppBookingFirstOfGroup(ctx, store.AppBookingFirstOfGroupParams{ClubID: info.ID, UserID: r.UserID, Code: *r.Code}); err != nil {
			return err
		}
	}
	payment, err := q.AppBookingPaymentTx(ctx, store.AppBookingPaymentTxParams{ClubID: info.ID, ReservationID: first})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	fields := map[string]any{"reservation_id": r.ID, "computer_id": r.ClubComputerID, "code": r.Code}
	for k, v := range extra {
		fields[k] = v
	}
	meta, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	res, err := billing.Credit(ctx, q, billing.ChargeParams{
		ClubID: info.ID, UserID: userID, Kind: ledger.KindReservationRefund, Revenue: ledger.AccountRevenueTime,
		Main: main, Bonus: bonus,
		IdempotencyKey: "reservation_refund:" + strconv.FormatInt(r.ID, 10) + keySuffix,
		Refs:           ledger.Refs{ReservationID: r.ID, ReversesTransactionID: payment},
		Meta:           meta, Actor: env.Actor,
	})
	if err != nil {
		return err
	}
	if res.Ledger.Replayed {
		return nil
	}
	if err := billing.SubtractSpending(ctx, q, info.ID, userID, main, env.Now); err != nil {
		return err
	}
	if env.Actor.UserID > 0 {
		if _, err := activity.Record(ctx, q, activity.Entry{
			ClubID: info.ID, Event: "reservation.refunded", UserID: env.Actor.UserID, SubjectType: subjectUser, SubjectID: userID,
			At: env.Now, Meta: env.Meta,
			Details: map[string]any{
				"main_balance_amount":  main.Tenge(),
				"bonus_balance_amount": bonus.Tenge(),
				"reservation_id":       r.ID,
			},
		}.Translated(logKey, map[string]string{"computer": computer})); err != nil {
			return err
		}
	}
	return balanceChanged(ctx, q, info, userID, env)
}

// balanceChanged — событие balance.changed (панель) и BalanceChanged агенту после коммита (гость может сидеть на ПК).
func balanceChanged(ctx context.Context, q *store.Queries, info club.Info, userID int64, env Env) error {
	mb, err := q.GetMemberBalance(ctx, store.GetMemberBalanceParams{ClubID: info.ID, UserID: userID})
	if err != nil {
		return err
	}
	main, bonus := money.Tiyn(mb.Balance), money.Tiyn(mb.BonusBalance)
	if err := billingsvc.AddEvent(ctx, q, info.ID, "balance.changed", billingsvc.BalanceEvent(userID, main, bonus)); err != nil {
		return err
	}
	if env.Notifier != nil {
		currency := info.Currency()
		billingsvc.AfterCommit(ctx, func(ctx context.Context) {
			billingsvc.NotifyBalance(ctx, env.Notifier, info.ID, userID, main, bonus, currency)
		})
	}
	return nil
}

// StartForSignedIn — гость вошёл на ПК своей оплаченной брони: её время начинается (с интервала до начала).
// started = false — запускать нечего или нельзя (ничего не изменено).
//
// Laravel: app/Services/ComputerReservations.php startForSignedIn() — в старом вызывался на каждом heartbeat ПК с
// токеном клиента. Здесь (UQ-11) его вызывают событие входа гостя на ПК и фоновая задача по времени начала
// (worker.StartAppBookings) — не каждый пинг; условия те же: ПК свободен или на нём своя сессия гостя; бронь этого
// гостя на этом ПК ожидающая, оплаченная, starts_at <= now + интервал, ends_at > now; самая ранняя. Порядок
// блокировок: ПК → бронь. Журнал reservation.arrived — от имени гостя.
func StartForSignedIn(ctx context.Context, q *store.Queries, info club.Info, computerID, userID int64, env Env) (Started, bool, error) {
	pc, err := q.LockComputer(ctx, store.LockComputerParams{ClubID: info.ID, ID: computerID})
	if errors.Is(err, pgx.ErrNoRows) {
		return Started{}, false, ErrNotFound
	}
	if err != nil {
		return Started{}, false, err
	}
	if hasPaidSession(pc, env.Now) && (pc.SessionGuest || pc.CurrentSessionUserID == nil || *pc.CurrentSessionUserID != userID) {
		return Started{}, false, nil
	}
	gap := time.Duration(info.BookingGapMinutes()) * time.Minute
	id, err := q.AppBookingDue(ctx, store.AppBookingDueParams{
		ClubID: info.ID, ComputerID: pc.ID, UserID: &userID, Until: env.Now.Add(gap), Now: env.Now,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Started{}, false, nil
	}
	if err != nil {
		return Started{}, false, err
	}
	r, err := q.LockReservation(ctx, store.LockReservationParams{ClubID: info.ID, ID: id})
	if err != nil {
		return Started{}, false, err
	}
	out, ok, err := startPaidSession(ctx, q, info, r, pc, false, env)
	if err != nil || !ok {
		return Started{}, false, err
	}
	computer := club.ComputerName(pc.DisplayName, pc.DeviceName, pc.Number)
	from := r.StartsAt.In(info.Loc)
	if _, err := activity.Record(ctx, q, activity.Entry{
		ClubID: info.ID, Event: "reservation.arrived", UserID: userID, SubjectType: subjectBooking, SubjectID: r.ID,
		At: env.Now, Meta: env.Meta,
		Details: map[string]any{"computer": computer, "starts_at": from.Format("02.01.2006 15:04")},
	}.Translated(logArrived, map[string]string{"computer": computer, "from": from.Format("02.01.2006 15:04")})); err != nil {
		return Started{}, false, err
	}
	return out, true, changed(ctx, q, info, r.ID, pc.ID, "arrived", from)
}

// StartByCode — код брони из приложения, набранный на экране входа ПК: если этот ПК — один из забронированных и
// бронь уже можно начинать (с интервала до начала), её оплаченное время начинается здесь сессией без аккаунта.
//
// Laravel: app/Services/ComputerReservations.php startByCode(); app/Http/Controllers/Api/AgentBookingCodeController.php
// (правило code: regex ^\d{6}$ — «Код брони — 6 цифр.»). Ошибки — *Error поля code:
//   - кода нет у ожидающих незакончившихся броней клуба — MsgCodeNotFound;
//   - код есть, но у брони другого ПК — MsgCodeOtherPC (ответы различаются, как в старом, UQ-14);
//   - рано (starts_at > now + интервал) — MsgCodeEarly (время начала — местное время клуба);
//   - на ПК идёт любая оплаченная сессия — MsgCodeBusy.
//
// Денег операция не двигает (оплата проведена при бронировании, UQ-15): сессия гостевая (user_id NULL), без
// части сессии — досрочное завершение ничего не возвращает. Журнал reservation.arrived с by_code — от имени гостя,
// сделавшего бронь. Лимит попыток (5/мин на ПК и 200/час на клуб, ratelimit.AgentBookingCode) — у вызывающего.
func StartByCode(ctx context.Context, q *store.Queries, info club.Info, computerID int64, code string, env Env) (Started, error) {
	if !codeRe.MatchString(code) {
		return Started{}, &Error{Field: "code", Message: MsgCodeFormat}
	}
	pc, err := q.LockComputer(ctx, store.LockComputerParams{ClubID: info.ID, ID: computerID})
	if errors.Is(err, pgx.ErrNoRows) {
		return Started{}, ErrNotFound
	}
	if err != nil {
		return Started{}, err
	}
	id, err := q.AppBookingByCode(ctx, store.AppBookingByCodeParams{ClubID: info.ID, ComputerID: pc.ID, Code: code, Now: env.Now})
	if errors.Is(err, pgx.ErrNoRows) {
		elsewhere, err := q.AppBookingCodeElsewhere(ctx, store.AppBookingCodeElsewhereParams{ClubID: info.ID, Code: code, Now: env.Now})
		if err != nil {
			return Started{}, err
		}
		if elsewhere {
			return Started{}, &Error{Field: "code", Message: MsgCodeOtherPC}
		}
		return Started{}, &Error{Field: "code", Message: MsgCodeNotFound}
	}
	if err != nil {
		return Started{}, err
	}
	r, err := q.LockReservation(ctx, store.LockReservationParams{ClubID: info.ID, ID: id})
	if err != nil {
		return Started{}, err
	}
	gapMinutes := info.BookingGapMinutes()
	from := r.StartsAt.In(info.Loc)
	if r.StartsAt.After(env.Now.Add(time.Duration(gapMinutes) * time.Minute)) {
		return Started{}, &Error{Field: "code", Message: MsgCodeEarly, Params: map[string]string{
			"time": from.Format("02.01 15:04"), "count": fmt.Sprint(gapMinutes),
		}}
	}
	out, ok, err := startPaidSession(ctx, q, info, r, pc, true, env)
	if err != nil {
		return Started{}, err
	}
	if !ok {
		return Started{}, &Error{Field: "code", Message: MsgCodeBusy}
	}
	computer := club.ComputerName(pc.DisplayName, pc.DeviceName, pc.Number)
	// Журнал — от имени гостя брони, пока он есть в клубе (запись журнала ссылается на членство).
	member := false
	if r.UserID != nil {
		_, err := q.GetMembershipForPricing(ctx, store.GetMembershipForPricingParams{ClubID: info.ID, UserID: *r.UserID})
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return Started{}, err
		}
		member = err == nil
	}
	if member {
		if _, err := activity.Record(ctx, q, activity.Entry{
			ClubID: info.ID, Event: "reservation.arrived", UserID: *r.UserID, SubjectType: subjectBooking, SubjectID: r.ID,
			At: env.Now, Meta: env.Meta,
			Details: map[string]any{"computer": computer, "starts_at": from.Format("02.01.2006 15:04"), "by_code": true},
		}.Translated(logArrivedByCode, map[string]string{"computer": computer, "from": from.Format("02.01.2006 15:04")})); err != nil {
			return Started{}, err
		}
	}
	return out, changed(ctx, q, info, r.ID, pc.ID, "arrived", from)
}
