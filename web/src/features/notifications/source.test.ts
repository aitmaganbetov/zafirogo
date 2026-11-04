import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { RealtimeClient } from '../../realtime/client';
import { notificationsSource } from './source';

describe('колокольчик: источник уведомлений', () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => {
    vi.useRealTimers();
    vi.restoreAllMocks();
  });

  it('перечитывает список по событиям WebSocket (одно чтение на пачку событий) и отмечает просмотренным', async () => {
    const payload = { unread_count: 1, items: [{ id: 'chat-1', url: '/chat', icon: 'fa-comments', title: 'T', body: 'B', unread: true, at: 'x', at_human: 'y' }] };
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockImplementation(async (input) => {
      const url = new URL((input as Request).url);
      if (url.pathname === '/api/notifications/seen') return new Response(JSON.stringify({ unread_count: 0 }), { headers: { 'Content-Type': 'application/json' } });
      return new Response(JSON.stringify(payload), { headers: { 'Content-Type': 'application/json' } });
    });
    const client = new RealtimeClient({ ticket: async () => 't', createSocket: () => ({ close() {} }) as unknown as WebSocket, url: () => 'ws://x' });
    const src = notificationsSource(client);
    expect(await src.load()).toEqual(payload);

    const onChange = vi.fn();
    const off = src.subscribe?.(onChange);
    client.dispatch({ id: '1', type: 'notifications.changed', at: '', data: {} });
    client.dispatch({ id: '2', type: 'chat.unread', at: '', data: { unread: 1 } });
    client.dispatch({ id: '3', type: 'computer.state', at: '', data: {} });
    await vi.advanceTimersByTimeAsync(400);
    expect(onChange).toHaveBeenCalledTimes(1);
    expect(onChange).toHaveBeenCalledWith(payload);
    off?.();

    await src.markSeen();
    const paths = fetchMock.mock.calls.map(([r]) => `${(r as Request).method} ${new URL((r as Request).url).pathname}`);
    expect(paths).toContain('POST /api/notifications/seen');
    expect(paths.filter((p) => p === 'GET /api/notifications')).toHaveLength(2);
  });
});
