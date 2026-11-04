import { expect, test, type Page, type Route } from '@playwright/test';

/*
 * Экраны «Пользователи», «Работники», «Права» с моком API (page.route) — против vite preview.
 * Laravel: users.blade.php, users-password.blade.php, workers/index.blade.php, settings/permissions*.blade.php.
 */

const ME = {
  user: { id: 2, name: 'Айдар Сейтказы', phone: '+77010000002', is_superadmin: false },
  club: { id: 1, name: 'Zafiro Arena', timezone: 'Asia/Almaty', locale: 'ru' },
  role: 'owner',
  permissions: ['customers.view', 'customers.create', 'customers.update', 'customer_money.update', 'workers.view', 'workers.create', 'workers.update', 'workers.delete', 'settings.view'],
  read_only: false,
  clubs: [],
  server_time: new Date().toISOString(),
};

const CUSTOMER = {
  user_id: 6, name: 'Нурлан Ахметов', phone: '+77011000006', email: null, role: 'client', discount: '0.00',
  discount_effective: '7.00', discount_cumulative: true, account_locked: false, total_spent_tiyn: 1840000,
  balance_tiyn: 350000, bonus_balance_tiyn: 25000,
};

const WORKERS = {
  items: [
    { membership_id: 1, user_id: 2, name: 'Айдар Сейтказы', phone: '+77010000002', role: 'owner', role_label: 'Владелец', on_shift: false, manageable: false },
    { membership_id: 3, user_id: 4, name: 'Ержан Касымов', phone: '+77010000004', role: 'cashier', role_label: 'Кассир', on_shift: true, manageable: true },
  ],
  next_cursor: null, prev_cursor: null, total: 2, page_size: 50,
  shift_limits: [{ role: 'cashier', count: 1, limit: 2 }, { role: 'operator', count: 0, limit: null }],
  assignable_roles: [
    { role: 'admin', label: 'Администратор' }, { role: 'manager', label: 'Менеджер' },
    { role: 'cashier', label: 'Кассир' }, { role: 'operator', label: 'Оператор' },
  ],
};

const PERMISSIONS = {
  member: { membership_id: 3, name: 'Ержан Касымов', phone: '+77010000004', role: 'cashier', role_label: 'Кассир', customized: true, editable: true },
  actions: [
    { action: 'view', label: 'Просмотр' }, { action: 'create', label: 'Создание' },
    { action: 'update', label: 'Изменение' }, { action: 'delete', label: 'Удаление' },
  ],
  sections: [
    { section: 'cash', label: 'Касса', cells: [
      { action: 'view', description: 'Касса, чеки, X/Z-отчёты', allowed: true, given: false },
      { action: 'create', description: 'Смены, продажи', allowed: true, given: false },
      { action: 'delete', description: 'Возвраты', allowed: true, given: true },
    ] },
  ],
};

interface Seen {
  createBody?: Record<string, unknown>;
  createKey?: string | null;
  permissionsBody?: Record<string, unknown>;
}

async function mockApi(page: Page, seen: Seen) {
  const fulfill = (route: Route, body: unknown, status = 200) =>
    route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
  await page.route('**/api/**', async (route) => {
    const req = route.request();
    const url = new URL(req.url());
    const key = `${req.method()} ${url.pathname}`;
    switch (key) {
      case 'GET /api/i18n':
        return fulfill(route, { languages: [{ code: 'ru', name: 'Русский' }], version: '1' });
      case 'GET /api/i18n/ru':
        return fulfill(route, {});
      case 'POST /api/auth/refresh':
        return fulfill(route, { error: 'unauthenticated', message: 'Unauthenticated.' }, 401);
      case 'POST /api/auth/login':
        return fulfill(route, { access_token: 't', token_type: 'Bearer', expires_in: 600, csrf_token: 'c', me: ME });
      case 'GET /api/layout/badges':
        return fulfill(route, { chat: 0, orders: 0 });
      case 'POST /api/ws-ticket':
        return fulfill(route, { error: 'service_unavailable' }, 503);
      case 'GET /api/customers':
        return fulfill(route, { items: [CUSTOMER], next_after_id: null, prev_before_id: null, total: 1 });
      case 'POST /api/customers':
        seen.createBody = req.postDataJSON() as Record<string, unknown>;
        seen.createKey = req.headers()['idempotency-key'] ?? null;
        return fulfill(route, { customer: { ...CUSTOMER, user_id: 20, name: 'Новый' }, message: 'Пользователь добавлен.' }, 201);
      case 'POST /api/customers/6/password':
        return fulfill(route, { user_id: 6, name: CUSTOMER.name, password: 'Ab3dE5gH' });
      case 'GET /api/workers':
        return fulfill(route, WORKERS);
      case 'POST /api/workers':
        return fulfill(route, {
          error: 'validation_failed', message: 'Пользователь с таким номером не зарегистрирован в Zafiro. Отправьте ему ссылку-приглашение.',
          errors: { phone: ['Пользователь с таким номером не зарегистрирован в Zafiro. Отправьте ему ссылку-приглашение.'] },
        }, 422);
      case 'POST /api/club-invitations':
        return fulfill(route, { path: '/invitations/' + 'a'.repeat(64), message: 'Ссылка-приглашение создана. Она действует 7 дней.' }, 201);
      case 'GET /api/settings/permissions/3':
        return fulfill(route, PERMISSIONS);
      case 'PUT /api/settings/permissions/3':
        seen.permissionsBody = req.postDataJSON() as Record<string, unknown>;
        return fulfill(route, { message: 'Права сохранены.' });
      default:
        return fulfill(route, { error: 'not_found' }, 404);
    }
  });
}

