import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { act, fireEvent, render, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { RealtimeClient } from '../../realtime/client';
import { RealtimeContext } from '../../realtime/context';
import { clubMapKey, type ClubMapPage as Page, type ComputerState } from './api';
import { ClubMapPage } from './ClubMapPage';
import { durationLabel } from './errors';
import { percent } from './healthText';
import { mergeLoad, mergeStates } from './useMapLive';

const page: Page = {
  zones: [
    { id: 1, name: 'VIP', description: 'Места', capacity: 8, price_per_hour_tiyn: 100000, icon: 'desktop', theme: 'green', status: 'available', is_featured: false, sort_order: 10, position_x: 20, position_y: 20, width: 390, height: 380, rotation: 0, tariff_ids: [] },
    { id: 2, name: 'Лаунж', description: null, capacity: null, price_per_hour_tiyn: null, icon: 'couch', theme: 'purple', status: 'available', is_featured: false, sort_order: 20, position_x: 500, position_y: 20, width: 300, height: 185, rotation: 90, tariff_ids: [] },
  ],
  computers: [
    { id: 7, zone_id: 1, number: 1, name: 'VIP-01', display_name: null, mac_address: null, position_x: 5, position_y: 5, details: 'LAN 10.0.0.1' },
    { id: 8, zone_id: 1, number: 2, name: 'VIP-02', display_name: null, mac_address: null, position_x: 52, position_y: 5, details: '' },
  ],
  states: [
    { id: 7, zone_id: 1, number: 1, name: 'VIP-01', status: 'online', light: 'pc-account', connection: 'online', is_reserved: false, has_session: true, session_guest: false, session_user: 'Айгерим · +77010000001', session_tariff: 'Час', agent_state: 'in_session', agent_version: '2.4.1', agent_outdated: false, staff_kind: '', session_ends_at: '2026-10-15T08:30:00Z' },
    { id: 8, zone_id: 1, number: 2, name: 'VIP-02', status: 'offline', light: '', connection: 'offline', is_reserved: false, has_session: false, session_guest: false, session_user: '', session_tariff: '', agent_state: 'idle', agent_version: '', agent_outdated: false, staff_kind: '' },
  ],
  tariffs: [],
  guest_mode: true,
  booking_start: '2026-10-15T13:00',
};

function setup() {
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
  return { qc, rt };
}

afterEach(() => vi.restoreAllMocks());

describe('ClubMapPage', () => {
  it('renders zones and PCs with status and glow classes', () => {
    setup();
    const pc = document.querySelector('.cm-computer[data-id="7"]');
    expect(pc?.className).toBe('cm-computer online pc-account');
    expect(pc?.getAttribute('title')).toBe('VIP-01 · online · LAN 10.0.0.1');
    expect(document.querySelector('.cm-zone.sofa.rotated-90')).not.toBeNull();
    expect(document.querySelector('.cm-capacity')?.textContent).toContain('8');
    expect(document.getElementById('map-canvas')?.style.height).toBe('430px');
  });

  it('selects a PC and shows its information', () => {
    setup();
    const pc = document.querySelector('.cm-computer[data-id="7"]') as HTMLElement;
    fireEvent.click(pc);
    expect(pc.className).toContain('selected');
    expect(document.getElementById('computer-controls')?.className).toContain('show');
    expect(document.getElementById('selected-count')?.textContent).toBe('1');
    expect(document.getElementById('computer-info-panel')?.hidden).toBe(false);
    expect(document.getElementById('computer-info-name')?.textContent).toBe('VIP-01 (#1)');
    expect(document.getElementById('computer-info-session-user')?.textContent).toBe('Айгерим · +77010000001');
    fireEvent.click(pc);
    expect(document.getElementById('computer-controls')?.className).not.toContain('show');
  });

  // Вместо опроса /club-map/state — событие computer.state: связь сразу, остальное — запросом по id.
  it('updates a PC from a computer.state event', async () => {
    vi.useFakeTimers();
    const fresh = { ...(page.states[1] as ComputerState), status: 'online', light: 'pc-free', connection: 'online' };
    const fetchSpy = vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response(JSON.stringify({ computers: [fresh] }), { status: 200, headers: { 'Content-Type': 'application/json' } }));
    const { rt } = setup();
    act(() => rt.dispatch({ type: 'computer.state', at: '', data: { computer_id: 8, connection: 'online' } }));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(350);
    });
    vi.useRealTimers();
    expect(fetchSpy).toHaveBeenCalledTimes(1);
    const req = fetchSpy.mock.calls[0]?.[0] as Request;
    expect(new URL(req.url).searchParams.get('ids')).toBe('8');
    expect(document.querySelector('.cm-computer[data-id="8"]')?.className).toBe('cm-computer online pc-free');
  });

  it('merges states and labels booking durations', () => {
    expect(mergeStates(page, [{ ...(page.states[0] as ComputerState), light: '' }]).states[0]?.light).toBe('');
    const t = (k: string, o?: Record<string, unknown>) => `${String(o?.count)}|${k.includes('час') ? 'h' : 'm'}`;
    expect(durationLabel(t, 90)).toBe('1|h 30|m');
    expect(durationLabel(t, 30)).toBe('30|m');
  });

  // Этап 7: загрузка и проблемы на карточке — только при данных агента; без них разметка как в старом шаблоне.
  it('shows load bars and problem badge only with agent data', () => {
    const withLoad: Page = {
      ...page,
      states: page.states.map((st) => (st.id === 7 ? { ...st, load: { cpu_bp: 4250, ram_bp: 6100, idle_seconds: 0 }, problems: ['cpu_hot', 'mouse'] } : st)),
    };
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
    qc.setQueryData(clubMapKey, withLoad);
    render(
      <QueryClientProvider client={qc}>
        <RealtimeContext.Provider value={new RealtimeClient()}>
          <ClubMapPage />
        </RealtimeContext.Provider>
      </QueryClientProvider>,
    );
    const pc = document.querySelector('.cm-computer[data-id="7"]') as HTMLElement;
    const bars = pc.querySelectorAll('.cm-pc-load i');
    expect(bars).toHaveLength(2);
    expect((bars[0] as HTMLElement).style.width).toBe('43%');
    expect((bars[1] as HTMLElement).style.width).toBe('61%');
    expect(pc.querySelector('.cm-pc-alert')?.getAttribute('title')).toBe('Перегрев CPU, Мышь не подключена');
    expect(pc.getAttribute('title')).toBe('VIP-01 · online · LAN 10.0.0.1 · CPU 43 % · ОЗУ 61 %');
    const off = document.querySelector('.cm-computer[data-id="8"]') as HTMLElement;
    expect(off.querySelector('.cm-pc-load, .cm-pc-alert')).toBeNull();
  });

  it('renders the same PC markup without agent data', () => {
    setup();
    for (const id of [7, 8]) {
      const pc = document.querySelector(`.cm-computer[data-id="${id}"]`) as HTMLElement;
      expect(pc.querySelector('.cm-pc-load, .cm-pc-alert')).toBeNull();
      expect(pc.children).toHaveLength(2); // номер и кнопка правки, как в club-map/index.blade.php
    }
  });

  // computer.load — готовые данные без запроса к серверу.
  it('updates load from a computer.load event without fetching', async () => {
    const fetchSpy = vi.spyOn(globalThis, 'fetch');
    const { rt, qc } = setup();
    act(() => rt.dispatch({ type: 'computer.load', at: '', data: { computer_id: 7, load: { cpu_bp: 9000, ram_bp: 1000, idle_seconds: 5 }, problems: ['gpu_hot'] } }));
    const pc = document.querySelector('.cm-computer[data-id="7"]') as HTMLElement;
    await waitFor(() => expect(pc.querySelector('.cm-pc-load i')).not.toBeNull());
    expect((pc.querySelector('.cm-pc-load i') as HTMLElement).style.width).toBe('90%');
    expect(pc.querySelector('.cm-pc-alert')?.getAttribute('title')).toBe('Перегрев GPU');
    expect(fetchSpy).not.toHaveBeenCalled();
    expect(qc.getQueryData<Page>(clubMapKey)?.states[0]?.problems).toEqual(['gpu_hot']);
  });

  it('shows the state and problem history of a PC in the info panel', async () => {
    const json = (v: unknown) => new Response(JSON.stringify(v), { status: 200, headers: { 'Content-Type': 'application/json' } });
    vi.spyOn(globalThis, 'fetch').mockImplementation((input) => {
      const url = new URL((input as Request).url);
      if (url.pathname.endsWith('/health/events')) {
        return Promise.resolve(json({ items: [{ id: 2, kind: 'mouse', raised_at: '2026-10-15T08:00:00Z', cleared_at: null, details: {} },
          { id: 1, kind: 'cpu_hot', raised_at: '2026-10-14T08:00:00Z', cleared_at: '2026-10-14T09:00:00Z', details: {} }], next_cursor: 'abc' }));
      }
      return Promise.resolve(json({
        computer_id: 7, connection: 'online', agent_version: '2.4.1', rtt_ms: 18, problems: ['mouse'],
        load: { cpu_bp: 4250, ram_bp: 6100, idle_seconds: 120 },
        health: { reported_at: '2026-10-15T08:00:00Z', cpu_temp_deci: 655, gpu_temp_deci: null, gpu_load_bp: null, gpu_memory_bp: null,
          disks: [{ mount: 'C:', system: true, total_mb: 512000, free_mb: 102400, smart: 'warning' }],
          peripherals: [{ kind: 'mouse', connected: false, name: 'G102' }], link_mbps: 1000, uptime_seconds: 93784,
          unexpected_shutdowns_7d: 1, bugchecks_7d: 0 },
      }));
    });
    setup();
    fireEvent.click(document.querySelector('.cm-computer[data-id="7"]') as HTMLElement);
    await waitFor(() => expect(document.getElementById('computer-health-info')).not.toBeNull());
    const info = document.getElementById('computer-health-info')?.textContent ?? '';
    for (const part of ['Состояние', 'Мышь не подключена', '43 %', '18 мс', '66 °C', 'свободно 100 из 500 ГБ · SMART: предупреждение', 'G102 · не подключено', '1000 Мбит/с', '1 д 2 ч']) {
      expect(info).toContain(part);
    }
    await waitFor(() => expect(document.getElementById('computer-health-history')).not.toBeNull());
    const history = document.getElementById('computer-health-history') as HTMLElement;
    expect(history.querySelectorAll('dt')).toHaveLength(2);
    expect(history.querySelector('.cm-info-more')).not.toBeNull();
  });

  it('shows no state sections without agent data', async () => {
    const json = (v: unknown) => new Response(JSON.stringify(v), { status: 200, headers: { 'Content-Type': 'application/json' } });
    const fetchSpy = vi.spyOn(globalThis, 'fetch').mockImplementation((input) =>
      Promise.resolve(new URL((input as Request).url).pathname.endsWith('/events')
        ? json({ items: [], next_cursor: null })
        : json({ computer_id: 8, connection: 'offline', problems: [] })));
    setup();
    fireEvent.click(document.querySelector('.cm-computer[data-id="8"]') as HTMLElement);
    await waitFor(() => expect(fetchSpy).toHaveBeenCalledTimes(2));
    await act(async () => {
      await Promise.resolve();
    });
    expect(document.getElementById('computer-health-info')).toBeNull();
    expect(document.getElementById('computer-health-history')).toBeNull();
  });

  it('merges computer.load and converts basis points', () => {
    expect(mergeLoad(page, { computer_id: 8, load: { cpu_bp: 1, ram_bp: 2, idle_seconds: 3 }, problems: [] }).states[1]?.load?.ram_bp).toBe(2);
    expect(mergeLoad(page, {})).toBe(page);
    expect([percent(0), percent(4249), percent(4250), percent(10000), percent(20000)]).toEqual([0, 42, 43, 100, 100]);
  });
});
