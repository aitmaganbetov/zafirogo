package reservations

// Чистые расчёты брони из приложения: делёж бонусной части оплаты по ПК (BR-176) и оплаченное время при запуске
// (BR-178). Все суммы — целые тиыны; произведения считаются в 128 битах (bits.Mul64), чтобы не переполниться.

import (
	"math"
	"math/bits"
	"time"

	"github.com/aitmaganbetov/zafirogo/server/internal/money"
)

// mulDiv — intdiv($a * $b, $c) для неотрицательных a, b и c > 0 без переполнения произведения.
func mulDiv(a, b, c int64) int64 {
	if a <= 0 || b <= 0 || c <= 0 {
		return 0
	}
	hi, lo := bits.Mul64(uint64(a), uint64(b))
	if hi >= uint64(c) {
		return 0 // частное не помещается в 64 бита: при a ≤ c (доля от целого) сюда не попасть
	}
	q, _ := bits.Div64(hi, lo, uint64(c))
	if q > math.MaxInt64 {
		return 0 // недостижимо для доли от целого (см. выше)
	}
	return int64(q)
}

// SplitBonus делит бонусную часть оплаты группы по ПК пропорционально ценам (ПК — в порядке id).
//
// Laravel: app/Services/GuestBookings.php GuestBookings::book():
//
//	$pcBonus = $index === $computers->count() - 1 ? $bonusLeft
//	         : ($price === 0 ? 0 : intdiv($bonus * $prices[$computer->id], $price));
//	$bonusLeft -= $pcBonus;
//	'paid_main_cents' => $prices[$computer->id] - $pcBonus, 'paid_bonus_cents' => $pcBonus,
//
// Доля каждого ПК, кроме последнего, округляется вниз, последнему достаётся остаток. Остаток может превысить цену
// последнего ПК (три ПК и больше, оплата бонусами почти целиком: потерянные при округлении тиыны складываются) —
// в старом коде paid_main_cents становился отрицательным, MySQL отклонял запись, и бронь не создавалась
// (GAP-041). Решение UQ-8 (docs/DECISIONS.md): доля бонусов ПК не больше его цены, излишек последнего ПК
// отдаётся предыдущим ПК с конца (каждому — не больше его основной части). Там, где старый код работал, числа те
// же; сумма долей всегда равна bonus, а сумма основных частей — оплате с основного баланса.
//
// Требуется 0 ≤ bonus ≤ сумма цен (правила оплаты это гарантируют); иначе ok = false.
func SplitBonus(prices []money.Tiyn, bonus money.Tiyn) (shares []money.Tiyn, ok bool) {
	var total money.Tiyn
	for _, p := range prices {
		if p < 0 {
			return nil, false
		}
		total += p
	}
	if len(prices) == 0 || bonus < 0 || bonus > total {
		return nil, false
	}
	shares = make([]money.Tiyn, len(prices))
	left := bonus
	last := len(prices) - 1
	for i, p := range prices {
		if i == last {
			shares[i] = left
			break
		}
		if total != 0 {
			shares[i] = money.Tiyn(mulDiv(int64(bonus), int64(p), int64(total)))
		}
		left -= shares[i]
	}
	// UQ-8: излишек последнего ПК — предыдущим, с конца.
	if over := shares[last] - prices[last]; over > 0 {
		shares[last] = prices[last]
		for i := last - 1; i >= 0 && over > 0; i-- {
			take := min(over, prices[i]-shares[i])
			shares[i] += take
			over -= take
		}
		if over != 0 {
			return nil, false // недостижимо при bonus ≤ total
		}
	}
	return shares, true
}

// DurationMinutes — ComputerReservation::durationMinutes():
//
//	max(1, (int) round(($this->ends_at->getTimestamp() - $this->starts_at->getTimestamp()) / 60))
//
// Laravel: app/Models/ComputerReservation.php durationMinutes(). Секунды — целые, поэтому round() половиной от нуля
// равен intdiv(секунды + 30, 60) (дробь .5 у секунды/60 представима точно; сверено с PHP 8.4 — split_test.go).
func DurationMinutes(startsAt, endsAt time.Time) int64 {
	secs := endsAt.Unix() - startsAt.Unix()
	if secs <= 0 {
		return 1
	}
	return max(1, (secs+30)/60)
}

// StartPlan — сколько оплаченного времени брони начинается сейчас и с какими суммами части сессии.
type StartPlan struct {
	Duration int64      // длительность брони, мин
	Minutes  int64      // сколько начинается (≤ 0 — бронь закончилась, запускать нечего)
	Main     money.Tiyn // main_cents части сессии
	Bonus    money.Tiyn // bonus_cents части сессии
}

// PlanStart — оплаченное время брони при запуске в момент now.
//
// Laravel: app/Services/ComputerReservations.php startPaidSession():
//
//	$duration = $reservation->durationMinutes();
//	$minutes = min($duration, intdiv(max(0, $reservation->ends_at->getTimestamp() - now()->getTimestamp()) + 59, 60));
//	'main_cents'  => intdiv($reservation->paid_main_cents  * $minutes, $duration),
//	'bonus_cents' => intdiv($reservation->paid_bonus_cents * $minutes, $duration),
//
// Пришёл заранее или вовремя — вся длительность; опоздал — минуты до конца брони (вверх), деньги части —
// пропорционально, вниз, раздельно по основной и бонусной части; пропущенная доля не возвращается (BR-178).
func PlanStart(startsAt, endsAt, now time.Time, paidMain, paidBonus money.Tiyn) StartPlan {
	d := DurationMinutes(startsAt, endsAt)
	left := max(0, endsAt.Unix()-now.Unix())
	m := min(d, (left+59)/60)
	p := StartPlan{Duration: d, Minutes: m}
	if m > 0 {
		p.Main = money.Tiyn(mulDiv(int64(paidMain), m, d))
		p.Bonus = money.Tiyn(mulDiv(int64(paidBonus), m, d))
	}
	return p
}

// BookedMinutes — минуты брони тарифом: время тарифа, обрезанное концом его периода действия от начала брони.
//
// Laravel: app/Services/AgentTariffPurchaseService.php bookedMinutes():
//
//	$until = WeeklyPeriods::endOf($tariff->validity_periods, $localStart);
//	return $until === null ? $minutes : min($minutes, (int) ceil(($until->getTimestamp() - $localStart->getTimestamp()) / 60));
//
// until — конец периода действия, содержащего начало брони (нет ограничения — hasUntil = false). BR-175.
func BookedMinutes(effectiveMinutes int64, startsAt, until time.Time, hasUntil bool) int64 {
	if !hasUntil {
		return effectiveMinutes
	}
	secs := until.Unix() - startsAt.Unix()
	// (int) ceil($secs / 60) для целых секунд (отрицательные — к нулю, как ceil).
	var cut int64
	if secs > 0 {
		cut = (secs + 59) / 60
	} else {
		cut = -((-secs) / 60)
	}
	return min(effectiveMinutes, cut)
}
