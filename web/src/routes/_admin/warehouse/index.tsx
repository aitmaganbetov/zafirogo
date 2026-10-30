import { createFileRoute } from '@tanstack/react-router';
import { requirePermission } from '../../../auth/guards';
import { pageParam } from '../../../features/cash/search';
import { overviewQuery, productsPageQuery } from '../../../features/warehouse/api';
import { WarehousePage } from '../../../features/warehouse/WarehousePage';

// GET /warehouse (routes/web.php:194): club-area → warehouse.view.
export const Route = createFileRoute('/_admin/warehouse/')({
  beforeLoad: requirePermission('warehouse.view'),
  validateSearch: (s: Record<string, unknown>): { page?: number } => ({ page: s.page === undefined ? undefined : pageParam(s.page) }),
  loaderDeps: ({ search }) => search,
  loader: ({ context, deps }) =>
    Promise.all([context.queryClient.ensureQueryData(overviewQuery()), context.queryClient.ensureQueryData(productsPageQuery(deps.page ?? 1))]),
  component: function Warehouse() {
    const { page } = Route.useSearch();
    return <WarehousePage page={page ?? 1} />;
  },
});
