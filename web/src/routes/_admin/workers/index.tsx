import { createFileRoute } from '@tanstack/react-router';
import { requirePermission } from '../../../auth/guards';
import { parseWorkersSearch, workersQuery } from '../../../features/staff/api';
import { WorkersPage } from '../../../features/staff/WorkersPage';

// GET /workers (routes/web.php:263, workers.index): club-area workers.view.
export const Route = createFileRoute('/_admin/workers/')({
  beforeLoad: requirePermission('workers.view'),
  validateSearch: parseWorkersSearch,
  loaderDeps: ({ search }) => search,
  loader: ({ context, deps }) => context.queryClient.ensureQueryData(workersQuery(deps)),
  component: WorkersRoute,
});

function WorkersRoute() {
  const search = Route.useSearch();
  return <WorkersPage search={search} />;
}
