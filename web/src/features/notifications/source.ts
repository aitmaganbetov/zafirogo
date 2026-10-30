import { useMemo } from 'react';
import { createApiClient, unwrap } from '../../api/http';
import type { paths } from '../../api/gen/notifications';
import type { NotificationsPayload, NotificationsSource } from '../../layouts/types';
import type { RealtimeClient } from '../../realtime/client';
import { useRealtimeClient } from '../../realtime/context';

/*
 * Источник колокольчика (решение F1-11): GET /api/notifications (NotificationController::index()), «просмотрено» —
 * POST /api/notifications/seen, обновление — события WebSocket вместо опроса раз в 60 с. Список зависит от роли и
 * notifications_seen_at сотрудника, поэтому по событию он перечитывается целиком.
 */

const api = createApiClient<paths>();

/** События, после которых список уведомлений может измениться (docs/realtime.md). */
export const NOTIFICATION_EVENTS = ['notifications.*', 'chat.unread', 'shop_order.*', 'stock.*'] as const;

export function notificationsSource(client: RealtimeClient): NotificationsSource {
  const load = (): Promise<NotificationsPayload> => unwrap(api.GET('/api/notifications'));
  return {
    load,
    markSeen: async () => {
      await unwrap(api.POST('/api/notifications/seen'));
    },
    subscribe: (onChange) => {
      let timer: number | undefined;
      // Несколько событий подряд (одна операция пишет несколько) — одно перечитывание.
      const reload = () => {
        window.clearTimeout(timer);
        timer = window.setTimeout(() => {
          load()
            .then(onChange)
            .catch(() => undefined);
        }, 300);
      };
      const offs = NOTIFICATION_EVENTS.map((p) => client.on(p, reload));
      const offReconnect = client.onReconnect(reload);
      return () => {
        window.clearTimeout(timer);
        offs.forEach((off) => off());
        offReconnect();
      };
    },
  };
}

/** Источник уведомлений для AdminShell (одно соединение WebSocket на вкладку). */
export function useNotificationsSource(): NotificationsSource {
  const client = useRealtimeClient();
  return useMemo(() => notificationsSource(client), [client]);
}
