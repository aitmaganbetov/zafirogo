import { expect, test, type Page, type Route } from '@playwright/test';

/*
 * Реселлеры (тема Д, docs/upstream-2026-11.md) с моком API (page.route) — против vite preview: раздел «Реселлеры»
 * кабинета суперадмина, карточка «Реселлер» настроек клуба и кабинет реселлера на своём хосте. Laravel:
 * superadmin/resellers/index.blade.php, settings/club.blade.php (#reseller), reseller/dashboard.blade.php,
 * RestrictResellerHost. Сценарии GS-560…GS-565 на сервере — server/internal/http/resellers_integration_test.go.
 */

const SUPER = {
  user: { id: 1, name: 'Супер Админ', phone: '+77010000001', is_superadmin: true },
  club: null, role: null, permissions: [], read_only: false, clubs: [], server_time: new Date().toISOString(),
};
const OWNER = {
  user: { id: 2, name: 'Айдар Сейтказы', phone: '+77010000002', is_superadmin: false },
  club: { id: 1, name: 'Zafiro Arena', timezone: 'Asia/Almaty', locale: 'ru' },
  role: 'owner', permissions: ['settings.view', 'settings.update'], read_only: false, clubs: [], server_time: new Date().toISOString(),
};
const RESELLER = {
  user: { id: 15, name: 'Арман Сериков', phone: '+77010000015', is_superadmin: false },
  club: null, role: null, permissions: [], read_only: false, clubs: [], server_time: new Date().toISOString(),
};

// Service worker PWA отдал бы страницу из своего кэша мимо подмены ответов (page.route) — как в снимках tools/visual.
test.use({ serviceWorkers: 'block' });

type Handler = (route: Route, url: URL) => Promise<void> | void;

const json = (route: Route, body: unknown, status = 200) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });

/** Общие ответы: переводы, сессия (refresh по cookie → me; null — гость), вход. */
async function mockApi(page: Page, me: unknown, extra: Record<string, Handler> = {}, loginAs: unknown = me) {
  await page.addInitScript({ content: "document.cookie = '__Host-zafiro_csrf=c; Path=/; Secure';" });
  await page.route('**/api/**', async (route) => {
    const url = new URL(route.request().url());
    const key = `${route.request().method()} ${url.pathname}`;
    if (extra[key]) return extra[key](route, url);
    switch (key) {
      case 'GET /api/i18n':
        return json(route, { languages: [{ code: 'ru', name: 'Русский' }], version: '1' });
      case 'GET /api/i18n/ru':
        return json(route, {});
      case 'POST /api/auth/refresh':
        return me ? json(route, { access_token: 't', token_type: 'Bearer', expires_in: 600, csrf_token: 'c', me }) : json(route, { error: 'unauthenticated', message: 'Unauthenticated.' }, 401);
      case 'POST /api/auth/login':
        return json(route, { access_token: 't', token_type: 'Bearer', expires_in: 600, csrf_token: 'c', me: loginAs });
      case 'GET /api/layout/badges':
        return json(route, { chat: 0, orders: 0 });
      case 'POST /api/ws-ticket':
        return json(route, { error: 'service_unavailable' }, 503);
      default:
        return json(route, { error: 'not_found' }, 404);
    }
  });
}

const arman = {
  id: 1, code: 'arman-games', name: 'Arman Games', user_name: 'Арман Сериков', user_phone: '+77010000015', commission_bp: 1250, discount_percent: 15,
  is_active: true, earned_tiyn: 265625, paid_out_tiyn: 150000, balance_tiyn: 115625, clubs: [{ id: 2, name: 'Zafiro Lite', attached: '25.08.2026' }],
};

