import { QueryClientProvider } from '@tanstack/react-query';
import { RouterProvider, createMemoryHistory } from '@tanstack/react-router';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { setAccessToken } from '../../api/http';
import { session } from '../../auth/session';
import { createAppRouter, createQueryClient } from '../../router';

/*
 * GS-561 (ResellerTest.php «only resellers sign in on the reseller host; there is no sign-up there»): на хосте
 * реселлера — вход без «Зарегистрироваться», после входа — кабинет с корня; /register, /dashboard и страницы сайта
 * уходят в кабинет (RestrictResellerHost).
 */

const ME = {
  user: { id: 9, name: 'Arman', phone: '+77001234567', is_superadmin: false },
  club: null,
  role: null,
  permissions: [],
  read_only: false,
  clubs: [],
  server_time: '2026-11-20T07:00:00Z',
};
const CABINET = {
  code: 'arman-games', commission_bp: 1000, discount_percent: 15, is_active: true, earned_tiyn: 0, paid_out_tiyn: 0, balance_tiyn: 0,
  clubs: { items: [], page: 1, per_page: 30, total: 0 }, earnings: { items: [], page: 1, per_page: 20, total: 0 }, payouts: [],
};

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
}

describe('хост кабинета реселлера', () => {
  const calls: string[] = [];
  beforeEach(() => {
    calls.length = 0;
    document.documentElement.setAttribute('data-reseller-host', '');
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
          case '/api/auth/login':
            return json({ access_token: 'tok', token_type: 'Bearer', expires_in: 600, csrf_token: 'c', me: ME });
          case '/api/reseller/dashboard':
            return json(CABINET);
          default:
            return json({ error: 'not_found' }, 404);
        }
      }),
    );
  });
  afterEach(() => {
    vi.unstubAllGlobals();
    document.documentElement.removeAttribute('data-reseller-host');
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

  it('гость: корень → вход без регистрации; вход реселлера → кабинет с корня', async () => {
    const router = mount('/');
    await screen.findByText('Добро пожаловать');
    expect(router.state.location.pathname).toBe('/login');
    expect(screen.getByText('Кабинет реселлера Zafiro. Аккаунт реселлера создаёт Zafiro.')).toBeInTheDocument();
    expect(screen.queryByText('Зарегистрироваться')).not.toBeInTheDocument();

    fireEvent.input(screen.getByLabelText('Номер телефона'), { target: { value: '700 123-45-67' } });
    fireEvent.change(screen.getByLabelText('Номер телефона'), { target: { value: '700 123-45-67' } });
    fireEvent.change(screen.getByLabelText('Пароль'), { target: { value: 'secret12' } });
    fireEvent.submit(screen.getByText('Войти в систему').closest('form') as HTMLFormElement);

    expect(await screen.findByText('Пока ни один клуб не ввёл ваш код.')).toBeInTheDocument();
    expect(screen.getByText('arman-games')).toBeInTheDocument();
    // Внутри — маршрут /reseller, в адресе браузера — корень хоста.
    expect(router.state.location.pathname).toBe('/reseller');
    expect(router.history.location.pathname).toBe('/');
    expect(document.title).toBe('Мои клубы и доход · Zafiro');
    // Панель клуба не запрашивается.
    expect(calls).not.toContain('GET /api/layout/badges');
  });

  it.each(['/register', '/dashboard', '/pricing', '/product/features', '/superadmin', '/settings/club', '/no-such-page'])(
    '%s на хосте реселлера уходит в кабинет (гостя — на вход)',
    async (path) => {
      const router = mount(path);
      await screen.findByText('Добро пожаловать');
      await waitFor(() => expect(router.state.location.pathname).toBe('/login'));
      expect(screen.queryByText('Зарегистрироваться')).not.toBeInTheDocument();
      expect(calls).not.toContain('GET /api/landing');
    },
  );

  it('восстановление пароля на хосте реселлера открывается', async () => {
    const router = mount('/forgot-password');
    await waitFor(() => expect(router.state.status).toBe('idle'));
    expect(router.state.location.pathname).toBe('/forgot-password');
  });
});
