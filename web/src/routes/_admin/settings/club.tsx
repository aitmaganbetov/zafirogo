import { createFileRoute } from '@tanstack/react-router';
import { requirePermission } from '../../../auth/guards';
import { clubSettingsQuery } from '../../../features/settings/api';
import { ClubSettingsPage } from '../../../features/settings/ClubSettingsPage';

// GET /settings/club (settings.club, settings.view).
export const Route = createFileRoute('/_admin/settings/club')({
  beforeLoad: requirePermission('settings.view'),
  loader: ({ context }) => context.queryClient.ensureQueryData(clubSettingsQuery()),
  component: ClubSettingsPage,
});
