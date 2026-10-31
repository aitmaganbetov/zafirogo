import { expect, test, type Page, type Route } from '@playwright/test';

/*
 * Касса, склад, заказы из агента, услуги с моком API (page.route) — против vite preview.
 * Сессия: refresh по CSRF-cookie (как открытая заново вкладка).
 */

const ME = {
  user: { id: 4, name: 'Дана Кассир', phone: '+77010000004', is_superadmin: false },
  club: { id: 1, name: 'Zafiro Arena', timezone: 'Asia/Almaty', locale: 'ru' },
  role: 'cashier',
  permissions: ['cash.view', 'cash.create', 'stock.view', 'stock.create', 'orders.view', 'orders.update', 'orders.delete'],
  read_only: false,
  clubs: [],
  server_time: new Date().toISOString(),
};

const SHIFT = {
  id: 3, status: 'open', opened_by: 4, closed_by: null, opening_balance_tiyn: 2000000, closing_balance_tiyn: null,
  opened_at: '2026-10-15T04:00:00Z', closed_at: null, z_report_printed: false,
};

type Handler = (route: Route, url: URL) => Promise<void> | void;

const json = (route: Route, body: unknown, status = 200) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });

async function mockApi(page: Page, extra: Record<string, Handler> = {}) {
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
        return json(route, { access_token: 't', token_type: 'Bearer', expires_in: 600, csrf_token: 'c', me: ME });
      case '/api/layout/badges':
        return json(route, { chat: 0, orders: 1 });
      case '/api/ws-ticket':
        return json(route, { error: 'unavailable' }, 503);
      default:
        return json(route, { error: 'not_found' }, 404);
    }
  });
}

test('касса: корзина считает «НДС» в тиынах, продажа ведёт на чек с итогом сервера', async ({ page }) => {
  let sold: unknown = null;
  await mockApi(page, {
    'GET /api/cash/shift': (r) => json(r, { shift: SHIFT, totals: { opening_tiyn: 2000000, income_tiyn: 500000, expense_tiyn: 0, balance_tiyn: 2500000 } }),
    'GET /api/cash/catalog': (r) =>
      json(r, { products_total: 2, categories: [{ name: 'Напитки', count: 2 }], services: [{ id: 1, name: 'Печать А4', price_tiyn: 5000, unit: 'лист' }] }),
    'GET /api/cash/products': (r) =>
      json(r, {
        items: [
          { id: 1, name: 'Coca-Cola 0,5 л', category: 'Напитки', price_tiyn: 45000, quantity: 48, image_url: '/images/product-placeholder.svg' },
          { id: 3, name: 'Вода 0,5 л', category: 'Напитки', price_tiyn: 25000, quantity: 3, image_url: '/images/product-placeholder.svg' },
        ],
        next_cursor: null,
      }),
    'POST /api/cash/sales': async (r) => {
      sold = r.request().postDataJSON();
      expect(r.request().headers()['idempotency-key']).toBeTruthy();
      return json(r, { transaction: { id: 77, shift_id: 3, type: 'income', amount_tiyn: 95000, description: 'Продажа POS', created_at: '2026-10-15T06:00:00Z' }, replayed: false, message: 'Продажа проведена. Распечатайте чек.' }, 201);
    },
    'GET /api/cash/receipts/77': (r) =>
      json(r, {
        transaction: {
          id: 77, shift_id: 3, type: 'income', amount_tiyn: 95000, description: 'Продажа POS', created_at: '2026-10-15T06:00:00Z',
          items: [
            { product_id: 1, name: 'Coca-Cola 0,5 л', quantity: 2, price_tiyn: 45000, total_tiyn: 90000 },
            { service_id: 1, name: 'Печать А4', quantity: 1, price_tiyn: 5000, total_tiyn: 5000 },
          ],
        },
        refund: null, refundable: true, can_refund: false, shift_open: true,
      }),
  });
  await page.goto('/cash-register');
  await expect(page.locator('.pos-status')).toContainText('25 000,00 KZT');
  const cola = page.locator('.pos-product', { hasText: 'Coca-Cola' });
  await cola.locator('.pos-add').click();
  await cola.locator('.pos-add').click();
  await page.locator('.pos-product', { hasText: 'Печать А4' }).locator('.pos-add').click();
  await expect(page.locator('#subtotal')).toHaveText(/^950\s₸$/);
  // Math.round(950 * 5 / 105) = 45
  await expect(page.locator('#tax')).toHaveText('45 ₸');
  await expect(cola).toHaveClass(/selected/);
  await page.locator('#checkout').click();
  await expect(page).toHaveURL(/\/cash-register\/receipts\/77$/);
  expect(sold).toEqual({ items: [{ product_id: 1, quantity: 2 }, { service_id: 1, quantity: 1 }] });
  await expect(page.locator('.receipt-total')).toContainText('950,00 KZT');
  await expect(page.getByText('Продажа проведена. Распечатайте чек.')).toBeVisible();
});

