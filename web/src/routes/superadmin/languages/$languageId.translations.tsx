import { createFileRoute, notFound } from '@tanstack/react-router';
import { intParam, parseTranslationsSearch, translationsQuery } from '../../../features/superadmin/api';
import { TranslationsPage } from '../../../features/superadmin/TranslationsPage';

// GET /languages/{language}/translations — languages.translations (routes/web.php:104).
export const Route = createFileRoute('/superadmin/languages/$languageId/translations')({
  validateSearch: parseTranslationsSearch,
  loaderDeps: ({ search }) => search,
  loader: ({ context, params, deps }) => {
    const id = intParam(params.languageId);
    if (id === undefined) throw notFound();
    return context.queryClient.ensureQueryData(translationsQuery(id, deps));
  },
  component: TranslationsRoute,
});

function TranslationsRoute() {
  const { languageId } = Route.useParams();
  const search = Route.useSearch();
  return <TranslationsPage key={JSON.stringify(search)} languageId={Number(languageId)} search={search} />;
}
