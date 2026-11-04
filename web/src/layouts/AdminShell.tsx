import { useQueryClient } from '@tanstack/react-query';
import { useLocation, useNavigate } from '@tanstack/react-router';
import { useMemo, type ReactNode } from 'react';
import { session, useSession } from '../auth/session';
import { useRealtime } from '../realtime/hooks';
import { useSidebarBadges } from '../realtime/badges';
import { useNotificationsSource } from '../features/notifications/source';
import { saHref } from '../features/superadmin/host';
import { AdminLayout } from './AdminLayout';
import { AdminSessionContext } from './adminSession';
import { adminSessionFrom } from './adminSessionFrom';
import type { AdminSession } from './types';

/*
 * AdminLayout на настоящей сессии: Me (/api/auth/me) → AdminSession (как View::share в SetCurrentClub),
 * бейджи и уведомления — WebSocket (realtime/), смена клуба / выход / выход из просмотра — API.
 */

export function AdminShell({ children }: { children: ReactNode }) {
  const { me } = useSession();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { pathname } = useLocation();
  useRealtime();
  const badges = useSidebarBadges();
  const notifications = useNotificationsSource();

  const value = useMemo<AdminSession | null>(() => {
    if (!me) return null;
    return {
      ...adminSessionFrom(me, pathname),
      badges,
      notifications,
      onSwitchClub: (clubId) => {
        void session.selectClub(clubId).then(() => {
          queryClient.clear();
          void navigate({ to: '/dashboard' });
        });
      },
      onLogout: () => {
        void session.logout().then(() => {
          queryClient.clear();
          void navigate({ to: '/' });
        });
      },
      onLeaveViewedClub: () => {
        // ClubController::leave(): обратно в карточку клуба в кабинете.
        const viewed = me.club?.id;
        void session.leaveViewedClub().then(() => {
          queryClient.clear();
          void navigate({ href: saHref(viewed ? `/clubs/${viewed}` : '/clubs') });
        });
      },
    };
  }, [me, pathname, badges, notifications, navigate, queryClient]);

  if (!value) return null;
  return (
    <AdminSessionContext.Provider value={value}>
      <AdminLayout>{children}</AdminLayout>
    </AdminSessionContext.Provider>
  );
}
