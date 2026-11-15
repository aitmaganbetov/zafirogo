import { expect, test, type Page, type Route } from '@playwright/test';

/*
 * Оплата подписки (тема Б, Zafiro 77ae201) с моком API (page.route) — против vite preview: владелец выставляет счёт
 * на экране «Подписка» и видит, как его оплатить; суперадмин в «Оплатах» отмечает оплату.
 * Laravel: billing/index.blade.php, superadmin/invoices/index.blade.php.
 */

const OWNER = {
  user: { id: 2, name: 'Айдар Сейтказы', phone: '+77010000002', is_superadmin: false },
  club: { id: 1, name: 'Zafiro Arena', timezone: 'Asia/Almaty', locale: 'ru' },
  role: 'owner',
  permissions: ['billing.view', 'billing.pay'],
  read_only: false,
  clubs: [],
  server_time: new Date().toISOString(),
};
const SUPER = { ...OWNER, user: { id: 1, name: 'Супер Админ', phone: '+77010000001', is_superadmin: true }, club: null, role: null, permissions: [] };

const INVOICE = {
  id: 42, number: 'ZF-00042', plan_name: 'Профессиональный', months: 3, computers: 12, price_per_computer_tiyn: 2500000,
  discount_percent: 20, amount_tiyn: 72000000, status: 'pending', created_at: '2026-11-15T07:00:00Z', period_ends_at: null,
};

const json = (route: Route, body: unknown, status = 200) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });

type Handler = (route: Route) => Promise<void> | void;

async function mockApi(page: Page, me: unknown, extra: Record<string, Handler>) {
  await page.addInitScript({ content: "document.cookie = '__Host-zafiro_csrf=c; Path=/; Secure';" });
  await page.route('**/api/**', async (route) => {
    const url = new URL(route.request().url());
    const key = `${route.request().method()} ${url.pathname}`;
    if (extra[key]) return extra[key](route);
    switch (key) {
      case 'GET /api/i18n':
        return json(route, { languages: [{ code: 'ru', name: 'Русский' }], version: '1' });
      case 'GET /api/i18n/ru':
        return json(route, {});
      case 'POST /api/auth/refresh':
        return json(route, { access_token: 't', token_type: 'Bearer', expires_in: 600, csrf_token: 'c', me });
      case 'GET /api/layout/badges':
        return json(route, { chat: 0, orders: 0 });
      case 'POST /api/ws-ticket':
        return json(route, { error: 'service_unavailable' }, 503);
      default:
        return json(route, { error: 'not_found' }, 404);
    }
  });
}

