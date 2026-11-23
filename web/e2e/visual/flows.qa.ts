// Денежные сценарии кассира против настоящего сервера стенда tools/visual (не мок API): продажа → чек, пополнение
// баланса, X-отчёт, печать Z-отчёта → закрытие смены → открытие новой. Сценарии меняют данные стенда и идут
// по порядку; повторный прогон — после `tools/visual/new.sh seed` и перезапуска сервера (`new.sh start --no-build`).
// Запуск: npx playwright test -c e2e/visual/playwright.qa.config.ts flows
import { expect, test, type Page } from '@playwright/test';
import { baseURL, login } from './support';

test.describe.configure({ mode: 'serial' });

const base = baseURL.new;

/** Ответ API, который ждёт действие страницы; ошибка — если статус не 2xx. */
async function apiOk(page: Page, method: string, path: RegExp, action: () => Promise<unknown>) {
  const res = page.waitForResponse((r) => r.request().method() === method && path.test(new URL(r.url()).pathname));
  await action();
  const r = await res;
  expect(r.status(), `${method} ${r.url()}: ${await r.text().catch(() => '')}`).toBeLessThan(300);
  return r;
}

/** Остаток товара на карточке кассы («В наличии: N шт.»). */
async function stockOf(p: Page, name: string): Promise<number> {
  const text = await p.locator('.pos-product', { hasText: name }).locator('.pos-stock').innerText();
  const n = Number(text.match(/(\d+)\s*шт/)?.[1]);
  expect(Number.isInteger(n), `остаток «${name}»: ${text}`).toBe(true);
  return n;
}

let page: Page;
const problems: string[] = [];

test.beforeAll(async ({ browser }) => {
  page = await browser.newPage();
  page.on('pageerror', (e) => problems.push(`pageerror: ${e.message}`));
  page.on('response', (r) => {
    if (r.status() >= 500) problems.push(`http ${r.status()} ${r.url()}`);
  });
  await login(page, 'new', 'cashier', base);
});

test.afterAll(async () => {
  await page.close();
});

test.afterEach(() => {
  expect(problems, problems.join('\n')).toEqual([]);
});

test('продажа товара: чек, остаток склада −1', async () => {
  await page.goto(`${base}/cash-register`);
  const card = page.locator('.pos-product', { hasText: 'Coca-Cola 0,5 л' });
  const before = await stockOf(page, 'Coca-Cola 0,5 л');
  await card.locator('.add-product').click();
  await expect(page.locator('.pos-cart-item')).toHaveCount(1);
  const sale = await apiOk(page, 'POST', /^\/api\/cash\/sales$/, () => page.locator('#checkout').click());
  const body = (await sale.json()) as { transaction: { id: number; amount_tiyn?: number } };
  await expect(page).toHaveURL(new RegExp(`/cash-register/receipts/${body.transaction.id}$`));
  await expect(page.locator('body')).toContainText('Coca-Cola 0,5 л');

  // Повторное открытие кассы — остаток товара уменьшился на 1.
  await page.goto(`${base}/cash-register`);
  expect(await stockOf(page, 'Coca-Cola 0,5 л')).toBe(before - 1);
});

test('пополнение основного баланса клиента наличными', async () => {
  await page.goto(`${base}/cash-register`);
  await page.locator('[data-pos-view="balance"]').click();
  await page.locator('#top-up-user-search').fill('Нурлан');
  const option = page.locator('[data-user-option]', { hasText: 'Нурлан Ахметов' });
  await expect(option).toBeVisible();
  const balanceBefore = (await option.innerText()).match(/Основной ([\d\s ]+) ₸/)?.[1]?.replace(/\s/g, '');
  await option.click();
  await page.locator('#balance-view input[type="number"]').fill('1500');
  await apiOk(page, 'POST', /^\/api\/cash\/balance-top-ups$/, () => page.locator('#balance-view button[type="submit"]').click());

  // Баланс в списке клиентов вырос на 1 500 ₸.
  await page.goto(`${base}/cash-register`);
  await page.locator('[data-pos-view="balance"]').click();
  await page.locator('#top-up-user-search').fill('Нурлан');
  const again = page.locator('[data-user-option]', { hasText: 'Нурлан Ахметов' });
  await expect(again).toBeVisible();
  const balanceAfter = (await again.innerText()).match(/Основной ([\d\s ]+) ₸/)?.[1]?.replace(/\s/g, '');
  expect(Number(balanceAfter)).toBe(Number(balanceBefore) + 1500);
});

test('X-отчёт открытой смены открывается', async () => {
  await apiOk(page, 'GET', /^\/api\/cash\/reports\/x/, () => page.goto(`${base}/cash-register/reports/x`));
  await expect(page.locator('.report-page')).toBeVisible();
});

test('Z-отчёт: «Закрыть смену» только после печати; после печати смена закрывается', async () => {
  // window.print() в headless не открывает диалог — событие afterprint посылаем сами (как после печати в браузере).
  await page.addInitScript({ content: "window.print = () => window.dispatchEvent(new Event('afterprint'));" });
  await page.goto(`${base}/cash-register`);
  const zLink = page.locator('a[href*="/z-report"]').first();
  const href = await zLink.getAttribute('href');
  expect(href, 'ссылка на Z-отчёт на странице кассы').toBeTruthy();
  await apiOk(page, 'POST', /\/z-report\/nonce$/, () => page.goto(base + href!));

  const close = page.locator('#close-shift');
  await expect(close).toBeDisabled();
  await apiOk(page, 'POST', /\/z-report\/printed$/, () => page.locator('#print-report').click());
  await expect(close).toBeEnabled();
  await apiOk(page, 'POST', /^\/api\/cash\/shifts\/\d+\/close$/, () => close.click());
  await expect(page.locator('.report-error')).toHaveCount(0);

  // Касса без открытой смены предлагает открыть новую.
  await page.goto(`${base}/cash-register`);
  await expect(page.locator('.pos-open')).toBeVisible();
});

test('открытие новой смены', async () => {
  await page.goto(`${base}/cash-register`);
  await page.locator('.pos-open input[type="number"]').fill('5000');
  await apiOk(page, 'POST', /^\/api\/cash\/shifts$/, () => page.locator('.pos-open button').click());
  await expect(page.locator('.pos-page')).toBeVisible();
  await expect(page.locator('.pos-open')).toHaveCount(0);
});
