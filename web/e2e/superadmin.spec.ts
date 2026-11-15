import { expect, test, type Page, type Route } from '@playwright/test';
import { totpNow } from './visual/support';

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
      { id: 1, name: 'Супер Админ', phone: '+77010000001', is_superadmin: true, second_factor: true, blocked_since: null, self: true, memberships: [] },
      { id: 5, name: 'Кассир', phone: '+77010000005', is_superadmin: false, second_factor: false, blocked_since: blocked ? '15.10.2026' : null, self: false, memberships: [{ club_id: 1, club_name: 'Arena', role: 'cashier' }] },
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

test('вход суперадмина: пароль → настройка TOTP → коды восстановления → кабинет (Этап 7)', async ({ page }) => {
  let authed = false;
  let verified: Record<string, unknown> | null = null;
  const session = { access_token: 't', token_type: 'Bearer', expires_in: 600, csrf_token: 'c', me: ME };
  await mockApi(page, ME, {
    // До второго фактора сессии нет: refresh — 401.
    'POST /api/auth/refresh': (r) => (authed ? json(r, session) : json(r, { error: 'unauthenticated' }, 401)),
    'POST /api/auth/login': (r) => json(r, { challenge: 'ch-1', setup_required: true, expires_in: 300 }, 202),
    'POST /api/auth/second-factor/setup': (r) =>
      json(r, { secret: 'JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP', otpauth_uri: 'otpauth://totp/Zafiro:%2B77010000001?secret=JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP&issuer=Zafiro' }),
    'POST /api/auth/second-factor/verify': (r) => {
      verified = r.request().postDataJSON() as Record<string, unknown>;
      authed = true;
      return json(r, { session, recovery_codes: ['abcd-efgh-jkmn-pqrs', 'tvwx-yz01-2345-6789'] });
    },
    'GET /api/superadmin/dashboard': (r) => json(r, { error: 'not_found' }, 404),
  });
  await page.goto('/login');
  await page.locator('#phone').pressSequentially('7010000001');
  await page.locator('#password').fill('secret');
  await page.locator('.auth-submit').click();

  await expect(page.locator('.auth-card-head h2')).toHaveText('Настройка двухфакторной аутентификации');
  await expect(page.locator('#totp-secret')).toHaveValue('JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP');
  await expect(page).toHaveURL(/\/login$/);
  await page.locator('#code').fill('123456');
  await page.getByRole('button', { name: 'Включить и войти' }).click();

  await expect(page.locator('[data-recovery-codes]')).toContainText('abcd-efgh-jkmn-pqrs');
  expect(verified).toEqual({ challenge: 'ch-1', code: '123456' });
  await page.getByRole('button', { name: 'Я сохранил коды — продолжить' }).click();
  await expect(page).toHaveURL(/\/superadmin$/);
});

