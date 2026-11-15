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

/** Разброс перечитывания после переподключения, мс. */
export const RECONNECT_JITTER_MS = 3000;

/** Держать соединение открытым, пока смонтирован компонент (AdminShell). */
export function useRealtime(): void {
  const client = useRealtimeClient();
  const queryClient = useQueryClient();
  useEffect(() => {
    client.start();
    let timer: ReturnType<typeof setTimeout> | null = null;
    const off = client.onReconnect((reauth) => {
      // После 4001 (staff.access_changed: роль/права изменились) — свежие права для меню и гардов (Me) сразу.
      if (reauth) void session.reloadMe().catch(() => undefined);
      // Активные запросы — со случайной задержкой: после перезапуска инстанса все панели переподключаются разом.
      if (timer) clearTimeout(timer);
      timer = setTimeout(() => {
        timer = null;
        void queryClient.invalidateQueries({ type: 'active' });
      }, Math.round(Math.random() * RECONNECT_JITTER_MS));
    });
    return () => {
      off();
      if (timer) clearTimeout(timer);
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

/** Окно склейки событий для useInvalidateOn, мс. */
export const INVALIDATE_WINDOW_MS = 1500;

/**
 * Перечитать запросы с префиксом queryKey при событиях patterns. События склеиваются: первое перечитывает сразу,
 * остальные за окно INVALIDATE_WINDOW_MS — одним запросом в конце окна (пачка событий ≠ пачка запросов).
 * except — точные типы, которые шаблон ловит, но экран не слушает (телеметрия).
 * Идущий запрос отменяется (signal в queryFn) и заменяется новым: ответ, начатый до события, мог устареть.
 */
export function useInvalidateOn(patterns: readonly string[], queryKey: QueryKey, except: readonly string[] = []): void {
  const client = useRealtimeClient();
  const queryClient = useQueryClient();
  const key = JSON.stringify(queryKey);
  const pkey = patterns.join('|');
  const ekey = except.join('|');
  useEffect(() => {
    const skip = ekey ? ekey.split('|') : [];
    const qk = JSON.parse(key) as QueryKey;
    let timer: ReturnType<typeof setTimeout> | null = null;
    let dirty = false;
    const run = () => void queryClient.invalidateQueries({ queryKey: qk });
    const onEvent = (m: RealtimeMessage) => {
      if (skip.includes(m.type)) return;
      if (timer) {
        dirty = true;
        return;
      }
      run();
      const tick = () => {
        timer = null;
        if (!dirty) return;
        dirty = false;
        run();
        timer = setTimeout(tick, INVALIDATE_WINDOW_MS);
      };
      timer = setTimeout(tick, INVALIDATE_WINDOW_MS);
    };
    const offs = pkey.split('|').map((p) => client.on(p, onEvent));
    return () => {
      offs.forEach((off) => off());
      if (timer) clearTimeout(timer);
    };
  }, [client, queryClient, key, pkey, ekey]);
}

/**
 * Телеметрия ПК (docs/realtime.md §7: до одного события в 15 с на ПК). Данные списка ПК и дашборда она не
 * меняет — эти экраны слушают `computer.*`, кроме неё.
 */
export const COMPUTER_TELEMETRY = ['computer.load'] as const;
