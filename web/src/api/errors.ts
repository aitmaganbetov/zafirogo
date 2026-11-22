import { isApiError } from './http';

/*
 * Тексты ошибки ответа для формы — как $errors в Blade. Тексты сервер уже перевёл на язык клуба.
 * Свои варианты с другим запасным текстом — clubmap/errors.ts, warehouse/errors.ts, settings/api.ts (по полям).
 */

/** $errors->first(): первая ошибка поля, иначе message. */
export function firstError(e: unknown): string {
  if (isApiError(e)) return Object.values(e.fieldErrors).flat()[0] ?? e.message;
  return e instanceof Error ? e.message : String(e);
}

/** $errors->all(): все ошибки полей по порядку, иначе message. */
export function allErrors(e: unknown): string[] {
  if (isApiError(e)) {
    const list = Object.values(e.fieldErrors).flat();
    return list.length > 0 ? list : [e.message];
  }
  return [e instanceof Error ? e.message : String(e)];
}
