import createClient, { type Client } from 'openapi-fetch';

/*
 * HTTP-клиент API (server/internal/http/sections/auth/auth.go):
 * - access-токен — только в памяти вкладки (Authorization: Bearer), срок ~10 мин;
 * - refresh-токен — в HttpOnly-cookie `__Secure-zafiro_rt` (Path=/api/auth); обновление — POST /api/auth/refresh
 *   с заголовком X-CSRF-Token = cookie `__Host-zafiro_csrf` (double submit);
 * - на 401 — один refresh (общий для параллельных запросов) и один повтор запроса;
 * - ошибки — ApiError с типом (validation/forbidden/conflict/rate_limited/…), 422 — ошибки полей формы.
 */

export const CSRF_COOKIE = '__Host-zafiro_csrf';
const REFRESH_PATH = '/api/auth/refresh';

/** Ответ входа/обновления (AuthSession в api/auth.yaml), без привязки к сгенерированным типам. */
export interface TokenGrant {
  readonly access_token: string;
  readonly expires_in: number;
}

export type ApiErrorKind =
  | 'unauthenticated' // 401
  | 'forbidden' // 403 (нет права, режим просмотра — code read_only, межсайтовый запрос)
  | 'not_found' // 404
  | 'conflict' // 409 (idempotency_conflict, already_member, refresh_conflict)
  | 'gone' // 410
  | 'validation' // 422 {message, errors{поле: [текст]}}
  | 'rate_limited' // 429 (Retry-After)
  | 'bad_request' // 400
  | 'unavailable' // 503
  | 'server' // 5xx
  | 'network'; // нет ответа

export interface ApiErrorBody {
  readonly error?: string;
  readonly message?: string;
  readonly errors?: Readonly<Record<string, readonly string[]>>;
}

/** Ошибка API в типизированном виде. message — русский текст старого кода (ключ перевода) или ''. */
export class ApiError extends Error {
  readonly status: number;
  readonly kind: ApiErrorKind;
  /** Машинный код: forbidden, read_only, validation_failed, idempotency_conflict, … */
  readonly code: string;
  /** 422: поле → тексты (как Laravel withErrors). */
  readonly fieldErrors: Readonly<Record<string, readonly string[]>>;
  /** 429: через сколько секунд повторить. */
  readonly retryAfter: number | null;

  constructor(status: number, body: ApiErrorBody | null, retryAfter: number | null = null) {
    super(body?.message ?? '');
    this.name = 'ApiError';
    this.status = status;
    this.kind = kindOf(status);
    this.code = body?.error ?? '';
    this.fieldErrors = body?.errors ?? {};
    this.retryAfter = retryAfter;
  }

  /** Первая ошибка поля (показывается под полем, как @error в Blade). */
  field(name: string): string | undefined {
    return this.fieldErrors[name]?.[0];
  }
}

export function kindOf(status: number): ApiErrorKind {
  switch (status) {
    case 0:
      return 'network';
    case 400:
      return 'bad_request';
    case 401:
      return 'unauthenticated';
    case 403:
      return 'forbidden';
    case 404:
      return 'not_found';
    case 409:
      return 'conflict';
    case 410:
      return 'gone';
    case 422:
      return 'validation';
    case 429:
      return 'rate_limited';
    case 503:
      return 'unavailable';
    default:
      return 'server';
  }
}

export function isApiError(e: unknown): e is ApiError {
  return e instanceof ApiError;
}

/** ApiError из ответа (тело JSON — если есть). */
export async function errorFromResponse(res: Response): Promise<ApiError> {
  let body: ApiErrorBody | null;
  try {
    body = (await res.clone().json()) as ApiErrorBody;
  } catch {
    body = null;
  }
  const ra = Number(res.headers.get('Retry-After'));
  return new ApiError(res.status, body, Number.isFinite(ra) && ra > 0 ? ra : null);
}

// ---------------------------------------------------------------------------
// Токен в памяти и обновление
// ---------------------------------------------------------------------------

let accessToken: string | null = null;
let refreshing: Promise<TokenGrant | null> | null = null;
const grantListeners = new Set<(grant: unknown) => void>();
let onUnauthenticated: (() => void) | null = null;

/** Текущий access-токен (только память; не в localStorage). */
export function getAccessToken(): string | null {
  return accessToken;
}

/** Запомнить токен из ответа login/register/refresh. */
export function setAccessToken(token: string | null): void {
  accessToken = token;
}

