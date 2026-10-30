import { expect, test } from '@playwright/test';

test('главная без входа ведёт на страницу входа, без нарушений CSP', async ({ page }) => {
  const cspViolations: string[] = [];
  page.on('console', (msg) => {
    if (msg.text().includes('Content Security Policy')) cspViolations.push(msg.text());
  });

  await page.goto('/');
  await expect(page).toHaveURL(/\/login$/);
  await expect(page).toHaveTitle(/· Zafiro$/);
  await expect(page.locator('.auth-shell')).toBeVisible();
  expect(cspViolations).toEqual([]);
});

test('inline-скрипт темы применяет ночную тему до загрузки стилей', async ({ page }) => {
  await page.goto('/login');
  await page.evaluate(() => localStorage.setItem('zafiro.theme', 'dark'));
  await page.reload();
  await expect(page.locator('html')).toHaveClass(/\bdark\b/);
});

test('шрифты и иконки — со своего origin (Q-064)', async ({ page, baseURL }) => {
  const origin = new URL(baseURL ?? 'http://localhost').origin;
  const external: string[] = [];
  page.on('request', (r) => {
    const u = new URL(r.url());
    if (u.protocol.startsWith('http') && u.origin !== origin) external.push(r.url());
  });
  await page.goto('/login');
  await expect(page.locator('.auth-input-wrap i.fa-phone')).toBeVisible();
  const family = await page.evaluate<string>("getComputedStyle(document.querySelector('.fa-phone'), '::before').fontFamily");
  expect(family).toContain('Font Awesome 6 Free');
  expect(external).toEqual([]);
});
