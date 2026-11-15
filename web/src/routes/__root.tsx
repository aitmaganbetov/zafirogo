import { Outlet, createRootRouteWithContext, redirect, useRouter } from '@tanstack/react-router';
import { useEffect } from 'react';
import type { RouterContext } from '../router';
import { NotFoundPage, RouteErrorPage } from '../pages/ErrorPage';
import { I18nGate } from '../i18n/I18nGate';
import { interceptLinks } from '../lib/linkInterception';
import { allowedOnResellerHost, isResellerHost } from '../features/reseller/host';

export const Route = createRootRouteWithContext<RouterContext>()({
  // Сессия (refresh по cookie) — до любой страницы: язык и гарды зависят от неё.
  beforeLoad: async ({ context, location }) => {
    // RestrictResellerHost (UQ-21): на хосте реселлера — только кабинет, вход и восстановление пароля; регистрация,
    // панель клуба и страницы сайта уходят в кабинет.
    if (isResellerHost() && !allowedOnResellerHost(location.pathname)) throw redirect({ href: '/reseller' });
    await context.session.ensureLoaded();
  },
  component: Root,
  notFoundComponent: NotFoundPage,
  errorComponent: RouteErrorPage,
});

/** Ссылки из Blade (<a href>): переход роутера и предзагрузка по наведению — lib/linkInterception.ts. */
function useLinkInterception() {
  const router = useRouter();
  useEffect(
    () =>
      interceptLinks({
        navigate: ({ href }) => router.navigate({ href }),
        preloadRoute: ({ href }) => router.preloadRoute({ href } as never),
      }),
    [router],
  );
}

function Root() {
  useLinkInterception();
  return (
    <I18nGate>
      <Outlet />
    </I18nGate>
  );
}
