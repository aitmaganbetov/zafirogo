package money

import (
	"math"
	"strconv"
	"strings"
)

// Буквальные переносы функций старого кода, которые новый сервер не вызывает (аудит BC-13): эталоны для сверки
// со старыми данными и ответами (BR-001, BR-002, решение Q-004) и для дифференциального теста против PHP
// (oracle_test.go). Живут в тестовом файле: документируют старое поведение, но в бинарник не попадают.

// LegacyAmountToCents — буквальный перенос AgentTariffPurchaseService::amountToCents()
// (Laravel: app/Services/AgentTariffPurchaseService.php amountToCents(), стр. 385–390; BR-001):
//
//	[$whole, $fraction] = array_pad(explode('.', $amount, 2), 2, '00');
//	return ((int) $whole * 100) + (int) str_pad(substr($fraction, 0, 2), 2, '0');
//
// Сохранены все особенности: дробная часть усекается ("12.999" → 1299); знак целой части не
// распространяется на дробную ("-1.50" → -50, BR-001 / §15.1 п. 1); (int)"строка" берёт ведущее
// число по правилам PHP (пробелы в начале, знак; "abc" → 0; переполнение → насыщение до int64).
// Нужен только для сверки с данными старой системы (миграция, оракул), а не для нового кода:
// новые суммы хранятся в тиынах. Решение Q-004 (docs/DECISIONS.md).
func LegacyAmountToCents(amount string) (int64, error) {
	whole, fraction, ok := strings.Cut(amount, ".")
	if !ok {
		fraction = "00"
	}
	if len(fraction) > 2 {
		fraction = fraction[:2]
	}
	for len(fraction) < 2 {
		fraction += "0"
	}
	// PHP: при переполнении (int)$whole * 100 (или «+») даёт float, и функция `: int` бросает TypeError.
	// Здесь это ErrOutOfRange. В данных такого не бывает (decimal(12,2)).
	v, err := MulChecked(PHPStringToInt(whole), 100)
	if err != nil {
		return 0, err
	}
	return AddChecked(v, PHPStringToInt(fraction))
}

// LegacyFormatCents — буквальный перенос AgentTariffPurchaseService::formatCents()
// (Laravel: app/Services/AgentTariffPurchaseService.php formatCents(), стр. 392–395):
// sprintf('%d.%02d', intdiv($cents, 100), $cents % 100). Для отрицательных даёт "0.-50" и "-1.-50" —
// старый баг (BR-001, §15.1 п. 1). Не используйте в новом коде (там Tiyn.Tenge()); функция — для
// сверки со старыми ответами. Решение Q-004 (docs/DECISIONS.md).
func LegacyFormatCents(cents int64) string {
	return strconv.FormatInt(IntDiv(cents, 100), 10) + "." + phpSprintf02d(PHPMod(cents, 100))
}

// phpSprintf02d — PHP sprintf('%02d', $n): ширина 2, заполнение нулями; знак входит в ширину
// ("-5" → "-5", "-50" → "-50", "5" → "05").
func phpSprintf02d(n int64) string {
	s := strconv.FormatInt(n, 10)
	if len(s) < 2 {
		s = "0" + s
	}
	return s
}

// StoredCents — Friends::storedCents(): (int) round((float) $amount * 100) для decimal-строки из БД
// (Laravel: app/Services/Friends.php storedCents(), стр. 189–192; BR-002). Корректна для отрицательных.
func StoredCents(amount string) Tiyn {
	return FromTengeFloat(PHPStringToFloat(amount))
}

// PHPMod — PHP $a % $b: знак результата как у делимого (как в Go). b == 0 → panic(ErrDivisionByZero)
// (PHP: DivisionByZeroError «Modulo by zero»). PHP для b == -1 всегда возвращает 0.
func PHPMod(a, b int64) int64 {
	if b == 0 {
		panic(ErrDivisionByZero)
	}
	if b == -1 {
		return 0
	}
	return a % b
}

// ParseTengeViaFloat — сумма из запроса, которую старый код сначала приводил к float, а затем писал в decimal:
// is_numeric (правило `numeric`) → (float) $s → SQL-строка (string)$float (precision 14) → округление MySQL.
//   - Laravel: app/Http/Controllers/Api/Staff/CashController.php topUp() — (float) $data['amount'];
//   - Laravel: app/Services/CashRegisterService.php topUpBalance(float $amount) — $balance->increment($balanceType, $amount) (BR-053).
//
// Отличается от ParseTengeRounded только для строк с > 14 значащими цифрами: "1.00499999999999999" → float 1.005 →
// "1.005" → 101 тиын (а по строке было бы 100). Перевод приход/расход (CashRegisterService::transact(), BR-054)
// передаёт строку как есть — для него ParseTengeRounded.
func ParseTengeViaFloat(s string) (Tiyn, error) {
	if !PHPIsNumeric(s) {
		return 0, ErrInvalidAmount
	}
	return FromTengeFloatDecimal(PHPStringToFloat(s))
}

// FormatFloat2 — PHP number_format((float) $amount, 2, '.', ”) для произвольного float
// (Laravel: StaffApiController::money(), GuestHistory, CashRegisterService::sell() — webhook 'amount').
// Округление — PHPRound(|x|, 2), печать "%.2F", «-0.00» не бывает (как в
// ext/standard/math.c _php_math_number_format_ex()).
// NaN/Inf печатаются как "nan"/"inf"/"-inf" (PHP: "nan", "inf", "-inf").
func FormatFloat2(d float64) string {
	neg := false
	if d < 0 {
		neg = true
		d = -d
	}
	d = PHPRound(d, 2)
	if d == 0 {
		// PHP-шный "%.*F" (__cvt) печатает -0.0 без знака, а is_negative сбрасывается при d == 0.
		return "0.00"
	}
	if math.IsNaN(d) {
		return "nan"
	}
	if math.IsInf(d, 0) {
		if neg {
			return "-inf"
		}
		return "inf"
	}
	s := strconv.FormatFloat(d, 'f', 2, 64)
	if neg {
		return "-" + s
	}
	return s
}
