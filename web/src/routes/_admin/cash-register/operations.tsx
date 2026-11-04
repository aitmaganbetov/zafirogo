import { createFileRoute } from '@tanstack/react-router';
import { requirePermission } from '../../../auth/guards';
import { operationsQuery } from '../../../features/cash/api';
import { OperationsPage } from '../../../features/cash/OperationsPage';
import { pageParam } from '../../../features/cash/search';

// GET /cash-register/operations (routes/web.php:278): cash.view; без открытой смены — 404 (firstOrFail).
export const Route = createFileRoute('/_admin/cash-register/operations')({
  beforeLoad: requirePermission('cash.view'),
  validateSearch: (s: Record<string, unknown>): { page?: number } => ({ page: s.page === undefined ? undefined : pageParam(s.page) }),
  loaderDeps: ({ search }) => search,
  loader: ({ context, deps }) => context.queryClient.ensureQueryData(operationsQuery(deps.page ?? 1)),
  component: function Operations() {
    const { page } = Route.useSearch();
    return <OperationsPage page={page ?? 1} />;
  },
});
