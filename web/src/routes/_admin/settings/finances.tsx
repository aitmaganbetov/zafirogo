import { createFileRoute } from '@tanstack/react-router';
import { requirePermission } from '../../../auth/guards';
import { clubSettingsQuery } from '../../../features/settings/api';
import { FinancesPage } from '../../../features/settings/FinancesPage';

// GET /settings/finances (settings.finances, settings.view).
export const Route = createFileRoute('/_admin/settings/finances')({
  beforeLoad: requirePermission('settings.view'),
  loader: ({ context }) => context.queryClient.ensureQueryData(clubSettingsQuery()),
  component: FinancesPage,
});
