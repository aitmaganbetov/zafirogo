import { expect, test, type Page, type Route } from '@playwright/test';

/*
 * Карта клуба, список ПК, подключение ПК и дашборд с моком API (page.route) — против vite preview.
 * Сессия: refresh по CSRF-cookie (как открытая заново вкладка).
 */

const ME = {
  user: { id: 2, name: 'Арман Владелец', phone: '+77010000002', is_superadmin: false },
  club: { id: 1, name: 'Zafiro Arena', timezone: 'Asia/Almaty', locale: 'ru' },
  role: 'owner',
  permissions: ['club_map.view', 'club_map.create', 'club_map.update', 'club_map.delete', 'pc_control.update', 'pc_control.delete', 'cash.create', 'cash.view', 'reservations.create'],
  read_only: false,
  clubs: [],
  server_time: new Date().toISOString(),
};

const zone = (id: number, name: string, x: number, extra: Record<string, unknown> = {}) => ({
  id, name, description: 'Зал', capacity: 8, price_per_hour_tiyn: 100000, icon: 'desktop', theme: 'green', status: 'available',
  is_featured: false, sort_order: id * 10, position_x: x, position_y: 20, width: 330, height: 180, rotation: 0, tariff_ids: [], ...extra,
});
const state = (id: number, extra: Record<string, unknown> = {}) => ({
  id, zone_id: 1, number: id, name: `PC-0${id}`, status: 'online', light: 'pc-free', connection: 'online', is_reserved: false,
  has_session: false, session_guest: false, session_user: '', session_tariff: '', agent_state: 'idle', agent_version: '2.4.1',
  agent_outdated: false, staff_kind: '', ...extra,
});

const MAP = {
  zones: [zone(1, 'Основной зал', 20), zone(2, 'Консоли', 400, { icon: 'gamepad', capacity: null })],
  computers: [
    { id: 1, zone_id: 1, number: 1, name: 'PC-01', display_name: null, mac_address: 'AA:BB:CC:DD:EE:01', position_x: 5, position_y: 5, details: 'MAC AA:BB:CC:DD:EE:01' },
    { id: 2, zone_id: 1, number: 2, name: 'PC-02', display_name: null, mac_address: null, position_x: 52, position_y: 5, details: '' },
  ],
  states: [state(1), state(2, { status: 'offline', light: '', connection: 'offline' })],
  tariffs: [{ id: 5, name: 'Час', duration_minutes: 60, price_tiyn: 60000, currency: 'KZT', is_active: true, guest_sellable: true }],
  guest_mode: true,
  booking_start: '2026-10-15T13:00',
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
        return json(route, { chat: 0, orders: 0 });
      case '/api/ws-ticket':
        return json(route, { error: 'unavailable' }, 503);
      default:
        return json(route, { error: 'not_found' }, 404);
    }
  });
}

test('карта: выбор ПК, перезагрузка с текстом сервера, информация о ПК', async ({ page }) => {
  let control: unknown = null;
  await mockApi(page, {
    'GET /api/club-map': (r) => json(r, MAP),
    'POST /api/club-computers/control': (r) => {
      control = r.request().postDataJSON();
      return json(r, { updated: 1, status: 'restarting', message: 'Перезагрузка: ПК получит команду в течение минуты (1 ПК).' });
    },
  });
  await page.goto('/club-map');
  const pc = page.locator('.cm-computer[data-id="1"]');
  await expect(pc).toHaveClass('cm-computer online pc-free');
  await pc.click();
  await expect(page.locator('#computer-controls')).toHaveClass(/show/);
  await expect(page.locator('#computer-info-name')).toHaveText('PC-01 (#1)');
  await page.locator('.cm-control-button', { hasText: 'Перезагрузить' }).click();
  await expect(page.locator('#layout-status')).toHaveText('Перезагрузка: ПК получит команду в течение минуты (1 ПК).');
  await expect(pc).toHaveClass(/restarting/);
  expect(control).toEqual({ computer_ids: [1], action: 'restart' });
});

