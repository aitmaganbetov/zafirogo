// Визуальное сравнение старого Laravel и нового сервера: конфигурация, вход, стабилизация страницы.
// Описание — tools/visual/README.md. Снимки сравнивает tools/visual/compare.mjs (pixelmatch).
import { execFile } from 'node:child_process';
import { createHash } from 'node:crypto';
import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { homedir } from 'node:os';
import path from 'node:path';
import { promisify } from 'node:util';
import type { BrowserContext, Page, Route } from '@playwright/test';

export type AppName = 'old' | 'new';

export interface Viewport {
  width: number;
  height: number;
  isMobile?: boolean;
  hasTouch?: boolean;
}

type Masks = string[] | { old?: string[]; new?: string[] };

export interface PageSpec {
  id: string;
  old: string;
  new: string;
  role?: string | null;
  viewports?: string[];
  themes?: Theme[];
  mask?: Masks;
  waitFor?: string;
  /** false — только окно (каркас страницы), без растягивания под содержимое. По умолчанию true. */
  fullPage?: boolean;
  /** 'print' — снимок печатной версии (@media print: чек, X/Z-отчёт). */
  media?: 'print';
  maxDiffPercent?: number;
  pixelThreshold?: number;
  status?: 'ported' | 'pending';
  note?: string;
  /** Хост вместо localhost в адресах обоих приложений (домен кабинета суперадмина, SUPERADMIN_DOMAIN — Q-069). */
  host?: string;
}

export type Theme = 'light' | 'dark';

interface PagesFile {
  defaults: Required<Pick<PageSpec, 'viewports' | 'themes' | 'role' | 'pixelThreshold' | 'maxDiffPercent' | 'status'>> & {
    mask: string[];
  };
  viewports: Record<string, Viewport>;
  maskSets: Record<string, string[]>;
  pages: PageSpec[];
}

interface Demo {
  now: string;
  login: { password: string; roles: Record<string, number> };
  users: { id: number; phone: string }[];
}

export const VISUAL_DIR = path.resolve(import.meta.dirname, '../../../tools/visual');
export const REPORT_DIR = process.env.VISUAL_REPORT_DIR ?? path.join(VISUAL_DIR, 'report');
const CACHE_DIR = process.env.VISUAL_CACHE ?? path.join(homedir(), '.cache', 'zafirogo-visual');

export const pagesFile: PagesFile = JSON.parse(readFileSync(path.join(VISUAL_DIR, 'pages.json'), 'utf8'));
export const demo: Demo = JSON.parse(readFileSync(path.join(VISUAL_DIR, 'fixtures', 'demo.json'), 'utf8'));

export const baseURL: Record<AppName, string> = {
  old: process.env.VISUAL_OLD_URL ?? 'http://localhost:8001',
  new: process.env.VISUAL_NEW_URL ?? 'http://localhost:8091',
};

/** Базовый адрес приложения для страницы: с хостом страницы (домен кабинета) или как есть. */
export function baseFor(app: AppName, spec: Pick<PageSpec, 'host'>): string {
  if (!spec.host) return baseURL[app];
  const u = new URL(baseURL[app]);
  u.hostname = spec.host;
  return u.origin;
}

/** Страницы прогона: VISUAL_PAGES=id1,id2 (task visual -- --page id) или все. */
export function selectedPages(): PageSpec[] {
  const only = (process.env.VISUAL_PAGES ?? '').split(',').map((s) => s.trim()).filter(Boolean);
  const unknown = only.filter((id) => !pagesFile.pages.some((p) => p.id === id));
  if (unknown.length > 0) throw new Error(`pages.json: нет страниц ${unknown.join(', ')}`);
  return pagesFile.pages.filter((p) => only.length === 0 || only.includes(p.id));
}

/** Значения страницы с умолчаниями из pages.json. */
export function resolved(p: PageSpec) {
  const d = pagesFile.defaults;
  return {
    ...p,
    role: p.role === undefined ? d.role : p.role,
    viewports: p.viewports ?? d.viewports,
    themes: p.themes ?? d.themes,
    status: p.status ?? d.status,
  };
}

/** Селекторы масок для приложения: наборы @имя раскрываются из maskSets. */
export function masksFor(p: PageSpec, app: AppName): string[] {
  const raw = p.mask ?? pagesFile.defaults.mask;
  const list = Array.isArray(raw) ? raw : (raw[app] ?? []);
  return list.flatMap((s) => (s.startsWith('@') ? (pagesFile.maskSets[s] ?? []) : [s]));
}

export function shotDir(id: string, viewport: string, theme: Theme): string {
  const dir = path.join(REPORT_DIR, 'shots', id, `${viewport}-${theme}`);
  mkdirSync(dir, { recursive: true });
  return dir;
}

