import { expect, test, type Locator, type Page, type Route } from '@playwright/test';

/*
 * Телефон (390 px): раскладка шести экранов с таблицей, у которых вид на телефоне осознанно отличается от старого
 * Laravel (docs/open-questions/audit-ui.md AU-1, подтверждено владельцем 15.11.2026). В старом страница была шире
 * экрана (828 px у «Пользователей»): телефон уменьшал её, окна «Добавить/Изменить» открывались за экраном.
 * Сравнить новый вид со старым попиксельно нельзя (снимки разной ширины), поэтому эталон здесь — сама раскладка:
 *
 *   — документ не шире окна (и браузер телефона не раздвинул окно раскладки);
 *   — таблица прокручивается внутри своего блока, последняя колонка с действиями достижима;
 *   — окна «Добавить/Изменить» целиком в пределах экрана, кнопка сохранения достижима и нажимается.
 *
 * Почему не снимки (toHaveScreenshot): эталонные PNG зависят от растеризации шрифтов — локальный Chromium и
 * Chromium раннера CI (ubuntu-24.04, другая сборка FreeType/HarfBuzz) дают разные пиксели сглаживания, тест стал бы
 * нестабильным или потребовал бы порога, в котором тонет сам дефект (лишние 70–440 px ширины страницы — это
 * изменение размера снимка, а не доли пикселей). Числа раскладки от сглаживания не зависят. С моком API
 * (page.route), против vite preview — как остальные e2e; запускается задачей playwright в CI.
 */

const WIDTH = 390;
const HEIGHT = 844;
test.use({ viewport: { width: WIDTH, height: HEIGHT }, isMobile: true, hasTouch: true, deviceScaleFactor: 1, locale: 'ru-RU', timezoneId: 'Asia/Almaty' });

const ME = {
  user: { id: 2, name: 'Айдар Сейтказы', phone: '+77010000002', is_superadmin: false },
  club: { id: 1, name: 'Zafiro Arena', timezone: 'Asia/Almaty', locale: 'ru' },
  role: 'owner',
  permissions: [
    'customers.view', 'customers.create', 'customers.update', 'customer_money.update', 'workers.view', 'workers.create', 'workers.update',
    'workers.delete', 'settings.view', 'services.view', 'services.create', 'services.update', 'services.delete', 'tariffs.view',
    'tariffs.create', 'tariffs.update', 'tariffs.delete', 'reservations.view', 'reservations.create', 'reservations.update', 'reservations.delete',
  ],
  read_only: false,
  clubs: [],
  server_time: '2026-10-15T07:00:00Z',
};

