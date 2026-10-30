import { QueryClientProvider } from '@tanstack/react-query';
import { RouterProvider, createMemoryHistory } from '@tanstack/react-router';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { setAccessToken } from './api/http';
import { session } from './auth/session';
import { createAppRouter, createQueryClient } from './router';

const ME = {
  user: { id: 1, name: 'Иван', phone: '+77011112233', is_superadmin: false },
  club: { id: 7, name: 'Клуб Звезда', timezone: 'Asia/Almaty', locale: 'ru' },
  role: 'cashier',
  permissions: ['cash.view', 'chat.view', 'orders.view'],
  read_only: false,
  clubs: [],
  server_time: '2026-10-30T12:00:00Z',
};

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
}

class NoopSocket {
  onmessage = null;
  onclose = null;
  close(): void {}
}

describe('маршруты: гард входа, вход, панель клуба', () => {
  const calls: string[] = [];
  beforeEach(() => {
    calls.length = 0;
    vi.stubGlobal('WebSocket', NoopSocket);
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: Request | string) => {
        const req = input instanceof Request ? input : new Request(new URL(input, location.origin));
        const path = new URL(req.url).pathname;
        calls.push(`${req.method} ${path}`);
        switch (path) {
          case '/api/i18n':
            return json({ languages: [{ code: 'ru', name: 'Русский' }], version: '1' });
          case '/api/i18n/ru':
            return json({});
          case '/api/auth/login': {
            const body = (await req.json()) as { phone: string };
            if (body.phone !== '7011112233') {
              return json({ error: 'validation_failed', message: 'Неверные учетные данные.', errors: { phone: ['Неверные учетные данные.'] } }, 422);
            }
            return json({ access_token: 'tok', token_type: 'Bearer', expires_in: 600, csrf_token: 'c', me: ME });
          }
          case '/api/layout/badges':
            return json({ chat: 3, orders: 2 });
          case '/api/ws-ticket':
            return json({ ticket: 'x'.repeat(43), expires_in: 30 });
          default:
            return json({ error: 'not_found' }, 404);
        }
      }),
    );
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

  it('гость на /dashboard → /login; ошибка входа под полем; вход → назад (intended), меню с бейджами', async () => {
    const router = mount('/dashboard?tab=1');
    await screen.findByText('Добро пожаловать');
    expect(router.state.location.pathname).toBe('/login');
    expect(document.title).toBe('Вход · Zafiro');

    fireEvent.input(screen.getByLabelText('Номер телефона'), { target: { value: '7000000000' } });
    fireEvent.change(screen.getByLabelText('Пароль'), { target: { value: 'pw' } });
    fireEvent.submit(screen.getByText('Войти в систему').closest('form') as HTMLFormElement);
    expect(await screen.findAllByText('Неверные учетные данные.')).toHaveLength(2);

    fireEvent.input(screen.getByLabelText('Номер телефона'), { target: { value: '701 111-22-33' } });
    fireEvent.change(screen.getByLabelText('Номер телефона'), { target: { value: '701 111-22-33' } });
    fireEvent.change(screen.getByLabelText('Пароль'), { target: { value: 'pw' } });
    fireEvent.submit(screen.getByText('Войти в систему').closest('form') as HTMLFormElement);

    await screen.findByTestId('dashboard');
    expect(router.state.location.href).toBe('/dashboard?tab=1');
    expect(screen.getByText('Иван')).toBeInTheDocument();
    await waitFor(() => expect(calls).toContain('GET /api/layout/badges'));
    await waitFor(() => expect(screen.getByText('3')).toBeInTheDocument());
    await waitFor(() => expect(calls).toContain('POST /api/ws-ticket'));
  });

  it('неизвестный путь — 404 как в Laravel', async () => {
    mount('/no-such-page');
    expect(await screen.findByText('Not Found')).toBeInTheDocument();
    expect(screen.getByText('404')).toBeInTheDocument();
  });
});
