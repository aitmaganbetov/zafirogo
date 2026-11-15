/*
 * Разметка перенесена из Blade с обычными <a href> — клик по ссылке своего сайта переводится в переход роутера
 * (без перезагрузки и потери access-токена в памяти). Ссылки с target, download и чужие — как обычно;
 * `data-reload` — принудительная перезагрузка.
 *
 * Предзагрузка (FE-06): `defaultPreload: 'intent'` роутера работает только на компонентах <Link>, а в разметке
 * из Blade — обычные <a>. Поэтому намерение (наведение, фокус, касание) ловится здесь же, делегированием:
 * чанк страницы и её данные (loader) начинают грузиться до клика. DOM и классы ссылок не меняются.
 */

export interface LinkRouter {
  navigate(opts: { href: string }): unknown;
  preloadRoute(opts: { href: string }): Promise<unknown>;
}

/** Задержка перед предзагрузкой, мс: проведение мышью через меню не запускает запросы (как у <Link>: 50 мс). */
export const PRELOAD_DELAY_MS = 50;

/** Внутренний адрес ссылки для роутера или null (чужая ссылка, target, download, data-reload, /api, /ws). */
export function internalHref(a: HTMLAnchorElement | null | undefined): string | null {
  if (!a || a.target || a.hasAttribute('download') || a.hasAttribute('data-reload')) return null;
  const href = a.getAttribute('href');
  if (!href || !href.startsWith('/') || href.startsWith('//')) return null;
  const url = new URL(href, location.origin);
  if (url.pathname.startsWith('/api/') || url.pathname === '/ws') return null;
  return url.pathname + url.search + url.hash;
}

const anchorOf = (e: Event) => (e.target as Element | null)?.closest?.('a') as HTMLAnchorElement | null | undefined;

/** Подключить перехват кликов и предзагрузку по намерению; возвращает отключение. */
export function interceptLinks(router: LinkRouter, doc: Document = document): () => void {
  const onClick = (e: MouseEvent) => {
    if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
    const href = internalHref(anchorOf(e));
    if (!href) return;
    e.preventDefault();
    void router.navigate({ href });
  };

  let timer: ReturnType<typeof setTimeout> | null = null;
  let pending: HTMLAnchorElement | null = null;
  const cancel = () => {
    if (timer) clearTimeout(timer);
    timer = null;
    pending = null;
  };
  const preload = (href: string) => {
    // Ошибка предзагрузки (сеть, гард) не должна шуметь: переход по клику покажет её как обычно.
    void Promise.resolve()
      .then(() => router.preloadRoute({ href }))
      .catch(() => undefined);
  };
  const onIntent = (e: Event) => {
    const a = anchorOf(e);
    if (a === pending && e.type !== 'touchstart') return; // движение внутри той же ссылки
    cancel();
    const href = internalHref(a);
    if (!a || !href || href === location.pathname + location.search + location.hash) return;
    if (e.type === 'touchstart') {
      preload(href);
      return;
    }
    pending = a;
    timer = setTimeout(() => {
      timer = null;
      preload(href);
    }, PRELOAD_DELAY_MS);
  };
  const onLeave = (e: MouseEvent) => {
    // Ушли со ссылки раньше задержки — не грузим.
    if (pending && !pending.contains(e.relatedTarget as Node | null)) cancel();
  };

  doc.addEventListener('click', onClick);
  doc.addEventListener('mouseover', onIntent);
  doc.addEventListener('mouseout', onLeave);
  doc.addEventListener('focusin', onIntent);
  doc.addEventListener('touchstart', onIntent, { passive: true });
  return () => {
    cancel();
    doc.removeEventListener('click', onClick);
    doc.removeEventListener('mouseover', onIntent);
    doc.removeEventListener('mouseout', onLeave);
    doc.removeEventListener('focusin', onIntent);
    doc.removeEventListener('touchstart', onIntent);
  };
}
