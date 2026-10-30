import { createFileRoute } from '@tanstack/react-router';
import { requirePermission } from '../../../auth/guards';
import { pageParam } from '../../../features/cash/search';
import { stockQuery } from '../../../features/warehouse/api';
import { StockPage } from '../../../features/warehouse/StockPage';

// GET /stock (routes/web.php:310): club-area → stock.view. ?search= (max:100), ?page=.
export const Route = createFileRoute('/_admin/stock/')({
  beforeLoad: requirePermission('stock.view'),
  validateSearch: (s: Record<string, unknown>): { search?: string; page?: number } => ({
    search: typeof s.search === 'string' && s.search.trim() !== '' ? s.search.trim().slice(0, 100) : undefined,
    page: s.page === undefined ? undefined : pageParam(s.page),
  }),
  loaderDeps: ({ search }) => search,
  loader: ({ context, deps }) => context.queryClient.ensureQueryData(stockQuery(deps.search ?? '', deps.page ?? 1)),
  component: function Stock() {
    const { search, page } = Route.useSearch();
    return <StockPage key={search ?? ''} search={search ?? ''} page={page ?? 1} />;
  },
});
