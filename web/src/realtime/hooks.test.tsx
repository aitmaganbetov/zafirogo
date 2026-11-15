import { QueryClient, QueryClientProvider, useQuery } from '@tanstack/react-query';
import { act, render } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { RealtimeClient } from './client';
import { RealtimeContext } from './context';
import { COMPUTER_TELEMETRY, INVALIDATE_WINDOW_MS, useInvalidateOn } from './hooks';

/* FE-02: пачка событий WebSocket не должна превращаться в пачку REST-запросов. */

function Screen({ fetcher, patterns, except }: { fetcher: () => Promise<number>; patterns: readonly string[]; except?: readonly string[] }) {
  useInvalidateOn(patterns, ['screen'], except);
  useQuery({ queryKey: ['screen'], queryFn: fetcher });
  return null;
}

describe('useInvalidateOn', () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  function mount(patterns: readonly string[], except?: readonly string[]) {
    const client = new RealtimeClient({ ticket: async () => 't' });
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: 30_000 } } });
    const fetcher = vi.fn(async () => 1);
    const view = render(
      <QueryClientProvider client={qc}>
        <RealtimeContext.Provider value={client}>
          <Screen fetcher={fetcher} patterns={patterns} except={except} />
        </RealtimeContext.Provider>
      </QueryClientProvider>,
    );
    return { client, fetcher, view };
  }

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
