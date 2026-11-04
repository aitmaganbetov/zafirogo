/*
 * Отдельный домен кабинета суперадмина (Q-069, SUPERADMIN_DOMAIN). Laravel: routes/web.php — кабинет на своём хосте
 * с корня (Route::domain()), без домена — с префиксом /superadmin. Сервер отмечает index.html хоста кабинета
 * атрибутом <html data-superadmin-host> (server/internal/webui). Внутри SPA маршруты кабинета всегда /superadmin/…;
 * на хосте кабинета адрес в браузере — без префикса (rewrite роутера).
 */

/** Пути кабинета на его хосте (GET-маршруты superadmin.* и languages.* старого routes/web.php). */
const CABINET_PATHS = ['/clubs', '/accounts', '/plans', '/logs', '/languages', '/telegram', '/platform-news'];

export function isCabinetHost(): boolean {
  return typeof document !== 'undefined' && document.documentElement.hasAttribute('data-superadmin-host');
}

function isCabinetPath(pathname: string): boolean {
  return pathname === '/' || CABINET_PATHS.some((p) => pathname === p || pathname.startsWith(p + '/'));
}

/** Внутренний путь /superadmin/… → адрес в браузере (на хосте кабинета — без префикса). */
export function publicPath(internal: string): string {
  if (!isCabinetHost() || !internal.startsWith('/superadmin')) return internal;
  const rest = internal.slice('/superadmin'.length);
  return rest === '' || rest.startsWith('?') || rest.startsWith('#') ? '/' + rest : rest;
}

/** Ссылка кабинета: path — путь как на хосте кабинета («/clubs/5», «/»). */
export function saHref(path: string): string {
  if (isCabinetHost()) return path;
  if (path === '/' || path.startsWith('/?')) return '/superadmin' + path.slice(1);
  return '/superadmin' + path;
}

/** Адрес браузера → внутренний путь роутера. */
export function toInternal(pathname: string): string {
  if (!isCabinetHost() || pathname.startsWith('/superadmin') || !isCabinetPath(pathname)) return pathname;
  return pathname === '/' ? '/superadmin' : '/superadmin' + pathname;
}

/** Rewrite TanStack Router на хосте кабинета (undefined — без изменений). */
export const cabinetRewrite = {
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
