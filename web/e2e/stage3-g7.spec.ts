import { expect, test, type Page, type Route } from '@playwright/test';

/*
 * Группа 7: лендинг, QR-вход на ПК с телефона — с моком API (page.route), против vite preview.
 */

const CODE = 'AbCdEfGhIjKlMnOpQrStUvWxYz0123456789abcd';
const ME = {
  user: { id: 9, name: 'Гость Иванов', phone: '+77011112233', is_superadmin: false },
  club: { id: 7, name: 'Клуб Звезда', timezone: 'Asia/Almaty', locale: 'ru' },
  role: 'client',
  permissions: [],
  read_only: false,
  clubs: [],
  server_time: new Date().toISOString(),
};

interface MockState {
  open: boolean;
  approve: 'ok' | 'error' | 'stale';
  loginBody?: { pc_login?: boolean };
}

async function mockApi(page: Page, state: MockState) {
  const fulfill = (route: Route, body: unknown, status = 200) =>
    route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
  let authed = false;
  await page.route('**/api/**', async (route) => {
    const url = new URL(route.request().url());
    const method = route.request().method();
    switch (url.pathname) {
      case '/api/i18n':
        return fulfill(route, { languages: [{ code: 'ru', name: 'Русский' }], version: '1' });
      case '/api/i18n/ru':
        return fulfill(route, {});
      case '/api/auth/refresh':
        return fulfill(route, { error: 'unauthenticated', message: 'Unauthenticated.' }, 401);
      case '/api/auth/login':
        state.loginBody = route.request().postDataJSON() as MockState['loginBody'];
        authed = true;
        return fulfill(route, { access_token: 't', token_type: 'Bearer', expires_in: 600, csrf_token: 'c', me: ME });
      case '/api/landing':
        return fulfill(route, { trial_days: 30, download_url: 'https://example.test/agent.exe' });
      case `/api/pc-login/${CODE}`:
        if (!authed) return fulfill(route, { error: 'unauthenticated', message: 'Unauthenticated.' }, 401);
        if (method === 'GET') {
          return fulfill(route, state.open
            ? { open: true, club_name: 'Клуб Звезда', computer_name: 'PC-07', user_name: 'Гость Иванов' }
            : { open: false, user_name: 'Гость Иванов' });
        }
        if (state.approve === 'error') {
          return fulfill(route, { error: 'validation_failed', message: 'Подписка клуба не активна.', errors: { code: ['Подписка клуба не активна.'] } }, 422);
        }
        if (state.approve === 'stale') {
          state.open = false;
          return fulfill(route, { approved: false });
        }
        return fulfill(route, { approved: true });
      default:
        return fulfill(route, { error: 'not_found' }, 404);
    }
  });
}

test('лендинг: пробный период с сервера, ссылки входа и wiki', async ({ page }) => {
  await mockApi(page, { open: true, approve: 'ok' });
  await page.goto('/');
  await expect(page).toHaveTitle('Zafiro — система управления компьютерным клубом');
  await expect(page.locator('main a[href="/register"]').first()).toHaveText('30 дней бесплатно');
  await expect(page.locator('a[href="https://example.test/agent.exe"]')).toBeVisible();
  await expect(page.locator('header a[href="/login"]')).toBeVisible();
  await expect(page.locator('body')).toHaveClass(/bg-\[#0b0d10\]/);
  await expect(page.getByText('Сейчас занято: 10 из 16')).toBeVisible();
});

test('QR-вход: гость → вход (pc_login) → подтверждение → «Готово»', async ({ page }) => {
  const state: MockState = { open: true, approve: 'error' };
  await mockApi(page, state);
  await page.goto(`/pc-login/${CODE}`);
  await expect(page).toHaveURL(/\/login$/);
  await page.locator('#phone').pressSequentially('7011112233');
  await page.locator('#password').fill('secret');
  await page.locator('.auth-submit').click();

  await expect(page).toHaveURL(new RegExp(`/pc-login/${CODE}$`));
  expect(state.loginBody?.pc_login).toBe(true);
  await expect(page.locator('.auth-card-head p')).toHaveText('Клуб Клуб Звезда · ПК PC-07');
  await expect(page.locator('.auth-footer').first()).toHaveText('Вы войдёте на этом ПК как Гость Иванов.');

  await page.locator('.auth-submit').click();
  await expect(page.locator('.auth-errors')).toHaveText('Подписка клуба не активна.');

  state.approve = 'ok';
  await page.locator('.auth-submit').click();
  await expect(page).toHaveURL(/\/pc-login\/done$/);
  await expect(page.locator('.auth-card-head h2')).toHaveText('Готово');
});

test('QR-вход: код устарел между показом и подтверждением', async ({ page }) => {
  const state: MockState = { open: true, approve: 'stale' };
  await mockApi(page, state);
  await page.goto(`/pc-login/${CODE}`);
  await page.locator('#phone').pressSequentially('7011112233');
  await page.locator('#password').fill('secret');
  await page.locator('.auth-submit').click();
  await expect(page.locator('.auth-submit')).toHaveText('Войти на ПК');
  await page.locator('.auth-submit').click();
  await expect(page.locator('.auth-card-head h2')).toHaveText('QR-код устарел');
});

test('QR-вход: неверный код — 404', async ({ page }) => {
  await mockApi(page, { open: true, approve: 'ok' });
  await page.goto('/pc-login/short');
  await expect(page.getByText('Not Found')).toBeVisible();
});
