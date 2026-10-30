/*
 * Ошибки страниц: 403 — `throw new ForbiddenError(текст)` в beforeLoad/loader (как abort(403, __('…'))),
 * 404 — `throw notFound()` из @tanstack/react-router (как abort(404) / firstOrFail()).
 */

export class ForbiddenError extends Error {
  /** Текст старого кода (ключ перевода); пусто — «Forbidden». */
  constructor(message = '') {
    super(message);
    this.name = 'ForbiddenError';
  }
}

export function isForbidden(e: unknown): e is ForbiddenError {
  return e instanceof ForbiddenError;
}
