import { expect, test } from '@playwright/test';

test('главная страница открывается', async ({ page }) => {
  const cspViolations: string[] = [];
  page.on('console', (msg) => {
    if (msg.text().includes('Content Security Policy')) cspViolations.push(msg.text());
  });

  await page.goto('/');
  await expect(page).toHaveTitle(/Zafiro/);
  await expect(page.getByRole('heading', { level: 1, name: 'Zafiro' })).toBeVisible();
  expect(cspViolations).toEqual([]);
});

test('inline-скрипт темы применяет ночную тему до загрузки стилей', async ({ page }) => {
  await page.goto('/');
  await page.evaluate(() => localStorage.setItem('zafiro.theme', 'dark'));
  await page.reload();
  await expect(page.locator('html')).toHaveClass(/\bdark\b/);
});
