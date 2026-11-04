package cash

import "github.com/aitmaganbetov/zafirogo/server/internal/money"

// VATRatePercent — «НДС 5% (включён)» в корзине кассы.
const VATRatePercent = 5

// VATIncluded — «НДС 5% (включён)» суммы корзины, как показывает касса: целые тенге, Math.round(total * 5 / 105)
// (половина — вверх), результат в тиынах (кратен 100). Решение Q-019: показ как в старом, расчёт — в тиынах без
// float; фронт (web/src/features/cash/vat.ts) считает так же и сверяется с testdata/vat.json.
//
// Laravel: resources/views/cash-register/index.blade.php render(): document.getElementById('tax').textContent =
// money(Math.round(total * 5 / 105)) — total в тенге (float). Отрицательных сумм в корзине нет; для них — 0.
func VATIncluded(total money.Tiyn) money.Tiyn {
	if total <= 0 {
		return 0
	}
	// round(T/100 · 5/105) = floor((5·T + 5250) / 10500) для T ≥ 0.
	const den = 100 * (100 + VATRatePercent)
	return money.Tiyn((int64(total)*VATRatePercent+den/2)/den) * 100
}
