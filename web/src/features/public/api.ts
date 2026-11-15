import { queryOptions } from '@tanstack/react-query';
import { createApiClient, unwrap } from '../../api/http';
import type { components, paths } from '../../api/gen/public';

/* Публичные страницы (api/public.yaml). Ключи Query — ['public', …]. */

export const publicApi = createApiClient<paths>();
export type Landing = components['schemas']['Landing'];
export type PricingPlan = components['schemas']['PricingPlan'];

/** Данные всех страниц сайта (LandingController::data()): пробный период и ссылка на установщик агента. */
export const landingQuery = () =>
  queryOptions({
    queryKey: ['public', 'landing'],
    queryFn: ({ signal }) => unwrap(publicApi.GET('/api/landing', { signal })),
  });

/** Тарифы платформы для /pricing (LandingController::pricing()). */
export const pricingQuery = () =>
  queryOptions({
    queryKey: ['public', 'pricing'],
    queryFn: ({ signal }) => unwrap(publicApi.GET('/api/pricing', { signal })),
  });
