import type { ReactNode } from 'react';
import type { RealtimeClient } from './client';
import { RealtimeContext, realtime } from './context';

/** Свой клиент WebSocket для поддерева (тесты, сторибуки). По умолчанию — общий `realtime`. */
export function RealtimeProvider({ client = realtime, children }: { client?: RealtimeClient; children: ReactNode }) {
  return <RealtimeContext.Provider value={client}>{children}</RealtimeContext.Provider>;
}
