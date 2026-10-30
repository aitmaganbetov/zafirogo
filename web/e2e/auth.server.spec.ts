import { expect, test } from '@playwright/test';

/*
 * Сквозной вход против собранного бинарника с настоящим сервером (Postgres, Valkey, NATS):
 *   E2E_REAL_SERVER=1 E2E_BASE_URL=http://127.0.0.1:18080 npx playwright test auth.server
 * Стенд — web/README.md («Тесты»). Регистрация создаёт новый клуб с уникальным номером.
 */

test.skip(!process.env.E2E_REAL_SERVER, 'нужен настоящий сервер (E2E_REAL_SERVER=1, E2E_BASE_URL)');

test('регистрация владельца → панель → перезагрузка (refresh-cookie) → WebSocket → выход → вход', async ({ page }) => {
  const digits = `70${String(Date.now()).slice(-8)}`;
  const password = 'secret-e2e';

  await page.goto('/register');
  await page.locator('#name').fill('Владелец E2E');
  await page.locator('#club_name').fill('Клуб E2E');
  await page.locator('#phone').pressSequentially(digits);
  await page.locator('#password').fill(password);
  await page.locator('#password_confirmation').fill(password);
  const ws = page.waitForEvent('websocket');
  await page.locator('.auth-submit').click();

  await expect(page).toHaveURL(/\/dashboard$/);
  await expect(page.locator('.sidebar')).toContainText('Владелец E2E');
  await expect(page.locator('.sidebar-link', { hasText: 'Склад' })).toBeVisible(); // владелец — все права

  // WebSocket по одноразовому билету: hello от сервера.
  const socket = await ws;
  expect(socket.url()).toMatch(/\/ws\?ticket=/);
  const hello = await socket.waitForEvent('framereceived');
  expect(JSON.parse(String(hello.payload)).type).toBe('hello');

  // Новая загрузка страницы: access-токена в памяти нет — сессию восстанавливает refresh-cookie.
  await page.reload();
  await expect(page.locator('.sidebar')).toContainText('Владелец E2E');

  // Выход (POST /api/auth/logout) — на /, затем гарды ведут на /login; /dashboard недоступен.
  await page.locator('.sidebar form[action="/logout"] button').click();
  await expect(page).toHaveURL(/\/login$/);
  await page.goto('/dashboard');
  await expect(page).toHaveURL(/\/login$/);

  await page.locator('#phone').pressSequentially(digits);
  await page.locator('#password').fill(password);
  await page.locator('.auth-submit').click();
  await expect(page).toHaveURL(/\/dashboard$/);
  await expect(page.locator('.admin-title')).toHaveText('Админпанель');
});

test('переводы с сервера: /api/i18n/kk отдаёт пакет с ETag', async ({ request }) => {
  const res = await request.get('/api/i18n/kk');
  expect(res.status()).toBe(200);
  const etag = res.headers()['etag'];
  expect(etag).toBeTruthy();
  const body = (await res.json()) as Record<string, string>;
  expect(body['Касса']).toBeTruthy();
  const again = await request.get('/api/i18n/kk', { headers: { 'If-None-Match': etag ?? '' } });
  expect(again.status()).toBe(304);
});