// Длинные значения — как в жизни: имя на всю ширину колонки, суммы с разрядами.
const CUSTOMERS = {
  items: [
    { user_id: 6, name: 'Нурлан Ахметов', phone: '+77011000006', email: 'nurlan@example.kz', role: 'client', discount: '0.00', discount_effective: '7.00',
      discount_cumulative: true, account_locked: false, total_spent_tiyn: 184000000, balance_tiyn: 35000000, bonus_balance_tiyn: 2500000 },
    { user_id: 7, name: 'Александра Константинопольская-Задунайская', phone: '+77011000007', email: null, role: 'client', discount: '5.00',
      discount_effective: '5.00', discount_cumulative: false, account_locked: true, total_spent_tiyn: 999999900, balance_tiyn: 0, bonus_balance_tiyn: 0 },
  ],
  next_after_id: null, prev_before_id: null, total: 2,
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

const SERVICES = {
  items: [
    { id: 1, name: 'Печать документа А4 (цветная, двусторонняя)', price_tiyn: 5000, unit: 'лист', is_active: true },
    { id: 2, name: 'Аренда гарнитуры', price_tiyn: 150000, unit: null, is_active: false },
  ],
  total: 2, per_page: 50, can_create: true, can_update: true, can_delete: true,
};

const WEEK = [{ start_day: 'monday', start_time: '00:00', end_day: 'sunday', end_time: '23:59' }];
const tariff = (id: number, name: string, extra: Record<string, unknown> = {}) => ({
  id, name, description: null, duration_minutes: 180, price_tiyn: 250000, currency: 'KZT', is_active: true, payment_method: 'main_or_bonus',
  discount_enabled: true, usage_limit_enabled: false, usage_limit_minutes: null, quantity_limit_enabled: false, quantity_limit: null, sold: 0,
  zone_ids: [1], zone_names: ['VIP-зал'], sale_periods: WEEK, validity_periods: WEEK, on_sale: true, in_validity_period: true, ...extra,
});
const TARIFFS = {
  zones: [{ id: 1, name: 'VIP-зал', price_per_hour_tiyn: 100000 }, { id: 2, name: 'Основной зал', price_per_hour_tiyn: 60000 }],
  zone_filter: null, currency: 'KZT', can_delete: true,
  tariffs: [tariff(1, 'Ночной пакет'), tariff(2, 'Три часа в VIP-зале по будням', { zone_ids: [], zone_names: [], quantity_limit_enabled: true, quantity_limit: 100, sold: 12 })],
};

const RESERVATIONS = {
  date: '2026-10-15', page: 1, per_page: 50, total: 2, gap_minutes: 30, default_start: '2026-10-15T13:00',
  computers: [{ id: 9, number: 9, name: 'PC-09', zone_name: 'Основной зал' }],
  items: [
    { id: 1, computer_id: 9, computer_name: 'PC-09', zone_name: 'Основной зал', customer_label: 'Алия Жумабекова · +77010000010', note: 'День рождения, нужна гарнитура',
      starts_at: '2026-10-15T06:30:00Z', ends_at: '2026-10-15T08:00:00Z', state: 'active' },
    { id: 2, computer_id: 9, computer_name: 'PC-09', zone_name: 'Основной зал', customer_label: 'Тимур · +77010000011', note: null,
      starts_at: '2026-10-15T10:00:00Z', ends_at: '2026-10-15T12:00:00Z', state: 'pending' },
  ],
};

const json = (route: Route, body: unknown, status = 200) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });

async function mockApi(page: Page) {
  await page.addInitScript({ content: "document.cookie = '__Host-zafiro_csrf=c; Path=/; Secure';" });
  await page.route('**/api/**', (route) => {
    const url = new URL(route.request().url());
    switch (`${route.request().method()} ${url.pathname}`) {
      case 'GET /api/i18n':
        return json(route, { languages: [{ code: 'ru', name: 'Русский' }], version: '1' });
      case 'GET /api/i18n/ru':
        return json(route, {});
      case 'POST /api/auth/refresh':
        return json(route, { access_token: 't', token_type: 'Bearer', expires_in: 600, csrf_token: 'c', me: ME });
      case 'GET /api/layout/badges':
        return json(route, { chat: 0, orders: 0 });
      case 'GET /api/notifications':
        return json(route, { unread_count: 0, items: [] });
      case 'POST /api/ws-ticket':
        return json(route, { error: 'service_unavailable' }, 503);
      case 'GET /api/customers':
        return json(route, CUSTOMERS);
      case 'GET /api/workers':
        return json(route, WORKERS);
      case 'GET /api/services':
        return json(route, SERVICES);
      case 'GET /api/tariffs/page':
        return json(route, TARIFFS);
      case 'GET /api/reservations':
        return json(route, RESERVATIONS);
      default:
        return json(route, { error: 'not_found' }, 404);
    }
  });
}

/*
 * Код, выполняемый в странице. tsconfig e2e — без lib "dom": выражение страницы передаётся строкой (как в
 * e2e/visual/support.ts), а функции над элементом получают его нетипизированным и берут окно из него самого.
 */
const PAGE_WIDTHS = `({
  window: window.innerWidth,
  document: document.documentElement.scrollWidth,
  body: document.body.scrollWidth,
  scale: window.visualViewport ? window.visualViewport.scale : 1,
})`;

interface ScrollBlock {
  left: number;
  right: number;
  client: number;
  scroll: number;
}

