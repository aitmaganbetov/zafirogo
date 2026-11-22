import { createContext, useContext } from 'react';
import { RealtimeClient } from './client';

/** Один клиент WebSocket на вкладку (тесты подставляют свой через RealtimeContext.Provider). */
const realtime = new RealtimeClient();

export const RealtimeContext = createContext<RealtimeClient>(realtime);

export function useRealtimeClient(): RealtimeClient {
  return useContext(RealtimeContext);
}
