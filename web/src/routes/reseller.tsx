import { useQueryClient } from '@tanstack/react-query';
import { Outlet, createFileRoute, useLocation, useNavigate } from '@tanstack/react-router';
import { requireAuth } from '../auth/guards';
import { session, useSession } from '../auth/session';
import { ResellerLayout } from '../features/reseller/ResellerLayout';

/*
 * Кабинет реселлера (layouts/reseller.blade.php): middleware `auth`; не реселлеру сервер отвечает 403 «Этот аккаунт
 * не реселлер Zafiro.» (страница ошибки). Без RESELLER_DOMAIN — /reseller, на хосте реселлера — с корня (rewrite
 * роутера, features/reseller/host.ts; UQ-21).
 */
export const Route = createFileRoute('/reseller')({
  beforeLoad: requireAuth,
  component: ResellerRoute,
});

function ResellerRoute() {
  const { me } = useSession();
  const { pathname } = useLocation();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  if (!me) return null;
  return (
    <ResellerLayout
      userName={me.user.name}
      path={pathname}
      onLogout={() => {
        void session.logout().then(() => {
          queryClient.clear();
          void navigate({ to: '/login' });
        });
      }}
    >
      <Outlet />
    </ResellerLayout>
  );
}
