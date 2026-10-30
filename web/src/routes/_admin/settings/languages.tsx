import { createFileRoute } from '@tanstack/react-router';
import { requirePermission } from '../../../auth/guards';
import { languagesQuery } from '../../../features/settings/api';
import { LanguagesPage } from '../../../features/settings/LanguagesPage';

// GET /settings/languages (settings.languages, settings.view).
export const Route = createFileRoute('/_admin/settings/languages')({
  beforeLoad: requirePermission('settings.view'),
  loader: ({ context }) => context.queryClient.ensureQueryData(languagesQuery()),
  component: LanguagesPage,
});
