/*
 * «НДС 5% (включён)» корзины кассы — решение Q-019: показ как в старом (целые тенге), расчёт в тиынах без float.
 * Тот же расчёт на сервере — server/internal/cash/vat.go VATIncluded(); общие векторы — server/internal/cash/testdata/vat.json.
 * Laravel: cash-register/index.blade.php render(): Math.round(total * 5 / 105) (total в тенге).
 */

export const VAT_RATE_PERCENT = 5;

/** НДС, включённый в сумму (тиыны), — целые тенге (половина вверх), результат в тиынах (кратен 100). */
export function vatIncludedTiyn(totalTiyn: number): number {
  if (!Number.isSafeInteger(totalTiyn)) throw new RangeError(`vat: not an integer tiyn amount: ${totalTiyn}`);
  if (totalTiyn <= 0) return 0;
  const den = 100 * (100 + VAT_RATE_PERCENT);
  // floor((5·T + den/2) / den) — без потери точности, пока 5·T < 2^53 (суммы корзины — до 18 трлн ₸).
  return Math.floor((totalTiyn * VAT_RATE_PERCENT + den / 2) / den) * 100;
}
