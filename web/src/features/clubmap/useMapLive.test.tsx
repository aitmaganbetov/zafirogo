import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { act, renderHook } from '@testing-library/react';
import type { ReactNode } from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { RealtimeClient } from '../../realtime/client';
import { RealtimeContext } from '../../realtime/context';
import { clubMapKey, type ClubMapPage, type ComputerState } from './api';
import { currentStates, useMapLive } from './useMapLive';

/* Ревью 22.11.2026: ответы досылки состояний (/club-map/state?ids=…) применяются по порядку запросов, а не ответов. */

const state = (id: number, status: string) => ({ id, status, light: '', connection: 'online' }) as unknown as ComputerState;
const page = { zones: [], computers: [{ id: 7 }, { id: 8 }], states: [state(7, 'offline'), state(8, 'offline')] } as unknown as ClubMapPage;

describe('useMapLive: порядок ответов досылки', () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  function mount() {
    const answers: { ids: string; reply: (computers: ComputerState[]) => void; fail: () => void }[] = [];
    vi.stubGlobal(
      'fetch',
      vi.fn((input: Request | string) => {
        const url = new URL(input instanceof Request ? input.url : input, location.origin);
        return new Promise<Response>((resolve, reject) => {
          answers.push({
            ids: url.searchParams.get('ids') ?? '',
            reply: (computers) => resolve(new Response(JSON.stringify({ computers }), { status: 200, headers: { 'Content-Type': 'application/json' } })),
            fail: () => reject(new TypeError('Failed to fetch')),
          });
        });
      }),
    );
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
    qc.setQueryData(clubMapKey, page);
    const rt = new RealtimeClient();
    const wrapper = ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={qc}>
        <RealtimeContext.Provider value={rt}>{children}</RealtimeContext.Provider>
      </QueryClientProvider>
    );
    const view = renderHook(() => useMapLive(), { wrapper });
    const event = async (...ids: number[]) => {
      for (const id of ids) act(() => rt.dispatch({ type: 'computer.state', at: '', data: { computer_id: id } }));
      await act(() => vi.advanceTimersByTimeAsync(350));
    };
    const status = (id: number) => qc.getQueryData<ClubMapPage>(clubMapKey)?.states.find((s) => s.id === id)?.status;
    const settle = () => act(() => vi.advanceTimersByTimeAsync(0));
    return { answers, event, status, settle, view };
  }

  it('ответ раннего запроса, пришедший после позднего, состояние ПК не перезаписывает', async () => {
    const { answers, event, status, settle } = mount();
    await event(7, 8); // запрос 1: ПК 7 и 8
    await event(7); // запрос 2: ПК 7
    expect(answers.map((a) => a.ids)).toEqual(['7,8', '7']);
    answers[1]?.reply([state(7, 'busy')]);
    await settle();
    expect(status(7)).toBe('busy');
    // Запоздавший ответ 1: о ПК 7 он старее запроса 2, о ПК 8 — последний.
    answers[0]?.reply([state(7, 'online'), state(8, 'online')]);
    await settle();
    expect(status(7)).toBe('busy');
    expect(status(8)).toBe('online');
  });

  it('ответы по порядку применяются оба; после размонтирования ответ не применяется', async () => {
    const { answers, event, status, settle, view } = mount();
    await event(7);
    await event(7);
    answers[0]?.reply([state(7, 'online')]);
    await settle();
    // Запрос 2 ещё идёт: ответ 1 устарел заранее и не показывается.
    expect(status(7)).toBe('offline');
    answers[1]?.reply([state(7, 'busy')]);
    await settle();
    expect(status(7)).toBe('busy');

    await event(8);
    view.unmount();
    answers[2]?.reply([state(8, 'busy')]);
    await settle();
    expect(status(8)).toBe('offline');
  });

  it('currentStates: ПК без более нового запроса остаётся', () => {
    const fresh = [state(7, 'a'), state(8, 'b'), state(9, 'c')];
    expect(currentStates(fresh, 2, new Map([[7, 3], [8, 2]])).map((s) => s.id)).toEqual([8, 9]);
  });
});