test('касса: пополнение — пользователь только из списка поиска, сумма в тиынах', async ({ page }) => {
  let body: unknown = null;
  await mockApi(page, {
    'GET /api/cash/shift': (r) => json(r, { shift: SHIFT, totals: { opening_tiyn: 0, income_tiyn: 0, expense_tiyn: 0, balance_tiyn: 0 } }),
    'GET /api/cash/catalog': (r) => json(r, { products_total: 0, categories: [], services: [] }),
    'GET /api/cash/products': (r) => json(r, { items: [], next_cursor: null }),
    'GET /api/cash/customers': (r, url) =>
      json(r, url.searchParams.get('q') === 'нет' ? [] : [{ id: 9, name: 'Ерлан', phone: '+77011234567', balance_tiyn: 1500000, bonus_balance_tiyn: 0 }]),
    'POST /api/cash/balance-top-ups': async (r) => {
      body = r.request().postDataJSON();
      return json(r, { transaction: { id: 80, shift_id: 3, type: 'income', amount_tiyn: 500050, description: 'x', created_at: '2026-10-15T06:00:00Z' }, bonus_tiyn: 0, new_balance_tiyn: 2000050, new_bonus_balance_tiyn: 0, replayed: false, message: 'Баланс пользователя пополнен.' }, 201);
    },
  });
  await page.goto('/cash-register');
  await page.locator('[data-pos-view="balance"]').click();
  const search = page.locator('#top-up-user-search');
  await search.fill('нет');
  await expect(page.locator('[data-user-no-results]')).toBeVisible();
  await search.fill('Ерл');
  await page.locator('.pos-user-option', { hasText: 'Ерлан' }).click();
  await expect(search).toHaveValue('Ерлан · +77011234567');
  await page.locator('input[name="amount"]').fill('5000.5');
  await page.locator('.pos-balance-form .pos-checkout').click();
  await expect(page.getByText('Баланс пользователя пополнен.')).toBeVisible();
  expect(body).toEqual({ user_id: 9, amount_tiyn: 500050, balance_type: 'balance', payment_method: 'cash' });
});

test('Z-отчёт: закрыть смену можно только после печати (afterprint → nonce)', async ({ page }) => {
  let printedWith = '';
  const report = {
    type: 'Z', shift: SHIFT, totals: { opening_tiyn: 2000000, income_tiyn: 0, expense_tiyn: 0, balance_tiyn: 2000000 },
    kinds: [{ label: 'Продажа товаров', count: 0, amount_tiyn: 0 }], refunds: { count: 0, amount_tiyn: 0 }, items: [],
    items_total_tiyn: 0, discount_tiyn: 0, staff: [], cashier_name: 'Дана Кассир', closed_by_name: '', operator_name: 'Дана Кассир',
  };
  await mockApi(page, {
    'GET /api/cash/shifts/3/z-report': (r) => json(r, report),
    'POST /api/cash/shifts/3/z-report/nonce': (r) => json(r, { nonce: 'N'.repeat(40) }),
    'POST /api/cash/shifts/3/z-report/printed': async (r) => {
      printedWith = (r.request().postDataJSON() as { nonce: string }).nonce;
      return json(r, { printed: true });
    },
  });
  await page.addInitScript({ content: "window.print = () => window.dispatchEvent(new Event('afterprint'));" });
  // Ожидание регистрируется до перехода: nonce запрашивается сразу при показе отчёта и может прийти раньше.
  const nonce = page.waitForResponse('**/z-report/nonce');
  await page.goto('/cash-register/shifts/3/z-report');
  await expect(page.locator('#close-shift')).toBeDisabled();
  await nonce;
  await page.locator('#print-report').click();
  await expect(page.locator('#close-shift')).toBeEnabled();
  await expect(page.locator('#print-report')).toHaveText('Z-отчёт распечатан');
  expect(printedWith).toBe('N'.repeat(40));
});

test('заказы из агента: отмена с подтверждением и Idempotency-Key', async ({ page }) => {
  let cancelKey = '';
  await mockApi(page, {
    'GET /api/shop-orders': (r) =>
      json(r, {
        items: [{ id: 3, status: 'new', computer_name: 'VIP-01', customer_name: 'Айгерим', items: [{ product_id: 2, name: 'Red Bull', quantity: 2, total_tiyn: 180000 }], amount_tiyn: 180000, created_at: '2026-10-15T06:52:00Z', handler_name: null, handled_at: null }],
        total: 1, per_page: 30, status: 'new', new_count: 1, latest: 3,
      }),
    'POST /api/shop-orders/3/cancel': (r) => {
      cancelKey = r.request().headers()['idempotency-key'] ?? '';
      return json(r, { id: 3, status: 'cancelled', computer_name: 'VIP-01', customer_name: 'Айгерим', items: [], amount_tiyn: 180000, created_at: null, handler_name: 'Дана', handled_at: null });
    },
  });
  await page.goto('/shop-orders');
  await expect(page.locator('article')).toContainText('1 800,00');
  page.once('dialog', (d) => {
    expect(d.message()).toBe('Отменить заказ №3? Деньги вернутся на счёт клиента, товар — на склад.');
    void d.accept();
  });
  await page.getByRole('button', { name: 'Отменить' }).click();
  await expect(page.getByText('Заказ №3 отменён, деньги возвращены на счёт клиента.')).toBeVisible();
  expect(cancelKey).not.toBe('');
});
