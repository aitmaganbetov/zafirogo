import { createFileRoute } from '@tanstack/react-router';
import { requirePermission } from '../../../auth/guards';
import { activityLogsQuery, parseActivityLogsSearch } from '../../../features/activity-logs/api';
import { ActivityLogsPage } from '../../../features/activity-logs/ActivityLogsPage';

// GET /activity-logs (routes/web.php:195, activity-logs.index): club-area activity_logs.view.
export const Route = createFileRoute('/_admin/activity-logs/')({
  beforeLoad: requirePermission('activity_logs.view'),
  validateSearch: parseActivityLogsSearch,
  loaderDeps: ({ search }) => search,
  loader: ({ context, deps }) => context.queryClient.ensureQueryData(activityLogsQuery(deps)),
  component: ActivityLogsRoute,
});

function ActivityLogsRoute() {
  const search = Route.useSearch();
  return <ActivityLogsPage key={JSON.stringify(search)} search={search} />;
}
