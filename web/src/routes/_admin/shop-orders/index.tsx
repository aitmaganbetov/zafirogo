import { createFileRoute } from '@tanstack/react-router';
import { requirePermission } from '../../../auth/guards';
import { pageParam } from '../../../features/cash/search';
import { shopOrdersQuery, type OrderStatusFilter } from '../../../features/shop-orders/api';
import { ShopOrdersPage } from '../../../features/shop-orders/ShopOrdersPage';

const STATUSES: readonly string[] = ['new', 'done', 'cancelled', 'all'];

// GET /shop-orders (routes/web.php:186): club-area → orders.view. ?status=new|done|cancelled|all (по умолчанию new).
export const Route = createFileRoute('/_admin/shop-orders/')({
  beforeLoad: requirePermission('orders.view'),
  validateSearch: (s: Record<string, unknown>): { status?: OrderStatusFilter; page?: number } => ({
    status: typeof s.status === 'string' && STATUSES.includes(s.status) ? (s.status as OrderStatusFilter) : undefined,
    page: s.page === undefined ? undefined : pageParam(s.page),
  }),
  loaderDeps: ({ search }) => search,
  loader: ({ context, deps }) => context.queryClient.ensureQueryData(shopOrdersQuery(deps.status ?? 'new', deps.page ?? 1)),
  component: function ShopOrders() {
    const { status, page } = Route.useSearch();
    return <ShopOrdersPage status={status ?? 'new'} page={page ?? 1} />;
  },
});
