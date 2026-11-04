import { createFileRoute } from '@tanstack/react-router';
import { requirePermission } from '../../../auth/guards';
import { shiftQuery } from '../../../features/cash/api';
import { CashRegisterPage } from '../../../features/cash/CashRegisterPage';

// GET /cash-register (routes/web.php:270): club-area → cash.view.
export const Route = createFileRoute('/_admin/cash-register/')({
  beforeLoad: requirePermission('cash.view'),
  loader: ({ context }) => context.queryClient.ensureQueryData(shiftQuery()),
  component: CashRegisterPage,
});
