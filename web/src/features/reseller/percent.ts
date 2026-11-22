import { hundredthsToInput } from '../../lib/money';

/*
 * Сотые доли без float: процент реселлера хранится в базисных пунктах (1235 = 12,35 %), суммы — в тиынах. На экране
 * и в полях форм — число без хвостовых нулей, как rtrim(rtrim(number_format((float) $percent, 2, '.', ''), '0'), '.'),
 * `(float) $reseller->commission_percent` и `round($balance, 2)` старых шаблонов: 1250 → «12.5», 18000 → «180».
 */
export function trimHundredths(n: number): string {
  if (!Number.isSafeInteger(n) || n < 0) throw new RangeError(`not a non-negative integer of hundredths: ${n}`);
  return hundredthsToInput(n);
}
