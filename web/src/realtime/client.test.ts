import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { RealtimeClient, matchType } from './client';

class FakeSocket {
  static last: FakeSocket | null = null;
  onmessage: ((ev: MessageEvent) => void) | null = null;
  onclose: ((ev: CloseEvent) => void) | null = null;
  closed = false;
  readonly url: string;
  constructor(url: string) {
    this.url = url;
    FakeSocket.last = this;
  }
  send(): void {}
  close(): void {
    this.closed = true;
  }
  emit(msg: unknown): void {
    this.onmessage?.({ data: JSON.stringify(msg) } as MessageEvent);
  }
  drop(code = 1006): void {
    this.onclose?.({ code } as CloseEvent);
  }
}

describe('WebSocket-клиент', () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  function client(tickets: string[] = ['t1', 't2', 't3', 't4']) {
    let i = 0;
    return new RealtimeClient({
      ticket: async () => tickets[i++] ?? 'tx',
      createSocket: (url) => new FakeSocket(url) as unknown as WebSocket,
      url: (t) => `ws://x/ws?ticket=${t}`,
      random: () => 0.5,
    });
  }

  it('шаблоны типов', () => {
    expect(matchType('chat.*', 'chat.unread')).toBe(true);
    expect(matchType('chat.*', 'chatter.x')).toBe(false);
    expect(matchType('computer.state', 'computer.state')).toBe(true);
    expect(matchType('*', 'x.y')).toBe(true);
  });

  it('подключение по билету, раздача событий по типу, дедупликация по id', async () => {
    const c = client();
    const got: string[] = [];
    c.on('shop_order.*', (m) => got.push(m.type));
    c.start();
    await vi.runOnlyPendingTimersAsync();
    expect(FakeSocket.last?.url).toBe('ws://x/ws?ticket=t1');
    FakeSocket.last?.emit({ type: 'hello', at: '', data: {} });
    expect(c.getState()).toBe('open');
    FakeSocket.last?.emit({ id: 'a', type: 'shop_order.created', at: '', data: { new_count: 2 } });
    FakeSocket.last?.emit({ id: 'a', type: 'shop_order.created', at: '', data: { new_count: 2 } });
    FakeSocket.last?.emit({ id: 'b', type: 'chat.unread', at: '', data: {} });
    expect(got).toEqual(['shop_order.created']);
    c.stop();
  });

  it('обрыв — переподключение с backoff и новым билетом; после него — onReconnect', async () => {
    const c = client();
    const reconnected = vi.fn();
    c.onReconnect(reconnected);
    c.start();
    await vi.runOnlyPendingTimersAsync();
    FakeSocket.last?.emit({ type: 'hello', at: '', data: {} });
    FakeSocket.last?.drop();
    expect(c.getState()).toBe('closed');
    await vi.advanceTimersByTimeAsync(999);
    expect(FakeSocket.last?.url).toContain('t1');
    await vi.advanceTimersByTimeAsync(10);
    expect(FakeSocket.last?.url).toContain('t2');
    FakeSocket.last?.emit({ type: 'hello', at: '', data: {} });
    expect(reconnected).toHaveBeenCalledTimes(1);
    c.stop();
  });

  it('4001 — сразу с новым билетом', async () => {
    const c = client();
    c.start();
    await vi.runOnlyPendingTimersAsync();
    FakeSocket.last?.drop(4001);
    await vi.advanceTimersByTimeAsync(0);
    expect(FakeSocket.last?.url).toContain('t2');
    c.stop();
  });

  it('задержки растут до 30 с', () => {
    const c = client();
    expect([0, 1, 2, 3, 4, 5, 10].map((a) => c.delay(a))).toEqual([1000, 2000, 4000, 8000, 16000, 30000, 30000]);
  });

  it('stop закрывает сокет и не переподключается', async () => {
    const c = client();
    c.start();
    await vi.runOnlyPendingTimersAsync();
    const s = FakeSocket.last;
    c.stop();
    expect(s?.closed).toBe(true);
    s?.drop();
    await vi.advanceTimersByTimeAsync(60_000);
    expect(FakeSocket.last).toBe(s);
  });
});
