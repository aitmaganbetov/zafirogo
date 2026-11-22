import { expect, test, type Page, type Route } from '@playwright/test';

/*
 * Брони из гостевого приложения в панели (тема Г, docs/upstream-2026-11.md) с моком API (page.route) — против
 * vite preview: строка «Из приложения, оплачено N · код …» и текст подтверждения отмены оплаченной брони
 * (reservations/index.blade.php), карточка «Бронь из приложения» настроек клуба (settings/club.blade.php #app-booking).
 */

const ME = {
  user: { id: 2, name: 'Айдар Сейтказы', phone: '+77010000002', is_superadmin: false },
  club: { id: 1, name: 'Zafiro Arena', timezone: 'Asia/Almaty', locale: 'ru' },
  role: 'owner',
  permissions: ['reservations.view', 'reservations.create', 'reservations.update', 'reservations.delete', 'settings.view', 'settings.update'],
  read_only: false,
  clubs: [],
  server_time: new Date().toISOString(),
};

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
  club_time: '15.11.2026 10:00', agent_pin_set_at: null, agent_staff_minutes: 15, can_manage_agent_pin: true, logo_url: null, signin_image_url: null,
  city: null, address: null, cities: [{ key: 'almaty', name: 'Алматы' }],
};

const item = (id: number, extra: Record<string, unknown>) => ({
  id, computer_id: id, computer_name: `PC-0${id}`, zone_name: 'VIP', customer_label: 'Алия · +77010000010', note: null,
  starts_at: '2026-11-15T07:00:00Z', ends_at: '2026-11-15T08:30:00Z', state: 'pending', from_app: false, paid: 0, code: null, ...extra,
});

interface Seen {
  cancelled: number[];
  clubBody?: Record<string, unknown>;
}

async function mockApi(page: Page, seen: Seen) {
  const json = (route: Route, body: unknown, status = 200) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
  await page.addInitScript({ content: "document.cookie = '__Host-zafiro_csrf=c; Path=/; Secure';" });
  await page.route('**/api/**', async (route) => {
    const req = route.request();
    const url = new URL(req.url());
    switch (`${req.method()} ${url.pathname}`) {
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
      case 'GET /api/reservations':
        return json(route, {
          date: '2026-11-15', page: 1, per_page: 50, total: 3, gap_minutes: 10, default_start: '2026-11-15T11:00',
          computers: [{ id: 1, number: 1, name: 'PC-01', zone_name: 'VIP' }],
          items: [
            item(1, { from_app: true, paid: 90050, code: '123456' }),
            item(2, { from_app: true, paid: 0, code: null }),
            item(3, {}),
          ],
        });
      case 'POST /api/reservations/1/cancel':
      case 'POST /api/reservations/3/cancel':
        seen.cancelled.push(Number(url.pathname.split('/')[3]));
        return json(route, { message: 'Бронь отменена.' });
      case 'GET /api/settings':
        return json(route, SETTINGS);
      case 'PUT /api/settings/sections/club':
        seen.clubBody = req.postDataJSON() as Record<string, unknown>;
        return json(route, { message: 'Настройки сохранены' });
      default:
        return json(route, { error: 'not_found' }, 404);
    }
  });
}

test('брони: бронь из приложения — оплата и код; отмена оплаченной предупреждает о возврате', async ({ page }) => {
  const seen: Seen = { cancelled: [] };
  await mockApi(page, seen);
  await page.goto('/reservations?date=2026-11-15');
  const rows = page.locator('tbody tr');
  await expect(rows).toHaveCount(3);
  // number_format(…, 0, '.', ' '): 900,50 ₸ → «901».
  await expect(rows.nth(0).locator('td').nth(2).locator('div.text-xs.text-gray-500')).toHaveText('Из приложения, оплачено 901 · код 123456');
  await expect(rows.nth(1).locator('td').nth(2).locator('div.text-xs.text-gray-500')).toHaveText('Из приложения');
  await expect(rows.nth(2).locator('td').nth(2).locator('div')).toHaveCount(0);

  const messages: string[] = [];
  page.on('dialog', (d) => {
    messages.push(d.message());
    void d.accept();
  });
  await rows.nth(0).getByRole('button', { name: 'Отменить' }).click();
  await rows.nth(2).getByRole('button', { name: 'Отменить' }).click();
  await expect.poll(() => seen.cancelled).toEqual([1, 3]);
  expect(messages).toEqual(['Отменить бронь? Оплата вернётся клиенту на баланс.', 'Отменить бронь?']);
});

test('настройка клуба: карточка «Бронь из приложения» — два переключателя общей формы', async ({ page }) => {
  const seen: Seen = { cancelled: [] };
  await mockApi(page, seen);
  await page.goto('/settings/club');
  const card = page.locator('#app-booking');
  await expect(card.locator('#app-booking-title')).toHaveText('Бронь из приложения');
  await expect(page.locator("[data-settings-anchors] a[href='#app-booking']")).toHaveText('Бронь из приложения');
  const enabled = card.locator('#app_booking_enabled');
  const cancel = card.locator('#app_booking_cancel');
  await expect(enabled).not.toBeChecked();
  await expect(cancel).toBeChecked();
  await expect(card.getByText('Гость может отменять бронь')).toBeVisible();
  await enabled.check();
  await cancel.uncheck();
  await page.locator('form[action="/settings/save"] > div.flex.justify-end button[type="submit"]').click();
  await expect.poll(() => seen.clubBody).toBeTruthy();
  expect(seen.clubBody?.app_booking_enabled).toBe('1');
  expect(seen.clubBody?.app_booking_cancel).toBe('0');
});
