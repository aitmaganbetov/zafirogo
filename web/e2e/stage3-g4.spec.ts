import { expect, test, type Page, type Route } from '@playwright/test';

/*
 * Тарифы, брони, новости, задачи, чат, журнал действий, колокольчик — с моком API (page.route), против vite preview.
 */

const ME = {
  user: { id: 2, name: 'Айбек Владелец', phone: '+77010000002', is_superadmin: false },
  club: { id: 1, name: 'Zafiro Arena', timezone: 'Asia/Almaty', locale: 'ru' },
  role: 'owner',
  permissions: [
    'tariffs.view', 'tariffs.create', 'tariffs.update', 'reservations.view', 'reservations.create', 'reservations.update', 'reservations.delete',
    'todos.view', 'todos.create', 'todos.update', 'todos.delete', 'chat.view', 'chat.create', 'news.view', 'news.create', 'news.update', 'news.delete',
    'activity_logs.view',
  ],
  read_only: false,
  clubs: [],
  server_time: new Date().toISOString(),
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
      case '/api/notifications':
        return json(route, {
          unread_count: 1,
          items: [{ id: 'chat-5', icon: 'fa-comments', title: 'Новые сообщения в чате: 1', body: 'Привет', url: '/chat', at: '2026-10-15T07:00:00Z', at_human: '5 минут назад', unread: true }],
        });
      case '/api/ws-ticket':
        return json(route, { error: 'unavailable' }, 503);
      default:
        return json(route, { error: 'not_found' }, 404);
    }
  });
}

test('задачи: добавить, отметить выполненной; колокольчик показывает уведомление и отмечает просмотренным', async ({ page }) => {
  type Task = { id: number; title: string; due_on: string; done: boolean; done_by: string | null; done_at: string | null };
  let tasks: Task[] = [{ id: 1, title: 'Протереть столы', due_on: '2026-10-15', done: false, done_by: null, done_at: null }];
  let seen = false;
  await mockApi(page, {
    'GET /api/todos': (r) => json(r, { date: '2026-10-15', today: '2026-10-15', is_today: true, tasks }),
    'POST /api/todos': async (r) => {
      const b = r.request().postDataJSON() as { title: string; due_on: string };
      tasks = [...tasks, { id: 2, title: b.title, due_on: b.due_on, done: false, done_by: null, done_at: null }];
      return json(r, { message: 'Задача добавлена.', date: '2026-10-15' }, 201);
    },
    'POST /api/todos/1/toggle': (r) => {
      tasks = tasks.map((x) => (x.id === 1 ? { ...x, done: true, done_by: 'Айбек', done_at: '2026-10-15T07:30:00Z' } : x));
      return json(r, tasks[0]);
    },
    'POST /api/notifications/seen': (r) => {
      seen = true;
      return json(r, { unread_count: 0 });
    },
  });
  await page.goto('/todos');
  await expect(page.locator('[data-task="1"] [data-title]')).toHaveText('Протереть столы');
  await page.locator('input[name="title"]').fill('Проверить кассу');
  await page.getByRole('button', { name: 'Добавить задачу' }).click();
  await expect(page.getByText('Задача добавлена.')).toBeVisible();
  await expect(page.locator('[data-task="2"]')).toBeVisible();
  await page.locator('[data-task="1"] [data-toggle]').click();
  await expect(page.locator('[data-task="1"] [data-meta]')).toHaveText('Выполнил(а) Айбек в 12:30');

  await expect(page.locator('[data-notifications-dot]')).toBeVisible();
  await page.locator('#notifications-button').click();
  await expect(page.locator('#notifications-panel')).toContainText('Новые сообщения в чате: 1');
  await expect.poll(() => seen).toBe(true);
});

