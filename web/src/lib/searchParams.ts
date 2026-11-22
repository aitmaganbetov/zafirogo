/* Параметры адреса (validateSearch маршрутов): число или строка из ?…, иначе undefined/значение по умолчанию. */

/** Целое ≥ min из параметра (страница — min 2: первая страница без параметра, id — min 1) или undefined. */
export function intParam(v: unknown, min = 1): number | undefined {
  const n = typeof v === 'number' ? v : typeof v === 'string' && v !== '' ? Number(v) : NaN;
  return Number.isSafeInteger(n) && n >= min ? n : undefined;
}

/** Непустая строка из параметра (число — строкой) или undefined. */
export function strParam(v: unknown): string | undefined {
  if (typeof v === 'number') return String(v);
  return typeof v === 'string' && v !== '' ? v : undefined;
}

/** ?page= Laravel: целое 1…100000, иначе 1 (как paginate()). */
export function pageParam(v: unknown): number {
  const n = Number(v);
  return Number.isInteger(n) && n >= 1 && n <= 100000 ? n : 1;
}
