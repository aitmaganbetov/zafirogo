import { expect, test, type Page, type Route } from '@playwright/test';

/*
 * Вход и панель клуба с моком API (page.route) — работает против vite preview. Настоящий сервер —
 * auth.server.spec.ts (E2E_REAL_SERVER=1).
 */

const ME = {
  user: { id: 1, name: 'Иван Петров', phone: '+77011112233', is_superadmin: false },
  club: { id: 7, name: 'Клуб Звезда', timezone: 'Asia/Almaty', locale: 'ru' },
  role: 'cashier',
  permissions: ['cash.view', 'chat.view', 'orders.view'],
  read_only: false,
  clubs: [],
  server_time: new Date().toISOString(),
};

async function mockApi(page: Page) {
  const fulfill = (route: Route, body: unknown, status = 200) =>
    route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
  await page.route('**/api/**', async (route) => {
    const url = new URL(route.request().url());
    switch (url.pathname) {
      case '/api/i18n':
        return fulfill(route, { languages: [{ code: 'ru', name: 'Русский' }, { code: 'en', name: 'English' }], version: '1' });
      case '/api/i18n/ru':
      case '/api/i18n/en':
        return fulfill(route, {});
      case '/api/auth/refresh':
        return fulfill(route, { error: 'unauthenticated', message: 'Unauthenticated.' }, 401);
      case '/api/auth/login': {
        const body = route.request().postDataJSON() as { phone: string; password: string };
        if (body.password !== 'secret') {
          return fulfill(route, { error: 'validation_failed', message: 'Неверные учетные данные.', errors: { phone: ['Неверные учетные данные.'] } }, 422);
        }
        expect(body.phone).toBe('7011112233');
        return fulfill(route, { access_token: 't', token_type: 'Bearer', expires_in: 600, csrf_token: 'c', me: ME });
      }
      case '/api/layout/badges':
        return fulfill(route, { chat: 4, orders: 2 });
      case '/api/ws-ticket':
        return fulfill(route, { ticket: 'x'.repeat(43), expires_in: 30 });
      default:
        return fulfill(route, { error: 'not_found' }, 404);
    }
  });
}

test('вход: маска телефона, ошибка под полем, затем панель клуба с меню по правам и бейджами', async ({ page }) => {
  await mockApi(page);
  await page.goto('/dashboard');
  await expect(page).toHaveURL(/\/login$/);

  const phone = page.locator('#phone');
  await phone.pressSequentially('7011112233');
  await expect(phone).toHaveValue('701 111-22-33');
  await page.locator('#password').fill('wrong');
  await page.locator('.auth-submit').click();
  await expect(page.locator('.auth-errors')).toHaveText('Неверные учетные данные.');
  await expect(page.locator('.auth-error-text')).toHaveText('Неверные учетные данные.');
  await expect(page.locator('#password')).toHaveValue('');

  await page.locator('#password').fill('secret');
  await page.locator('.auth-submit').click();
  await expect(page).toHaveURL(/\/dashboard$/);
  await expect(page.locator('.admin-app')).toBeVisible();
  await expect(page.locator('.sidebar')).toContainText('Иван Петров');
  // Кассир: «Касса» есть, «Админпанель» скрыта (@unless($frontDesk)), «Склад» нет права.
  await expect(page.locator('.sidebar-link', { hasText: 'Касса' })).toBeVisible();
  await expect(page.locator('.sidebar-link', { hasText: 'Склад' })).toHaveCount(0);
  await expect(page.locator('.sidebar-link', { hasText: 'Чат' })).toContainText('4');
  await expect(page.locator('.sidebar-link', { hasText: 'Заказы' })).toContainText('2');
});

test('регистрация: ошибки 422 под полями', async ({ page }) => {
  await mockApi(page);
  await page.route('**/api/auth/register', (route) =>
    route.fulfill({
      status: 422,
      contentType: 'application/json',
      body: JSON.stringify({
        error: 'validation_failed',
        message: 'Имя обязательно.',
        errors: { name: ['Имя обязательно.'], phone: ['Этот номер телефона уже зарегистрирован.'] },
      }),
    }),
  );
  await page.goto('/register');
  await expect(page).toHaveTitle('Регистрация · Zafiro');
  await page.locator('#club_name').fill('Клуб');
  await page.locator('#phone').pressSequentially('7011112233');
  await page.locator('#password').fill('secret1');
  await page.locator('#password_confirmation').fill('secret1');
  await page.locator('#name').fill('x');
  await page.locator('.auth-submit').click();
  await expect(page.locator('.auth-errors')).toHaveText('Имя обязательно.');
  await expect(page.locator('.auth-error-text')).toHaveText(['Имя обязательно.', 'Этот номер телефона уже зарегистрирован.']);
});

test('неизвестный адрес — 404 Laravel', async ({ page }) => {
  await mockApi(page);
  await page.goto('/no-such-page');
  await expect(page.locator('h1')).toHaveText('404');
  await expect(page.getByText('Not Found')).toBeVisible();
});