/** Подписка на новые сессии из refresh (полное тело AuthSession — с me). */
export function onGrant(fn: (grant: unknown) => void): () => void {
  grantListeners.add(fn);
  return () => grantListeners.delete(fn);
}

/** Что делать, когда сессия окончательно потеряна (refresh отклонён): store сессии → гость → /login. */
export function setUnauthenticatedHandler(fn: (() => void) | null): void {
  onUnauthenticated = fn;
}

export function readCookie(name: string): string | null {
  if (typeof document === 'undefined') return null;
  for (const part of document.cookie.split(';')) {
    const [k, ...v] = part.trim().split('=');
    if (k === name) return decodeURIComponent(v.join('='));
  }
  return null;
}

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

async function doRefresh(): Promise<TokenGrant | null> {
  const csrf = readCookie(CSRF_COOKIE);
  if (!csrf) return null; // нет сессии в этом браузере
  for (let attempt = 0; attempt < 3; attempt++) {
    let res: Response;
    try {
      res = await fetch(REFRESH_PATH, {
        method: 'POST',
        credentials: 'same-origin',
        headers: { 'X-CSRF-Token': csrf, Accept: 'application/json' },
      });
    } catch {
      throw new ApiError(0, null);
    }
    if (res.ok) {
      const grant = (await res.json()) as TokenGrant;
      accessToken = grant.access_token;
      for (const fn of grantListeners) fn(grant);
      return grant;
    }
    // 409 refresh_conflict: другая вкладка уже обновила токен — повторить с новой cookie.
    if (res.status === 409) {
      await sleep(150 * (attempt + 1));
      continue;
    }
    if (res.status === 401 || res.status === 403) {
      accessToken = null;
      return null;
    }
    throw await errorFromResponse(res);
  }
  return null;
}

/** Обновить access-токен по refresh-cookie; параллельные вызовы ждут один запрос. null — сессии нет. */
export function refreshSession(): Promise<TokenGrant | null> {
  refreshing ??= doRefresh().finally(() => {
    refreshing = null;
  });
  return refreshing;
}

function withAuth(req: Request): Request {
  if (accessToken && !req.headers.has('Authorization')) {
    const headers = new Headers(req.headers);
    headers.set('Authorization', `Bearer ${accessToken}`);
    return new Request(req, { headers });
  }
  return req;
}

/**
 * fetch с токеном: на 401 — refresh и один повтор. Тело запроса клонируется до отправки (повтор с тем же телом
 * и тем же Idempotency-Key).
 */
export async function apiFetch(input: Request): Promise<Response> {
  const retry = input.clone();
  let res: Response;
  try {
    res = await fetch(withAuth(input));
  } catch {
    throw new ApiError(0, null);
  }
  if (res.status !== 401 || new URL(input.url, location.origin).pathname.startsWith('/api/auth/refresh')) {
    return res;
  }
  const grant = await refreshSession();
  if (!grant) {
    onUnauthenticated?.();
    return res;
  }
  const headers = new Headers(retry.headers);
  headers.set('Authorization', `Bearer ${grant.access_token}`);
  try {
    return await fetch(new Request(retry, { headers }));
  } catch {
    throw new ApiError(0, null);
  }
}

/**
 * Клиент раздела по его типам (`import type { paths } from '../api/gen/cash'`):
 *
 *   export const cashApi = createApiClient<paths>();
 *   const shift = await unwrap(cashApi.GET('/api/cash/shift'));
 */
export function createApiClient<Paths extends object>(): Client<Paths> {
  // baseUrl — origin страницы: openapi-fetch строит Request с абсолютным URL (работает и вне браузера).
  return createClient<Paths>({ baseUrl: typeof location === 'undefined' ? '' : location.origin, fetch: apiFetch, credentials: 'same-origin' });
}

/** Результат openapi-fetch → данные или ApiError. */
export async function unwrap<T>(p: Promise<{ data?: T; error?: unknown; response: Response }>): Promise<T> {
  const { data, error, response } = await p;
  if (!response.ok) {
    const body = error !== null && typeof error === 'object' ? (error as ApiErrorBody) : null;
    const ra = Number(response.headers.get('Retry-After'));
    throw new ApiError(response.status, body, Number.isFinite(ra) && ra > 0 ? ra : null);
  }
  return data as T;
}

/** Новый ключ идемпотентности для денежной формы (один на отправку, CSQ-4). */
export function idempotencyKey(): string {
  return crypto.randomUUID();
}
