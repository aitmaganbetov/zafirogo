package session

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/money"
)

// Kind — вид части оплаты сессии (session_purchases.kind).
type Kind string

// Виды частей. Laravel: app/Models/SessionPurchase.php KIND_TARIFF, KIND_MINUTES.
const (
	KindTariff  Kind = "tariff"  // тариф или запущенный пакет
	KindMinutes Kind = "minutes" // время поминутно по цене часа зоны
)

// Purchase — часть оплаты сессии (строка session_purchases).
type Purchase struct {
	ID         int64 // порядок покупок: возврат идёт от большего ID к меньшему
	UserID     int64
	Kind       Kind
	TariffID   *int64 // nil — поминутно или тариф удалён физически
	Name       string // имя тарифа или зоны
	Minutes    int64
	MainCents  money.Tiyn // оплачено с основного баланса
	BonusCents money.Tiyn // оплачено бонусами
	// ExpiresAt — только тариф с лимитом использования (и запущенный пакет): до этого момента
	// неиспользованное время сохраняется пакетом. nil — тариф без лимита, остаток сгорает.
	ExpiresAt *time.Time
}

// PartOutcome — что стало с одной частью при расчёте (для ledger минут и аудита).
type PartOutcome struct {
	PurchaseID int64
	// UnusedSeconds — сколько неиспользованных секунд пришлось на эту часть (min(left, minutes*60)).
	UnusedSeconds int64
	RefundMain    money.Tiyn
	RefundBonus   money.Tiyn
	KeptMinutes   int64 // минут сохранено пакетом
	// LostSeconds — неиспользованные секунды, за которые ничего не вернули: тариф без лимита, истёкший
	// тариф, чужая часть, неполная минута пакета.
	LostSeconds int64
	Outcome     Outcome
}

// Outcome — ветка расчёта части.
type Outcome string

// Ветки settle() для части.
const (
	OutcomeUsed            Outcome = "used"              // неиспользованного времени на часть не осталось
	OutcomeForeign         Outcome = "foreign"           // часть другого клиента: время «съедено», ничего не возвращается
	OutcomeRefunded        Outcome = "refunded"          // поминутка: деньги пропорционально
	OutcomeKept            Outcome = "kept"              // тариф с неистёкшим лимитом: целые минуты пакетом
	OutcomeLostNoLimit     Outcome = "lost_no_limit"     // тариф без лимита использования: не возвращается
	OutcomeLostExpired     Outcome = "lost_expired"      // лимит использования уже истёк
	OutcomeLostUnderMinute Outcome = "lost_under_minute" // у тарифа с лимитом осталось < 60 с
)

// KeptPackage — пакет времени, который нужно создать (строка time_packages).
type KeptPackage struct {
	SourcePurchaseID int64
	UserID           int64
	TariffID         *int64
	Name             string
	MinutesLeft      int64
	ExpiresAt        time.Time // тот же срок, что у части (не продлевается)
}

// Settlement — план расчёта досрочно завершённой сессии.
type Settlement struct {
	// SecondsLeft — неиспользованные целые секунды сессии на момент завершения.
	SecondsLeft int64
	RefundMain  money.Tiyn // вернуть на основной баланс
	RefundBonus money.Tiyn // вернуть на бонусный баланс
	KeptMinutes int64      // всего минут сохранено пакетами
	Packages    []KeptPackage
	Parts       []PartOutcome // в порядке расчёта (ID по убыванию)
	// DeletePurchaseIDs — удалить ВСЕ части сессии (включая чужие и использованные).
	DeletePurchaseIDs []int64
	// LoyaltySpendingDecrease — на сколько уменьшить накопительные траты клиента (BR-042: только
	// основной баланс; «не ниже нуля» — забота исполнителя, LoyaltyProgram::subtractSpending()).
	LoyaltySpendingDecrease money.Tiyn
	// LogRefunded / LogPackageKept — писать события time.refunded / time.package_kept.
	LogRefunded    bool
	LogPackageKept bool
}

// ErrInvalidPurchase — часть с отрицательными минутами или суммами (в БД это запрещают CHECK).
var ErrInvalidPurchase = errors.New("session: некорректная часть оплаты")

