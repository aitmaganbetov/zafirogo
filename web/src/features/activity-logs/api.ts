import { queryOptions } from '@tanstack/react-query';
import { createApiClient, unwrap } from '../../api/http';
import type { components, paths } from '../../api/gen/activitylogs';

/*
 * Логи действий (api/activitylogs.yaml). Laravel: ActivityLogController::index(). Страницы — keyset (cursor):
 * назад — по стеку курсоров в адресе (back), номера страниц у keyset-пагинации неизвестны.
 */

export const activityLogsApi = createApiClient<paths>();

type S = components['schemas'];
export type ActivityLogPage = S['ActivityLogPage'];
export type ActivityLogEntry = S['ActivityLogEntry'];

export interface ActivityLogsSearch {
  readonly from?: string;
  readonly to?: string;
  readonly event?: string;
  readonly user_id?: string;
  readonly search?: string;
  readonly cursor?: string;
  /** Курсоры предыдущих страниц через «.» (для ссылки «Назад»). */
  readonly back?: string;
}

const KEYS = ['from', 'to', 'event', 'user_id', 'search', 'cursor', 'back'] as const;

export function parseActivityLogsSearch(s: Record<string, unknown>): ActivityLogsSearch {
  const out: Record<string, string> = {};
  for (const k of KEYS) {
    const v = s[k];
    if (typeof v === 'string' && v !== '') out[k] = v;
    else if (typeof v === 'number') out[k] = String(v);
  }
  return out;
}

/** Адрес страницы журнала с фильтрами (withQueryString()). */
export function activityLogsHref(p: ActivityLogsSearch): string {
  const q = new URLSearchParams();
  for (const k of KEYS) {
    const v = p[k];
    if (v) q.set(k, v);
  }
  const qs = q.toString();
  return qs ? `/activity-logs?${qs}` : '/activity-logs';
}

export const activityLogsQuery = (p: ActivityLogsSearch) =>
  queryOptions({
    queryKey: ['activity-logs', p.from ?? '', p.to ?? '', p.event ?? '', p.user_id ?? '', p.search ?? '', p.cursor ?? ''],
    queryFn: () =>
      unwrap(
        activityLogsApi.GET('/api/activity-logs', {
          params: { query: { from: p.from, to: p.to, event: p.event, user_id: p.user_id, search: p.search, cursor: p.cursor } },
        }),
      ),
  });

/** number_format($n, 0, ',', ' '). */
export function numberFormat(n: number): string {
  return String(Math.trunc(n)).replace(/\B(?=(\d{3})+(?!\d))/g, ' ');
}
