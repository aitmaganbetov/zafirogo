import { createFileRoute } from '@tanstack/react-router';
import { requirePermission } from '../../../auth/guards';
import { customersQuery, parseCustomersSearch } from '../../../features/customers/api';
import { UsersPage } from '../../../features/customers/UsersPage';

// GET /users (routes/web.php:290, users.index): club-area customers.view.
export const Route = createFileRoute('/_admin/users/')({
  beforeLoad: requirePermission('customers.view'),
  validateSearch: parseCustomersSearch,
  loaderDeps: ({ search }) => search,
  loader: ({ context, deps }) => context.queryClient.ensureQueryData(customersQuery(deps)),
  component: UsersRoute,
});

function UsersRoute() {
  const search = Route.useSearch();
  return <UsersPage search={search} />;
}
