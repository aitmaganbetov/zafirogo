import { queryOptions } from '@tanstack/react-query';
import { createApiClient, unwrap } from '../../api/http';
import type { components, paths } from '../../api/gen/billing';

/* Экран «Подписка» (api/billing.yaml, BillingController::show). Ключи Query — ['billing']. */

export const billingApi = createApiClient<paths>();
export type BillingSubscription = components['schemas']['BillingSubscription'];

export const billingQuery = () =>
  queryOptions({
    queryKey: ['billing'],
    queryFn: () => unwrap(billingApi.GET('/api/billing')),
  });