test('карта: загрузка и проблемы ПК, раздел «Состояние» и история проблем (Этап 7)', async ({ page }) => {
  const map = {
    ...MAP,
    states: [state(1, { load: { cpu_bp: 4250, ram_bp: 6100, idle_seconds: 0 }, problems: ['cpu_hot'], rtt_ms: 18 }), MAP.states[1]],
  };
  await mockApi(page, {
    'GET /api/club-map': (r) => json(r, map),
    'GET /api/club-computers/1/health': (r) =>
      json(r, {
        computer_id: 1, connection: 'online', agent_version: '2.4.1', rtt_ms: 18, problems: ['cpu_hot'], load: { cpu_bp: 4250, ram_bp: 6100, idle_seconds: 0 },
        health: { reported_at: '2026-10-15T08:00:00Z', cpu_temp_deci: 925, disks: [{ mount: 'C:', system: true, total_mb: 512000, free_mb: 256000, smart: 'ok' }],
          peripherals: [{ kind: 'mouse', connected: true, name: 'G102' }], link_mbps: 1000, uptime_seconds: 3600, unexpected_shutdowns_7d: 0, bugchecks_7d: 0 },
      }),
    'GET /api/club-computers/1/health/events': (r) =>
      json(r, { items: [{ id: 1, kind: 'cpu_hot', raised_at: '2026-10-15T07:00:00Z', cleared_at: null, details: { temp_deci: 925 } }], next_cursor: null }),
  });
  await page.goto('/club-map');
  const pc = page.locator('.cm-computer[data-id="1"]');
  await expect(pc.locator('.cm-pc-load i')).toHaveCount(2);
  await expect(pc.locator('.cm-pc-alert')).toHaveAttribute('title', 'Перегрев CPU');
  await expect(page.locator('.cm-computer[data-id="2"] .cm-pc-load')).toHaveCount(0);
  await pc.click();
  await expect(page.locator('#computer-health-info')).toContainText('93 °C');
  await expect(page.locator('#computer-health-info')).toContainText('18 мс');
  await expect(page.locator('#computer-health-history dt')).toHaveText('Перегрев CPU');
});

test('карта: зона — ошибка формы сверху, затем «Зона добавлена.»', async ({ page }) => {
  let calls = 0;
  await mockApi(page, {
    'GET /api/club-map': (r) => json(r, MAP),
    'POST /api/club-map': (r) => {
      calls++;
      if (calls === 1) return json(r, { error: 'validation_failed', message: 'Поле «название» обязательно.', errors: { name: ['Поле «название» обязательно.'] } }, 422);
      return json(r, { id: 3, message: 'Зона добавлена.' }, 201);
    },
  });
  await page.goto('/club-map');
  await page.locator('.cm-editor-switch').click();
  await expect(page.locator('#editor-toggle')).toBeChecked();
  await expect(page.locator('#editor-hint')).toHaveText('Перетаскивайте карточки мышью. Размер меняется за зелёный угол снизу справа.');
  await page.locator('#add-zone').click();
  await expect(page.locator('#zone-modal')).toHaveClass(/open/);
  await page.locator('#zone-name').fill('Бар');
  await page.locator('#zone-form .cm-save').click();
  await expect(page.locator('.cm-alert.error')).toHaveText('Поле «название» обязательно.');
  await page.locator('#zone-form .cm-save').click();
  await expect(page.locator('.cm-alert.ok')).toHaveText('Зона добавлена.');
  await expect(page.locator('#zone-modal')).not.toHaveClass(/open/);
});

