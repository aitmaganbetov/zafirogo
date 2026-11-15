import { expect, test, type Page, type Route } from '@playwright/test';

/*
 * Группа 7: сайт (главная, страницы продукта, тарифы — тема Е), QR-вход на ПК с телефона — с моком API (page.route), против vite preview.
 */

const CODE = 'AbCdEfGhIjKlMnOpQrStUvWxYz0123456789abcd';
const ME = {
  user: { id: 9, name: 'Гость Иванов', phone: '+77011112233', is_superadmin: false },
  club: { id: 7, name: 'Клуб Звезда', timezone: 'Asia/Almaty', locale: 'ru' },
  role: 'client',
  permissions: [],
  read_only: false,
  clubs: [],
  server_time: new Date().toISOString(),
};

interface MockState {
  open: boolean;
  approve: 'ok' | 'error' | 'stale';
  loginBody?: { pc_login?: boolean };
}

async function mockApi(page: Page, state: MockState) {
  const fulfill = (route: Route, body: unknown, status = 200) =>
    route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
  let authed = false;
  await page.route('**/api/**', async (route) => {
    const url = new URL(route.request().url());
    const method = route.request().method();
    switch (url.pathname) {
      case '/api/i18n':
        return fulfill(route, { languages: [{ code: 'ru', name: 'Русский' }], version: '1' });
      case '/api/i18n/ru':
        return fulfill(route, {});
      case '/api/auth/refresh':
        return fulfill(route, { error: 'unauthenticated', message: 'Unauthenticated.' }, 401);
      case '/api/auth/login':
        state.loginBody = route.request().postDataJSON() as MockState['loginBody'];
        authed = true;
        return fulfill(route, { access_token: 't', token_type: 'Bearer', expires_in: 600, csrf_token: 'c', me: ME });
      case '/api/landing':
        return fulfill(route, { trial_days: 30, download_url: 'https://example.test/agent.exe' });
      case '/api/pricing':
        return fulfill(route, { plans: [{ id: 1, name: 'Клуб до 30 ПК', price_monthly: 2500000, trial_days: 0, features: ['До 30 ПК'] }] });
      case `/api/pc-login/${CODE}`:
        if (!authed) return fulfill(route, { error: 'unauthenticated', message: 'Unauthenticated.' }, 401);
        if (method === 'GET') {
          return fulfill(route, state.open
            ? { open: true, club_name: 'Клуб Звезда', computer_name: 'PC-07', user_name: 'Гость Иванов' }
            : { open: false, user_name: 'Гость Иванов' });
        }
        if (state.approve === 'error') {
          return fulfill(route, { error: 'validation_failed', message: 'Подписка клуба не активна.', errors: { code: ['Подписка клуба не активна.'] } }, 422);
        }
        if (state.approve === 'stale') {
          state.open = false;
          return fulfill(route, { approved: false });
        }
        return fulfill(route, { approved: true });
      default:
        return fulfill(route, { error: 'not_found' }, 404);
    }
  });
}

test('лендинг: пробный период с сервера, ссылки входа и wiki', async ({ page }) => {
  await mockApi(page, { open: true, approve: 'ok' });
  await page.goto('/');
  await expect(page).toHaveTitle('Zafiro — система управления компьютерным клубом');
  await expect(page.locator('h1')).toHaveText('Весь клуб - в одном окне');
  await expect(page.locator('main a[href="/register"]').first()).toHaveText('30 дней бесплатно');
  await expect(page.locator('a[href="https://example.test/agent.exe"]')).toBeVisible();
  await expect(page.locator('header a[href="/login"]').last()).toBeVisible();
  // Новый вид (тема Е): белый фон, снимок карты клуба, тёмный блок мобильных приложений.
  await expect(page.locator('body')).toHaveClass(/bg-white/);
  await expect(page.locator('main img[src="/images/club-map.webp"]')).toBeVisible();
  await expect(page.locator('#apps h2')).toHaveText('Мобильные приложения для гостей и сотрудников');
});

