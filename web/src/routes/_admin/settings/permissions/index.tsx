import { createFileRoute } from '@tanstack/react-router';
import { requireRole } from '../../../../auth/guards';
import { permissionStaffQuery } from '../../../../features/staff/api';
import { PermissionsPage } from '../../../../features/staff/PermissionsPage';

// GET /settings/permissions (routes/web.php:240, settings.permissions): club-role:owner,admin.
export const Route = createFileRoute('/_admin/settings/permissions/')({
  beforeLoad: requireRole('admin'),
  loader: ({ context }) => context.queryClient.ensureQueryData(permissionStaffQuery()),
  component: PermissionsPage,
});
