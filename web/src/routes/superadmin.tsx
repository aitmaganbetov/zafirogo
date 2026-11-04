import { useQueryClient } from '@tanstack/react-query';
import { Outlet, createFileRoute, useLocation, useNavigate } from '@tanstack/react-router';
import { requireSuperadmin } from '../auth/guards';
import { session, useSession } from '../auth/session';
import { SuperadminLayout } from '../features/superadmin/SuperadminLayout';

/*
 * Кабинет суперадмина (layouts/superadmin.blade.php): middleware `auth` + `superadmin`. Без SUPERADMIN_DOMAIN —
 * /superadmin/…, на хосте кабинета — с корня (rewrite роутера, features/superadmin/host.ts; Q-069).
 */
export const Route = createFileRoute('/superadmin')({
  beforeLoad: requireSuperadmin,
  component: SuperadminRoute,
});

function SuperadminRoute() {
  const { me } = useSession();
  const { pathname } = useLocation();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  if (!me) return null;
  return (
    <SuperadminLayout
      userName={me.user.name}
      hasClub={me.clubs.length > 0}
      path={pathname}
      onLogout={() => {
        void session.logout().then(() => {
          queryClient.clear();
          void navigate({ to: '/login' });
        });
      }}
    >
      <Outlet />
    </SuperadminLayout>
  );
}