test('карта: «Подключить тариф» — предпросмотр в тиынах и Idempotency-Key', async ({ page }) => {
  let body: unknown = null;
  await mockApi(page, {
    'GET /api/club-map': (r) => json(r, MAP),
    'POST /api/club-computers/guest-session': (r) => {
      body = r.request().postDataJSON();
      expect(r.request().headers()['idempotency-key']).toBeTruthy();
      return json(r, { amount_tiyn: 60000, minutes: 60, computers: [{ computer_id: 1, name: 'PC-01', session_id: 's', session_ends_at: '2026-10-15T08:00:00Z', extends: false, price_tiyn: 60000 }], description: 'Гость', cash_transaction_id: 9 }, 201);
    },
  });
  await page.goto('/club-map');
  await page.locator('.cm-computer[data-id="1"]').click();
  await page.locator('#guest-session-open').click();
  await expect(page.locator('#guest-session-total')).toHaveText('К оплате: 600 за 1 ПК');
  await page.locator('input[name=choice][value=minutes]').check();
  await page.locator('.cm-guest-minutes').fill('7');
  await expect(page.locator('#guest-session-total')).toHaveText('К оплате: 116,67 за 1 ПК');
  await page.locator('#guest-session-submit').click();
  await expect(page.locator('#layout-status')).toHaveText('Тариф подключён: 1 ПК, к оплате 600,00.');
  expect(body).toEqual({ computer_ids: [1], payment_method: 'cash', minutes: 7 });
});

test('список ПК и подключение ПК: код выдаётся один раз', async ({ page }) => {
  await mockApi(page, {
    'GET /api/club-computers': (r) =>
      json(r, {
        items: [{ id: 1, number: 1, name: 'PC-01', zone_id: 1, zone_name: 'Основной зал', connection: 'online', status: 'online', is_reserved: false, position_x: 5, position_y: 5, agent_outdated: true, agent_version: '2.3.0', ram_total_mb: 16384, cpu_cores: 8 }],
        page: 1, per_page: 50, total: 1, agent_update: { version: '2.4.1', download_url: null, outdated: 1, total: 1 },
      }),
    'GET /api/agent-enrollment': (r) => json(r, { can_register: true, can_manage_image_keys: true, zones: [{ id: 1, name: 'Основной зал' }], codes: [], image_keys: [], pending: [] }),
    'POST /api/agent-enrollment/codes': (r) => json(r, { id: 4, code: 'ABCD-EFGH', expires_at: '2026-10-15T07:15:00Z' }, 201),
  });
  await page.goto('/club-computers');
  await expect(page.locator('#agent-update-title')).toHaveText('Новая версия агента 2.4.1');
  await expect(page.locator('tbody tr').first()).toContainText('16,0 GB');
  await page.locator('a[href="/club-computers/enrollment"]').click();
  await page.locator('button', { hasText: 'Выдать код' }).click();
  await expect(page.locator('#enrollment-codes input[readonly]')).toHaveValue('ABCD-EFGH');
});

test('дашборд: сводка и график', async ({ page }) => {
  await mockApi(page, {
    'GET /api/dashboard': (r) =>
      json(r, {
        today_income_tiyn: 1250050, today_expenses_tiyn: 50000, today_transaction_count: 3, today_expense_count: 1, currency: 'KZT',
        days: Array.from({ length: 7 }, (_, i) => ({ label: `0${i + 9}.10`, income_tiyn: i * 100000, expenses_tiyn: 0 })),
        product_count: 8, low_stock_count: 2, zone_count: 5, computer_count: 12, online_computer_count: 7, reserved_computer_count: 1, user_count: 1234,
        recent_transactions: [{ id: 1, type: 'expense', amount_tiyn: 50000, description: 'Закупка', created_at: '2026-10-15T05:00:00Z' }],
      }),
  });
  await page.goto('/dashboard');
  await expect(page.locator('[data-testid=dashboard]')).toContainText('12 501 KZT');
  await expect(page.locator('[data-testid=dashboard]')).toContainText('7 / 12');
  await expect(page.locator('[data-testid=dashboard] tbody')).toContainText('−500 KZT');
  await expect(page.locator('#chart')).toBeVisible();
});
