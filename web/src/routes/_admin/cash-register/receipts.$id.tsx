import { createFileRoute, notFound } from '@tanstack/react-router';
import { requirePermission } from '../../../auth/guards';
import { receiptQuery } from '../../../features/cash/api';
import { ReceiptPage } from '../../../features/cash/ReceiptPage';

// GET /cash-register/receipts/{cashTransaction} (routes/web.php:277): cash.view; не приход — 404.
export const Route = createFileRoute('/_admin/cash-register/receipts/$id')({
  beforeLoad: requirePermission('cash.view'),
  loader: ({ context, params }) => {
    const id = Number(params.id);
    if (!Number.isSafeInteger(id) || id < 1) throw notFound();
    return context.queryClient.ensureQueryData(receiptQuery(id));
  },
  component: function Receipt() {
    const { id } = Route.useParams();
    return <ReceiptPage id={Number(id)} />;
  },
});
