import { QueryClient } from '@tanstack/react-query';
import { createRouter, type RouterHistory } from '@tanstack/react-router';
import { routeTree } from './routeTree.gen';
import { session, type SessionStore } from './auth/session';
import { NotFoundPage, RouteErrorPage } from './pages/ErrorPage';

/** Контекст маршрутов: кэш запросов и сессия (гарды берут их из context в beforeLoad). */
export interface RouterContext {
  readonly queryClient: QueryClient;
  readonly session: SessionStore;
}

export function createQueryClient(): QueryClient {
  return new QueryClient({
    defaultOptions: {
      // Данные экранов обновляются событиями WebSocket (realtime/), а не опросом.
      queries: { staleTime: 30_000, retry: 1, refetchOnWindowFocus: false },
    },
  });
}

export function createAppRouter(queryClient: QueryClient, store: SessionStore = session, history?: RouterHistory) {
  return createRouter({
    routeTree,
    ...(history ? { history } : {}),
    context: { queryClient, session: store },
    defaultPreload: 'intent',
    defaultPreloadStaleTime: 0,
    defaultNotFoundComponent: NotFoundPage,
    defaultErrorComponent: RouteErrorPage,
    scrollRestoration: true,
  });
}

declare module '@tanstack/react-router' {
  interface Register {
    router: ReturnType<typeof createAppRouter>;
  }
}
