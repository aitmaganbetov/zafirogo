/*
 * Ошибки страниц: 403 — `throw new ForbiddenError(текст)` в beforeLoad/loader (как abort(403, __('…'))),
 * 404 — `throw notFound()` из @tanstack/react-router (как abort(404) / firstOrFail()).
 */

import { useRouterState } from '@tanstack/react-router';

declare module '@tanstack/history' {
  interface HistoryState {
    /** Ошибки неверных параметров адреса после перехода с BadRequestPage (как $errors после redirect()->back()). */
    searchErrors?: readonly string[];
  }
}

/** $errors после возврата с неверных параметров адреса (ErrorPage.tsx BadRequestPage); пусто — ошибок нет. */
export function useSearchErrors(): readonly string[] {
  return useRouterState({ select: (s) => s.location.state.searchErrors }) ?? [];
}

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
