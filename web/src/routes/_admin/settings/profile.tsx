import { createFileRoute } from '@tanstack/react-router';
import { requirePermission } from '../../../auth/guards';
import { clubProfileQuery } from '../../../features/settings/api';
import { ProfilePage } from '../../../features/settings/ProfilePage';

// GET /settings/profile (settings.profile, settings.view).
export const Route = createFileRoute('/_admin/settings/profile')({
  beforeLoad: requirePermission('settings.view'),
  loader: ({ context }) => context.queryClient.ensureQueryData(clubProfileQuery()),
  component: ProfilePage,
});