test('кабинет: «Реселлеры» в меню, выплата — окно кода, сумма в тиынах и ключ идемпотентности', async ({ page }) => {
  const payouts: Array<{ body: Record<string, unknown>; key: string | undefined }> = [];
  let paid = false;
  await mockApi(page, SUPER, {
    'GET /api/superadmin/resellers': (r) =>
      json(r, { host: 'resseler.zafiro.kz', page: 1, per_page: 30, total: 1, items: [paid ? { ...arman, paid_out_tiyn: 265625, balance_tiyn: 0 } : arman] }),
    'POST /api/superadmin/resellers/1/payouts': (r) => {
      payouts.push({ body: r.request().postDataJSON() as Record<string, unknown>, key: r.request().headers()['idempotency-key'] });
      if (payouts.length === 1) return json(r, { error: 'validation_failed', message: 'Неверный код.', errors: { code: ['Неверный код.'] } }, 422);
      paid = true;
      return json(r, { message: 'Выплата реселлеру arman-games записана.' });
    },
  });
  await page.goto('/superadmin/resellers');
  await expect(page.locator('h2.admin-title')).toHaveText('Реселлеры');
  // Пункт меню — между «Тарифы платформы» и «Оплаты», как в старом сайдбаре.
  await expect(page.locator('.sidebar-nav .sidebar-link span')).toHaveText(
    ['Сервер', 'Клубы', 'Пользователи', 'Тарифы платформы', 'Реселлеры', 'Оплаты', 'Новости', 'Журнал', 'Языки и переводы', 'Telegram'],
  );
  await expect(page.locator('.sidebar-link.active')).toHaveText('Реселлеры');
  const card = page.locator('article', { hasText: 'arman-games' });
  await expect(card).toContainText('Заработано 2 656 ₸, выплачено 1 500 ₸, к выплате 1 156 ₸');
  await expect(card.locator('input[name=commission_percent]')).toHaveValue('12.5');
  // В поле выплаты подставлен остаток до тиына.
  await expect(card.locator('input[name=amount]')).toHaveValue('1156.25');
  await card.locator('input[name=note]').fill('Kaspi, октябрь');
  await card.getByRole('button', { name: 'Записать выплату' }).click();

  const dialog = page.getByRole('dialog');
  await expect(dialog).toContainText('Выплата реселлеру arman-games: 1 156.25 ₸');
  expect(payouts).toHaveLength(0);
  await dialog.locator('input[name=code]').fill('111111');
  await dialog.getByRole('button', { name: 'Подтвердить' }).click();
  await expect(dialog.getByRole('alert')).toHaveText('Неверный код.');
  await dialog.locator('input[name=code]').fill('222222');
  await dialog.getByRole('button', { name: 'Подтвердить' }).click();
  await expect(page.getByRole('status')).toHaveText('Выплата реселлеру arman-games записана.');
  await expect(dialog).toBeHidden();
  expect(payouts.map((p) => p.body)).toEqual([
    { amount_tiyn: 115625, note: 'Kaspi, октябрь', code: '111111' },
    { amount_tiyn: 115625, note: 'Kaspi, октябрь', code: '222222' },
  ]);
  // Повторный ввод кода — та же отправка формы: ключ идемпотентности один.
  expect(payouts[0]?.key).toBeTruthy();
  expect(payouts[1]?.key).toBe(payouts[0]?.key);
  await expect(card).toContainText('к выплате 0 ₸');
  await expect(card.locator('input[name=amount]')).toHaveValue('');
});

const SETTINGS = {
  club_name: 'Zafiro Arena',
  settings: {
    club: {
      agent_shell_theme: 'standard', session_idle_timeout: 15, pc_action_after_session_end: 'none', pc_restart_after_session_end: 0.5,
      session_auto_terminate_when_pc_unavailable: 5, booking_interval: 30, max_cashiers_on_shift: 0, max_operators_on_shift: 0,
      agent_idle_logout_minutes: 0, agent_shop_enabled: true, agent_auto_update: true, app_booking_enabled: false, app_booking_cancel: true,
    },
    finances: {
      currency: 'KZT', loyalty_enabled: false, loyalty_levels: [], topup_bonus_enabled: false, topup_bonus_levels: [], discount_scope: 'all',
      bonus_max_percent: 100, allow_mixed_payment: true, allow_whole_balance: false, auto_billing: false, auto_billing_pause_minutes: 0,
    },
    guests: { show_qr_code_on_login: false, allow_guest_self_transfer: false, allow_adding_friends: false, allow_balance_transfer_to_friends: false, guest_mode_enabled: true },
  },
  shell_themes: [{ key: 'standard', accent: '#8b5cf6', accent_2: '#5eb7ff', bg_1: '#071018' }],
  timezone: 'Asia/Almaty', timezones_kazakhstan: [{ id: 'Asia/Almaty', label: '(UTC+05:00) Asia/Almaty' }], timezones_all: [],
  club_time: '15.10.2026 12:00', agent_pin_set_at: null, agent_staff_minutes: 15, can_manage_agent_pin: true, logo_url: null, signin_image_url: null,
  city: null, address: null, cities: [{ key: 'almaty', name: 'Алматы' }],
};

