import { createFileRoute } from '@tanstack/react-router';
import { logsQuery, parseLogsSearch } from '../../features/superadmin/api';
import { LogsPage } from '../../features/superadmin/LogsPage';

// GET /logs — superadmin.logs.index (routes/web.php:82).
export const Route = createFileRoute('/superadmin/logs')({
  validateSearch: parseLogsSearch,
  loaderDeps: ({ search }) => search,
  loader: ({ context, deps }) => context.queryClient.ensureQueryData(logsQuery(deps)),
  component: LogsRoute,
});

function LogsRoute() {
  const search = Route.useSearch();
  return <LogsPage key={JSON.stringify(search)} search={search} />;
}