test('пользователи: сброс 2FA и выдача прав — окно подтверждения с кодом TOTP (Этап 7, аудит A-10)', async ({ page }) => {
  let reset = false;
  let granted = false;
  const bodies: { url: string; type: string | undefined; body: unknown }[] = [];
  const seen = (r: Route) => bodies.push({ url: new URL(r.request().url()).pathname, type: r.request().headers()['content-type'], body: r.request().postDataJSON() });
  const list = () => ({
    items: [
      { id: 1, name: 'Супер Админ', phone: '+77010000001', is_superadmin: true, second_factor: true, blocked_since: null, self: true, memberships: [] },
      { id: 7, name: 'Второй', phone: '+77010000007', is_superadmin: true, second_factor: !reset, blocked_since: null, self: false, memberships: [] },
      { id: 8, name: 'Кандидат', phone: '+77010000008', is_superadmin: granted, second_factor: false, blocked_since: null, self: false, memberships: [] },
    ],
    page: 1, per_page: 30, total: 3,
  });
  const good = totpNow();
  const wrong = good === '000000' ? '000001' : '000000';
  await mockApi(page, ME, {
    'GET /api/superadmin/accounts': (r) => json(r, list()),
    'DELETE /api/superadmin/accounts/7/second-factor': (r) => {
      seen(r);
      reset = true;
      return json(r, { message: 'Двухфакторная аутентификация сброшена.' });
    },
    'POST /api/superadmin/accounts/8/superadmin': (r) => {
      seen(r);
      // Сервер: неверный или уже использованный код — 422 по полю code.
      if ((r.request().postDataJSON() as { code?: string }).code !== good) {
        return json(r, { error: 'validation_failed', message: 'Неверный код.', errors: { code: ['Неверный код.'] } }, 422);
      }
      granted = true;
      return json(r, { message: 'Права суперадмина выданы.' });
    },
  });
  await page.goto('/superadmin/accounts');
  // Браузерного confirm больше нет: вопрос — в окне с полем кода.
  let nativeDialogs = 0;
  page.on('dialog', (d) => {
    nativeDialogs++;
    void d.dismiss();
  });
  const dialog = page.getByRole('dialog');
  const code = dialog.getByLabel('Код из приложения');

  await page.getByRole('button', { name: 'Сбросить 2FA' }).click();
  await expect(dialog).toBeVisible();
  await expect(dialog).toContainText('Сбросить двухфакторную аутентификацию Второй?');
  await expect(code).toBeFocused();
  // Без кода форма не уходит (required).
  await dialog.getByRole('button', { name: 'Подтвердить' }).click();
  expect(bodies).toHaveLength(0);
  await code.fill(good);
  await dialog.getByRole('button', { name: 'Подтвердить' }).click();
  await expect(page.getByRole('status')).toHaveText('Двухфакторная аутентификация сброшена.');
  await expect(dialog).toBeHidden();
  expect(bodies[0]).toEqual({ url: '/api/superadmin/accounts/7/second-factor', type: 'application/json', body: { code: good } });
  await expect(page.getByText('2FA не настроена').first()).toBeVisible();
  await expect(page.getByRole('button', { name: 'Сбросить 2FA' })).toHaveCount(0);

  // Выдача прав: неверный код — ошибка под полем, окно открыто; верный — права выданы.
  await page.getByRole('button', { name: 'Сделать суперадмином' }).click();
  await expect(dialog).toContainText('Выдать Кандидат права суперадмина?');
  await expect(code).toHaveValue('');
  await code.fill(wrong);
  await code.press('Enter');
  await expect(dialog.getByRole('alert')).toHaveText('Неверный код.');
  await expect(dialog).toBeVisible();
  expect(granted).toBe(false);
  await code.fill(good);
  await code.press('Enter');
  await expect(page.getByRole('status')).toHaveText('Права суперадмина выданы.');
  await expect(dialog).toBeHidden();
  expect(bodies.slice(1).map((b) => b.body)).toEqual([{ code: wrong }, { code: good }]);

  // Escape закрывает окно без запроса.
  await page.getByRole('button', { name: 'Снять права суперадмина' }).first().click();
  await expect(dialog).toBeVisible();
  await page.keyboard.press('Escape');
  await expect(dialog).toBeHidden();
  expect(bodies).toHaveLength(3);
  expect(nativeDialogs).toBe(0);

  // Телефон (390 px): окно в пределах экрана, «Подтвердить» достижима.
  await page.setViewportSize({ width: 390, height: 844 });
  await page.getByRole('button', { name: 'Снять права суперадмина' }).first().click();
  await expect(dialog).toBeVisible();
  const box = await dialog.boundingBox();
  expect(box && box.x >= 0 && box.x + box.width <= 390 && box.y >= 0 && box.y + box.height <= 844).toBe(true);
  await expect(dialog.getByRole('button', { name: 'Подтвердить' })).toBeInViewport();
});

test('не суперадмину кабинет не открывается (403)', async ({ page }) => {
  await mockApi(page, { ...ME, user: { ...ME.user, is_superadmin: false }, club: { id: 1, name: 'Arena', timezone: 'Asia/Almaty', locale: 'ru' }, role: 'owner' });
  await page.goto('/superadmin/plans');
  await expect(page.locator('body')).toContainText('403');
});