test('сайт: меню шапки ведут на страницы продукта и тарифы без перезагрузки (GS-572, GS-573)', async ({ page }) => {
  await mockApi(page, { open: true, approve: 'ok' });
  await page.goto('/');
  await page.evaluate('window.spa = true');
  const menu = (title: string) => page.locator('header details[data-landing-menu]:visible', { hasText: title });

  await menu('О продукте').locator('summary').click();
  await expect(menu('О продукте')).toHaveAttribute('open', '');
  // Одно меню за раз.
  await menu('Продукты').locator('summary').click();
  await expect(menu('О продукте')).not.toHaveAttribute('open', '');
  await menu('Продукты').locator('a[href="/product/agent"]').click();
  await expect(page).toHaveURL(/\/product\/agent$/);
  await expect(page).toHaveTitle('Агент Zafiro на ПК — вход, тарифы, магазин и защита ПК');
  await expect(page.locator('h1')).toHaveText('Агент Zafiro на ПК');
  await expect(page.locator('main a[href="https://example.test/agent.exe"]')).toBeVisible();

  await menu('Продукты').locator('summary').click();
  await page.keyboard.press('Escape');
  await expect(menu('Продукты')).not.toHaveAttribute('open', '');

  await menu('О продукте').locator('summary').click();
  await menu('О продукте').locator('a[href="/product/features"]').click();
  await expect(page.locator('h1')).toHaveText('Возможности Zafiro');
  // Новая страница — меню снова закрыто (как после загрузки страницы в старом), её пункт отмечен.
  await expect(menu('О продукте')).not.toHaveAttribute('open', '');
  await expect(page.locator('header a[aria-current="page"]').first()).toHaveAttribute('href', '/product/features');

  for (const [href, h1] of [['/product/guest-app', 'Приложение для гостей'], ['/product/staff-app', 'Приложение для сотрудников']] as const) {
    await page.locator(`footer a[href="${href}"]`).click();
    await expect(page.locator('h1')).toHaveText(h1);
  }

  await page.locator('header a[href="/pricing"]:visible').click();
  await expect(page).toHaveTitle('Тарифы Zafiro — подписка для компьютерного клуба');
  const card = page.locator('main article');
  await expect(card).toHaveCount(1);
  await expect(card.locator('h2')).toHaveText('Клуб до 30 ПК');
  await expect(card).toContainText('25 000 ₸');
  await expect(card).toContainText('за ПК в месяц');
  await expect(card.locator('li')).toHaveText(['До 30 ПК']);
  await expect(card.locator('a[href="/register"]')).toHaveText('Зарегистрировать клуб');

  // С другой страницы «Вопросы» ведут к блоку главной.
  await page.locator('footer a[href="/#faq"]').click();
  await expect(page).toHaveURL(/\/#faq$/);
  await expect(page.locator('#faq h2')).toBeInViewport();
  // Все переходы — роутером, без перезагрузки документа.
  expect(await page.evaluate<boolean>('window.spa === true')).toBe(true);
});

test('сайт на телефоне: одно меню «Меню» с обоими разделами, документ не шире окна', async ({ page }) => {
  await page.setViewportSize({ width: 360, height: 740 });
  await mockApi(page, { open: true, approve: 'ok' });
  for (const path of ['/', '/pricing', '/product/features', '/product/agent', '/product/guest-app', '/product/staff-app']) {
    await page.goto(path);
    await expect(page.locator('h1')).toBeVisible();
    expect(await page.evaluate<number>('document.documentElement.scrollWidth - window.innerWidth'), path).toBeLessThanOrEqual(0);
  }
  const menu = page.locator('header details[data-landing-menu]:visible');
  await expect(menu).toHaveCount(1);
  await menu.locator('summary').click();
  await expect(menu.locator('a')).toHaveCount(9); // 6 пунктов меню, «Тарифы», Wiki, «Войти»
  await expect(menu.locator('a[href="/login"]')).toBeVisible();
  await menu.locator('a[href="/product/guest-app"]').click();
  await expect(page.locator('h1')).toHaveText('Приложение для гостей');
});

test('QR-вход: гость → вход (pc_login) → подтверждение → «Готово»', async ({ page }) => {
  const state: MockState = { open: true, approve: 'error' };
  await mockApi(page, state);
  await page.goto(`/pc-login/${CODE}`);
  await expect(page).toHaveURL(/\/login$/);
  await page.locator('#phone').pressSequentially('7011112233');
  await page.locator('#password').fill('secret');
  await page.locator('.auth-submit').click();

  await expect(page).toHaveURL(new RegExp(`/pc-login/${CODE}$`));
  expect(state.loginBody?.pc_login).toBe(true);
  await expect(page.locator('.auth-card-head p')).toHaveText('Клуб Клуб Звезда · ПК PC-07');
  await expect(page.locator('.auth-footer').first()).toHaveText('Вы войдёте на этом ПК как Гость Иванов.');

  await page.locator('.auth-submit').click();
  await expect(page.locator('.auth-errors')).toHaveText('Подписка клуба не активна.');

  state.approve = 'ok';
  await page.locator('.auth-submit').click();
  await expect(page).toHaveURL(/\/pc-login\/done$/);
  await expect(page.locator('.auth-card-head h2')).toHaveText('Готово');
});

test('QR-вход: код устарел между показом и подтверждением', async ({ page }) => {
  const state: MockState = { open: true, approve: 'stale' };
  await mockApi(page, state);
  await page.goto(`/pc-login/${CODE}`);
  await page.locator('#phone').pressSequentially('7011112233');
  await page.locator('#password').fill('secret');
  await page.locator('.auth-submit').click();
  await expect(page.locator('.auth-submit')).toHaveText('Войти на ПК');
  await page.locator('.auth-submit').click();
  await expect(page.locator('.auth-card-head h2')).toHaveText('QR-код устарел');
});

test('QR-вход: неверный код — 404', async ({ page }) => {
  await mockApi(page, { open: true, approve: 'ok' });
  await page.goto('/pc-login/short');
  await expect(page.getByText('Not Found')).toBeVisible();
});
