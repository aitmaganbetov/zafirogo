import { expect, test, type Page, type Route } from '@playwright/test';

/*
 * «Настройки → Профиль клуба» и карточка «Город и адрес» (тема В, docs/upstream-2026-11.md) с моком API
 * (page.route) — против vite preview. Laravel: settings/profile.blade.php, settings/club.blade.php (#location),
 * settings/layout.blade.php (пункт меню «Профиль клуба»).
 */

const ME = {
  user: { id: 2, name: 'Айдар Сейтказы', phone: '+77010000002', is_superadmin: false },
  club: { id: 1, name: 'Zafiro Arena', timezone: 'Asia/Almaty', locale: 'ru' },
  role: 'owner',
  permissions: ['settings.view', 'settings.update', 'club_map.view'],
  read_only: false,
  clubs: [],
  server_time: new Date().toISOString(),
};

const FIELDS = [
  ['cpu', 'Процессор'], ['gpu', 'Видеокарта'], ['ram', 'Оперативная память'], ['monitor', 'Монитор'],
  ['keyboard', 'Клавиатура'], ['mouse', 'Мышь'], ['headset', 'Гарнитура'], ['chair', 'Кресло'],
].map(([key, label]) => ({ key, label }));

// 1×1 PNG.
const PIXEL = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==', 'base64');

function profile(photos: Array<{ id: number; url: string }>) {
  return {
    profile: { description: 'Лучший клуб', phone: '+7 700 000 00 00', hours: 'Круглосуточно', instagram: 'asia.game' },
    photos, photo_limit: 10,
    zones: [
      { id: 5, name: 'VIP', description: null, computers_count: 2, price_per_hour: 80000, devices: { monitor: 'ASUS 240 Гц' }, detected: { cpu: 'Intel Core i5-11400F', ram: '16 ГБ' } },
      { id: 6, name: 'Main', description: null, computers_count: 10, price_per_hour: 50000, devices: {}, detected: {} },
    ],
    device_fields: FIELDS,
  };
}

const SETTINGS = {
  club_name: 'Zafiro Arena',
  settings: {
    club: {
      session_idle_timeout: 15, pc_action_after_session_end: 'none', pc_restart_after_session_end: 0.5,
      session_auto_terminate_when_pc_unavailable: 5, booking_interval: 30, max_cashiers_on_shift: 0, max_operators_on_shift: 0,
      agent_idle_logout_minutes: 0, agent_shop_enabled: true, agent_auto_update: true,
    },
    finances: {
      currency: 'KZT', loyalty_enabled: false, loyalty_levels: [], topup_bonus_enabled: false, topup_bonus_levels: [], discount_scope: 'all',
      bonus_max_percent: 100, allow_mixed_payment: true, allow_whole_balance: false, auto_billing: false, auto_billing_pause_minutes: 0,
    },
    guests: { show_qr_code_on_login: false, allow_guest_self_transfer: false, allow_adding_friends: false, allow_balance_transfer_to_friends: false, guest_mode_enabled: true },
  },
  timezone: 'Asia/Almaty', timezones_kazakhstan: [{ id: 'Asia/Almaty', label: '(UTC+05:00) Asia/Almaty' }], timezones_all: [],
  club_time: '15.10.2026 12:00', agent_pin_set_at: null, agent_staff_minutes: 15, can_manage_agent_pin: true, logo_url: null, signin_image_url: null,
  city: null, address: null, cities: [{ key: 'aktobe', name: 'Актобе' }, { key: 'almaty', name: 'Алматы' }],
};

interface Seen {
  zoneBody?: Record<string, unknown>;
  locationBody?: Record<string, unknown>;
  uploadType?: string | null;
  uploaded: boolean;
  city: string | null;
}