test('подписка: владелец выставляет счёт и видит, как оплатить его в Kaspi', async ({ page }) => {
  let pending: typeof INVOICE | null = null;
  let sent: unknown = null;
  const amounts = (price: number) => [1, 3, 6, 12].map((months) => ({ months, amount_tiyn: (price * 12 * months * 80) / 100 }));
  await mockApi(page, OWNER, {
    'GET /api/billing': (r) =>
      json(r, {
        subscription: {
          plan_name: 'Профессиональный', saas_plan_id: 1, status: 'active', state: 'active', trial_ends_at: null,
          current_period_ends_at: '2026-12-01T05:00:00Z', monthly_price_tiyn: 2000000, monthly_total_tiyn: 24000000,
          discount_percent: 20, discount_until: null,
        },
        can_pay: true, computers: 12, discount_percent: 20, months: [1, 3, 6, 12],
        plans: [{ id: 1, name: 'Профессиональный', price_monthly_tiyn: 2500000, amounts: amounts(2500000) }],
        kaspi: { url: 'https://pay.kaspi.kz/pay/zafiro', recipient: 'ИП Zafiro' },
        pending, invoices: pending ? [pending] : [],
      }),
    'POST /api/billing/invoices': (r) => {
      sent = r.request().postDataJSON();
      pending = INVOICE;
      return json(r, { message: 'Счёт выставлен. Оплатите его через Kaspi.', invoice: INVOICE }, 201);
    },
    'POST /api/billing/invoices/42/cancel': (r) => {
      pending = null;
      return json(r, { message: 'Счёт отменён.' });
    },
  });
  await page.goto('/billing');
  await expect(page.getByText('20 000 ₸ за ПК · 240 000 ₸ за 12 ПК со скидкой 20%')).toBeVisible();
  await expect(page.getByText('ПК в клубе: 12. Сумма считается за все ПК клуба. Ваша скидка: 20%.')).toBeVisible();
  const total = page.locator('[data-billing-total]');
  await expect(total).toHaveText('240 000');
  // Срок — радиокнопки, спрятанные под подписями (peer sr-only): выбираем подписью.
  await page.locator('label', { has: page.locator('input[name="months"][value="3"]') }).click();
  await expect(total).toHaveText('720 000');
  await page.getByRole('button', { name: 'Выставить счёт' }).click();
  expect(sent).toEqual({ saas_plan_id: 1, months: 3 });

  await expect(page.getByRole('status')).toHaveText('Счёт выставлен. Оплатите его через Kaspi.');
  await expect(page.getByRole('heading', { name: 'Счёт ZF-00042 ожидает оплаты' })).toBeVisible();
  await expect(page.getByText('Переведите 720 000 ₸.')).toBeVisible();
  await expect(page.getByText('В комментарии к платежу укажите номер счёта ZF-00042.')).toBeVisible();
  await expect(page.getByText('Получатель: ИП Zafiro')).toBeVisible();
  const pay = page.getByRole('link', { name: 'Оплатить в Kaspi' });
  await expect(pay).toHaveAttribute('href', 'https://pay.kaspi.kz/pay/zafiro');
  await expect(pay).toHaveAttribute('rel', 'noopener noreferrer');
  await expect(page.getByRole('button', { name: 'Выставить счёт' })).toHaveCount(0);

  page.once('dialog', (d) => void d.accept());
  await page.getByRole('button', { name: 'Отменить счёт' }).click();
  await expect(page.getByRole('status')).toHaveText('Счёт отменён.');
  await expect(page.getByRole('button', { name: 'Выставить счёт' })).toBeVisible();
});

test('кабинет: «Оплаты» в меню, суперадмин отмечает оплату счёта', async ({ page }) => {
  let paid = false;
  await mockApi(page, SUPER, {
    'GET /api/superadmin/invoices': (r) =>
      json(r, {
        pending: paid ? [] : [{ id: 42, number: 'ZF-00042', club_id: 1, club_name: 'Zafiro Arena', plan_name: 'Профессиональный', months: 3, computers: 12, price_per_computer_tiyn: 2500000, discount_percent: 20, amount_tiyn: 72000000, created: '15.11.2026 07:00' }],
        closed: paid ? [{ id: 42, number: 'ZF-00042', club_id: 1, club_name: 'Zafiro Arena', plan_name: 'Профессиональный', months: 3, amount_tiyn: 72000000, status: 'paid', created: '15.11.2026', paid: '15.11.2026' }] : [],
        page: 1, per_page: 30, total: paid ? 1 : 0,
        kaspi: { url: 'https://pay.kaspi.kz/pay/zafiro', recipient: 'ИП Zafiro' },
      }),
    'POST /api/superadmin/invoices/42/pay': (r) => {
      paid = true;
      return json(r, { message: 'Счёт ZF-00042 оплачен, подписка продлена до 01.03.2027.' });
    },
  });
  await page.goto('/superadmin/invoices');
  await expect(page.locator('h2.admin-title')).toHaveText('Оплаты');
  await expect(page.locator('a.sidebar-link.active')).toHaveText('Оплаты');
  await expect(page.getByText('720 000 ₸')).toBeVisible();
  await expect(page.getByText('Пока пусто.')).toBeVisible();
  await expect(page.locator('input[name="url"]')).toHaveValue('https://pay.kaspi.kz/pay/zafiro');

  page.once('dialog', (d) => {
    expect(d.message()).toBe('Деньги по счёту ZF-00042 пришли? Подписка клуба будет продлена.');
    void d.accept();
  });
  await page.getByRole('button', { name: 'Оплата получена' }).click();
  await expect(page.getByRole('status')).toHaveText('Счёт ZF-00042 оплачен, подписка продлена до 01.03.2027.');
  await expect(page.getByText('Неоплаченных счетов нет.')).toBeVisible();
  await expect(page.getByRole('row', { name: /ZF-00042.*Оплачен 15\.11\.2026/ })).toBeVisible();
});
