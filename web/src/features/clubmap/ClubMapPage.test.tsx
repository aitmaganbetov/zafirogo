import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { act, fireEvent, render, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { RealtimeClient } from '../../realtime/client';
import { RealtimeContext } from '../../realtime/context';
import { clubMapKey, type ClubMapPage as Page, type ComputerState } from './api';
import { ClubMapPage } from './ClubMapPage';
import { durationLabel } from './errors';
import { percent } from './healthText';
import { LIVE_WINDOW_MS, pushSample, resetLiveSeries, sampleFromEvent, samplesOf } from './liveSeries';
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

function setup(computer?: number) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
  qc.setQueryData(clubMapKey, page);
  const rt = new RealtimeClient();
  render(
    <QueryClientProvider client={qc}>
      <RealtimeContext.Provider value={rt}>
        <ClubMapPage computer={computer} />
      </RealtimeContext.Provider>
    </QueryClientProvider>,
  );
  return { qc, rt };
}

afterEach(() => {
  vi.restoreAllMocks();
  resetLiveSeries();
});

const json = (v: unknown) => new Response(JSON.stringify(v), { status: 200, headers: { 'Content-Type': 'application/json' } });

const onlineView = (cpuTempDeci: number) => ({
  computer_id: 7, connection: 'online', agent_version: '2.4.1', rtt_ms: 18, problems: [],
  load: { cpu_bp: 4250, ram_bp: 6100, idle_seconds: 120 },
  health: { reported_at: '2026-10-15T08:00:00Z', cpu_temp_deci: cpuTempDeci, gpu_temp_deci: null, gpu_load_bp: null, gpu_memory_bp: null,
    disks: [{ mount: 'C:', system: true, total_mb: 512000, free_mb: 102400, smart: 'ok' }],
    peripherals: [{ kind: 'mouse', connected: true, name: 'G102' }], link_mbps: 1000, uptime_seconds: 600,
    unexpected_shutdowns_7d: 0, bugchecks_7d: 0 },
});

