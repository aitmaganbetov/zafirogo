import { createFileRoute } from '@tanstack/react-router';
import { requirePermission } from '../../../auth/guards';
import { pageParam } from '../../../features/cash/search';
import { servicesQuery } from '../../../features/services/api';
import { ServicesPage } from '../../../features/services/ServicesPage';

// GET /services (routes/web.php:232): club-area → services.view.
export const Route = createFileRoute('/_admin/services/')({
  beforeLoad: requirePermission('services.view'),
  validateSearch: (s: Record<string, unknown>): { page?: number } => ({ page: s.page === undefined ? undefined : pageParam(s.page) }),
  loaderDeps: ({ search }) => search,
  loader: ({ context, deps }) => context.queryClient.ensureQueryData(servicesQuery(deps.page ?? 1)),
  component: function Services() {
    const { page } = Route.useSearch();
    return <ServicesPage page={page ?? 1} />;
  },
});
