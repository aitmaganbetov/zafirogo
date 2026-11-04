import { createFileRoute, notFound } from '@tanstack/react-router';
import { requireRole } from '../../../../auth/guards';
import { staffPermissionsQuery } from '../../../../features/staff/api';
import { PermissionsEditPage } from '../../../../features/staff/PermissionsEditPage';

// GET /settings/permissions/{membership} (routes/web.php:241, settings.permissions.edit): club-role:owner,admin.
export const Route = createFileRoute('/_admin/settings/permissions/$membership')({
  beforeLoad: requireRole('admin'),
  params: {
    parse: (p) => {
      if (!/^\d+$/.test(p.membership)) throw notFound();
      return { membership: p.membership };
    },
  },
  loader: ({ context, params }) => context.queryClient.ensureQueryData(staffPermissionsQuery(Number(params.membership))),
  component: PermissionsEditRoute,
});

function PermissionsEditRoute() {
  const { membership } = Route.useParams();
  return <PermissionsEditPage membershipId={Number(membership)} />;
}