const weeks = [
  { start: '2026-11-01T19:00:00Z', end: '2026-11-08T19:00:00Z', online_minutes: 150, session_minutes: 0, cpu_temp_avg_deci: 724, cpu_temp_max_deci: 950,
    cpu_load_avg_bp: 3000, ram_avg_bp: 5000, cpu_hot_hours: 3, gpu_hot_hours: 0, problem_hours: 1, unexpected_shutdowns: 1, bugchecks: 1,
    peripheral_disconnects: 2, system_disk_free_mb: 40000, system_disk_free_bp: 781, smart: 'warning',
    verdict: { level: 'repair', reasons: [{ code: 'bugchecks', level: 'repair', value: 1, threshold: 1 }, { code: 'cpu_hot', level: 'watch', value: 3, threshold: 90 },
      { code: 'disk_low', level: 'watch', value: 7, threshold: 10 }] } },
  { start: '2026-10-25T19:00:00Z', end: '2026-11-01T19:00:00Z', online_minutes: 60, session_minutes: 0, cpu_temp_avg_deci: 600, cpu_temp_max_deci: 650,
    cpu_hot_hours: 0, gpu_hot_hours: 0, problem_hours: 0, unexpected_shutdowns: 0, bugchecks: 0, peripheral_disconnects: 0,
    verdict: { level: 'ok', reasons: [] } },
];

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

  // Переход из колокольчика «Проблемы с ПК» (/club-map?computer=8) — открыта карточка этого ПК, без выделения.
  it('opens the PC card from the ?computer= link', () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response(JSON.stringify({ items: [], next_cursor: null }), { status: 200, headers: { 'Content-Type': 'application/json' } }));
    setup(8);
    expect(document.getElementById('computer-info-panel')?.hidden).toBe(false);
    expect(document.getElementById('computer-info-name')?.textContent).toBe('VIP-02 (#2)');
    expect(document.getElementById('computer-controls')?.className).not.toContain('show');
  });

  it('switches the PC card when the ?computer= link changes on an open map', () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response(JSON.stringify({ items: [], next_cursor: null }), { status: 200, headers: { 'Content-Type': 'application/json' } }));
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
    qc.setQueryData(clubMapKey, page);
    const rt = new RealtimeClient();
    const view = (computer?: number) => (
      <QueryClientProvider client={qc}>
        <RealtimeContext.Provider value={rt}>
          <ClubMapPage computer={computer} />
        </RealtimeContext.Provider>
      </QueryClientProvider>
    );
    const { rerender } = render(view());
    expect(document.getElementById('computer-info-panel')?.hidden).toBe(true);
    rerender(view(7));
    expect(document.getElementById('computer-info-name')?.textContent).toBe('VIP-01 (#1)');
    rerender(view(8));
    expect(document.getElementById('computer-info-name')?.textContent).toBe('VIP-02 (#2)');
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
    const fetchSpy = vi.spyOn(globalThis, 'fetch').mockImplementation((input) => {
      const path = new URL((input as Request).url).pathname;
      return Promise.resolve(path.endsWith('/events')
        ? json({ items: [], next_cursor: null })
        : path.endsWith('/weekly') ? json({ weeks: [] }) : json({ computer_id: 8, connection: 'offline', problems: [] }));
    });
    setup();
    fireEvent.click(document.querySelector('.cm-computer[data-id="8"]') as HTMLElement);
    await waitFor(() => expect(fetchSpy).toHaveBeenCalledTimes(3));
    await act(async () => {
      await Promise.resolve();
    });
    expect(document.getElementById('computer-health-info')).toBeNull();
    expect(document.getElementById('computer-health-history')).toBeNull();
  });

  // Открытая панель ПК обновляется живым событием computer.load без перезагрузки: состояние перечитывается
  // (температуры, диски, периферия), точка — в живой график.
  it('updates the open PC panel from a computer.load event', async () => {
    let temp = 655;
    const fetchSpy = vi.spyOn(globalThis, 'fetch').mockImplementation((input) => {
      const path = new URL((input as Request).url).pathname;
      if (path.endsWith('/events')) return Promise.resolve(json({ items: [], next_cursor: null }));
      if (path.endsWith('/weekly')) return Promise.resolve(json({ weeks: [] }));
      return Promise.resolve(json(onlineView(temp)));
    });
    const { rt } = setup();
    fireEvent.click(document.querySelector('.cm-computer[data-id="7"]') as HTMLElement);
    await waitFor(() => expect(document.getElementById('computer-health-info')?.textContent).toContain('66 °C'));
    temp = 801;
    act(() => rt.dispatch({ type: 'computer.load', at: '', data: { computer_id: 7, load: { cpu_bp: 9000, ram_bp: 1000, idle_seconds: 5 }, problems: [], cpu_temp_deci: 801 } }));
    await waitFor(() => expect(document.getElementById('computer-health-info')?.textContent).toContain('80 °C'));
    expect(fetchSpy.mock.calls.filter((c) => new URL((c[0] as Request).url).pathname.endsWith('/health'))).toHaveLength(2);
    const last = samplesOf(7).at(-1);
    expect(last?.cpuTemp).toBe(80);
    // Без вкладок, если недель нет; живой график — когда в окне ≥ 2 точек.
    expect(document.querySelector('#computer-health-info [role="tablist"]')).toBeNull();
    expect(document.getElementById('computer-health-live-chart')).toBeNull();
    act(() => pushSample(7, { at: Date.now() + 60_000, cpu: 50, cpuTemp: 70 }));
    expect(document.getElementById('computer-health-live-chart')).not.toBeNull();
  });

  it('keeps 15 minutes of live samples per PC', () => {
    pushSample(7, sampleFromEvent({ computer_id: 7, load: { cpu_bp: 4250, ram_bp: 6100, idle_seconds: 0 }, cpu_temp_deci: 655 }, 1_000_000));
    pushSample(7, sampleFromEvent({ computer_id: 7, load: { cpu_bp: 5000, ram_bp: 6100, idle_seconds: 0 } }, 1_000_500)); // то же измерение
    expect(samplesOf(7)).toEqual([{ at: 1_000_500, cpu: 50, ram: 61, cpuTemp: undefined, gpuTemp: undefined }]);
    pushSample(7, sampleFromEvent({ computer_id: 7 }, 1_100_000)); // без данных агента — не точка
    pushSample(7, sampleFromEvent({ computer_id: 7, load: { cpu_bp: 100, ram_bp: 100, idle_seconds: 0 } }, 1_000_500 + LIVE_WINDOW_MS));
    expect(samplesOf(7).map((s) => s.cpu)).toEqual([1]);
    expect(samplesOf(8)).toEqual([]);
  });

  // «По неделям»: вердикт с причинами (ключи перевода с подстановками) и сводка; выбор недели — график по часам.
  it('shows weekly verdicts and the hourly chart of a selected week', async () => {
    const fetchSpy = vi.spyOn(globalThis, 'fetch').mockImplementation((input) => {
      const url = new URL((input as Request).url);
      if (url.pathname.endsWith('/events')) return Promise.resolve(json({ items: [], next_cursor: null }));
      if (url.pathname.endsWith('/weekly')) return Promise.resolve(json({ weeks }));
      if (url.pathname.endsWith('/hourly')) {
        return Promise.resolve(json({ items: [{ hour: '2026-11-01T19:00:00Z', online_minutes: 30, session_minutes: 0, cpu_temp_avg_deci: 700 },
          { hour: '2026-11-02T10:00:00Z', online_minutes: 60, session_minutes: 0, cpu_temp_avg_deci: 700 }] }));
      }
      return Promise.resolve(json({ computer_id: 8, connection: 'offline', problems: [] }));
    });
    setup();
    fireEvent.click(document.querySelector('.cm-computer[data-id="8"]') as HTMLElement);
    await waitFor(() => expect(document.querySelectorAll('#computer-health-info [data-week]')).toHaveLength(2));
    const info = document.getElementById('computer-health-info') as HTMLElement;
    expect(info.querySelector('[role="tablist"]')).toBeNull(); // ПК не в сети — только недели
    const [cur, prev] = [...info.querySelectorAll('[data-week]')] as HTMLElement[];
    expect(cur?.querySelector('dd.cm-info-warning')?.textContent).toBe(
      'Требует ремонта · «Синих экранов» за неделю: 1 · Перегрев CPU (от 90 °C): 3 ч · Системный диск: свободно 7 % (меньше 10 %)');
    expect(cur?.textContent).toContain('Онлайн: 3 ч, с проблемами: 1 ч');
    expect(cur?.textContent).toContain('CPU: в среднем 72 °C, максимум 95 °C');
    expect(cur?.textContent).toContain('Сбои: выключений 1, «синих экранов» 1, отключений периферии 2');
    expect(prev?.querySelector('dd')?.textContent).toBe('Исправен');
    expect(prev?.querySelector('dd')?.className ?? '').toBe('');
    expect(document.getElementById('computer-health-week-chart')).toBeNull();
    fireEvent.click(cur?.querySelector('button') as HTMLElement);
    await waitFor(() => expect(document.getElementById('computer-health-week-chart')).not.toBeNull());
    const hourly = fetchSpy.mock.calls.map((c) => new URL((c[0] as Request).url)).find((u) => u.pathname.endsWith('/hourly'));
    expect(hourly?.searchParams.get('from')).toBe('2026-11-01T19:00:00Z');
    expect(hourly?.searchParams.get('to')).toBe('2026-11-08T19:00:00Z');
    expect(cur?.querySelector('button')?.getAttribute('aria-pressed')).toBe('true');
  });

  // Бездисковый ПК (S7-H-18): ни строк дисков и SMART «Сейчас», ни системного диска «По неделям», ни проблем дисков в
  // истории — пометка «Бездисковый ПК», сеть сразу после проблем, медленная сеть в сводке и причине вердикта.
  it('hides disks of a diskless PC in both tabs', async () => {
    const view = { ...onlineView(655), diskless: true, problems: ['network_slow'] };
    view.health = { ...view.health, link_mbps: 100, disks: [{ mount: 'C:', system: true, total_mb: 512000, free_mb: 1024, smart: 'failing' }] };
    const disklessWeeks = [{ ...weeks[0], network_slow_hours: 5,
      verdict: { level: 'watch', reasons: [{ code: 'network_slow', level: 'watch', value: 5, threshold: 1000 }] } }];
    vi.spyOn(globalThis, 'fetch').mockImplementation((input) => {
      const path = new URL((input as Request).url).pathname;
      if (path.endsWith('/events')) {
        return Promise.resolve(json({ items: [{ id: 2, kind: 'disk_smart', raised_at: '2026-10-15T08:00:00Z', cleared_at: null, details: {} },
          { id: 1, kind: 'network_slow', raised_at: '2026-10-14T08:00:00Z', cleared_at: null, details: {} }], next_cursor: null }));
      }
      if (path.endsWith('/weekly')) return Promise.resolve(json({ diskless: true, weeks: disklessWeeks }));
      return Promise.resolve(json(view));
    });
    setup();
    fireEvent.click(document.querySelector('.cm-computer[data-id="7"]') as HTMLElement);
    await waitFor(() => expect(document.querySelectorAll('#computer-health-info [role="tab"]')).toHaveLength(2));
    const info = document.getElementById('computer-health-info') as HTMLElement;
    const rows = () => [...info.querySelectorAll('dt')].map((dt) => dt.textContent);
    expect(rows().slice(0, 3)).toEqual(['Бездисковый ПК', 'Проблемы', 'Сеть']);
    expect(info.textContent).toContain('Медленная сеть');
    expect(info.textContent).not.toContain('Диск C:');
    expect(info.textContent).not.toContain('SMART');
    const [, byWeek] = [...info.querySelectorAll('[role="tab"]')] as HTMLElement[];
    fireEvent.click(byWeek as HTMLElement);
    const week = info.querySelector('[data-week]') as HTMLElement;
    expect(rows()[0]).toBe('Бездисковый ПК');
    expect(week.textContent).toContain('Медленная сеть: 5 ч');
    expect(week.textContent).toContain('Наблюдать · Медленная сеть (меньше 1000 Мбит/с): 5 ч');
    expect(week.textContent).not.toContain('Системный диск');
    await waitFor(() => expect(document.getElementById('computer-health-history')).not.toBeNull());
    expect([...(document.getElementById('computer-health-history') as HTMLElement).querySelectorAll('dt')].map((dt) => dt.textContent))
      .toEqual(['Медленная сеть']);
  });

  it('shows disks of a regular PC', async () => {
    vi.spyOn(globalThis, 'fetch').mockImplementation((input) => {
      const path = new URL((input as Request).url).pathname;
      if (path.endsWith('/events')) return Promise.resolve(json({ items: [], next_cursor: null }));
      if (path.endsWith('/weekly')) return Promise.resolve(json({ diskless: false, weeks }));
      return Promise.resolve(json({ ...onlineView(655), diskless: false }));
    });
    setup();
    fireEvent.click(document.querySelector('.cm-computer[data-id="7"]') as HTMLElement);
    await waitFor(() => expect(document.querySelectorAll('#computer-health-info [role="tab"]')).toHaveLength(2));
    const info = document.getElementById('computer-health-info') as HTMLElement;
    expect(info.textContent).toContain('Диск C:');
    expect(info.textContent).not.toContain('Бездисковый ПК');
    fireEvent.click([...info.querySelectorAll('[role="tab"]')][1] as HTMLElement);
    expect(info.textContent).toContain('Системный диск: свободно не меньше 7 %');
  });

  it('switches between the live state and weeks tabs', async () => {
    vi.spyOn(globalThis, 'fetch').mockImplementation((input) => {
      const path = new URL((input as Request).url).pathname;
      if (path.endsWith('/events')) return Promise.resolve(json({ items: [], next_cursor: null }));
      if (path.endsWith('/weekly')) return Promise.resolve(json({ weeks }));
      return Promise.resolve(json(onlineView(655)));
    });
    setup();
    fireEvent.click(document.querySelector('.cm-computer[data-id="7"]') as HTMLElement);
    await waitFor(() => expect(document.querySelectorAll('#computer-health-info [role="tab"]')).toHaveLength(2));
    const [now, byWeek] = [...document.querySelectorAll('#computer-health-info [role="tab"]')] as HTMLElement[];
    expect(now?.getAttribute('aria-selected')).toBe('true');
    expect(document.getElementById('computer-health-info')?.textContent).toContain('66 °C');
    expect(document.querySelector('#computer-health-info [data-week]')).toBeNull();
    fireEvent.click(byWeek as HTMLElement);
    expect(document.querySelectorAll('#computer-health-info [data-week]')).toHaveLength(2);
    expect(document.getElementById('computer-health-info')?.textContent).not.toContain('66 °C');
  });

  it('merges computer.load and converts basis points', () => {
    expect(mergeLoad(page, { computer_id: 8, load: { cpu_bp: 1, ram_bp: 2, idle_seconds: 3 }, problems: [] }).states[1]?.load?.ram_bp).toBe(2);
    expect(mergeLoad(page, {})).toBe(page);
    expect([percent(0), percent(4249), percent(4250), percent(10000), percent(20000)]).toEqual([0, 42, 43, 100, 100]);
  });
});
