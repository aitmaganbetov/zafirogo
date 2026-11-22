import { QueryClient, QueryClientProvider, useQuery } from '@tanstack/react-query';
import { act, render } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { RealtimeClient } from './client';
import { RealtimeContext } from './context';
import { session } from '../auth/session';
import { COMPUTER_TELEMETRY, INVALIDATE_WINDOW_MS, RECONNECT_JITTER_MS, useInvalidateOn, useRealtime } from './hooks';

/* FE-02: пачка событий WebSocket не должна превращаться в пачку REST-запросов. */

function Screen({ fetcher, patterns, except }: { fetcher: () => Promise<number>; patterns: readonly string[]; except?: readonly string[] }) {
  useInvalidateOn(patterns, ['screen'], except);
  useQuery({ queryKey: ['screen'], queryFn: fetcher });
  return null;
}

describe('useInvalidateOn', () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  function mount(patterns: readonly string[], except?: readonly string[], fetcher = vi.fn(async () => 1)) {
    const client = new RealtimeClient({ ticket: async () => 't' });
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: 30_000 } } });
    const view = render(
      <QueryClientProvider client={qc}>
        <RealtimeContext.Provider value={client}>
          <Screen fetcher={fetcher} patterns={patterns} except={except} />
        </RealtimeContext.Provider>
      </QueryClientProvider>,
    );
    return { client, fetcher, view, qc };
  }

  /** Ответ через ms; значение — номер запроса (что именно дошло до экрана). */
  function slow(ms: number) {
    let n = 0;
    const done = vi.fn();
    const fetcher = vi.fn(async () => {
      const mine = ++n;
      await new Promise((r) => setTimeout(r, ms));
      done(mine);
      return mine;
    });
    return { fetcher, done };
  }

  // Ревью 22.11.2026: отмена идущего запроса каждым окном при ответе дольше окна — запрос не завершался никогда.
  it('события каждые 500 мс и ответ дольше окна: запрос завершается, данные обновляются', async () => {
    const { fetcher } = slow(INVALIDATE_WINDOW_MS + 500);
    const { client, view, qc } = mount(['cash.*'], undefined, fetcher);
    await act(() => vi.advanceTimersByTimeAsync(INVALIDATE_WINDOW_MS + 500));
    expect(qc.getQueryData(['screen'])).toBe(1);

    fetcher.mockClear();
    const seen = new Set<unknown>();
    for (let i = 0; i < 20; i++) {
      client.dispatch({ type: 'cash.transaction', at: '', data: {} });
      await act(() => vi.advanceTimersByTimeAsync(500));
      seen.add(qc.getQueryData(['screen']));
    }
    // За 10 с при ответе 2 с экран получил несколько свежих ответов, а не застыл на первом.
    expect(seen.size).toBeGreaterThanOrEqual(3);
    // FE-02 в силе: запросы идут друг за другом, не пачкой.
    expect(fetcher.mock.calls.length).toBeLessThanOrEqual(Math.ceil(10_000 / INVALIDATE_WINDOW_MS) + 2);

    // Последнее событие не теряется: ответ, начатый до него, перечитан; после тишины запросов нет.
    await act(() => vi.advanceTimersByTimeAsync(INVALIDATE_WINDOW_MS * 4));
    expect(qc.getQueryData(['screen'])).toBe(fetcher.mock.calls.length + 1);
    fetcher.mockClear();
    await act(() => vi.advanceTimersByTimeAsync(60_000));
    expect(fetcher).not.toHaveBeenCalled();
    view.unmount();
  });

  it('событие во время идущего запроса: запрос не отменяется, после него — ровно одно повторное чтение', async () => {
    const { fetcher, done } = slow(2000);
    const { client, view, qc } = mount(['cash.*'], undefined, fetcher);
    await act(() => vi.advanceTimersByTimeAsync(2000));
    expect(qc.getQueryData(['screen'])).toBe(1);
    // Событие перечитывает сразу; ещё три приходят, пока этот запрос идёт (начат до них — ответ мог устареть).
    client.dispatch({ type: 'cash.transaction', at: '', data: {} });
    await act(() => vi.advanceTimersByTimeAsync(500));
    expect(fetcher).toHaveBeenCalledTimes(2);
    for (let i = 0; i < 3; i++) client.dispatch({ type: 'cash.transaction', at: '', data: {} });
    // Конец окна (1500 мс): идущий запрос не отменён и не продублирован.
    await act(() => vi.advanceTimersByTimeAsync(1400));
    expect(fetcher).toHaveBeenCalledTimes(2);
    expect(done).not.toHaveBeenCalledWith(2);
    await act(() => vi.advanceTimersByTimeAsync(200));
    expect(done).toHaveBeenCalledWith(2);
    expect(fetcher).toHaveBeenCalledTimes(3);
    await act(() => vi.advanceTimersByTimeAsync(60_000));
    expect(fetcher).toHaveBeenCalledTimes(3);
    expect(qc.getQueryData(['screen'])).toBe(3);
    view.unmount();
  });

  it('40 событий за 10 с → не «1 событие = 1 запрос»; 40 событий разом → ≤ 2 запросов', async () => {
    const { client, fetcher, view } = mount(['cash.*']);
    await act(() => vi.advanceTimersByTimeAsync(0));
    expect(fetcher).toHaveBeenCalledTimes(1);

    // Пачка: первое событие перечитывает сразу, остальные 39 — одним запросом в конце окна.
    fetcher.mockClear();
    for (let i = 0; i < 40; i++) client.dispatch({ type: 'cash.transaction', at: '', data: {} });
    await act(() => vi.advanceTimersByTimeAsync(INVALIDATE_WINDOW_MS * 3));
    expect(fetcher.mock.calls.length).toBeLessThanOrEqual(2);
    expect(fetcher.mock.calls.length).toBeGreaterThanOrEqual(1);

    // Поток: 40 событий равномерно за 10 с — не больше одного запроса на окно.
    fetcher.mockClear();
    for (let i = 0; i < 40; i++) {
      client.dispatch({ type: 'cash.transaction', at: '', data: {} });
      await act(() => vi.advanceTimersByTimeAsync(250));
    }
    await act(() => vi.advanceTimersByTimeAsync(INVALIDATE_WINDOW_MS * 2));
    expect(fetcher.mock.calls.length).toBeLessThanOrEqual(Math.ceil(10_000 / INVALIDATE_WINDOW_MS) + 2);
    expect(fetcher.mock.calls.length).toBeGreaterThanOrEqual(2);

    // Последнее событие пачки не теряется: после тишины данные свежие, новых запросов нет.
    fetcher.mockClear();
    await act(() => vi.advanceTimersByTimeAsync(60_000));
    expect(fetcher).not.toHaveBeenCalled();
    view.unmount();
  });

  it('одиночное событие перечитывает сразу; телеметрия computer.load запрос не трогает', async () => {
    const { client, fetcher, view } = mount(['computer.*', 'cash.*'], COMPUTER_TELEMETRY);
    await act(() => vi.advanceTimersByTimeAsync(0));
    fetcher.mockClear();
    for (let i = 0; i < 40; i++) client.dispatch({ type: 'computer.load', at: '', data: { computer_id: 1 } });
    await act(() => vi.advanceTimersByTimeAsync(INVALIDATE_WINDOW_MS * 2));
    expect(fetcher).not.toHaveBeenCalled();
    client.dispatch({ type: 'computer.state', at: '', data: { computer_id: 1 } });
    await act(() => vi.advanceTimersByTimeAsync(0));
    expect(fetcher).toHaveBeenCalledTimes(1);
    view.unmount();
  });

  it('после размонтирования отложенный запрос не уходит', async () => {
    const { client, fetcher, view } = mount(['cash.*']);
    await act(() => vi.advanceTimersByTimeAsync(0));
    client.dispatch({ type: 'cash.transaction', at: '', data: {} });
    client.dispatch({ type: 'cash.transaction', at: '', data: {} });
    await act(() => vi.advanceTimersByTimeAsync(0));
    fetcher.mockClear();
    view.unmount();
    await act(() => vi.advanceTimersByTimeAsync(INVALIDATE_WINDOW_MS * 2));
    expect(fetcher).not.toHaveBeenCalled();
  });
});

