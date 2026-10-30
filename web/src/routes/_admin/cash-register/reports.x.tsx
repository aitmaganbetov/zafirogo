import { createFileRoute } from '@tanstack/react-router';
import { requirePermission } from '../../../auth/guards';
import { xReportQuery } from '../../../features/cash/api';
import { XReportPage } from '../../../features/cash/ReportPage';

// GET /cash-register/reports/x (routes/web.php:280): cash.view; без открытой смены — 404.
export const Route = createFileRoute('/_admin/cash-register/reports/x')({
  beforeLoad: requirePermission('cash.view'),
  loader: ({ context }) => context.queryClient.ensureQueryData(xReportQuery()),
  component: XReportPage,
});
