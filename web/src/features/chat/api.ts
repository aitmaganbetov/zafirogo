import { queryOptions } from '@tanstack/react-query';
import { createApiClient, unwrap } from '../../api/http';
import type { components, paths } from '../../api/gen/chat';

/*
 * Чат с клиентами (api/chat.yaml). Laravel: ChatController. Ключи Query — ['chat', …]; новые сообщения —
 * событие chat.message (WebSocket) вместо опроса /chat/poll раз в 5 с.
 */

export const chatApi = createApiClient<paths>();

type S = components['schemas'];
export type ChatPage = S['ChatPage'];
export type ChatMessage = S['ChatMessage'];

export interface ChatSearch {
  readonly customer?: number;
}

export function parseChatSearch(s: Record<string, unknown>): ChatSearch {
  const n = typeof s.customer === 'number' ? s.customer : typeof s.customer === 'string' ? Number(s.customer) : NaN;
  return { customer: Number.isSafeInteger(n) && n > 0 ? n : undefined };
}

export const chatQuery = (customer?: number) =>
  queryOptions({
    queryKey: ['chat', customer ?? 0],
    queryFn: () => unwrap(chatApi.GET('/api/chat', { params: { query: { customer } } })),
  });

export const replyChat = (customerId: number, body: string) =>
  unwrap(chatApi.POST('/api/chat/{customerId}/messages', { params: { path: { customerId } }, body: { body } }));