// Settle — расчёт досрочно завершённой сессии клиента.
//
// Laravel: app/Services/SessionSettlement.php SessionSettlement::settle() (стр. 26–79), BR-032.
//
//	parts = session_purchases WHERE session_id ORDER BY id DESC     // от последней покупки к первой
//	left  = endsAt == null ? 0 : max(0, endsAt->getTimestamp() - now()->getTimestamp())
//	для каждой части: seconds = min(left, minutes*60); left -= seconds;
//	    seconds <= 0 или чужая часть → пропуск (время чужой части всё равно «съедено»);
//	    minutes → intdiv(main_cents*seconds, total), intdiv(bonus_cents*seconds, total) — по каждой
//	              части и каждому балансу отдельно, дробь тиына остаётся клубу;
//	    иначе, если expires_at в будущем и intdiv(seconds, 60) > 0 → пакет целыми минутами.
//	удалить все части; если возврат > 0 — балансы += возврат, траты −= refund_main.
//
// customerID — current_session_user_id ПК до завершения (для гостевой сессии settle() не вызывается,
// BR-031). endsAt — session_ends_at ПК до завершения (nil у старых агентов без оплаты).
func Settle(purchases []Purchase, customerID int64, endsAt *time.Time, now time.Time) (Settlement, error) {
	var res Settlement

	parts := slices.Clone(purchases)
	// Laravel: ->orderByDesc('id').
	slices.SortStableFunc(parts, func(a, b Purchase) int {
		switch {
		case a.ID > b.ID:
			return -1
		case a.ID < b.ID:
			return 1
		}
		return 0
	})

	// Laravel: $left = $endsAt === null ? 0 : max(0, $endsAt->getTimestamp() - now()->getTimestamp());
	// getTimestamp() — целые секунды Unix (доли отброшены у обоих), поэтому now = 10:00:00.9 и
	// конец 10:30:00 дают 1800 с, а не 1799.
	res.SecondsLeft = SecondsUntil(endsAt, now)
	left := res.SecondsLeft

	for _, p := range parts {
		if p.Minutes < 0 || p.MainCents < 0 || p.BonusCents < 0 {
			return Settlement{}, fmt.Errorf("%w: id %d", ErrInvalidPurchase, p.ID)
		}
		res.DeletePurchaseIDs = append(res.DeletePurchaseIDs, p.ID)

		total, err := money.MulChecked(p.Minutes, 60)
		if err != nil {
			return Settlement{}, fmt.Errorf("%w: id %d: %w", ErrInvalidPurchase, p.ID, err)
		}
		seconds := min(left, total)
		left -= seconds
		out := PartOutcome{PurchaseID: p.ID, UnusedSeconds: seconds}

		switch {
		case seconds <= 0:
			out.Outcome = OutcomeUsed
		case p.UserID != customerID:
			// Laravel: `$part->user_id !== $userId` → continue: время списано с left, но не возвращается.
			out.Outcome = OutcomeForeign
			out.LostSeconds = seconds
		case p.Kind == KindMinutes:
			// Laravel: $result['refund_main'] += intdiv($part->main_cents * $seconds, $total);
			main, err := money.MulDivRound(int64(p.MainCents), seconds, 0, total)
			if err != nil {
				return Settlement{}, err
			}
			// Laravel: $result['refund_bonus'] += intdiv($part->bonus_cents * $seconds, $total);
			bonus, err := money.MulDivRound(int64(p.BonusCents), seconds, 0, total)
			if err != nil {
				return Settlement{}, err
			}
			out.RefundMain, out.RefundBonus = money.Tiyn(main), money.Tiyn(bonus)
			out.Outcome = OutcomeRefunded
			if res.RefundMain, err = money.Add(res.RefundMain, out.RefundMain); err != nil {
				return Settlement{}, err
			}
			if res.RefundBonus, err = money.Add(res.RefundBonus, out.RefundBonus); err != nil {
				return Settlement{}, err
			}
		default:
			// Laravel: elseif ($part->expires_at?->isFuture() && ($minutes = intdiv($seconds, 60)) > 0)
			// Любой вид, кроме minutes, идёт сюда (как в PHP: проверяется только KIND_MINUTES).
			switch {
			case p.ExpiresAt == nil:
				out.Outcome = OutcomeLostNoLimit
				out.LostSeconds = seconds
			case !p.ExpiresAt.After(now): // Carbon isFuture(): expires_at > now() с микросекундами
				out.Outcome = OutcomeLostExpired
				out.LostSeconds = seconds
			case money.IntDiv(seconds, 60) <= 0:
				out.Outcome = OutcomeLostUnderMinute
				out.LostSeconds = seconds
			default:
				m := money.IntDiv(seconds, 60)
				out.KeptMinutes = m
				out.LostSeconds = seconds - m*60 // неполная минута пропадает
				out.Outcome = OutcomeKept
				res.KeptMinutes += m
				res.Packages = append(res.Packages, KeptPackage{
					SourcePurchaseID: p.ID,
					UserID:           customerID,
					TariffID:         p.TariffID,
					Name:             p.Name,
					MinutesLeft:      m,
					ExpiresAt:        StoredTime(*p.ExpiresAt),
				})
			}
		}
		res.Parts = append(res.Parts, out)
	}

	// Laravel: if ($result['refund_main'] + $result['refund_bonus'] > 0) { …; $this->loyalty->subtractSpending($userId, $result['refund_main']); }
	// subtractSpending() сам ничего не делает при cents <= 0, поэтому уменьшение = refund_main.
	if res.RefundMain+res.RefundBonus > 0 {
		res.LoyaltySpendingDecrease = res.RefundMain
		res.LogRefunded = true
	}
	res.LogPackageKept = res.KeptMinutes > 0
	return res, nil
}

// SecondsUntil — max(0, endsAt->getTimestamp() - now()->getTimestamp()); nil → 0.
// Laravel: app/Services/SessionSettlement.php settle() стр. 31.
func SecondsUntil(endsAt *time.Time, now time.Time) int64 {
	if endsAt == nil {
		return 0
	}
	return max(0, endsAt.Unix()-now.Unix())
}

// StoredTime — время так, как его сохранил бы старый код в MySQL: Eloquent форматирует дату
// `Y-m-d H:i:s`, доли секунды отбрасываются (усечение, не округление). Монотонные часы снимаются.
func StoredTime(t time.Time) time.Time {
	return t.Truncate(time.Second)
}
