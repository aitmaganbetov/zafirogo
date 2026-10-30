import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useEffect, useRef, type FormEvent, type KeyboardEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { useSession } from '../../auth/session';
import { useAdminPageTitle } from '../../layouts/adminSession';
import { formatDate } from '../../lib/datetime';
import { BADGES_KEY } from '../../realtime/badges';
import { useInvalidateOn } from '../../realtime/hooks';
import { chatQuery, replyChat, type ChatMessage, type ChatPage as ChatPageData } from './api';

/*
 * Чат с клиентами — перенос resources/views/chat/index.blade.php (ChatController::index()). Опрос /chat/poll раз в
 * 5 с и location.reload() при новых непрочитанных заменены событием chat.message: страница перечитывается сама
 * (текст сообщений — только по REST, в событии лишь id, docs/realtime.md). Ответ — Enter, перенос — Shift+Enter.
 */

/** ChatMessage::MAX_LENGTH. */
const MAX_LENGTH = 1000;

export function ChatPage({ customer }: { customer?: number }) {
  const { t } = useTranslation();
  const { me } = useSession();
  const queryClient = useQueryClient();
  useAdminPageTitle(t('Чат с клиентами'));
  useInvalidateOn(['chat.message'], ['chat']);
  const { data } = useQuery(chatQuery(customer));
  const tz = me?.club?.timezone ?? 'UTC';
  const threadRef = useRef<HTMLOListElement>(null);
  const formRef = useRef<HTMLFormElement>(null);

  // Открытие диалога отвечающим помечает сообщения прочитанными — бейдж «Чат» берёт счётчик из ответа.
  useEffect(() => {
    if (data) queryClient.setQueryData(BADGES_KEY, (old: { chat: number; orders: number } | undefined) => ({ chat: data.unread, orders: old?.orders ?? 0 }));
  }, [data, queryClient]);

  const last = data?.messages.at(-1)?.id ?? 0;
  useEffect(() => {
    const el = threadRef.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [last]);

  const reply = useMutation({
    mutationFn: (body: string) => replyChat(data?.customer?.id ?? 0, body),
    onSuccess: (r) => {
      queryClient.setQueryData(chatQuery(customer).queryKey, (old: ChatPageData | undefined) =>
        old && !old.messages.some((m) => m.id === r.message.id) ? { ...old, messages: [...old.messages, r.message] } : old,
      );
      formRef.current?.reset();
    },
  });

  const submit = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    const body = String(new FormData(e.currentTarget).get('body') ?? '').trim();
    if (body !== '' && !reply.isPending) reply.mutate(body);
  };
  const onKey = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault();
      formRef.current?.requestSubmit();
    }
  };

  if (!data) return null;
  const meta = (m: ChatMessage) => [m.created_at ? formatDate(m.created_at, tz, 'd.m H:i') : '', m.author ?? '', m.computer ?? ''].filter(Boolean).join(' · ');

  return (
    <div className="p-6 lg:p-8">
      <header className="mb-6">
        <h3 className="mb-2 text-2xl font-bold text-gray-900 dark:text-white">{t('Чат с клиентами')}</h3>
        <p className="text-sm text-gray-600 dark:text-gray-400">
          {data.can_answer ? t('Клиенты пишут из агента на ПК. Ответ сразу придёт клиенту на ПК.') : t('Отвечают кассиры и операторы на смене; вам чат доступен для просмотра.')}
        </p>
      </header>

      <div className="grid gap-4 lg:grid-cols-[320px_minmax(0,1fr)]">
        <nav className="max-h-[70vh] overflow-y-auto rounded-xl border border-gray-200 bg-white dark:border-gray-800 dark:bg-gray-900" aria-label={t('Диалоги')}>
          {data.conversations.length === 0 && <p className="px-4 py-10 text-center text-sm text-gray-500 dark:text-gray-400">{t('Сообщений от клиентов пока нет.')}</p>}
          {data.conversations.map((c) => (
            <a
              key={c.customer_id}
              href={`/chat?customer=${c.customer_id}`}
              className={`flex items-center gap-3 border-b border-gray-100 px-4 py-3 dark:border-gray-800 ${data.customer?.id === c.customer_id ? 'bg-green-50 dark:bg-green-900/20' : 'hover:bg-gray-50 dark:hover:bg-gray-800'}`}
            >
              <span className="min-w-0 flex-1">
                <strong className="block truncate text-sm text-gray-900 dark:text-white">{c.customer_name ?? '—'}</strong>
                <small className="block truncate text-xs text-gray-500 dark:text-gray-400">
                  {c.last_from_staff ? `${t('Вы')}: ` : ''}
                  {c.last_body}
                </small>
              </span>
              {c.unread > 0 && <span className="rounded-full bg-red-600 px-2 py-0.5 text-xs font-bold text-white">{c.unread}</span>}
            </a>
          ))}
        </nav>

        <section className="flex min-h-[50vh] flex-col rounded-xl border border-gray-200 bg-white dark:border-gray-800 dark:bg-gray-900" aria-label={t('Переписка')}>
          {data.customer ? (
            <>
              <header className="border-b border-gray-100 px-4 py-3 dark:border-gray-800">
                <strong className="text-gray-900 dark:text-white">{data.customer.name}</strong> <span className="text-sm text-gray-500">{data.customer.phone}</span>
              </header>
              <ol className="flex-1 space-y-2 overflow-y-auto p-4" data-chat-thread ref={threadRef}>
                {data.messages.map((m) => (
                  <li key={m.id} className={`flex ${m.from_staff ? 'justify-end' : 'justify-start'}`}>
                    <div className={`max-w-[75%] rounded-xl px-3 py-2 text-sm ${m.from_staff ? 'bg-green-600 text-white' : 'bg-gray-100 text-gray-900 dark:bg-gray-800 dark:text-white'}`}>
                      <p className="whitespace-pre-line break-words">{m.body}</p>
                      <small className="mt-1 block text-xs opacity-75">{meta(m)}</small>
                    </div>
                  </li>
                ))}
              </ol>
              {data.can_answer && (
                <form method="POST" className="flex gap-2 border-t border-gray-100 p-3 dark:border-gray-800" data-chat-form ref={formRef} onSubmit={submit}>
                  <textarea
                    name="body"
                    rows={2}
                    maxLength={MAX_LENGTH}
                    required
                    placeholder={t('Ответ клиенту…')}
                    aria-label={t('Ответ клиенту')}
                    className="min-w-0 flex-1 rounded-lg border border-gray-300 bg-gray-50 px-3 py-2 text-sm text-gray-900 dark:border-gray-700 dark:bg-gray-800 dark:text-white"
                    onKeyDown={onKey}
                  ></textarea>
                  <button type="submit" className="brand-button rounded-lg px-4 text-sm" disabled={reply.isPending}>
                    <i className="fas fa-paper-plane"></i>
                    {t('Отправить')}
                  </button>
                </form>
              )}
            </>
          ) : (
            <p className="m-auto p-10 text-sm text-gray-500 dark:text-gray-400">{t('Выберите диалог слева.')}</p>
          )}
        </section>
      </div>
    </div>
  );
}
