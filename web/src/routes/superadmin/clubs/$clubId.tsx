import { createFileRoute, notFound } from '@tanstack/react-router';
import { clubQuery, intParam } from '../../../features/superadmin/api';
import { ClubPage } from '../../../features/superadmin/ClubPage';

// GET /clubs/{club} — superadmin.clubs.show (routes/web.php:65).
export const Route = createFileRoute('/superadmin/clubs/$clubId')({
  validateSearch: (s: Record<string, unknown>) => ({ page: intParam(s.page, 2) }),
  loaderDeps: ({ search }) => search,
  loader: ({ context, params, deps }) => {
    const id = intParam(params.clubId);
    if (id === undefined) throw notFound();
    return context.queryClient.ensureQueryData(clubQuery(id, deps.page));
  },
  component: ClubRoute,
});

function ClubRoute() {
  const { clubId } = Route.useParams();
  const { page } = Route.useSearch();
  return <ClubPage clubId={Number(clubId)} page={page} />;
}
