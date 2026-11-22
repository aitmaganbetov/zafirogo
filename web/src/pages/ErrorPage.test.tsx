import { QueryClientProvider } from '@tanstack/react-query';
import { RouterProvider, createMemoryHistory } from '@tanstack/react-router';
import { render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { setAccessToken } from '../api/http';
import { session } from '../auth/session';
import { createAppRouter, createQueryClient } from '../router';

/*
 * UI-1 (аудит 22.11.2026): ответ API 422/400 на загрузку экрана — не «500 Server Error», а тот же экран без
 * параметров адреса (как redirect()->back()->withErrors() провалившейся валидации GET в Laravel).
 */

const ME = {
  user: { id: 1, name: 'Иван', phone: '+77011112233', is_superadmin: false },
  club: { id: 7, name: 'Клуб Звезда', timezone: 'Asia/Almaty', locale: 'ru' },
  role: 'owner',
  permissions: ['reservations.view', 'activity_logs.view'],
  read_only: false,
  clubs: [],
  server_time: '2026-10-30T12:00:00Z',
};

const DATE_ERROR = 'Поле date должно соответствовать формату Y-m-d.';

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
}

class NoopSocket {
  onmessage = null;
  onclose = null;
  close(): void {}
}

describe('ErrorPage: неверные параметры адреса (400/422)', () => {
  const calls: string[] = [];
  beforeEach(() => {
    calls.length = 0;
    vi.stubGlobal('WebSocket', NoopSocket);
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: Request | string) => {
        const req = input instanceof Request ? input : new Request(new URL(input, location.origin));
        const url = new URL(req.url);
        calls.push(`${req.method} ${url.pathname}${url.search}`);
        switch (url.pathname) {
          case '/api/i18n':
            return json({ languages: [{ code: 'ru', name: 'Русский' }], version: '1' });
          case '/api/i18n/ru':
            return json({});
          case '/api/layout/badges':
            return json({ chat: 0, orders: 0 });
          case '/api/reservations':
            if (url.searchParams.get('date') === 'bad') {
              return json({ error: 'validation_failed', message: DATE_ERROR, errors: { date: [DATE_ERROR] } }, 422);
            }
            return json({ date: '2026-10-30', items: [], page: 1, per_page: 20, total: 0, computers: [], gap_minutes: 15, default_start: '2026-10-30T13:00' });
          case '/api/activity-logs':
            return json({ error: 'validation_failed', message: 'Ошибка.', errors: {} }, 422);
          default:
            return json({ error: 'not_found' }, 404);
        }
      }),
    );
    session.accept({ access_token: 'tok', token_type: 'Bearer', expires_in: 600, csrf_token: 'c', me: ME } as Parameters<typeof session.accept>[0]);
  });
  afterEach(() => {
    vi.unstubAllGlobals();
    setAccessToken(null);
    session.clear();
    sessionStorage.clear();
  });

  function mount(path: string) {
    const qc = createQueryClient();
    const r = createAppRouter(qc, session, createMemoryHistory({ initialEntries: [path] }));
    render(
      <QueryClientProvider client={qc}>
        <RouterProvider router={r} />
      </QueryClientProvider>,
    );
    return r;
  }

  it('/reservations?date=bad → экран броней без параметра, ошибка в блоке $errors, не 500', async () => {
    const router = mount('/reservations?date=bad');
    expect(await screen.findByRole('alert', {}, { timeout: 5000 })).toHaveTextContent(DATE_ERROR);
    expect(router.state.location.href).toBe('/reservations');
    expect(screen.queryByText('Server Error')).not.toBeInTheDocument();
    expect(calls).toContain('GET /api/reservations?date=bad');
    expect(calls).toContain('GET /api/reservations');
  });

  it('422 без параметров адреса — повторять нечего, «500» как раньше (без цикла переходов)', async () => {
    const router = mount('/activity-logs');
    expect(await screen.findAllByText('Server Error', {}, { timeout: 5000 })).not.toHaveLength(0);
    expect(router.state.location.href).toBe('/activity-logs');
    await waitFor(() => expect(calls.filter((c) => c.startsWith('GET /api/activity-logs')).length).toBeGreaterThan(0));
  });
});