/** Телефон и пароль пользователя роли (demo.json login.roles). */
export function credentials(role: string): { phone: string; password: string } {
  const userId = demo.login.roles[role];
  const user = demo.users.find((u) => u.id === userId);
  if (!user) throw new Error(`demo.json: нет пользователя роли ${role}`);
  return { phone: user.phone, password: demo.login.password };
}

// ---------------------------------------------------------------------------
// Внешние ресурсы старого проекта (CDN): из npm-пакетов tools/visual/node_modules и дискового кэша
// ---------------------------------------------------------------------------

const NODE_MODULES = path.join(VISUAL_DIR, 'node_modules');
// Те же файлы, что на CDN (cdnjs и jsdelivr раздают опубликованные npm-пакеты); версии — в tools/visual/package.json.
const CDN_TO_NPM: [RegExp, string][] = [
  [/^https:\/\/cdnjs\.cloudflare\.com\/ajax\/libs\/font-awesome\/6\.4\.0\/(.+)$/, '@fortawesome/fontawesome-free/$1'],
  [/^https:\/\/cdn\.jsdelivr\.net\/npm\/chart\.js@4\.5\.1\/(.+)$/, 'chart.js/$1'],
];
// Google Fonts отвечают по User-Agent: кэшируем ответ для UA Chromium Playwright.
const CACHED_HOSTS = new Set(['fonts.googleapis.com', 'fonts.gstatic.com']);
const MIME: Record<string, string> = {
  '.css': 'text/css; charset=utf-8',
  '.js': 'text/javascript; charset=utf-8',
  '.woff2': 'font/woff2',
  '.woff': 'font/woff',
  '.ttf': 'font/ttf',
  '.svg': 'image/svg+xml',
};
const run = promisify(execFile);

export interface NetworkLog {
  served: string[];
  blocked: string[];
  failed: string[];
}

async function serveExternal(route: Route, log: NetworkLog, userAgent: string): Promise<void> {
  const url = route.request().url();
  for (const [re, target] of CDN_TO_NPM) {
    if (re.test(url)) {
      const file = path.join(NODE_MODULES, url.replace(re, target).split('?')[0]);
      if (existsSync(file)) {
        log.served.push(url);
        return route.fulfill({ path: file, contentType: MIME[path.extname(file)] ?? 'application/octet-stream' });
      }
      log.blocked.push(`${url} (нет ${file}: npm ci в tools/visual)`);
      return route.abort('blockedbyclient');
    }
  }
  const host = new URL(url).hostname;
  if (CACHED_HOSTS.has(host)) {
    const key = createHash('sha256').update(`${userAgent}\n${url}`).digest('hex').slice(0, 32);
    const dir = path.join(CACHE_DIR, 'external');
    const body = path.join(dir, key);
    const meta = `${body}.type`;
    if (!existsSync(body)) {
      mkdirSync(dir, { recursive: true });
      try {
        // curl уважает HTTPS_PROXY и системные сертификаты (fetch Node — нет).
        const { stdout } = await run('curl', ['-fsSL', '-A', userAgent, '-o', body, '-w', '%{content_type}', url]);
        writeFileSync(meta, stdout.trim());
      } catch {
        log.blocked.push(`${url} (не скачался)`);
        return route.abort('internetdisconnected');
      }
    }
    log.served.push(url);
    return route.fulfill({ path: body, contentType: existsSync(meta) ? readFileSync(meta, 'utf8') : undefined });
  }
  // Остальное внешнее (аналитика и т. п.) — отключено: снимок не должен зависеть от сети.
  log.blocked.push(url);
  return route.abort('blockedbyclient');
}

/** Внешние запросы контекста: CDN → npm, Google Fonts → кэш, прочее — блок. */
export async function routeExternal(context: BrowserContext, log: NetworkLog, userAgent: string): Promise<void> {
  // localhost и его поддомены (домен кабинета admin.localhost) — свои приложения.
  await context.route(/^https?:\/\/(?!(?:[a-z0-9-]+\.)*localhost[:/]|127\.0\.0\.1[:/])/, (route) => serveExternal(route, log, userAgent));
}

// ---------------------------------------------------------------------------
// Вход и стабилизация
// ---------------------------------------------------------------------------

/**
 * Вход пользователем роли. Старый — форма /login (телефон без +7 и пароль). Новый — та же форма, если она уже
 * перенесена (#phone на /login), иначе POST /api/auth/login (refresh-cookie в контексте; SPA восстановит сессию).
 */
