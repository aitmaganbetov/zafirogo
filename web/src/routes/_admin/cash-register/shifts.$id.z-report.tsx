import { createFileRoute, notFound } from '@tanstack/react-router';
import { requirePermission } from '../../../auth/guards';
import { zReportQuery } from '../../../features/cash/api';
import { ZReportPage } from '../../../features/cash/ReportPage';

// GET /cash-register/shifts/{cashShift}/z-report (routes/web.php:282): cash.view.
export const Route = createFileRoute('/_admin/cash-register/shifts/$id/z-report')({
  beforeLoad: requirePermission('cash.view'),
  loader: ({ context, params }) => {
    const id = Number(params.id);
    if (!Number.isSafeInteger(id) || id < 1) throw notFound();
    return context.queryClient.ensureQueryData(zReportQuery(id));
  },
  component: function ZReport() {
    const { id } = Route.useParams();
    return <ZReportPage shiftId={Number(id)} />;
  },
});
