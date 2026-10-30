import { expect, test, type Page, type Route } from '@playwright/test';

/*
 * Кабинет суперадмина (Этап 3, группа 6) — с моком API (page.route), против vite preview: тарифы платформы, блокировка
 * пользователя, сохранение переводов; не суперадмину — 403.
 */

const ME = {
  user: { id: 1, name: 'Супер Админ', phone: '+77010000001', is_superadmin: true },
  club: null,
  role: null,
  permissions: [],
  read_only: false,
  clubs: [],
  server_time: new Date().toISOString(),
};

type Handler = (route: Route, url: URL) => Promise<void> | void;

const json = (route: Route, body: unknown, status = 200) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });

async function mockApi(page: Page, me: unknown, extra: Record<string, Handler> = {}) {
  await page.addInitScript({ content: "document.cookie = '__Host-zafiro_csrf=c; Path=/; Secure';" });
  await page.route('**/api/**', async (route) => {
    const url = new URL(route.request().url());
    const key = `${route.request().method()} ${url.pathname}`;
    if (extra[key]) return extra[key](route, url);
    switch (url.pathname) {
      case '/api/i18n':
        return json(route, { languages: [{ code: 'ru', name: 'Русский' }], version: '1' });
      case '/api/i18n/ru':
        return json(route, {});
      case '/api/auth/refresh':
        return json(route, { access_token: 't', token_type: 'Bearer', expires_in: 600, csrf_token: 'c', me });
      default:
        return json(route, { error: 'not_found' }, 404);
    }
  });
}

const plan = { id: 1, name: 'Профессиональный', slug: 'pro', price_monthly_tiyn: 2500000, trial_days: 0, features: [], is_active: true, clubs_count: 2 };

test('тарифы платформы: цена уходит в тиынах, ошибка формы — в layout', async ({ page }) => {
  let sent: Record<string, unknown> | null = null;
  await mockApi(page, ME, {
    'GET /api/superadmin/plans': (r) => json(r, { items: [plan] }),
    'POST /api/superadmin/plans': (r) => {
      sent = r.request().postDataJSON() as Record<string, unknown>;
      return json(r, { error: 'validation_failed', message: 'The slug has already been taken.', errors: { slug: ['The slug has already been taken.'] } }, 422);
    },
  });
  await page.goto('/superadmin/plans');
  await expect(page.locator('h2.admin-title')).toHaveText('Тарифы платформы');
  await expect(page.locator('input[name="price_monthly"]').first()).toHaveValue('25000.00');
  const form = page.locator('form').last();
  await form.locator('input[name="name"]').fill('Старт');
  await form.locator('input[name="slug"]').fill('pro');
  await form.locator('input[name="price_monthly"]').fill('1500.5');
  await form.locator('textarea[name="features"]').fill('Карта\nКасса');
  await form.getByRole('button', { name: 'Создать тариф' }).click();
  await expect(page.getByRole('alert')).toHaveText('The slug has already been taken.');
  expect(sent).toMatchObject({ name: 'Старт', slug: 'pro', price_monthly_tiyn: 150050, features: 'Карта\nКасса', is_active: true });
});

test('пользователи: блокировка с подтверждением, сообщение в layout', async ({ page }) => {
  let blocked = false;
  const list = () => ({
    items: [
      { id: 1, name: 'Супер Админ', phone: '+77010000001', is_superadmin: true, blocked_since: null, self: true, memberships: [] },
      { id: 5, name: 'Кассир', phone: '+77010000005', is_superadmin: false, blocked_since: blocked ? '15.10.2026' : null, self: false, memberships: [{ club_id: 1, club_name: 'Arena', role: 'cashier' }] },
    ],
    page: 1, per_page: 30, total: 2,
  });
  await mockApi(page, ME, {
    'GET /api/superadmin/accounts': (r) => json(r, list()),
    'POST /api/superadmin/accounts/5/block': (r) => {
      blocked = true;
      return json(r, { message: 'Пользователь заблокирован.' });
    },
  });
  await page.goto('/superadmin/accounts');
  page.once('dialog', (d) => void d.accept());
  await page.getByRole('button', { name: 'Заблокировать' }).click();
  await expect(page.getByRole('status')).toHaveText('Пользователь заблокирован.');
  await expect(page.getByText('Заблокирован с 15.10.2026')).toBeVisible();
  await expect(page.getByRole('button', { name: 'Разблокировать' })).toBeVisible();
  // Свой аккаунт — без кнопок.
  await expect(page.getByText('(вы)')).toBeVisible();
});

test('переводы: поля страницы уходят хэшами, редактор показывает :name', async ({ page }) => {
  let body: unknown = null;
  await mockApi(page, ME, {
    'GET /api/superadmin/languages/2/translations': (r) =>
      json(r, {
        language: { id: 2, code: 'kk', name: 'Қазақша' },
        items: [{ hash: 'abc', label: 'Привет, :name', source: 'Привет, :name', value: 'Сәлем, :name' }],
        page: 1, per_page: 50, total: 1, translated: 1, source_total: 1,
      }),
    'PUT /api/superadmin/languages/2/translations': (r) => {
      body = r.request().postDataJSON();
      return json(r, { message: 'Сохранён 1 перевод.' });
    },
  });
  await page.goto('/superadmin/languages/2/translations');
  await expect(page.locator('textarea')).toHaveValue('Сәлем, :name');
  await page.locator('textarea').fill('Сәлеметсіз бе, :name');
  await page.getByRole('button', { name: 'Сохранить переводы на странице' }).click();
  await expect(page.getByRole('status')).toHaveText('Сохранён 1 перевод.');
  expect(body).toEqual({ translations: { abc: 'Сәлеметсіз бе, :name' } });
});

test('не суперадмину кабинет не открывается (403)', async ({ page }) => {
  await mockApi(page, { ...ME, user: { ...ME.user, is_superadmin: false }, club: { id: 1, name: 'Arena', timezone: 'Asia/Almaty', locale: 'ru' }, role: 'owner' });
  await page.goto('/superadmin/plans');
  await expect(page.locator('body')).toContainText('403');
});
