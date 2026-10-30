import { createFileRoute } from '@tanstack/react-router';
import { languagesQuery } from '../../../features/superadmin/api';
import { LanguagesPage } from '../../../features/superadmin/LanguagesPage';

// GET /languages — superadmin.languages (routes/web.php:83).
export const Route = createFileRoute('/superadmin/languages/')({
  loader: ({ context }) => context.queryClient.ensureQueryData(languagesQuery()),
  component: LanguagesPage,
});
