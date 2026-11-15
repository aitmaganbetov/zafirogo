import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { render } from '@testing-library/react';
import { Suspense, type ReactNode } from 'react';
import { vi } from 'vitest';
import { session, type Me } from '../auth/session';

/*
 * Общее для тестов страниц с деньгами (FE-11): подменённый сервер (fetch), вошедший сотрудник, рендер страницы
 * с кэшем запросов. Проверяется то, что видит кассир (суммы из целых тиынов) и что уходит на сервер (тело запроса,
 * Idempotency-Key).
 */

export interface ApiCall {
  readonly method: string;
  readonly path: string;
  readonly search: URLSearchParams;
  /** Тело запроса (JSON — разобранное; иначе текст). */
  readonly body: unknown;
  readonly headers: Headers;
}

type Handler = (call: ApiCall) => unknown;

/** `'GET /api/cash/shift': () => данные`; ключ может быть шаблоном пути с `*` вместо сегмента. Ответ Response — как есть. */
export function mockApi(routes: Record<string, Handler | unknown>): { calls: ApiCall[]; of: (method: string, path: string) => ApiCall[] } {
  const calls: ApiCall[] = [];
  const table = Object.entries(routes).map(([key, handler]) => {
    const [method = 'GET', pattern = ''] = key.split(' ');
    const re = new RegExp('^' + pattern.replace(/[.+?^${}()|[\]\\]/g, '\\$&').replace(/\*/g, '[^/]+') + '$');
    return { method, re, handler };
  });
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: Request | string, init?: RequestInit) => {
      const req = input instanceof Request ? input : new Request(new URL(input, location.origin), init);
      const url = new URL(req.url);
      const text = await req.text();
      let body: unknown = text;
      try {
        body = text === '' ? undefined : JSON.parse(text);
      } catch {
        /* не JSON (multipart) — текст */
      }
      const call: ApiCall = { method: req.method, path: url.pathname, search: url.searchParams, body, headers: req.headers };
      calls.push(call);
      const route = table.find((r) => r.method === req.method && r.re.test(url.pathname));
      if (!route) return new Response(JSON.stringify({ error: 'not_found', message: `нет подмены: ${req.method} ${url.pathname}` }), { status: 404, headers: { 'Content-Type': 'application/json' } });
      const result = typeof route.handler === 'function' ? (route.handler as Handler)(call) : route.handler;
      if (result instanceof Response) return result;
      return new Response(JSON.stringify(result), { status: 200, headers: { 'Content-Type': 'application/json' } });
    }),
  );
  return { calls, of: (method, path) => calls.filter((c) => c.method === method && c.path === path) };
}

/** Ответ-ошибка API (422 — ошибки полей). */
export function apiError(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
}

/** Вошедший сотрудник клуба с правами. */
export function signIn(role: string, permissions: readonly string[], extra: Partial<Me> = {}): Me {
  const me: Me = {
    server_time: '2026-10-15T07:00:00Z',
    user: { id: 1, name: 'Айдана', phone: '+77010000001', is_superadmin: false },
    club: { id: 1, name: 'Zafiro Arena', timezone: 'Asia/Almaty', locale: 'ru' },
    role,
    permissions: [...permissions],
    read_only: false,
    clubs: [],
    ...extra,
  };
  session.setMe(me);
  return me;
}

/** seed — данные, которые в приложении к первому рендеру уже загрузил loader маршрута. */
export function renderPage(ui: ReactNode, seed?: (queryClient: QueryClient) => void) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: 30_000 } } });
  seed?.(queryClient);
  const view = render(
    <QueryClientProvider client={queryClient}>
      <Suspense fallback={null}>{ui}</Suspense>
    </QueryClientProvider>,
  );
  return { ...view, queryClient };
}

/** jsdom не реализует <dialog>.showModal()/close(): открытие — атрибут open. */
export function stubDialog(): void {
  HTMLDialogElement.prototype.showModal = function showModal(this: HTMLDialogElement) {
    this.setAttribute('open', '');
  };
  HTMLDialogElement.prototype.close = function close(this: HTMLDialogElement) {
    this.removeAttribute('open');
    this.dispatchEvent(new Event('close'));
  };
}
