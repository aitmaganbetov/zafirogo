import { Outlet, createFileRoute } from '@tanstack/react-router';
import { requireClub } from '../auth/guards';
import { AdminShell } from '../layouts/AdminShell';

/*
 * Панель клуба (layouts/admin.blade.php): middleware `auth` + `current-club`. Страницы разделов —
 * routes/_admin/<URI старого маршрута>/…, право — в beforeLoad страницы (requirePermission('cash.view')).
 */
export const Route = createFileRoute('/_admin')({
  beforeLoad: requireClub,
  component: AdminRoute,
});

function AdminRoute() {
  return (
    <AdminShell>
      <Outlet />
    </AdminShell>
  );
}
