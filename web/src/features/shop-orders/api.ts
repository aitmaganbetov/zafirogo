import { queryOptions } from '@tanstack/react-query';
import { createApiClient, idempotencyKey, unwrap } from '../../api/http';
import type { components, paths } from '../../api/gen/shoporders';

/* Заказы из агента (api/shoporders.yaml, ShopOrderController). Ключи — ['shop-orders', …]. */

export const shopOrdersApi = createApiClient<paths>();
export type ShopOrder = components['schemas']['ShopOrder'];
export type OrderStatusFilter = 'new' | 'done' | 'cancelled' | 'all';

export const shopOrdersQuery = (status: OrderStatusFilter, page: number) =>
  queryOptions({
    queryKey: ['shop-orders', status, page],
    queryFn: () => unwrap(shopOrdersApi.GET('/api/shop-orders', { params: { query: { status, page } } })),
  });

export const markDone = (orderId: number) =>
  unwrap(shopOrdersApi.POST('/api/shop-orders/{orderId}/done', { params: { path: { orderId } } }));

export const cancelOrder = (orderId: number) =>
  unwrap(shopOrdersApi.POST('/api/shop-orders/{orderId}/cancel', { params: { path: { orderId }, header: { 'Idempotency-Key': idempotencyKey() } } }));
