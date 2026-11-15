/*
 * Отдельный домен кабинета реселлера (UQ-21, RESELLER_DOMAIN; в проде — resseler.zafiro.kz, написание как в старом
 * config/app.php). Laravel: routes/web.php — кабинет на своём хосте с корня (Route::domain()), без домена — /reseller;
 * app/Http/Middleware/RestrictResellerHost.php — на хосте реселлера доступны только кабинет, вход и восстановление
 * пароля, всё остальное (регистрация, панель клуба, страницы сайта) уходит в кабинет.
 * Сервер отмечает index.html хоста реселлера атрибутом <html data-reseller-host> (server/internal/webui). Внутри SPA
 * кабинет всегда /reseller; на хосте реселлера адрес в браузере — «/» (rewrite роутера).
 */

const CABINET = '/reseller';

/** Страницы, доступные на хосте реселлера кроме кабинета: auth.show-login, auth.password.*. */
const HOST_PATHS = ['/login', '/forgot-password', '/reset-password'];

export function isResellerHost(): boolean {
  return typeof document !== 'undefined' && document.documentElement.hasAttribute('data-reseller-host');
}

const isCabinet = (pathname: string) => pathname === CABINET || pathname.startsWith(CABINET + '/');

/** Адрес браузера → внутренний путь роутера. */
export function toInternal(pathname: string): string {
  return isResellerHost() && pathname === '/' ? CABINET : pathname;
}

/** Внутренний путь → адрес в браузере (на хосте реселлера кабинет — с корня). */
export function publicPath(internal: string): string {
  return isResellerHost() && (internal === CABINET || internal === CABINET + '/') ? '/' : internal;
}

/** Ссылка на кабинет реселлера: route('reseller.dashboard'). */
export function resellerHref(search = ''): string {
  return publicPath(CABINET) + search;
}

/**
 * RestrictResellerHost: страница (внутренний путь) открывается на хосте реселлера. Остальные — редирект в кабинет
 * (корневой beforeLoad, routes/__root.tsx).
 */
export function allowedOnResellerHost(internalPath: string): boolean {
  return isCabinet(internalPath) || HOST_PATHS.includes(internalPath);
}

/** Rewrite TanStack Router на хосте реселлера (undefined — без изменений). */
export const resellerRewrite = {
  input: ({ url }: { url: URL }) => {
    const next = toInternal(url.pathname);
    if (next === url.pathname) return undefined;
    const u = new URL(url.href);
    u.pathname = next;
    return u;
  },
  output: ({ url }: { url: URL }) => {
    const next = publicPath(url.pathname);
    if (next === url.pathname) return undefined;
    const u = new URL(url.href);
    u.pathname = next;
    return u;
  },
};
