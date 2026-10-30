import { createFileRoute } from '@tanstack/react-router';
import { dashboardQuery, parseDashboardSearch } from '../../features/superadmin/api';
import { DashboardPage } from '../../features/superadmin/DashboardPage';

// GET / (хост кабинета) или /superadmin — superadmin.dashboard (routes/web.php:60).
export const Route = createFileRoute('/superadmin/')({
  validateSearch: parseDashboardSearch,
  loaderDeps: ({ search }) => search,
  loader: ({ context, deps }) => context.queryClient.ensureQueryData(dashboardQuery(deps)),
  component: DashboardRoute,
});

function DashboardRoute() {
  const search = Route.useSearch();
  return <DashboardPage search={search} />;
}
