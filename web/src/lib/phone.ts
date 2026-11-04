/*
 * Телефон в формах входа — resources/js/app.js: поле .auth-phone-local (без +7) в формате «XXX XXX-XX-XX»,
 * курсор сохраняется при вводе, перед отправкой остаются только цифры. Нормализацию (+7 и 10 цифр) делает
 * сервер (AuthController::normalizePhone).
 */

/** formatPhone() из app.js. */
export function formatPhone(digits: string): string {
  let formatted = digits.slice(0, 3);
  if (digits.length > 3) formatted += ` ${digits.slice(3, 6)}`;
  if (digits.length > 6) formatted += `-${digits.slice(6, 8)}`;
  if (digits.length > 8) formatted += `-${digits.slice(8, 10)}`;
  return formatted;
}

/** value="{{ substr(preg_replace('/\D+/', '', old('phone')), -10) }}" → в формате маски. */
export function initialPhone(old: string): string {
  return formatPhone(old.replace(/\D+/g, '').slice(-10).slice(0, 10));
}

/** Только цифры (перед отправкой). */
export function phoneDigits(value: string): string {
  return value.replace(/\D/g, '');
}

/** Обработчик input из app.js: переформатировать и вернуть позицию курсора. */
export function reformatPhone(value: string, cursor: number): { value: string; cursor: number } {
  const digitsBeforeCursor = value.slice(0, cursor).replace(/\D/g, '').length;
  const digits = value.replace(/\D/g, '').slice(0, 10);
  const formatted = formatPhone(digits);
  let next = 0;
  let counted = 0;
  while (next < formatted.length && counted < digitsBeforeCursor) {
    if (/\d/.test(formatted[next] ?? '')) counted++;
    next++;
  }
  if (next < formatted.length && /\D/.test(formatted[next] ?? '')) next++;
  return { value: formatted, cursor: next };
}
