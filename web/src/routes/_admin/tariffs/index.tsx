import { createFileRoute } from '@tanstack/react-router';
import { requirePermission } from '../../../auth/guards';
import { parseTariffsSearch, tariffsPageQuery } from '../../../features/tariffs/api';
import { TariffsPage } from '../../../features/tariffs/TariffsPage';

// GET /tariffs (routes/web.php:220, tariffs.index): club-area tariffs.view.
export const Route = createFileRoute('/_admin/tariffs/')({
  beforeLoad: requirePermission('tariffs.view'),
  validateSearch: parseTariffsSearch,
  loaderDeps: ({ search }) => search,
  loader: ({ context, deps }) => context.queryClient.ensureQueryData(tariffsPageQuery(deps.zone)),
  component: TariffsRoute,
});

function TariffsRoute() {
  const { zone } = Route.useSearch();
  return <TariffsPage zone={zone} />;
}
