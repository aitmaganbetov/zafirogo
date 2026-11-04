import { createFileRoute } from '@tanstack/react-router';
import { AccountsPage } from '../../features/superadmin/AccountsPage';
import { accountsQuery, parseAccountsSearch } from '../../features/superadmin/api';

// GET /accounts — superadmin.users.index (routes/web.php:72).
export const Route = createFileRoute('/superadmin/accounts')({
  validateSearch: parseAccountsSearch,
  loaderDeps: ({ search }) => search,
  loader: ({ context, deps }) => context.queryClient.ensureQueryData(accountsQuery(deps)),
  component: AccountsRoute,
});

function AccountsRoute() {
  const search = Route.useSearch();
  return <AccountsPage key={JSON.stringify(search)} search={search} />;
}