export async function login(page: Page, app: AppName, role: string, base: string = baseURL[app]): Promise<'form' | 'api'> {
  const { phone, password } = credentials(role);
  await page.goto(`${base}/login`);
  const phoneInput = page.locator('input#phone');
  const hasForm = await phoneInput
    .waitFor({ state: 'visible', timeout: app === 'old' ? 15_000 : 3_000 })
    .then(() => true)
    .catch(() => false);
  if (hasForm) {
    await phoneInput.fill(phone.replace(/^\+7/, ''));
    await page.locator('input#password').fill(password);
    await Promise.all([
      page.waitForURL((u) => !u.pathname.startsWith('/login'), { timeout: 15_000 }),
      page.locator('form button[type="submit"], form button:not([type])').first().click(),
    ]);
    return 'form';
  }
  if (app === 'old') throw new Error('старый /login: нет формы входа');
  const res = await page.request.post(`${base}/api/auth/login`, { data: { phone, password } });
  if (!res.ok()) throw new Error(`новый /api/auth/login: ${res.status()} ${await res.text()}`);
  return 'api';
}

/**
 * До загрузки страницы: тема, отклонённый баннер PWA, развёрнутый сайдбар и управляемые часы браузера с «сейчас»
 * демо-данных (Date, таймеры и requestAnimationFrame идут только по page.clock.runFor в settle()).
 */
export async function prepareContext(page: Page, theme: Theme): Promise<void> {
  const now = new Date(demo.now);
  await page.clock.install({ time: now });
  await page.addInitScript(
    ([t, dismissedAt]) => {
      try {
        localStorage.setItem('zafiro.theme', t);
        localStorage.setItem('zafiro.installDismissedAt', dismissedAt);
        localStorage.setItem('zafiro.sidebarCollapsed', '0');
        localStorage.setItem('zafiro.notificationSound', '0');
      } catch {
        /* хранилище недоступно — тема по умолчанию */
      }
    },
    [theme, String(now.getTime())] as const,
  );
}

const STABILIZE_CSS = `*, *::before, *::after {
  animation-duration: 0s !important; animation-delay: 0s !important;
  transition-duration: 0s !important; transition-delay: 0s !important;
  caret-color: transparent !important; scroll-behavior: auto !important;
}`;

// Выполняются в странице (строки: tsconfig.node без lib "dom").
const WAIT_FONTS_AND_IMAGES = `(async () => {
  await document.fonts.ready;
  await Promise.all(Array.from(document.images).filter((img) => !img.complete).map((img) => new Promise((resolve) => {
    img.addEventListener('load', resolve, { once: true });
    img.addEventListener('error', resolve, { once: true });
  })));
})()`;
// Наибольший непрокрученный остаток среди блоков с overflow-y: auto|scroll.
const SCROLL_OVERFLOW = `(() => {
  let max = 0;
  for (const el of document.querySelectorAll('body *')) {
    if (!/(auto|scroll)/.test(getComputedStyle(el).overflowY) || el.clientHeight === 0) continue;
    max = Math.max(max, el.scrollHeight - el.clientHeight);
  }
  return max;
})()`;

/** Сколько времени страницы прокрутить после загрузки: JS-анимации (Chart.js и т. п.) доходят до конца. */
const SETTLE_CLOCK_MS = Number(process.env.VISUAL_SETTLE_MS ?? 3_000);

/** После перехода: сеть, анимации (часы страницы вперёд), шрифты, картинки. */
export async function settle(page: Page, waitFor?: string): Promise<void> {
  await page.waitForLoadState('load');
  await page.clock.runFor(SETTLE_CLOCK_MS);
  // Опросы старого JS и WebSocket нового не дают «тишины» навсегда: ждём её не дольше 5 с.
  await page.waitForLoadState('networkidle', { timeout: 5_000 }).catch(() => undefined);
  if (waitFor) await page.locator(waitFor).first().waitFor({ timeout: 10_000 }).catch(() => undefined);
  await page.addStyleTag({ content: STABILIZE_CSS });
  await page.evaluate(WAIT_FONTS_AND_IMAGES);
  // Кадры после стилей и шрифтов (requestAnimationFrame — на управляемых часах страницы).
  await page.clock.runFor(100);
}

/**
 * Высота «полной страницы» для раскладок с прокруткой внутри блока (admin-app на 100vh, прокручивается main):
 * fullPage-снимок видит только окно. Окно увеличивается на непрокрученный остаток самого длинного прокручиваемого
 * блока — CSS страниц не меняется, раскладка на 100vh растягивается так же, как у высокого монитора.
 */
export async function growViewportToContent(page: Page, maxHeight = 12_000): Promise<void> {
  for (let i = 0; i < 3; i++) {
    const vp = page.viewportSize();
    if (!vp) return;
    const extra = await page.evaluate<number>(SCROLL_OVERFLOW);
    if (extra <= 1) return;
    const height = Math.min(vp.height + extra, maxHeight);
    if (height === vp.height) return;
    await page.setViewportSize({ width: vp.width, height });
    // Перерисовка после resize (графики пересчитывают размер и анимируются заново).
    await page.clock.runFor(SETTLE_CLOCK_MS);
  }
}
