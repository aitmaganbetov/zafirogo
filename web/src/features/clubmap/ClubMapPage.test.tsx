import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { act, fireEvent, render } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { RealtimeClient } from '../../realtime/client';
import { RealtimeContext } from '../../realtime/context';
import { clubMapKey, type ClubMapPage as Page, type ComputerState } from './api';
import { ClubMapPage } from './ClubMapPage';
import { durationLabel } from './errors';
import { mergeStates } from './useMapLive';

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
});
