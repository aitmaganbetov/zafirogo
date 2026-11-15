import { createFileRoute } from '@tanstack/react-router';
import { parseResellersSearch, resellersQuery } from '../../features/superadmin/api';
import { ResellersPage } from '../../features/superadmin/ResellersPage';

// GET /resellers — superadmin.resellers.index (routes/web.php:88).
export const Route = createFileRoute('/superadmin/resellers')({
  validateSearch: parseResellersSearch,
  loaderDeps: ({ search }) => search,
  loader: ({ context, deps }) => context.queryClient.ensureQueryData(resellersQuery(deps)),
  component: ResellersRoute,
});

function ResellersRoute() {
  return <ResellersPage search={Route.useSearch()} />;
}
