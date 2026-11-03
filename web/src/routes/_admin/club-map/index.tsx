import { createFileRoute } from '@tanstack/react-router';
import { requirePermission } from '../../../auth/guards';
import { clubMapQuery } from '../../../features/clubmap/api';
import { ClubMapPage } from '../../../features/clubmap/ClubMapPage';
import { computerParam } from '../../../features/clubmap/logic';

// GET /club-map (routes/web.php:197, club-map.index): club-area club_map.view. ?computer=7 — карточка ПК (колокольчик).
export const Route = createFileRoute('/_admin/club-map/')({
  beforeLoad: requirePermission('club_map.view'),
  validateSearch: (s: Record<string, unknown>): { computer?: number } => ({ computer: computerParam(s.computer) }),
  loader: ({ context }) => context.queryClient.ensureQueryData(clubMapQuery()),
  component: function ClubMap() {
    const { computer } = Route.useSearch();
    return <ClubMapPage computer={computer} />;
  },
});
