import { useQueryClient } from '@tanstack/react-query';
import { useEffect, useRef } from 'react';
import { useRealtimeEvent } from '../../realtime/hooks';
import { clubMapKey, computerHealthKey, fetchStates, type ClubMapPage, type ComputerState } from './api';
import { pushSample, sampleFromEvent } from './liveSeries';

/*
 * Живая карта: вместо опроса GET /club-map/state раз в 15 с (club-map/index.blade.php ~стр. 46–52) — события
 * computer.state по WebSocket (docs/realtime.md). Событие несёт дифф одного ПК; состояние, которое считает
 * сервер (статус с бронью, подсветка, что куплено), досылается одним запросом /club-map/state?ids=… на пачку
 * событий (300 мс). Событие без computer_id (удалена зона) или о неизвестном ПК — перечитать экран.
 */

interface StateEvent {
  readonly computer_id?: number;
  readonly connection?: 'online' | 'offline';
  readonly agent_state?: string;
  readonly agent_version?: string;
}

/** computer.load (Этап 7): загрузка, проблемы и температуры ПК — готовые данные, без запроса (docs/realtime.md). */
export interface LoadEvent {
  readonly computer_id?: number;
  readonly load?: ComputerState['load'];
  readonly problems?: ComputerState['problems'];
  readonly rtt_ms?: number;
  readonly cpu_temp_deci?: number;
  readonly gpu_temp_deci?: number;
}

/** Подставить загрузку и проблемы из computer.load в данные экрана. */
export function mergeLoad(page: ClubMapPage, ev: LoadEvent): ClubMapPage {
  if (!ev.computer_id) return page;
  return {
    ...page,
    states: page.states.map((s) => (s.id === ev.computer_id ? { ...s, load: ev.load ?? null, problems: ev.problems ?? [], rtt_ms: ev.rtt_ms ?? s.rtt_ms } : s)),
  };
}

/** Подставить свежие состояния ПК в данные экрана. */
export function mergeStates(page: ClubMapPage, fresh: readonly ComputerState[]): ClubMapPage {
  if (fresh.length === 0) return page;
  const byId = new Map(fresh.map((s) => [s.id, s]));
  return { ...page, states: page.states.map((s) => byId.get(s.id) ?? s) };
}

export function useMapLive(): void {
  const qc = useQueryClient();
  const pending = useRef(new Set<number>());
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);

  useEffect(
    () => () => {
      if (timer.current) clearTimeout(timer.current);
    },
    [],
  );

  // Загрузка и здоровье — сразу из события (и в живой график панели ПК); открытая панель перечитывает подробности
  // (температуры, диски, периферия).
  useRealtimeEvent<LoadEvent>('computer.load', (m) => {
    const id = m.data?.computer_id;
    if (!id) return;
    pushSample(id, sampleFromEvent(m.data, Date.now()));
    qc.setQueryData<ClubMapPage>(clubMapKey, (old) => (old ? mergeLoad(old, m.data) : old));
    void qc.invalidateQueries({ queryKey: computerHealthKey(id), exact: true });
  });

  useRealtimeEvent<StateEvent>('computer.state', (m) => {
    const id = m.data?.computer_id;
    const page = qc.getQueryData<ClubMapPage>(clubMapKey);
    if (!page) return;
    if (!id || !page.computers.some((c) => c.id === id)) {
      void qc.invalidateQueries({ queryKey: clubMapKey });
      return;
    }
    // Связь с агентом — сразу (готовое изменение, Q-062), остальное — с сервера.
    if (m.data.connection) {
      const conn = m.data.connection;
      qc.setQueryData<ClubMapPage>(clubMapKey, (old) =>
        old ? { ...old, states: old.states.map((s) => (s.id === id ? { ...s, connection: conn, agent_state: m.data.agent_state ?? s.agent_state } : s)) } : old,
      );
    }
    pending.current.add(id);
    if (timer.current) return;
    timer.current = setTimeout(() => {
      timer.current = null;
      const ids = [...pending.current];
      pending.current.clear();
      void fetchStates(ids)
        .then((r) => qc.setQueryData<ClubMapPage>(clubMapKey, (old) => (old ? mergeStates(old, r.computers) : old)))
        .catch(() => void qc.invalidateQueries({ queryKey: clubMapKey }));
    }, 300);
  });
}
