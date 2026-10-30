import { createFileRoute } from '@tanstack/react-router';
import { requirePermission } from '../../../auth/guards';
import { clubMapQuery } from '../../../features/clubmap/api';
import { ClubMapPage } from '../../../features/clubmap/ClubMapPage';

// GET /club-map (routes/web.php:197, club-map.index): club-area club_map.view.
export const Route = createFileRoute('/_admin/club-map/')({
  beforeLoad: requirePermission('club_map.view'),
  loader: ({ context }) => context.queryClient.ensureQueryData(clubMapQuery()),
  component: ClubMapPage,
});
