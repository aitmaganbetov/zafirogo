/*
 * Ключ без перевода в каталоге (CM-11, docs/open-questions/stage3-clubmap.md): i18next показывает сам ключ.
 * - Обычный ключ — ключ с подставленными параметрами ({{name}}), как __() Laravel без перевода.
 * - Ключ trans_choice («форма|форма|форма») с count — одна форма по MessageSelector::choose() Laravel (явные условия
 *   {n}, [a,b], [a,*], иначе правило множественного числа), а не все формы через «|». Ключ — русский текст, поэтому
 *   форма выбирается по правилам ru (3 формы). То же — server/internal/i18n/translate.go Choose().
 */

/** Явное условие формы: {0}, {1,2}, [2,*] (MessageSelector::extractFromString()). */
const EXPLICIT = /^[{[]([^[\]{}]*)[}\]]([\s\S]*)/;
/** Условие в начале формы (MessageSelector::stripConditions()). */
const CONDITION = /^[{[]([^[\]{}]*)[}\]]/;

function num(s: string): number {
  const v = Number(s.trim());
  return s.trim() === '' || Number.isNaN(v) ? -1 : v;
}

/** MessageSelector::getPluralIndex() для ru (uk, be): 0 — одна, 1 — несколько, 2 — много. */
export function ruPluralIndex(n: number): number {
  const i = Math.trunc(n);
  if (i % 10 === 1 && i % 100 !== 11) return 0;
  if (i % 10 >= 2 && i % 10 <= 4 && (i % 100 < 10 || i % 100 >= 20)) return 1;
  return 2;
}

/** MessageSelector::choose(): форма строки line для числа n (правило ru). */
export function chooseForm(line: string, n: number): string {
  const segments = line.split('|');
  for (const part of segments) {
    const m = EXPLICIT.exec(part);
    if (!m) continue;
    const cond = m[1] ?? '';
    const value = (m[2] ?? '').trim();
    if (cond.includes(',')) {
      const [from = '', to = ''] = cond.split(',', 2);
      if ((from.trim() === '*' || n >= num(from)) && (to.trim() === '*' || n <= num(to))) return value;
      continue;
    }
    if (num(cond) === n) return value;
  }
  const forms = segments.map((s) => s.replace(CONDITION, ''));
  const idx = ruPluralIndex(n);
  return forms.length === 1 || idx >= forms.length ? (forms[0] ?? line) : (forms[idx] ?? line);
}

type Interpolate = (text: string, params: Record<string, unknown>) => string;

/**
 * parseMissingKeyHandler для i18next: вызывается для ключа без перевода (после подстановки в сам ключ, но с исходным
 * ключом в аргументе). interpolate — подстановка {{name}} интерполятором i18next.
 */
export function makeMissingKeyHandler(interpolate: Interpolate) {
  return (key: string, defaultValue: unknown, options?: unknown): string => {
    const params = (options ?? {}) as Record<string, unknown>;
    let text = typeof defaultValue === 'string' ? defaultValue : key;
    const count = params.count;
    if (typeof count === 'number' && text.includes('|')) text = chooseForm(text, count);
    return interpolate(text, params);
  };
}