/** Блок с горизонтальной прокруткой, в котором лежит таблица (null — такого нет). */
function scrollBlock(table: Locator): Promise<ScrollBlock | null> {
  return table.evaluate((el) => {
    const win = el.ownerDocument.defaultView;
    let p = el.parentElement;
    while (p && !['auto', 'scroll'].includes(win.getComputedStyle(p).overflowX)) p = p.parentElement;
    if (!p) return null;
    const r = p.getBoundingClientRect();
    return { left: r.left, right: r.right, client: p.clientWidth, scroll: p.scrollWidth };
  });
}

/**
 * Видимые поля и кнопки окна, вышедшие за край экрана. Полоса с собственной горизонтальной прокруткой внутри окна
 * (строки периодов тарифа — .tariff-period-list, как в Blade) не считается: её содержимое обрезано и достижимо
 * прокруткой полосы.
 */
function controlsOutside(dialog: Locator): Promise<string[]> {
  return dialog.evaluate((el) => {
    const win = el.ownerDocument.defaultView;
    const scrolled = (c: { parentElement: unknown }) => {
      for (let p = c.parentElement; p && p !== el; p = (p as { parentElement: unknown }).parentElement) {
        if (['auto', 'scroll'].includes(win.getComputedStyle(p).overflowX)) return true;
      }
      return false;
    };
    const out: string[] = [];
    for (const c of el.querySelectorAll('input, select, textarea, button')) {
      if (c.offsetParent === null || scrolled(c)) continue;
      const r = c.getBoundingClientRect();
      if (r.width > 0 && (r.left < 0 || r.right > win.innerWidth)) out.push(c.getAttribute('name') || c.id || c.textContent || c.tagName);
    }
    return out;
  });
}

/** Документ не шире окна телефона, и окно раскладки не раздвинуто содержимым. */
async function expectPageFits(page: Page) {
  await page.evaluate('document.fonts.ready');
  const m = await page.evaluate<{ window: number; document: number; body: number; scale: number }>(PAGE_WIDTHS);
  expect(m, 'ширина страницы на телефоне').toEqual({ window: WIDTH, document: WIDTH, body: WIDTH, scale: 1 });
}

/** Таблица шире своего блока прокручивается в нём (а не растягивает страницу); действие последней колонки достижимо. */
async function expectTableScrollsInside(page: Page, table: Locator, action: Locator) {
  const block = await scrollBlock(table);
  if (!block) throw new Error('у таблицы нет блока с горизонтальной прокруткой');
  expect(block.left).toBeGreaterThanOrEqual(0);
  expect(block.right).toBeLessThanOrEqual(WIDTH);
  expect(block.scroll, 'таблица шире блока — прокрутка внутри блока').toBeGreaterThan(block.client);
  await action.scrollIntoViewIfNeeded();
  await expect(action).toBeInViewport({ ratio: 1 });
  await action.click({ trial: true }); // не перекрыта и нажимается
  await expectPageFits(page); // прокрутка блока страницу не расширила
}

/** Окно целиком в пределах экрана; поля не выходят за край; кнопка сохранения достижима (прокруткой окна) и нажимается. */
async function expectDialogFits(page: Page, dialog: Locator, submit: Locator) {
  await expect(dialog).toBeVisible();
  const box = await dialog.boundingBox();
  if (!box) throw new Error('окна нет на экране');
  expect(box.x).toBeGreaterThanOrEqual(0);
  expect(box.x + box.width).toBeLessThanOrEqual(WIDTH);
  expect(box.y).toBeGreaterThanOrEqual(0);
  expect(box.y + box.height).toBeLessThanOrEqual(HEIGHT);
  expect(await controlsOutside(dialog), 'поля и кнопки окна за краем экрана').toEqual([]);
  await submit.scrollIntoViewIfNeeded();
  await expect(submit).toBeInViewport({ ratio: 1 });
  const b = await submit.boundingBox();
  expect(b && b.x >= 0 && b.x + b.width <= WIDTH).toBe(true);
  await submit.click({ trial: true });
  await expectPageFits(page);
}

test.beforeEach(async ({ page }) => {
  await mockApi(page);
});