function Shell() {
  useRealtime();
  return null;
}

describe('useRealtime: переподключение', () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => {
    vi.useRealTimers();
    vi.restoreAllMocks();
  });

  function mount() {
    const client = new RealtimeClient({ ticket: async () => 't' });
    vi.spyOn(client, 'start').mockImplementation(() => undefined);
    vi.spyOn(client, 'stop').mockImplementation(() => undefined);
    let fire: (reauth: boolean) => void = () => undefined;
    vi.spyOn(client, 'onReconnect').mockImplementation((fn) => {
      fire = fn;
      return () => undefined;
    });
    const reload = vi.spyOn(session, 'reloadMe').mockResolvedValue({} as Awaited<ReturnType<typeof session.reloadMe>>);
    const qc = new QueryClient();
    const view = render(
      <QueryClientProvider client={qc}>
        <RealtimeContext.Provider value={client}>
          <Shell />
        </RealtimeContext.Provider>
      </QueryClientProvider>,
    );
    return { fire: (reauth: boolean) => fire(reauth), reload, view };
  }

  it('обычный разрыв: права (Me) перечитываются — могли смениться, пока сокета не было', async () => {
    const { fire, reload, view } = mount();
    act(() => fire(false));
    expect(reload).not.toHaveBeenCalled(); // с разбросом, не разом со всеми панелями
    await act(() => vi.advanceTimersByTimeAsync(RECONNECT_JITTER_MS + 1));
    expect(reload).toHaveBeenCalledTimes(1);
    view.unmount();
  });

  it('4001 (права изменились): Me сразу и один раз', async () => {
    const { fire, reload, view } = mount();
    act(() => fire(true));
    expect(reload).toHaveBeenCalledTimes(1);
    await act(() => vi.advanceTimersByTimeAsync(RECONNECT_JITTER_MS + 1));
    expect(reload).toHaveBeenCalledTimes(1);
    view.unmount();
  });
});
