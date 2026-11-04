import { useQueryClient, type QueryKey } from '@tanstack/react-query';
import { useEffect, useRef } from 'react';
import { session } from '../auth/session';
import type { RealtimeMessage } from './client';
import { useRealtimeClient } from './context';

/*
 * Подписки экранов на события WebSocket (docs/realtime.md):
 *   useRealtimeEvent('computer.state', (m) => queryClient.setQueryData(...))   — готовые изменения (Q-062);
 *   useInvalidateOn(['todo.*'], ['todos'])                                     — перечитать запрос.
 * Соединение держит AdminShell (useRealtime); после переподключения активные запросы перечитываются
 * (события за время разрыва не досылаются).
 */

/** Держать соединение открытым, пока смонтирован компонент (AdminShell). */
export function useRealtime(): void {
  const client = useRealtimeClient();
  const queryClient = useQueryClient();
  useEffect(() => {
    client.start();
    const off = client.onReconnect(() => {
      // После 4001 (staff.access_changed: роль/права изменились) — свежие права для меню и гардов (Me).
      void session.reloadMe().catch(() => undefined);
      void queryClient.invalidateQueries({ type: 'active' });
    });
    return () => {
      off();
      client.stop();
    };
  }, [client, queryClient]);
}

/** Подписка на события типа (шаблон 'chat.*' — весь раздел). handler может меняться между рендерами. */
export function useRealtimeEvent<D = unknown>(pattern: string, handler: (msg: RealtimeMessage<D>) => void): void {
  const client = useRealtimeClient();
  const ref = useRef(handler);
  useEffect(() => {
    ref.current = handler;
  });
  useEffect(() => client.on(pattern, (m) => ref.current(m as RealtimeMessage<D>)), [client, pattern]);
}

/** Перечитать запросы с префиксом queryKey при событиях patterns. */
export function useInvalidateOn(patterns: readonly string[], queryKey: QueryKey): void {
  const client = useRealtimeClient();
  const queryClient = useQueryClient();
  const key = JSON.stringify(queryKey);
  const pkey = patterns.join('|');
  useEffect(() => {
    const qk = JSON.parse(key) as QueryKey;
    const offs = pkey.split('|').map((p) => client.on(p, () => void queryClient.invalidateQueries({ queryKey: qk })));
    return () => offs.forEach((off) => off());
  }, [client, queryClient, key, pkey]);
}
