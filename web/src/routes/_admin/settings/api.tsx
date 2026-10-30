import { createFileRoute, redirect } from '@tanstack/react-router';
import { requirePermission } from '../../../auth/guards';

// GET /settings/api — справочник URL настроек для мобильного приложения не переносится (решение Q-059); пункт меню
// «API» оставлен (вид меню как в старом) и ведёт к «API и интеграции» или к настройкам клуба
// (docs/open-questions/stage3-settings.md S-4).
export const Route = createFileRoute('/_admin/settings/api')({
  beforeLoad: async (args) => {
    await requirePermission('settings.view')(args);
    const me = await args.context.session.ensureLoaded();
    throw redirect({ to: me.me?.club?.can_manage_integrations ? '/settings/integrations' : '/settings/club' });
  },
});
