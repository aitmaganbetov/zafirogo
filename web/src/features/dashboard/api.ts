import { queryOptions } from '@tanstack/react-query';
import { createApiClient, unwrap } from '../../api/http';
import type { components, paths } from '../../api/gen/dashboard';

/* Админпанель (api/dashboard.yaml). Laravel: DashboardController. Ключи Query — ['dashboard']. */

export const dashboardApi = createApiClient<paths>();
export type Dashboard = components['schemas']['Dashboard'];

export const dashboardQuery = () =>
  queryOptions({
    queryKey: ['dashboard'],
    queryFn: () => unwrap(dashboardApi.GET('/api/dashboard')),
  });

/** Куда уводит DashboardController: кассир — на кассу, оператор — на список ПК; остальным — админпанель. */
export function dashboardLanding(role: string | null | undefined): string | null {
  return ({ cashier: '/cash-register', operator: '/club-computers' } as Record<string, string>)[role ?? ''] ?? null;
}

/** number_format($n, 0, ',', ' ') для целого (счётчики). */
export function formatInt(n: number): string {
  const s = String(Math.abs(Math.trunc(n)));
  let out = '';
  for (let i = 0; i < s.length; i++) {
    if (i > 0 && (s.length - i) % 3 === 0) out += ' ';
    out += s[i];
  }
  return (n < 0 ? '-' : '') + out;
}
