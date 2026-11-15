import { queryOptions } from '@tanstack/react-query';
import { createApiClient, unwrap } from '../../api/http';
import type { components, paths } from '../../api/gen/reseller';

/*
 * Кабинет реселлера (api/reseller.yaml). Laravel: app/Http/Controllers/ResellerController.php. Ключи Query —
 * ['reseller', …]. Суммы — целые тиыны, процент — базисные пункты.
 */

const resellerApi = createApiClient<paths>();

export type ResellerCabinet = components['schemas']['ResellerCabinet'];

export interface CabinetSearch {
  /** Страницы списков — параметры `clubs_page` и `earnings_page`, как у paginate(…, 'clubs_page') старого экрана. */
  readonly clubs_page?: number;
  readonly earnings_page?: number;
}

export const cabinetQuery = (s: CabinetSearch) =>
  queryOptions({
    queryKey: ['reseller', 'dashboard', s.clubs_page ?? 1, s.earnings_page ?? 1],
    queryFn: ({ signal }) =>
      unwrap(resellerApi.GET('/api/reseller/dashboard', { params: { query: { clubs_page: s.clubs_page, earnings_page: s.earnings_page } }, signal })),
  });
