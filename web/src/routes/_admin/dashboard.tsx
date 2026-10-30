import { createFileRoute } from '@tanstack/react-router';
import { useTranslation } from 'react-i18next';
import { requireStaff } from '../../auth/guards';
import { useAdminPageTitle } from '../../layouts/adminSession';
import { useFlash } from '../../lib/flash';

// GET /dashboard (routes/web.php:177): club-role персонала. Сам экран переносит группа 1 (дашборд) — заглушка.
export const Route = createFileRoute('/_admin/dashboard')({
  beforeLoad: requireStaff,
  component: DashboardPlaceholder,
});

function DashboardPlaceholder() {
  const { t } = useTranslation();
  const status = useFlash('status');
  const success = useFlash('success');
  useAdminPageTitle(t('Админпанель'));
  return (
    <div className="rounded-xl border border-gray-200 bg-white p-6 dark:border-gray-800 dark:bg-gray-900" data-testid="dashboard">
      {(status ?? success) && <p className="mb-2 text-sm text-green-700">{t(status ?? success ?? '')}</p>}
      <h1 className="text-xl font-semibold text-gray-900 dark:text-gray-100">Zafiro</h1>
    </div>
  );
}
