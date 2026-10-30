import { createFileRoute } from '@tanstack/react-router';
import { requirePermission } from '../../../auth/guards';
import { computersQuery, parseComputersSearch } from '../../../features/computers/api';
import { ComputersPage } from '../../../features/computers/ComputersPage';

// GET /club-computers (routes/web.php:305, club-computers.index): club-area club_map.view.
export const Route = createFileRoute('/_admin/club-computers/')({
  beforeLoad: requirePermission('club_map.view'),
  validateSearch: parseComputersSearch,
  loaderDeps: ({ search }) => search,
  loader: ({ context, deps }) => context.queryClient.ensureQueryData(computersQuery(deps.page)),
  component: ComputersRoute,
});

function ComputersRoute() {
  const { page } = Route.useSearch();
  return <ComputersPage page={page} />;
}
