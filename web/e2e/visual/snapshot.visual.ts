// Снимки страниц старого Laravel и нового сервера для визуального сравнения (tools/visual/README.md).
// Для каждой страницы pages.json × viewport × тема: вход одним и тем же пользователем в оба приложения,
// фиксированное время, без анимаций, полностраничный снимок с масками живых элементов. Сравнение — compare.mjs.
import { writeFileSync } from 'node:fs';
import path from 'node:path';
import { test, type Browser } from '@playwright/test';
import {
  baseFor,
  growViewportToContent,
  login,
  masksFor,
  pagesFile,
  prepareContext,
  resolved,
  routeExternal,
  selectedPages,
  settle,
  shotDir,
  type AppName,
  type NetworkLog,
  type PageSpec,
  type Theme,
} from './support';

const MASK_COLOR = '#ff00ff';

interface ShotMeta {
  app: AppName;
  url: string;
  finalUrl: string;
  status: number | null;
  login: 'form' | 'api' | null;
  consoleErrors: string[];
  network: NetworkLog;
  masks: string[];
  error?: string;
}

async function shoot(browser: Browser, spec: PageSpec, app: AppName, viewport: string, theme: Theme): Promise<ShotMeta> {
  const p = resolved(spec);
  const vp = pagesFile.viewports[viewport];
  const context = await browser.newContext({
    viewport: { width: vp.width, height: vp.height },
    isMobile: vp.isMobile ?? false,
    hasTouch: vp.hasTouch ?? false,
    deviceScaleFactor: 1,
    locale: 'ru-RU',
    timezoneId: 'Asia/Almaty',
    colorScheme: theme,
    reducedMotion: 'reduce',
    serviceWorkers: 'block',
  });
  const network: NetworkLog = { served: [], blocked: [], failed: [] };
  const consoleErrors: string[] = [];
  const page = await context.newPage();
  const userAgent = await page.evaluate(() => navigator.userAgent);
  await routeExternal(context, network, userAgent);
  page.on('console', (m) => {
    if (m.type() === 'error') consoleErrors.push(m.text());
  });
  page.on('requestfailed', (r) => network.failed.push(`${r.url()} ${r.failure()?.errorText ?? ''}`));

  const base = baseFor(app, spec);
  const url = base + p[app];
  const masks = masksFor(spec, app);
  const meta: ShotMeta = { app, url, finalUrl: '', status: null, login: null, consoleErrors, network, masks };
  const file = path.join(shotDir(spec.id, viewport, theme), `${app}.png`);
  try {
    await prepareContext(page, theme);
    if (p.role) meta.login = await login(page, app, p.role, base);
    // Печатная версия — после входа (форма входа под @media print может быть скрыта).
    if (spec.media) await page.emulateMedia({ media: spec.media });
    const res = await page.goto(url);
    meta.status = res?.status() ?? null;
    await settle(page, spec.waitFor);
    const fullPage = spec.fullPage ?? true;
    if (fullPage) await growViewportToContent(page);
    meta.finalUrl = page.url();
    await page.screenshot({
      path: file,
      fullPage,
      animations: 'disabled',
      caret: 'hide',
      scale: 'css',
      mask: masks.map((s) => page.locator(s)),
      maskColor: MASK_COLOR,
    });
  } catch (e) {
    meta.error = e instanceof Error ? e.message : String(e);
    // Снимок того, что есть, — чтобы в отчёте было видно, где остановилось.
    await page.screenshot({ path: file, fullPage: true }).catch(() => undefined);
  } finally {
    await context.close();
  }
  writeFileSync(path.join(path.dirname(file), `${app}.json`), JSON.stringify(meta, null, 2));
  return meta;
}

for (const spec of selectedPages()) {
  const p = resolved(spec);
  for (const viewport of p.viewports) {
    for (const theme of p.themes) {
      test(`${spec.id} ${viewport} ${theme}`, async ({ browser }) => {
        // Оба приложения снимаются в одном тесте: пара всегда из одного прогона.
        const old = await shoot(browser, spec, 'old', viewport, theme);
        const neu = await shoot(browser, spec, 'new', viewport, theme);
        for (const m of [old, neu]) {
          if (m.error) test.info().annotations.push({ type: `${m.app}-error`, description: m.error });
        }
        // Ошибка входа/загрузки старого — поломка инструмента или данных, не расхождение: тест красный.
        if (old.error) throw new Error(`старый ${old.url}: ${old.error}`);
      });
    }
  }
}
