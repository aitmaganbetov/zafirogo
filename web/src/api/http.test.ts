import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { ApiError, apiFetch, createApiClient, getAccessToken, refreshSession, setAccessToken, setUnauthenticatedHandler, unwrap } from './http';
import type { paths } from './gen/auth';

function json(status: number, body: unknown, headers: Record<string, string> = {}): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json', ...headers } });
}

describe('apiFetch: токен, refresh по cookie, повтор', () => {
  let fetchMock: ReturnType<typeof vi.fn>;
  let cookie = '__Host-zafiro_csrf=csrf-1';
  beforeEach(() => {
    // __Host-cookie jsdom не принимает без https — подменяем document.cookie.
    cookie = 'a=b; __Host-zafiro_csrf=csrf-1';
    vi.spyOn(document, 'cookie', 'get').mockImplementation(() => cookie);
    fetchMock = vi.fn();
    vi.stubGlobal('fetch', fetchMock);
    setAccessToken('old');
  });
  afterEach(() => {
    vi.unstubAllGlobals();
    setAccessToken(null);
    setUnauthenticatedHandler(null);
    vi.restoreAllMocks();
  });

  it('ставит Bearer и на 401 делает один refresh с CSRF и один повтор с тем же телом', async () => {
    const bodies: string[] = [];
    fetchMock.mockImplementation(async (input: Request | string, init?: RequestInit) => {
      const req = input instanceof Request ? input : new Request(new URL(input, location.origin), init);
      const url = new URL(req.url).pathname;
      if (url === '/api/auth/refresh') {
        expect(req.headers.get('X-CSRF-Token')).toBe('csrf-1');
        return json(200, { access_token: 'new', expires_in: 600 });
      }
      bodies.push(await req.text());
      return req.headers.get('Authorization') === 'Bearer new' ? json(201, { ok: true }) : json(401, { error: 'unauthenticated' });
    });
    const res = await apiFetch(new Request(`${location.origin}/api/cash/sales`, { method: 'POST', body: '{"a":1}' }));
    expect(res.status).toBe(201);
    expect(bodies).toEqual(['{"a":1}', '{"a":1}']);
    expect(getAccessToken()).toBe('new');
    expect(fetchMock).toHaveBeenCalledTimes(3);
  });

  it('параллельные 401 — один refresh', async () => {
    let refreshes = 0;
    fetchMock.mockImplementation(async (input: Request | string) => {
      const req = input instanceof Request ? input : new Request(new URL(input, location.origin));
      if (new URL(req.url).pathname === '/api/auth/refresh') {
        refreshes++;
        await new Promise((r) => setTimeout(r, 10));
        return json(200, { access_token: 'new', expires_in: 600 });
      }
      return req.headers.get('Authorization') === 'Bearer new' ? json(200, {}) : json(401, {});
    });
    await Promise.all([1, 2, 3].map(() => apiFetch(new Request(`${location.origin}/api/x`))));
    expect(refreshes).toBe(1);
  });

  it('refresh отклонён — сессия потеряна (handler), ответ 401', async () => {
    const lost = vi.fn();
    setUnauthenticatedHandler(lost);
    fetchMock.mockImplementation(async () => json(401, { error: 'unauthenticated' }));
    const res = await apiFetch(new Request(`${location.origin}/api/x`));
    expect(res.status).toBe(401);
    expect(lost).toHaveBeenCalled();
    expect(getAccessToken()).toBeNull();
  });

  it('409 refresh_conflict (другая вкладка) — повтор refresh', async () => {
    let n = 0;
    fetchMock.mockImplementation(async () => (n++ === 0 ? json(409, { error: 'refresh_conflict' }) : json(200, { access_token: 't2', expires_in: 1 })));
    const g = await refreshSession();
    expect(g?.access_token).toBe('t2');
  });

  it('без CSRF-cookie refresh не идёт на сервер', async () => {
    cookie = 'a=b';
    expect(await refreshSession()).toBeNull();
    expect(fetchMock).not.toHaveBeenCalled();
  });
});

describe('ошибки API в типизированном виде', () => {
  afterEach(() => vi.unstubAllGlobals());

  it.each([
    [422, { error: 'validation_failed', message: 'Поле', errors: { amount: ['Поле'] } }, 'validation'],
    [403, { error: 'read_only', message: 'Режим просмотра' }, 'forbidden'],
    [409, { error: 'idempotency_conflict' }, 'conflict'],
    [429, { error: 'too_many_requests', message: 'Too Many Attempts.' }, 'rate_limited'],
    [404, { error: 'not_found' }, 'not_found'],
  ] as const)('%d → %s', async (status, body, kind) => {
    vi.stubGlobal('fetch', vi.fn(async () => json(status, body, status === 429 ? { 'Retry-After': '30' } : {})));
    const api = createApiClient<paths>();
    const err = await unwrap(api.GET('/api/auth/me')).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    const e = err as ApiError;
    expect(e.kind).toBe(kind);
    expect(e.code).toBe(body.error);
    if (status === 422) expect(e.field('amount')).toBe('Поле');
    if (status === 429) expect(e.retryAfter).toBe(30);
  });
});
