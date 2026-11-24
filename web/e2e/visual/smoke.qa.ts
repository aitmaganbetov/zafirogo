// Смоук всех страниц стенда tools/visual (новый сервер, демо-данные): каждая страница tools/visual/pages.json
// открывается под своей ролью; ошибка, если на ней исключение JS, console.error, ответ API ≥ 500 или 4xx,
// не перечисленный в EXPECTED_4XX. Снимков и сравнения со старым нет — только «страница работает».
// Запуск: tools/visual/new.sh start, затем
//   npx playwright test -c e2e/visual/playwright.qa.config.ts
import { expect, test } from '@playwright/test';
import { baseFor, login, pagesFile, resolved, type PageSpec } from './support';

// Ответы 4xx, которые страница получает по замыслу (страница показывает их, а не падает).
const EXPECTED_4XX: Record<string, RegExp[]> = {
  // Клуб с истёкшей подпиской: разделы клуба отвечают 402, страница подписки — нет (RequireActiveSubscription).
  'billing-expired': [/ 402 /],
  // Истёкший QR входа на ПК — 404/410 по коду.
  'pc-login-expired': [/ 40[4-9] /, / 410 /],
};

// Страницы со статусом pending в pages.json ещё не перенесены — их проверяем, но отличия от старого не ждём.
const pages: PageSpec[] = pagesFile.pages.filter((p) => !p.media);

for (const spec of pages) {
  test(`${spec.id} ${spec.new}`, async ({ page }) => {
    const p = resolved(spec);
    const base = baseFor('new', spec);
    const problems: string[] = [];
    page.on('pageerror', (e) => problems.push(`pageerror: ${e.message}`));
    page.on('console', (m) => {
      if (m.type() === 'error') problems.push(`console.error: ${m.text()}`);
    });
    page.on('response', (r) => {
      const u = new URL(r.url());
      if (!u.pathname.startsWith('/api/') && r.status() < 500) return;
      if (r.status() < 400) return;
      const line = `${r.request().method()} ${r.status()} ${u.pathname}${u.search}`;
      if ((EXPECTED_4XX[spec.id] ?? []).some((re) => re.test(` ${line.split(' ')[1]} `))) return;
      problems.push(`http: ${line}`);
    });
    page.on('requestfailed', (r) => {
      const err = r.failure()?.errorText ?? '';
      // Уход со страницы обрывает незавершённые запросы — это не ошибка.
      if (err.includes('ERR_ABORTED')) return;
      if (new URL(r.url()).hostname.endsWith('localhost')) problems.push(`requestfailed: ${r.url()} ${err}`);
    });

    if (p.role) await login(page, 'new', p.role, base);
    const res = await page.goto(base + p.new);
    expect(res?.status(), 'HTTP статус документа').toBeLessThan(400);
    await page.waitForLoadState('networkidle', { timeout: 15_000 }).catch(() => undefined);
    // Граница ошибок React (CatchBoundary) показывает текст ошибки вместо страницы.
    const boundary = await page.locator('text=/Something went wrong|Произошла ошибка|Server Error/i').count();
    if (boundary > 0) problems.push('страница показывает ошибку');
    expect(problems, problems.join('\n')).toEqual([]);
  });
}
