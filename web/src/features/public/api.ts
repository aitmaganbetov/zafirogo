import { queryOptions } from '@tanstack/react-query';
import { createApiClient, unwrap } from '../../api/http';
import type { components, paths } from '../../api/gen/public';

/* Публичные страницы (api/public.yaml). Ключи Query — ['public', …]. */

export const publicApi = createApiClient<paths>();
export type Landing = components['schemas']['Landing'];

export const landingQuery = () =>
  queryOptions({
    queryKey: ['public', 'landing'],
    queryFn: () => unwrap(publicApi.GET('/api/landing')),
  });
