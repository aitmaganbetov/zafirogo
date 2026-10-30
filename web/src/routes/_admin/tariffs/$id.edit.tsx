import { createFileRoute, notFound } from '@tanstack/react-router';
import { requirePermission } from '../../../auth/guards';
import { tariffsPageQuery } from '../../../features/tariffs/api';
import { TariffsPage } from '../../../features/tariffs/TariffsPage';

// GET /tariffs/{tariff}/edit (routes/web.php:227, tariffs.edit): список с открытой формой изменения (tariffs.view).
export const Route = createFileRoute('/_admin/tariffs/$id/edit')({
  beforeLoad: requirePermission('tariffs.view'),
  loader: async ({ context, params }) => {
    const id = Number(params.id);
    const page = await context.queryClient.ensureQueryData(tariffsPageQuery());
    if (!Number.isSafeInteger(id) || !page.tariffs.some((t) => t.id === id)) throw notFound();
    return id;
  },
  component: TariffEditRoute,
});

function TariffEditRoute() {
  const id = Route.useLoaderData();
  return <TariffsPage key={id} editId={id} />;
}
