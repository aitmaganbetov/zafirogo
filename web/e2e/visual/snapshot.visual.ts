// Снимки страниц старого Laravel и нового сервера для визуального сравнения (tools/visual/README.md).
// Для каждой страницы pages.json × viewport × тема: вход одним и тем же пользователем в оба приложения,
// фиксированное время, без анимаций, полностраничный снимок с масками живых элементов. Сравнение — compare.mjs.
import { writeFileSync } from 'node:fs';
import path from 'node:path';
import { test as base, type Browser } from '@playwright/test';
import {
  baseFor,
  growViewportToContent,
  hiddenFor,
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
/**
 * Наибольшая высота полностраничного снимка (CSS px). Выше ~14 500 px при ширине 1440 Chromium отвечает «Unable to
 * capture screenshot» и падает целиком (wiki — 26 000 px): снимается верх страницы этой высоты, в meta — truncatedFrom.
 */
const MAX_SHOT_HEIGHT = 12_000;
// Размер документа (строка: tsconfig.node без lib "dom").
const DOCUMENT_SIZE = `(() => {
  const d = document.documentElement;
  return { width: Math.max(d.scrollWidth, document.body ? document.body.scrollWidth : 0),
           height: Math.max(d.scrollHeight, document.body ? document.body.scrollHeight : 0) };
})()`;

/**
 * Браузер воркера, который переживает падение Chromium на одной странице: если процесс браузера упал
 * (страница уронила рендерер или GPU), следующий снимок запускает новый с теми же launchOptions — соседние
 * страницы в этом воркере не получают «Target page, context or browser has been closed».
 */
const test = base.extend<object, { liveBrowser: () => Promise<Browser> }>({
  liveBrowser: [
    async ({ browser, playwright, browserName, launchOptions }, use) => {
      let current = browser;
      const launched: Browser[] = [];
      await use(async () => {
        if (!current.isConnected()) {
          current = await playwright[browserName].launch(launchOptions);
          launched.push(current);
        }
        return current;
      });
      for (const b of launched) await b.close().catch(() => undefined);
    },
    { scope: 'worker' },
  ],
});

interface ShotMeta {
  app: AppName;
  url: string;
  finalUrl: string;
  status: number | null;
  login: 'form' | 'api' | null;
  consoleErrors: string[];
  network: NetworkLog;
  masks: string[];
  hidden: string[];
  /** Первая попытка снимка упала вместе с вкладкой или браузером (её ошибка); это — повтор. */
  retriedAfter?: string;
  /** Высота страницы, если снимок обрезан до MAX_SHOT_HEIGHT. */
  truncatedFrom?: number;
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
  const hidden = hiddenFor(spec, app);
  const meta: ShotMeta = { app, url, finalUrl: '', status: null, login: null, consoleErrors, network, masks, hidden };
  const file = path.join(shotDir(spec.id, viewport, theme), `${app}.png`);
  try {
    await prepareContext(page, theme);
    if (p.role) meta.login = await login(page, app, p.role, base);
    // Печатная версия — после входа (форма входа под @media print может быть скрыта).
    if (spec.media) await page.emulateMedia({ media: spec.media });
    const res = await page.goto(url);
    meta.status = res?.status() ?? null;
    await settle(page, spec.waitFor, hidden);
    const fullPage = spec.fullPage ?? true;
    if (fullPage) await growViewportToContent(page);
    meta.finalUrl = page.url();
    const doc = fullPage ? await page.evaluate<{ width: number; height: number }>(DOCUMENT_SIZE) : null;
    if (doc && doc.height > MAX_SHOT_HEIGHT) meta.truncatedFrom = doc.height;
    await page.screenshot({
      path: file,
      fullPage,
      clip: doc && doc.height > MAX_SHOT_HEIGHT ? { x: 0, y: 0, width: doc.width, height: MAX_SHOT_HEIGHT } : undefined,
      animations: 'disabled',
      caret: 'hide',
      scale: 'css',
      mask: masks.map((s) => page.locator(s)),
      maskColor: MASK_COLOR,
    });
  } catch (e) {
    meta.error = e instanceof Error ? e.message : String(e);
    // Снимок того, что есть (только окно: полностраничный мог и уронить браузер), — чтобы в отчёте было видно,
    // где остановилось.
    await page.screenshot({ path: file }).catch(() => undefined);
  } finally {
    await context.close().catch(() => undefined);
  }
  writeFileSync(path.join(path.dirname(file), `${app}.json`), JSON.stringify(meta, null, 2));
  return meta;
}

// Падение вкладки или браузера (не ошибка страницы): снимок повторяется один раз в живом браузере.
const CRASH = /Target crashed|Target page, context or browser has been closed|Unable to capture screenshot/;

async function shootWithRetry(
  liveBrowser: () => Promise<Browser>,
  spec: PageSpec,
  app: AppName,
  viewport: string,
  theme: Theme,
): Promise<ShotMeta> {
  const first = await shoot(await liveBrowser(), spec, app, viewport, theme);
  if (!first.error || !CRASH.test(first.error)) return first;
  const second = await shoot(await liveBrowser(), spec, app, viewport, theme);
  second.retriedAfter = first.error.split('\n')[0];
  writeFileSync(path.join(shotDir(spec.id, viewport, theme), `${app}.json`), JSON.stringify(second, null, 2));
  return second;
}

function defineTests(specs: PageSpec[]): void {
  for (const spec of specs) {
    const p = resolved(spec);
    for (const viewport of p.viewports) {
      for (const theme of p.themes) {
        test(`${spec.id} ${viewport} ${theme}`, async ({ liveBrowser }) => {
          // Оба приложения снимаются в одном тесте: пара всегда из одного прогона. Браузер — живой на момент
          // каждого снимка: падение Chromium на старой странице не роняет снимок новой и следующие тесты.
          const old = await shootWithRetry(liveBrowser, spec, 'old', viewport, theme);
          const neu = await shootWithRetry(liveBrowser, spec, 'new', viewport, theme);
          for (const m of [old, neu]) {
            if (m.error) test.info().annotations.push({ type: `${m.app}-error`, description: m.error });
          }
          // Ошибка входа/загрузки старого — поломка инструмента или данных, не расхождение: тест красный.
          if (old.error) throw new Error(`старый ${old.url}: ${old.error}`);
        });
      }
    }
  }
}

// Страницы суперадмина показывают журнал платформы, куда пишется каждый вход суперадмина (в том числе вход самого
// прогона). Параллельный воркер мог войти в одно приложение между снимками старого и нового — и число строк
// «Вход суперадмина» в паре расходилось. Поэтому они идут по порядку в одном воркере (mode: 'default' отменяет
// fullyParallel для группы; упавший тест не пропускает остальные, в отличие от serial).
const pages = selectedPages();
const superadmin = pages.filter((s) => resolved(s).role === 'superadmin');
defineTests(pages.filter((s) => !superadmin.includes(s)));
if (superadmin.length > 0) {
  test.describe('superadmin', () => {
    test.describe.configure({ mode: 'default' });
    defineTests(superadmin);
  });
}
