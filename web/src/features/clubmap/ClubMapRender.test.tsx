import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { act, render } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { RealtimeClient } from '../../realtime/client';
import { RealtimeContext } from '../../realtime/context';
import { clubMapKey, type ClubMapPage as Page, type ComputerState } from './api';
import { ClubMapPage } from './ClubMapPage';
import { LOAD_BATCH_MS, mergeLoads } from './useMapLive';

/* FE-03: событие computer.load об одном ПК перерисовывает только его плитку. */

const renders = new Map<number, number>();
vi.mock('./PcLoad', () => ({
  // PcLoad рисуется внутри плитки ПК без memo: число его рендеров = число рендеров плитки.
  PcLoad: ({ state }: { state: ComputerState | undefined }) => {
    if (state) renders.set(state.id, (renders.get(state.id) ?? 0) + 1);
    return null;
  },
}));

const N = 30;
const state = (id: number): ComputerState =>
  ({ id, zone_id: 1, number: id, name: `PC-${id}`, status: 'online', light: 'pc-free', connection: 'online', is_reserved: false, has_session: false,
    session_guest: false, session_user: '', session_tariff: '', agent_state: 'idle', agent_version: '2.4.1', agent_outdated: false, staff_kind: '' }) as ComputerState;
const page: Page = {
  zones: [{ id: 1, name: 'Зал', description: 'Места', capacity: N, price_per_hour_tiyn: 100000, icon: 'desktop', theme: 'green', status: 'available', is_featured: false, sort_order: 10, position_x: 20, position_y: 20, width: 800, height: 600, rotation: 0, tariff_ids: [] }],
  computers: Array.from({ length: N }, (_, i) => ({ id: i + 1, zone_id: 1, number: i + 1, name: `PC-${i + 1}`, display_name: null, mac_address: null, position_x: (i % 10) * 47, position_y: Math.floor(i / 10) * 42, details: '' })),
  states: Array.from({ length: N }, (_, i) => state(i + 1)),
  tariffs: [],
  guest_mode: true,
  booking_start: '2026-10-15T13:00',
};

describe('карта клуба: рендер на событие computer.load', () => {
  beforeEach(() => {
    vi.useFakeTimers();
    renders.clear();
  });
  afterEach(() => vi.useRealTimers());

  it('перерисовывает одну плитку ПК, а пачку событий применяет одним обновлением', async () => {
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
    qc.setQueryData(clubMapKey, page);
    const rt = new RealtimeClient();
    render(
      <QueryClientProvider client={qc}>
        <RealtimeContext.Provider value={rt}>
          <ClubMapPage />
        </RealtimeContext.Provider>
      </QueryClientProvider>,
    );
    await act(() => vi.advanceTimersByTimeAsync(0));
    renders.clear();
    let updates = 0;
    const off = qc.getQueryCache().subscribe((e) => {
      if (e.type === 'updated' && e.action.type === 'success') updates++;
    });

    // 20 событий об одном ПК за окно склейки + одно о другом.
    for (let i = 0; i < 20; i++) {
      act(() => rt.dispatch({ type: 'computer.load', at: '', data: { computer_id: 7, load: { cpu_bp: 1000 + i, ram_bp: 2000, idle_seconds: 1 }, problems: [] } }));
    }
    act(() => rt.dispatch({ type: 'computer.load', at: '', data: { computer_id: 9, load: { cpu_bp: 500, ram_bp: 500, idle_seconds: 1 }, problems: [] } }));
    await act(() => vi.advanceTimersByTimeAsync(LOAD_BATCH_MS + 10));
    off();

    expect(updates).toBe(1);
    expect([...renders.keys()].sort((a, b) => a - b)).toEqual([7, 9]);
    expect(renders.get(7)).toBe(1);
    expect(qc.getQueryData<Page>(clubMapKey)?.states[6]?.load?.cpu_bp).toBe(1019);
  });

  it('mergeLoads сохраняет ссылки зон, ПК и состояний остальных ПК', () => {
    const next = mergeLoads(page, new Map([[3, { computer_id: 3, load: { cpu_bp: 1, ram_bp: 2, idle_seconds: 3 } }]]));
    expect(next.zones).toBe(page.zones);
    expect(next.computers).toBe(page.computers);
    expect(next.states[0]).toBe(page.states[0]);
    expect(next.states[2]).not.toBe(page.states[2]);
    expect(next.states[2]?.load?.cpu_bp).toBe(1);
    expect(mergeLoads(page, new Map([[999, { computer_id: 999 }]]))).toBe(page);
    expect(mergeLoads(page, new Map())).toBe(page);
  });
});
