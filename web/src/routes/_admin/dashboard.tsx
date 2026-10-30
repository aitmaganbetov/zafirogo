import { createFileRoute, redirect } from '@tanstack/react-router';
import { requireClub, requireStaff } from '../../auth/guards';
import { dashboardLanding, dashboardQuery } from '../../features/dashboard/api';
import { DashboardPage } from '../../features/dashboard/DashboardPage';

// GET /dashboard (routes/web.php:177): club-role персонала; кассир — на кассу, оператор — на список ПК
// (DashboardController ~стр. 20).
export const Route = createFileRoute('/_admin/dashboard')({
  beforeLoad: async (args) => {
    await requireStaff(args);
    const me = await requireClub(args);
    const landing = dashboardLanding(me.role);
    if (landing) throw redirect({ href: landing });
  },
  loader: ({ context }) => context.queryClient.ensureQueryData(dashboardQuery()),
  component: DashboardPage,
});
