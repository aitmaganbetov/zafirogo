import { createFileRoute } from '@tanstack/react-router';
import { plansQuery } from '../../features/superadmin/api';
import { PlansPage } from '../../features/superadmin/PlansPage';

// GET /plans — superadmin.plans.index (routes/web.php:78).
export const Route = createFileRoute('/superadmin/plans')({
  loader: ({ context }) => context.queryClient.ensureQueryData(plansQuery()),
  component: PlansPage,
});