test('чат: ответ по Enter уходит POST и дописывается в переписку', async ({ page }) => {
  let replied: unknown = null;
  await mockApi(page, {
    'GET /api/chat': (r) =>
      json(r, {
        conversations: [{ customer_id: 9, customer_name: 'Ержан', unread: 0, last_body: 'Привет', last_from_staff: false }],
        customer: { id: 9, name: 'Ержан', phone: '+77011112233' },
        messages: [{ id: 1, from_staff: false, body: 'Привет', author: null, computer: 'PC-01', created_at: '2026-10-15T06:55:00Z' }],
        can_answer: true,
        unread: 0,
      }),
    'POST /api/chat/9/messages': (r) => {
      replied = r.request().postDataJSON();
      return json(r, { message: { id: 2, from_staff: true, body: 'Здравствуйте', author: 'Айбек', computer: null, created_at: '2026-10-15T07:00:00Z' } }, 201);
    },
  });
  await page.goto('/chat');
  await expect(page.locator('[data-chat-thread] li')).toHaveCount(1);
  await expect(page.locator('[data-chat-thread]')).toContainText('15.10 11:55 · PC-01');
  await page.locator('textarea[name="body"]').fill('Здравствуйте');
  await page.locator('textarea[name="body"]').press('Enter');
  await expect(page.locator('[data-chat-thread] li')).toHaveCount(2);
  expect(replied).toEqual({ body: 'Здравствуйте' });
});

test('тарифы: новый тариф отправляет цену в тиынах; ошибка сервера — над формой', async ({ page }) => {
  let body: Record<string, unknown> | null = null;
  await mockApi(page, {
    'GET /api/tariffs/page': (r) =>
      json(r, {
        zones: [{ id: 1, name: 'VIP-зал', price_per_hour_tiyn: 100000 }],
        zone_filter: null,
        currency: 'KZT',
        can_delete: true,
        tariffs: [],
      }),
    'POST /api/tariffs': (r) => {
      body = r.request().postDataJSON() as Record<string, unknown>;
      return json(r, { message: 'The name has already been taken.', error: 'validation_failed', errors: { name: ['The name has already been taken.'] } }, 422);
    },
  });
  await page.goto('/tariffs');
  await expect(page.getByText('16,67 KZT')).toBeVisible(); // 1000,00 / 60
  await page.locator('#open-tariff-dialog').click();
  await page.locator('#tariff-name').fill('1 час');
  await page.locator('#tariff-price').fill('600.50');
  await page.locator('[data-period-list]').first().locator('[data-add-period]').first().click();
  await expect(page.locator('[data-period-list]').first().locator('[data-period]')).toHaveCount(2);
  await page.getByRole('button', { name: 'Создать тариф' }).click();
  await expect(page.locator('#tariff-dialog')).toContainText('The name has already been taken.');
  expect(body).toMatchObject({ name: '1 час', price_tiyn: 60050, duration_value: 1, duration_unit: 'hours' });
  expect((body as unknown as { sale_periods: unknown[] }).sale_periods).toHaveLength(2);
});

test('брони и журнал: список дня и фильтр журнала', async ({ page }) => {
  await mockApi(page, {
    'GET /api/reservations': (r) =>
      json(r, {
        date: '2026-10-15', page: 1, per_page: 50, total: 1, gap_minutes: 30, default_start: '2026-10-15T13:00',
        computers: [{ id: 9, number: 9, name: 'PC-09', zone_name: 'Основной зал' }],
        items: [{ id: 1, computer_id: 9, computer_name: 'PC-09', zone_name: 'Основной зал', customer_label: 'Алия · +77010000010', note: null,
          starts_at: '2026-10-15T06:30:00Z', ends_at: '2026-10-15T08:00:00Z', state: 'active' }],
      }),
    'GET /api/activity-logs': (r, url) =>
      json(r, {
        total: url.searchParams.get('event') ? 1 : 1234,
        next_cursor: null,
        users: [],
        events: [{ event: 'cash.income', label: 'Пополнение кассы' }],
        items: [{ id: 1, event: 'cash.income', description: 'Пополнение', user_name: null, details: [{ key: 'amount', value: '1500' }], ip_address: '10.0.0.1', created_at: '2026-10-15T07:00:00Z' }],
      }),
  });
  await page.goto('/reservations');
  await expect(page.getByText('11:30 – 13:00')).toBeVisible();
  await expect(page.getByText('Идёт')).toBeVisible();
  await page.goto('/activity-logs');
  await expect(page.getByText('Записей: 1 234')).toBeVisible();
  await expect(page.getByText('amount: 1500')).toBeVisible();
  await page.locator('#log-event').selectOption('cash.income');
  await page.getByRole('button', { name: 'Фильтр' }).click();
  await expect(page).toHaveURL(/event=cash\.income/);
  await expect(page.getByText('Записей: 1', { exact: true })).toBeVisible();
});
