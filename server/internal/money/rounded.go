package money

import (
	"strings"
)

// ParseTengeRounded разбирает сумму так, как её сохранил бы старый код в колонку MySQL decimal(…,2):
// строка проходит Laravel-правило `numeric` (PHP is_numeric) и при записи MySQL округляет её до
// двух знаков «половина от нуля» по ДЕСЯТИЧНОЙ записи (а не по двоичному float).
//
// Где так было: суммы кассы — `numeric` без ограничения знаков после точки:
//   - Laravel: app/Http/Requests/StoreBalanceTopUpRequest.php rules() — 'amount' => numeric, gt:0, max:9999999999.99 (BR-053);
//   - Laravel: app/Http/Requests/StoreCashTransactionRequest.php rules() — 'amount' => numeric, gt:0 (BR-054);
//   - CashRegisterService::transact() передаёт строку в create() как есть → MySQL округляет строку (эта функция);
//   - пополнение сначала приводится к float — для него ParseTengeViaFloat.
//
// Решение Q-002 (docs/DECISIONS.md): пользовательский ввод разбирается строго (ParseTenge, > 2 знаков — ошибка);
// эта функция и ParseTengeViaFloat — только для чтения старых данных (инструмент переноса, суммы в старых JSON).
//
// Принимаемый формат (как is_numeric в PHP 8): необязательные ведущие пробельные символы
// (" \t\n\r\v\f"), необязательный знак «+»/«-», цифры с необязательной точкой (".5", "5." допустимы,
// но не одна «.»), необязательная экспонента e/E[±]цифры, необязательные завершающие пробельные символы.
// Запятая, разделители тысяч, hex, "inf"/"nan" — ErrInvalidAmount.
//
// Поведение MySQL (8.x, strict mode) при вставке в decimal(M,2):
//   - строка "1.005" → 1.01, "-1.005" → -1.01, "1.0049999" → 1.00 (округление по десятичной записи,
//     ROUND_HALF_UP от нуля; выдаётся Note 1265 «Data truncated», это не ошибка даже в strict mode);
//   - "1e-3" → 0.00, "1.5e2" → 150.00; знак «+» и пробелы по краям допустимы;
//   - значение вне диапазона decimal(M,2) — ошибка 1264 в strict mode (у нас — ErrOutOfRange по MaxAmount;
//     конкретный предел колонки проверяет вызывающий).
//   - PHP-float, привязанный PDO-параметром, уходит в MySQL строкой кратчайшего представления
//     (serialize_precision = -1: 100.005 → "100.005"), поэтому и для float-аргументов округление идёт
//     по десятичной записи. Для float используйте FromTengeFloatDecimal.
//
// Проверено на mysql:8.4 — см. TestParseTengeRoundedMySQL в rounded_test.go (таблица фактических ответов).
func ParseTengeRounded(s string) (Tiyn, error) {
	s = strings.Trim(s, " \t\n\r\v\f")
	neg := false
	if s != "" && (s[0] == '+' || s[0] == '-') {
		neg = s[0] == '-'
		s = s[1:]
	}
	mant, exp, hasExp := cutExp(s)
	whole, frac, _ := strings.Cut(mant, ".")
	if (whole == "" && frac == "") || !allDigitsOrEmpty(whole) || !allDigitsOrEmpty(frac) {
		return 0, ErrInvalidAmount
	}
	e := 0
	if hasExp {
		var ok bool
		if e, ok = parseExp(exp); !ok {
			return 0, ErrInvalidAmount
		}
	}
	// Цифры мантиссы без точки; десятичная точка стоит после position цифр.
	digits := strings.TrimLeft(whole+frac, "0")
	point := len(whole) - (len(whole+frac) - len(digits)) + e // позиция точки в digits
	if digits == "" {
		return 0, nil
	}
	// Ограничиваем экспоненту: в digits ≤ 14 цифр целой части после сдвига, иначе вне MaxAmount.
	if point > 14 {
		return 0, ErrOutOfRange
	}
	// Значение = 0.digits × 10^point. Нужны цифры до позиции point+2 (сотые) и следующая — для округления.
	cut := point + 2 // столько цифр digits образуют целое число тиынов
	var v int64
	if cut > 0 {
		head := digits
		if len(head) > cut {
			head = head[:cut]
		}
		for i := 0; i < len(head); i++ {
			v = v*10 + int64(head[i]-'0')
		}
		for i := len(head); i < cut; i++ {
			v *= 10
		}
	}
	// Половина от нуля: решает первая отброшенная цифра.
	if cut >= 0 && cut < len(digits) && digits[cut] >= '5' {
		v++
	}
	if neg {
		v = -v
	}
	t := Tiyn(v)
	if !t.Valid() {
		return 0, ErrOutOfRange
	}
	return t, nil
}

func cutExp(s string) (mant, exp string, ok bool) {
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		return s[:i], s[i+1:], true
	}
	return s, "", false
}

// parseExp разбирает показатель степени [+-]цифры; слишком большие значения насыщаются (±10000),
// что всё равно даёт 0 или ErrOutOfRange.
func parseExp(s string) (int, bool) {
	neg := false
	if s != "" && (s[0] == '+' || s[0] == '-') {
		neg = s[0] == '-'
		s = s[1:]
	}
	if !allDigits(s) {
		return 0, false
	}
	e := 0
	for i := 0; i < len(s) && e < 10000; i++ {
		e = e*10 + int(s[i]-'0')
	}
	if neg {
		e = -e
	}
	return e, true
}

func allDigitsOrEmpty(s string) bool { return s == "" || allDigits(s) }

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
