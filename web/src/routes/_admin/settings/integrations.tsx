import { createFileRoute } from '@tanstack/react-router';
import { requireClub } from '../../../auth/guards';
import { ForbiddenError } from '../../../pages/errors';
import { integrationsQuery } from '../../../features/settings/api';
import { IntegrationsPage } from '../../../features/settings/IntegrationsPage';

// GET /settings/integrations (routes/web.php:247): subscription, can:manage-integrations — владелец или сотрудник
// с доступом к интеграциям (me.club.can_manage_integrations; проверяет и сервер).
export const Route = createFileRoute('/_admin/settings/integrations')({
  beforeLoad: async (args) => {
    const me = await requireClub(args);
    if (!me.club?.can_manage_integrations) throw new ForbiddenError();
  },
  loader: ({ context }) => context.queryClient.ensureQueryData(integrationsQuery()),
  component: IntegrationsPage,
});
