import { createFileRoute } from '@tanstack/react-router';
import { cabinetQuery } from '../../features/reseller/api';
import { DashboardPage } from '../../features/reseller/DashboardPage';
import { intParam } from '../../lib/searchParams';

// GET / на хосте реселлера (или /reseller) — reseller.dashboard (routes/web.php:129).
export const Route = createFileRoute('/reseller/')({
  validateSearch: (s: Record<string, unknown>) => ({ clubs_page: intParam(s.clubs_page, 2), earnings_page: intParam(s.earnings_page, 2) }),
  loaderDeps: ({ search }) => search,
  loader: ({ context, deps }) => context.queryClient.ensureQueryData(cabinetQuery(deps)),
  component: ResellerDashboardRoute,
});

function ResellerDashboardRoute() {
  return <DashboardPage search={Route.useSearch()} />;
}
