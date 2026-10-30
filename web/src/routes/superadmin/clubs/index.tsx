import { createFileRoute } from '@tanstack/react-router';
import { clubsQuery, parseClubsSearch } from '../../../features/superadmin/api';
import { ClubsPage } from '../../../features/superadmin/ClubsPage';

// GET /clubs — superadmin.clubs.index (routes/web.php:64).
export const Route = createFileRoute('/superadmin/clubs/')({
  validateSearch: parseClubsSearch,
  loaderDeps: ({ search }) => search,
  loader: ({ context, deps }) => context.queryClient.ensureQueryData(clubsQuery(deps)),
  component: ClubsRoute,
});

function ClubsRoute() {
  const search = Route.useSearch();
  return <ClubsPage key={JSON.stringify(search)} search={search} />;
}