async function mockApi(page: Page, seen: Seen) {
  const fulfill = (route: Route, body: unknown, status = 200) =>
    route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
  await page.route('**/club-photos/**', (route) => route.fulfill({ status: 200, contentType: 'image/png', body: PIXEL }));
  await page.route('**/api/**', async (route) => {
    const req = route.request();
    const url = new URL(req.url());
    switch (`${req.method()} ${url.pathname}`) {
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
      case 'GET /api/settings':
        return fulfill(route, { ...SETTINGS, city: seen.city });
      case 'PUT /api/settings/location':
        seen.locationBody = req.postDataJSON() as Record<string, unknown>;
        seen.city = String(seen.locationBody.city ?? '') || null;
        return fulfill(route, { message: 'Город и адрес сохранены.' });
      case 'GET /api/settings/profile':
        return fulfill(route, profile(seen.uploaded
          ? [{ id: 11, url: '/club-photos/1/aaaa.png' }, { id: 12, url: '/club-photos/1/bbbb.png' }]
          : [{ id: 11, url: '/club-photos/1/aaaa.png' }]));
      case 'POST /api/settings/profile/photos':
        seen.uploadType = req.headers()['content-type'] ?? null;
        seen.uploaded = true;
        return fulfill(route, { message: 'Фото добавлены.' });
      case 'PUT /api/settings/profile/zones/5':
        seen.zoneBody = req.postDataJSON() as Record<string, unknown>;
        return fulfill(route, { message: 'Зона VIP сохранена.' });
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

/** Раздел «Настройки» из бокового меню (переход внутри приложения: вход живёт в памяти страницы). */
async function openSettings(page: Page) {
  await page.locator('.sidebar-link', { hasText: 'Настройки' }).click();
  await expect(page).toHaveURL(/\/settings\/club$/);
}

test('профиль клуба: пункт меню, три карточки, загрузка фото формой, оборудование зоны', async ({ page }) => {
  const seen: Seen = { uploaded: false, city: null };
  await mockApi(page, seen);
  await login(page);
  await openSettings(page);
  await page.locator('a[href="/settings/profile"]').click();
  await expect(page).toHaveURL(/\/settings\/profile$/);

  // Меню настроек: «Профиль клуба» — после «Настройка клуба», под ним — якоря страницы.
  const item = page.locator('a[href="/settings/profile"]');
  await expect(item).toHaveClass(/brand-tab/);
  await expect(item.locator('i')).toHaveClass('fas fa-mobile-screen w-4');
  await expect(page.locator('[data-settings-anchors] a')).toHaveText(['О клубе', 'Фотографии', 'Зоны и оборудование']);
  await expect(page.locator('section#club-about h5')).toHaveText('О клубе');
  await expect(page.locator('section#club-about input[name=instagram]')).toHaveValue('@asia.game');

  // Фото: одно — обложка; форма отправляет multipart с полем photos[].
  const photos = page.locator('section#club-photos');
  await expect(photos.locator('li')).toHaveCount(1);
  await expect(photos.locator('li').first()).toContainText('Обложка');
  await photos.locator('input[type=file]').setInputFiles({ name: 'hall.png', mimeType: 'image/png', buffer: PIXEL });
  await photos.getByRole('button', { name: 'Добавить фото' }).click();
  await expect(page.getByText('Фото добавлены.')).toBeVisible();
  expect(seen.uploadType).toContain('multipart/form-data');
  await expect(photos.locator('li')).toHaveCount(2);
  await expect(photos.locator('li').nth(1).getByRole('button', { name: 'Сделать обложкой' })).toBeVisible();

  // Зона: подсказки из данных агентов — placeholder; сохраняются все поля оборудования.
  const vip = page.locator('form#zone-5');
  await expect(vip).toContainText('2 ПК · 800 ₸/час');
  await expect(vip.locator('input[name="devices[cpu]"]')).toHaveAttribute('placeholder', 'Intel Core i5-11400F');
  await expect(vip.locator('input[name="devices[ram]"]')).toHaveAttribute('placeholder', '16 ГБ');
  await vip.locator('input[name="devices[mouse]"]').fill('Logitech G102');
  await vip.locator('input[name=description]').fill('Тихая комната');
  await vip.getByRole('button', { name: 'Сохранить зону' }).click();
  await expect(page.getByText('Зона VIP сохранена.')).toBeVisible();
  expect(seen.zoneBody).toEqual({
    description: 'Тихая комната',
    devices: { cpu: '', gpu: '', ram: '', monitor: 'ASUS 240 Гц', keyboard: '', mouse: 'Logitech G102', headset: '', chair: '' },
  });
});

test('настройка клуба: карточка «Город и адрес» сохраняет город из списка', async ({ page }) => {
  const seen: Seen = { uploaded: false, city: null };
  await mockApi(page, seen);
  await login(page);
  await openSettings(page);
  const card = page.locator('section#location');
  await expect(card.locator('h5')).toHaveText('Город и адрес');
  await expect(page.locator('[data-settings-anchors] a[href="#location"]')).toHaveText('Город и адрес');
  await expect(card.locator('select[name=city] option')).toHaveText(['Не указан', 'Актобе', 'Алматы']);
  await card.locator('select[name=city]').selectOption('aktobe');
  await card.locator('input[name=address]').fill('пр. Абилкайыр хана, 1');
  await card.getByRole('button', { name: 'Сохранить' }).click();
  await expect(page.getByText('Город и адрес сохранены.')).toBeVisible();
  expect(seen.locationBody).toEqual({ city: 'aktobe', address: 'пр. Абилкайыр хана, 1' });
  await expect(card.locator('select[name=city]')).toHaveValue('aktobe');
});
