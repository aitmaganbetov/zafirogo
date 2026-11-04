import { createFileRoute } from '@tanstack/react-router';
import { requirePermission } from '../../../auth/guards';
import { newsQuery, parseNewsSearch } from '../../../features/news/api';
import { NewsPage } from '../../../features/news/NewsPage';

// GET /news (routes/web.php:223, news.index): club-area news.view.
export const Route = createFileRoute('/_admin/news/')({
  beforeLoad: requirePermission('news.view'),
  validateSearch: parseNewsSearch,
  loaderDeps: ({ search }) => search,
  loader: ({ context, deps }) => context.queryClient.ensureQueryData(newsQuery(deps.page)),
  component: NewsRoute,
});

function NewsRoute() {
  const { page } = Route.useSearch();
  return <NewsPage page={page} />;
}
