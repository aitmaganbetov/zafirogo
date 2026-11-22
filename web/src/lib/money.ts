/*
 * Деньги на фронте — целые тиыны (CLAUDE.md «Деньги»): из API приходят `*_tiyn` (int64 как JSON number),
 * в API уходят тиыны. Никаких float: форматирование и разбор — строками и целыми.
 */

function assertTiyn(t: number): void {
  if (!Number.isSafeInteger(t)) throw new RangeError(`money: not an integer tiyn amount: ${t}`);
}

/**
 * PHP number_format($tiyn / 100, $decimals, $decPoint, $thousandsSep) для суммы в тиынах — как в старых шаблонах:
 * чаще всего `number_format($x, 2, ',', ' ')` (касса, отчёты) и `number_format($x, 0, ',', ' ')` (дашборд, тарифы).
 * Округление до целых тенге — как PHP (половина — от нуля), без float.
 */
export function formatMoney(tiyn: number, decimals: 0 | 2 = 2, decPoint = ',', thousandsSep = ' '): string {
  assertTiyn(tiyn);
  const neg = tiyn < 0;
  const abs = Math.abs(tiyn);
  let whole = Math.floor(abs / 100);
  const frac = abs % 100;
  let fracStr = '';
  if (decimals === 2) {
    fracStr = decPoint + String(frac).padStart(2, '0');
  } else if (frac >= 50) {
    whole += 1;
  }
  const digits = String(whole);
  let grouped = '';
  for (let i = 0; i < digits.length; i++) {
    if (i > 0 && (digits.length - i) % 3 === 0) grouped += thousandsSep;
    grouped += digits[i];
  }
  // PHP: "-0" не бывает — число округлилось до нуля, знак не пишется.
  const isZero = whole === 0 && (decimals === 0 || frac === 0);
  return (neg && !isZero ? '-' : '') + grouped + fracStr;
}

/** Тиыны → строка для поля ввода «1234.50» (value инпута суммы; без разделителей тысяч). */
export function tiynToInput(tiyn: number): string {
  assertTiyn(tiyn);
  const neg = tiyn < 0;
  const abs = Math.abs(tiyn);
  const frac = abs % 100;
  return (neg ? '-' : '') + String(Math.floor(abs / 100)) + (frac === 0 ? '' : '.' + String(frac).padStart(2, '0'));
}

/**
 * (string) decimal(12,2) — тиыны строкой с двумя знаками: 60000 → «600.00», 5 → «0.05», -150 → «-1.50».
 * Значение поля «Стоимость» тарифа клуба ((string) $tariff->price) и тарифа платформы (price_monthly, decimal:2).
 */
export function tiynToFixed2(tiyn: number): string {
  assertTiyn(tiyn);
  const abs = Math.abs(tiyn);
  return (tiyn < 0 ? '-' : '') + String(Math.floor(abs / 100)) + '.' + String(abs % 100).padStart(2, '0');
}

/**
 * Сотые (тиыны, базисные пункты) → число, как PHP выводит float: без хвостовых нулей — 100050 → «1000.5»,
 * 500 → «5», 1235 → «12.35», -150 → «-1.5». Поля порогов/процентов бонусов, процент и выплата реселлера.
 */
export function hundredthsToInput(n: number): string {
  assertTiyn(n);
  const abs = Math.abs(n);
  const frac = String(abs % 100).padStart(2, '0').replace(/0+$/, '');
  return (n < 0 ? '-' : '') + String(Math.floor(abs / 100)) + (frac === '' ? '' : '.' + frac);
}

export type ParseMoneyError = 'required' | 'numeric' | 'decimal' | 'negative' | 'too_large';

export type ParseMoneyResult = { readonly ok: true; readonly tiyn: number } | { readonly ok: false; readonly error: ParseMoneyError };

/**
 * Ввод суммы → тиыны. Строго: не больше 2 знаков после точки или запятой (решение Q-002: > 2 знаков — ошибка
 * формы, не округление). Пробелы (в т. ч. неразрывные) между цифрами допускаются как разделители тысяч.
 */
export function parseMoney(input: string, { allowNegative = false }: { allowNegative?: boolean } = {}): ParseMoneyResult {
  const s = input.replace(/[\s\u00a0\u202f]/g, '');
  if (s === '') return { ok: false, error: 'required' };
  const m = /^(-)?(\d+)(?:[.,](\d*))?$/.exec(s);
  if (!m) return { ok: false, error: 'numeric' };
  const [, minus, intPart = '0', fracPart = ''] = m;
  if (fracPart.length > 2) return { ok: false, error: 'decimal' };
  if (minus && !allowNegative) return { ok: false, error: 'negative' };
  const intDigits = intPart.replace(/^0+(?=\d)/, '');
  if (intDigits.length > 13) return { ok: false, error: 'too_large' };
  const tiyn = Number(intDigits) * 100 + Number(fracPart.padEnd(2, '0'));
  if (!Number.isSafeInteger(tiyn)) return { ok: false, error: 'too_large' };
  return { ok: true, tiyn: minus && tiyn !== 0 ? -tiyn : tiyn };
}

/** Тексты ошибок parseMoney — стандартные сообщения валидатора Laravel (ключи перевода), :attribute → {{attribute}}. */
export const PARSE_MONEY_MESSAGES: Readonly<Record<ParseMoneyError, string>> = {
  required: 'The {{attribute}} field is required.',
  numeric: 'The {{attribute}} field must be a number.',
  decimal: 'The {{attribute}} field must have 0-2 decimal places.',
  negative: 'The {{attribute}} field must be at least 0.',
  too_large: 'The {{attribute}} field must not be greater than 9999999999.99.',
};
