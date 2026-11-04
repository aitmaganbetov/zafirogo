import { Outlet, createRootRouteWithContext, useRouter } from '@tanstack/react-router';
import { useEffect } from 'react';
import type { RouterContext } from '../router';
import { NotFoundPage, RouteErrorPage } from '../pages/ErrorPage';
import { I18nGate } from '../i18n/I18nGate';

export const Route = createRootRouteWithContext<RouterContext>()({
  // Сессия (refresh по cookie) — до любой страницы: язык и гарды зависят от неё.
  beforeLoad: async ({ context }) => {
    await context.session.ensureLoaded();
  },
  component: Root,
  notFoundComponent: NotFoundPage,
  errorComponent: RouteErrorPage,
});

/**
 * Разметка перенесена из Blade с обычными <a href> — клик по ссылке своего сайта переводится в переход
 * роутера (без перезагрузки и потери access-токена в памяти). Ссылки с target, download, модификаторами
 * и чужие — как обычно; `data-reload` — принудительная перезагрузка.
 */
function useLinkInterception() {
  const router = useRouter();
  useEffect(() => {
    const onClick = (e: MouseEvent) => {
      if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
      const a = (e.target as Element | null)?.closest?.('a');
      if (!a || a.target || a.hasAttribute('download') || a.hasAttribute('data-reload')) return;
      const href = a.getAttribute('href');
      if (!href || !href.startsWith('/') || href.startsWith('//')) return;
      const url = new URL(href, location.origin);
      if (url.pathname.startsWith('/api/') || url.pathname === '/ws') return;
      e.preventDefault();
      void router.navigate({ href: url.pathname + url.search + url.hash });
    };
    document.addEventListener('click', onClick);
    return () => document.removeEventListener('click', onClick);
  }, [router]);
}

function Root() {
  useLinkInterception();
  return (
    <I18nGate>
      <Outlet />
    </I18nGate>
  );
}
