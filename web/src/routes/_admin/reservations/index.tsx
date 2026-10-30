import { createFileRoute } from '@tanstack/react-router';
import { requirePermission } from '../../../auth/guards';
import { parseReservationsSearch, reservationsQuery } from '../../../features/reservations/api';
import { ReservationsPage } from '../../../features/reservations/ReservationsPage';

// GET /reservations (routes/web.php:288, reservations.index): club-area reservations.view.
export const Route = createFileRoute('/_admin/reservations/')({
  beforeLoad: requirePermission('reservations.view'),
  validateSearch: parseReservationsSearch,
  loaderDeps: ({ search }) => search,
  loader: ({ context, deps }) => context.queryClient.ensureQueryData(reservationsQuery(deps)),
  component: ReservationsRoute,
});

function ReservationsRoute() {
  const search = Route.useSearch();
  return <ReservationsPage key={search.date ?? ''} search={search} />;
}
