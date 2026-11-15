package money

import "strconv"

// NumberFormat — PHP number_format($tenge, 2, $dec, $sep) для суммы в тиынах: «1 234,50» (dec ",", sep " "),
// «1 234.50», «-12.00»; sep "" — без групп (то же, что Tenge() с другим разделителем дробной части).
// Единственная реализация группировки тысяч на сервере (отчёты кассы, тексты статусов, старый мобильный API).
// Арифметика целочисленная; «-0,00» не бывает (ноль без знака), как в PHP.
func NumberFormat(t Tiyn, dec, sep string) string {
	whole, frac := int64(t)/100, int64(t)%100
	b := make([]byte, 0, 32)
	if t < 0 {
		b = append(b, '-')
		whole, frac = -whole, -frac // частное и остаток по отдельности: MinInt64 не переполняется
	}
	b = appendGrouped(b, whole, sep)
	b = append(b, dec...)
	return string(append(b, byte('0'+frac/10), byte('0'+frac%10)))
}

// GroupThousands — PHP number_format($n, 0, ”, $sep) для целого: «12,345» (sep ","), «1 000 000» (sep " "), «-1,234».
func GroupThousands(n int64, sep string) string {
	s := strconv.FormatInt(n, 10)
	if n < 0 {
		return "-" + groupDigits(s[1:], sep)
	}
	return groupDigits(s, sep)
}

// appendGrouped дописывает неотрицательное n с разделителем групп по три цифры.
func appendGrouped(b []byte, n int64, sep string) []byte {
	var buf [20]byte
	return append(b, groupDigits(string(strconv.AppendInt(buf[:0], n, 10)), sep)...)
}

func groupDigits(digits, sep string) string {
	if sep == "" || len(digits) <= 3 {
		return digits
	}
	out := make([]byte, 0, len(digits)+(len(digits)-1)/3*len(sep))
	for i := 0; i < len(digits); i++ {
		if i > 0 && (len(digits)-i)%3 == 0 {
			out = append(out, sep...)
		}
		out = append(out, digits[i])
	}
	return string(out)
}
