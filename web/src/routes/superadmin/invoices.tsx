import { createFileRoute } from '@tanstack/react-router';
import { intParam, invoicesQuery } from '../../features/superadmin/api';
import { InvoicesPage } from '../../features/superadmin/InvoicesPage';

// GET /invoices — superadmin.invoices.index (routes/web.php:95).
export const Route = createFileRoute('/superadmin/invoices')({
  validateSearch: (s: Record<string, unknown>) => ({ page: intParam(s.page, 2) }),
  loaderDeps: ({ search }) => search,
  loader: ({ context, deps }) => context.queryClient.ensureQueryData(invoicesQuery(deps.page)),
  component: InvoicesRoute,
});

function InvoicesRoute() {
  const { page } = Route.useSearch();
  return <InvoicesPage page={page} />;
}