// GS-562: код вводится один раз; неизвестный код — ошибка в карточке.
test('настройка клуба: карточка «Реселлер» — ошибка кода, привязка, затем реселлер и скидка', async ({ page }) => {
  const sent: unknown[] = [];
  let attached = false;
  await mockApi(page, OWNER, {
    'GET /api/settings': (r) => json(r, SETTINGS),
    'GET /api/settings/reseller': (r) => json(r, { reseller: attached ? { name: 'Arman Games', code: 'arman-games', discount_percent: 15 } : null }),
    'POST /api/settings/reseller': (r) => {
      const body = r.request().postDataJSON() as { reseller_code: string };
      sent.push(body);
      if (body.reseller_code.trim().toLowerCase() !== 'arman-games') {
        return json(r, { error: 'validation_failed', message: 'Реселлер с таким кодом не найден.', errors: { reseller_code: ['Реселлер с таким кодом не найден.'] } }, 422);
      }
      attached = true;
      return json(r, { message: 'Клуб привязан к реселлеру Arman Games.' });
    },
  });
  await page.goto('/settings/club');
  const card = page.locator('section#reseller');
  await expect(card.locator('h5')).toHaveText('Реселлер');
  // Якорь — после «Город и адрес», перед «Часовой пояс», как в старом меню настроек.
  const anchors = page.locator('[data-settings-anchors] a');
  await expect(anchors).toContainText(['Город и адрес', 'Реселлер', 'Часовой пояс']);
  await expect(card).toContainText('Код вводится один раз.');

  await card.locator('input[name=reseller_code]').fill('nobody');
  await card.getByRole('button', { name: 'Привязать' }).click();
  await expect(card.getByRole('alert')).toHaveText('Реселлер с таким кодом не найден.');

  await card.locator('input[name=reseller_code]').fill(' Arman-Games ');
  await card.getByRole('button', { name: 'Привязать' }).click();
  await expect(page.getByText('Клуб привязан к реселлеру Arman Games.')).toBeVisible();
  await expect(card).toContainText('Клуб подключён через реселлера Arman Games (код arman-games). Скидка на подписку: 15%.');
  await expect(card).toContainText('Сменить реселлера можно через поддержку Zafiro.');
  await expect(card.locator('form')).toHaveCount(0);
  expect(sent).toEqual([{ reseller_code: 'nobody' }, { reseller_code: ' Arman-Games ' }]);
});

const CABINET = {
  code: 'arman-games', commission_bp: 1250, discount_percent: 15, is_active: true, earned_tiyn: 265625, paid_out_tiyn: 150000, balance_tiyn: 115625,
  clubs: { items: [{ name: 'Zafiro Lite', city: 'Алматы', subscription: 'unpaid', attached: '25.08.2026' }], page: 1, per_page: 30, total: 1 },
  earnings: { items: [{ paid: '01.09.2026', club_name: 'Zafiro Lite', months: 1, amount_tiyn: 2125000, reseller_amount_tiyn: 265625, reseller_bp: 1250 }], page: 1, per_page: 20, total: 1 },
  payouts: [{ date: '20.09.2026', note: 'Kaspi, сентябрь', amount_tiyn: 150000 }],
};

/**
 * Хост кабинета реселлера: сервер отдаёт на нём index.html с атрибутом <html data-reseller-host>
 * (server/internal/webui). Здесь то же делает подмена ответа на запрос страницы.
 */