async function login(page: Page) {
  await page.goto('/dashboard');
  await page.locator('#phone').pressSequentially('7010000002');
  await page.locator('#password').fill('secret');
  await page.locator('.auth-submit').click();
  await expect(page).toHaveURL(/\/dashboard$/);
}

test('пользователи: список, новый клиент с балансом в тиынах, сброс пароля один раз', async ({ page }) => {
  const seen: Seen = {};
  await mockApi(page, seen);
  await login(page);
  await page.locator('.sidebar-link', { hasText: 'Пользователи' }).click();
  await expect(page).toHaveURL(/\/users$/);
  await expect(page.locator('h3')).toHaveText('Пользователи');
  const row = page.locator('[data-customer-row]');
  await expect(row).toContainText('Нурлан Ахметов');
  await expect(row).toContainText('7.00%');
  await expect(row).toContainText('накопительная');
  await expect(row).toContainText('18 400.00');
  await expect(row).toContainText('3 500.00');

  await page.locator('#open-user-dialog').click();
  await expect(page.locator('#user-create-dialog')).toBeVisible();
  await page.locator('#new-user-name').fill('Новый');
  await page.locator('#new-user-phone').fill('7001234567');
  await page.locator('#new-user-password').fill('secret1');
  await page.locator('#new-user-password-confirmation').fill('secret1');
  await page.locator('#new-user-balance').fill('1500.5');
  await page.locator('#user-create-dialog button[type=submit]').click();
  await expect(page.getByText('Пользователь добавлен.')).toBeVisible();
  await expect(page.locator('#user-create-dialog')).toBeHidden();
  expect(seen.createBody).toMatchObject({ name: 'Новый', phone: '7001234567', balance_tiyn: 150050, bonus_balance_tiyn: 0, discount: '0' });
  expect(seen.createKey).toBeTruthy();

  page.once('dialog', (d) => void d.accept());
  await row.getByText('Сбросить пароль').click();
  await expect(page).toHaveURL(/\/users\/6\/password$/);
  await expect(page.locator('[data-new-password]')).toHaveText('Ab3dE5gH');
  await page.getByText('К списку клиентов').click();
  await expect(page).toHaveURL(/\/users$/);
});

test('работники: ошибка добавления, ссылка-приглашение, права сотрудника', async ({ page }) => {
  const seen: Seen = {};
  await mockApi(page, seen);
  await login(page);
  await page.locator('.sidebar-link', { hasText: 'Работники' }).click();
  await expect(page).toHaveURL(/\/workers$/);
  await expect(page.locator('[data-worker-row]').nth(1)).toContainText('На смене');
  await expect(page.getByText('Кассиров на смене')).toBeVisible();
  await expect(page.locator('p', { hasText: 'Кассиров на смене' }).locator('strong').first()).toHaveText('1 / 2');

  await page.locator('input[name=phone]').fill('7009999999');
  await page.getByRole('button', { name: 'Добавить' }).click();
  await expect(page.getByRole('alert')).toHaveText('Пользователь с таким номером не зарегистрирован в Zafiro. Отправьте ему ссылку-приглашение.');

  await page.getByRole('button', { name: 'Создать ссылку' }).click();
  await expect(page.locator('#club-invite-url')).toHaveValue(new RegExp(`/invitations/${'a'.repeat(64)}$`));

  await page.locator('[data-worker-row]').nth(1).getByText('Права').click();
  await expect(page).toHaveURL(/\/settings\/permissions\/3$/);
  await expect(page.locator('#permissions-title')).toHaveText('Права: Ержан Касымов');
  const refund = page.getByLabel('Касса: Удаление — Возвраты');
  await expect(refund).toHaveClass(/ring-amber-400/);
  await refund.uncheck();
  await page.getByRole('button', { name: 'Сохранить права' }).click();
  await expect(page.getByText('Права сохранены.')).toBeVisible();
  expect(seen.permissionsBody).toEqual({ rights: { cash: { view: true, create: true, delete: false } } });
});