test('телефон: «Пользователи» — страница по ширине экрана, окна «Новый клиент» и «Изменить» на экране', async ({ page }) => {
  await page.goto('/users');
  const rows = page.locator('[data-customer-row]');
  await expect(rows).toHaveCount(2);
  await expectPageFits(page);
  await expectTableScrollsInside(page, page.locator('table'), rows.first().locator('[data-edit-user]'));

  await page.locator('#open-user-dialog').scrollIntoViewIfNeeded();
  await page.locator('#open-user-dialog').click();
  await expectDialogFits(page, page.locator('#user-create-dialog'), page.locator('#user-create-dialog button[type=submit]'));
  await page.locator('#user-create-dialog [data-close-user-dialog]').first().click();

  await rows.first().locator('[data-edit-user]').click();
  const edit = page.locator('#user-edit-dialog');
  await expectDialogFits(page, edit, edit.getByRole('button', { name: 'Сохранить' }));
});

test('телефон: «Услуги» — страница по ширине экрана, «Сохранить» в строке достижима', async ({ page }) => {
  await page.goto('/services');
  await expect(page.locator('tbody tr')).toHaveCount(2);
  await expectPageFits(page);
  await expectTableScrollsInside(page, page.locator('table'), page.locator('tbody tr').first().getByRole('button', { name: 'Сохранить' }));
  // Форма добавления над таблицей — в пределах экрана.
  const add = page.getByRole('button', { name: 'Добавить' }).first();
  await add.scrollIntoViewIfNeeded();
  await expect(add).toBeInViewport({ ratio: 1 });
});

test('телефон: «Работники» — страница по ширине экрана, формы и действия строки достижимы', async ({ page }) => {
  await page.goto('/workers');
  const rows = page.locator('[data-worker-row]');
  await expect(rows).toHaveCount(2);
  await expectPageFits(page);
  await expectTableScrollsInside(page, page.locator('table'), rows.nth(1).getByText('Права'));
  for (const name of ['Добавить', 'Создать ссылку']) {
    const button = page.getByRole('button', { name });
    await button.scrollIntoViewIfNeeded();
    await expect(button).toBeInViewport({ ratio: 1 });
    await button.click({ trial: true });
  }
  await expectPageFits(page);
});

test('телефон: «Тарифы» — страница по ширине экрана, окно «Добавить тариф» на экране', async ({ page }) => {
  await page.goto('/tariffs');
  await expect(page.getByText('Ночной пакет')).toBeVisible();
  await expectPageFits(page);
  // Первая таблица — поминутные цены зон, вторая — тарифы (с колонкой действий).
  const tariffs = page.locator('table').last();
  await expectTableScrollsInside(page, tariffs, tariffs.locator('tbody tr').first().locator('a, button').last());
  await page.locator('#open-tariff-dialog').scrollIntoViewIfNeeded();
  await page.locator('#open-tariff-dialog').click();
  const dialog = page.locator('#tariff-dialog');
  await expectDialogFits(page, dialog, dialog.getByRole('button', { name: 'Создать тариф' }));
});

test('телефон: «Тарифы» — окно изменения тарифа на экране, «Сохранить тариф» достижима', async ({ page }) => {
  await page.goto('/tariffs/2/edit');
  const dialog = page.locator('#tariff-dialog[open]');
  await expect(dialog.locator('#tariff-name')).toHaveValue('Три часа в VIP-зале по будням');
  await expectPageFits(page);
  await expectDialogFits(page, dialog, dialog.getByRole('button', { name: 'Сохранить тариф' }));
});

test('телефон: «Брони» — страница по ширине экрана, форма брони и действия строки достижимы', async ({ page }) => {
  await page.goto('/reservations');
  await expect(page.getByText('11:30 – 13:00')).toBeVisible();
  await expectPageFits(page);
  await expectTableScrollsInside(page, page.locator('table'), page.locator('table tbody tr').first().locator('button').last());
  const submit = page.locator('button.brand-button[type=submit]').first();
  await submit.scrollIntoViewIfNeeded();
  await expect(submit).toBeInViewport({ ratio: 1 });
  await expectPageFits(page);
});
