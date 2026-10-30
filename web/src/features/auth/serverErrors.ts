import { isApiError } from '../../api/http';

/**
 * Ошибки формы с сервера — как в Blade: первая ошибка сверху ($errors->first()) и под полем (@error).
 * 422 → поля; 429 → текст лимита; прочее — общий текст.
 */
export interface FormServerErrors {
  readonly first: string | null;
  readonly fields: Readonly<Record<string, string>>;
}

export const NO_ERRORS: FormServerErrors = { first: null, fields: {} };

/** Laravel ThrottleRequests: 429 «Too Many Attempts.» */
export const MSG_TOO_MANY = 'Too Many Attempts.';

export function serverErrors(e: unknown): FormServerErrors {
  if (isApiError(e)) {
    if (e.kind === 'validation') {
      const fields: Record<string, string> = {};
      for (const [k, v] of Object.entries(e.fieldErrors)) if (v[0] !== undefined) fields[k] = v[0];
      return { first: e.message || Object.values(fields)[0] || null, fields };
    }
    if (e.kind === 'rate_limited') return { first: MSG_TOO_MANY, fields: {} };
    if (e.message) return { first: e.message, fields: {} };
  }
  return { first: 'Server Error', fields: {} };
}
