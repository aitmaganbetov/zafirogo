import { createFileRoute } from '@tanstack/react-router';
import { requirePermission } from '../../auth/guards';
import { billingQuery } from '../../features/billing/api';
import { BillingPage } from '../../features/billing/BillingPage';

// GET /billing billing.show (routes/web.php:173): club-area → billing.view, без middleware subscription
// (сюда ведёт неактивная подписка — 402 разделов панели).
export const Route = createFileRoute('/_admin/billing')({
  beforeLoad: requirePermission('billing.view'),
  loader: ({ context }) => context.queryClient.ensureQueryData(billingQuery()),
  component: BillingPage,
});