const onResellerHost = (page: Page) =>
  page.route(
    (url) => !url.pathname.startsWith('/api/') && !/\.[a-z0-9]+$/i.test(url.pathname),
    async (route) => {
      const response = await route.fetch();
      await route.fulfill({ response, body: (await response.text()).replace('<html', '<html data-reseller-host') });
    },
  );

// GS-561: на хосте реселлера нет регистрации; вход ведёт в кабинет с корня.
test('хост реселлера: вход без регистрации → кабинет с корня; суммы до тиына округлены до тенге', async ({ page }) => {
  const calls: string[] = [];
  await onResellerHost(page);
  await mockApi(page, null, { 'GET /api/reseller/dashboard': (r) => { calls.push('dashboard'); return json(r, CABINET); } }, RESELLER);
  await page.goto('/');
  await expect(page).toHaveURL(/\/login$/);
  await expect(page.locator('.auth-footer')).toHaveText('Кабинет реселлера Zafiro. Аккаунт реселлера создаёт Zafiro.');
  await expect(page.getByText('Зарегистрироваться')).toHaveCount(0);
  await page.locator('#phone').pressSequentially('7010000015');
  await page.locator('#password').fill('secret12');
  await page.locator('.auth-submit').click();

  await expect(page.locator('h2.admin-title')).toHaveText('Мои клубы и доход');
  expect(new URL(page.url()).pathname).toBe('/');
  await expect(page.locator('.sidebar-brand small')).toHaveText('Кабинет реселлера');
  await expect(page.locator('.sidebar-link.active')).toHaveText('Мои клубы и доход');
  await expect(page.getByText('arman-games')).toBeVisible();
  await expect(page.getByText('12.5%', { exact: true })).toBeVisible();
  await expect(page.getByText('Выплачено: 1 500 ₸')).toBeVisible();
  // 2 656,25 ₸ и 1 156,25 ₸ — на экране целые тенге.
  await expect(page.locator('div', { hasText: /^Заработано всего/ }).first()).toContainText('2 656 ₸');
  await expect(page.locator('div', { hasText: /^К выплате/ }).first()).toContainText('1 156 ₸');
  await expect(page.getByRole('row', { name: /Zafiro Lite\s*Алматы Не оплачена 25\.08\.2026/ })).toBeVisible();
  await expect(page.getByRole('row', { name: /01\.09\.2026 Zafiro Lite 21 250 ₸ · 1 месяц 2 656 ₸ \(12\.5%\)/ })).toBeVisible();
  await expect(page.locator('section[aria-labelledby=reseller-payouts] li')).toHaveText('20.09.2026 · Kaspi, сентябрь1 500 ₸');
  expect(calls.length).toBeGreaterThan(0);
});

test('хост реселлера: регистрация, панель клуба и страницы сайта уходят в кабинет', async ({ page }) => {
  await onResellerHost(page);
  const requested: string[] = [];
  await mockApi(page, RESELLER, {
    'GET /api/reseller/dashboard': (r) => json(r, CABINET),
    'GET /api/landing': (r) => { requested.push('landing'); return json(r, {}); },
    'GET /api/settings': (r) => { requested.push('settings'); return json(r, SETTINGS); },
  });
  for (const path of ['/register', '/dashboard', '/settings/club', '/pricing', '/product/features', '/superadmin']) {
    await page.goto(path);
    await expect(page.locator('h2.admin-title')).toHaveText('Мои клубы и доход');
    expect(new URL(page.url()).pathname, path).toBe('/');
  }
  // Вошедший реселлер на /login — тоже в кабинет.
  await page.goto('/login');
  await expect(page.locator('h2.admin-title')).toHaveText('Мои клубы и доход');
  expect(requested).toEqual([]);
});

test('кабинет реселлера не реселлеру: 403 «Этот аккаунт не реселлер Zafiro.»', async ({ page }) => {
  await mockApi(page, OWNER, {
    'GET /api/reseller/dashboard': (r) => json(r, { error: 'forbidden', message: 'Этот аккаунт не реселлер Zafiro.' }, 403),
  });
  await page.goto('/reseller');
  await expect(page.getByText('403')).toBeVisible();
  await expect(page.getByText('Этот аккаунт не реселлер Zafiro.')).toBeVisible();
});
