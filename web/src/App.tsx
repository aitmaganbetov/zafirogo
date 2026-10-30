import { useTranslation } from 'react-i18next';
import { AdminLayout } from './layouts/AdminLayout';
import { AdminSessionContext, STUB_ADMIN_SESSION, useAdminPageTitle } from './layouts/adminSession';

// Этап 0: каркас панели клуба с заглушкой сессии и контента. Роутинг (TanStack Router) — Этап 1.
function Placeholder() {
  const { t } = useTranslation();
  useAdminPageTitle(t('Панель управления'));
  return (
    <div className="rounded-xl border border-gray-200 bg-white p-6 dark:border-gray-800 dark:bg-gray-900">
      <h1 className="text-xl font-semibold text-gray-900 dark:text-gray-100">Zafiro</h1>
      <p className="mt-2 text-sm text-gray-500 dark:text-gray-400">{t('Сервер работает')}</p>
    </div>
  );
}

export function App() {
  return (
    <AdminSessionContext.Provider value={STUB_ADMIN_SESSION}>
      <AdminLayout>
        <Placeholder />
      </AdminLayout>
    </AdminSessionContext.Provider>
  );
}
