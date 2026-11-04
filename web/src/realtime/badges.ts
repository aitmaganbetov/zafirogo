import { useQuery, useQueryClient } from '@tanstack/react-query';
import { createApiClient, unwrap } from '../api/http';
import type { paths } from '../api/gen/layout';
import type { SidebarBadges } from '../layouts/types';
import { useRealtimeEvent } from './hooks';

/*
 * Живые бейджи меню «Чат» и «Заказы» (решение Q-063): начальные значения — GET /api/layout/badges
 * (ChatController::unreadTotal(), ShopOrder::newCount()), дальше — события chat.unread {unread} и
 * shop_order.created|updated {new_count} (docs/realtime.md).
 */

const layoutApi = createApiClient<paths>();
export const BADGES_KEY = ['layout', 'badges'] as const;

export function useSidebarBadges(): SidebarBadges {
  const queryClient = useQueryClient();
  const { data } = useQuery({
    queryKey: BADGES_KEY,
    queryFn: () => unwrap(layoutApi.GET('/api/layout/badges')),
    staleTime: Infinity,
  });
  useRealtimeEvent<{ unread?: number }>('chat.unread', (m) => {
    if (typeof m.data.unread === 'number') {
      queryClient.setQueryData(BADGES_KEY, (old: SidebarBadges | undefined) => ({ chat: m.data.unread ?? 0, orders: old?.orders ?? 0 }));
    }
  });
  useRealtimeEvent<{ new_count?: number }>('shop_order.*', (m) => {
    if (typeof m.data.new_count === 'number') {
      queryClient.setQueryData(BADGES_KEY, (old: SidebarBadges | undefined) => ({ chat: old?.chat ?? 0, orders: m.data.new_count ?? 0 }));
    }
  });
  return { chat: data?.chat ?? 0, orders: data?.orders ?? 0 };
}
