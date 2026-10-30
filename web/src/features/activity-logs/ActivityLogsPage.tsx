import { useQuery } from '@tanstack/react-query';
import { useNavigate } from '@tanstack/react-router';
import type { FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { useSession } from '../../auth/session';
import { SimplePagination } from '../../components/SimplePagination';
import { useAdminPageTitle } from '../../layouts/adminSession';
import { formatDate } from '../../lib/datetime';
import { activityLogsHref, activityLogsQuery, numberFormat, type ActivityLogsSearch } from './api';

/*
 * Логи действий — перенос resources/views/activity-logs/index.blade.php (ActivityLogController::index()).
 * Отличие: страницы журнала — keyset (activity_logs партиционирована, CLAUDE.md «Производительность»), поэтому
 * вместо номеров страниц — «Назад»/«Вперёд» (вид simple-tailwind Laravel). Описания и ярлыки — на языке клуба
 * (переводит сервер: ключ + параметры, W-3).
 */

const FIELD = 'w-full rounded-lg border border-gray-300 bg-gray-50 px-3 py-2 text-sm text-gray-900 dark:border-gray-700 dark:bg-gray-800 dark:text-white';
const LABEL = 'mb-2 block text-xs font-medium text-gray-600 dark:text-gray-400';

export function ActivityLogsPage({ search }: { search: ActivityLogsSearch }) {
  const { t } = useTranslation();
  const { me } = useSession();
  const navigate = useNavigate();
  useAdminPageTitle(t('Логи действий'));
  const { data } = useQuery(activityLogsQuery(search));
  const tz = me?.club?.timezone ?? 'UTC';

  const submit = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    const f = new FormData(e.currentTarget);
    const v = (k: string) => {
      const s = String(f.get(k) ?? '');
      return s === '' ? undefined : s;
    };
    void navigate({ href: activityLogsHref({ from: v('from'), to: v('to'), event: v('event'), user_id: v('user_id'), search: v('search') }) });
  };

  if (!data) return null;
  const labels = new Map(data.events.map((e) => [e.event, e.label]));
  const filters = { from: search.from, to: search.to, event: search.event, user_id: search.user_id, search: search.search };
  const back = search.back ? search.back.split('.') : [];
  // Стек курсоров: back — курсоры страниц до текущей (без первой, у неё курсора нет).
  const join = (list: string[]) => (list.length > 0 ? list.join('.') : undefined);
  const prevHref =
    search.cursor === undefined
      ? null
      : activityLogsHref(back.length > 0 ? { ...filters, cursor: back.at(-1), back: join(back.slice(0, -1)) } : filters);
  const nextHref = data.next_cursor
    ? activityLogsHref({ ...filters, cursor: data.next_cursor, back: join(search.cursor ? [...back, search.cursor] : []) })
    : null;

  return (
    <div className="p-6 lg:p-8">
      <header className="mb-6 flex flex-wrap items-end justify-between gap-4">
        <div>
          <h3 className="mb-2 text-2xl font-bold text-gray-900 dark:text-white">{t('Логи действий')}</h3>
          <p className="text-sm text-gray-600 dark:text-gray-400">{t('Операции склада, кассы и входы пользователей.')}</p>
        </div>
        <span className="text-sm text-gray-500 dark:text-gray-400">{t('Записей: {{count}}', { count: numberFormat(data.total) })}</span>
      </header>

      <form
        method="GET"
        action="/activity-logs"
        className="mb-6 grid grid-cols-1 items-end gap-3 rounded-xl border border-gray-200 bg-white p-4 dark:border-gray-800 dark:bg-gray-900 md:grid-cols-2 xl:grid-cols-5"
        onSubmit={submit}
      >
        <div>
          <label htmlFor="log-from" className={LABEL}>
            {t('С даты')}
          </label>
          <input id="log-from" name="from" type="date" defaultValue={search.from ?? ''} className={FIELD} />
        </div>
        <div>
          <label htmlFor="log-to" className={LABEL}>
            {t('По дату')}
          </label>
          <input id="log-to" name="to" type="date" defaultValue={search.to ?? ''} className={FIELD} />
        </div>
        <div>
          <label htmlFor="log-event" className={LABEL}>
            {t('Тип события')}
          </label>
          <select id="log-event" name="event" defaultValue={search.event ?? ''} className={FIELD}>
            <option value="">{t('Все события')}</option>
            {data.events.map((e) => (
              <option key={e.event} value={e.event}>
                {e.label}
              </option>
            ))}
          </select>
        </div>
        <div>
          <label htmlFor="log-user" className={LABEL}>
            {t('Пользователь')}
          </label>
          <select id="log-user" name="user_id" defaultValue={search.user_id ?? ''} className={FIELD}>
            <option value="">{t('Все пользователи')}</option>
            {data.users.map((u) => (
              <option key={u.id} value={u.id}>
                {u.name} · {u.phone}
              </option>
            ))}
          </select>
        </div>
        <div className="flex gap-2">
          <input
            name="search"
            defaultValue={search.search ?? ''}
            maxLength={120}
            placeholder={t('Описание, имя, телефон')}
            aria-label={t('Поиск по журналу')}
            className="min-w-0 flex-1 rounded-lg border border-gray-300 bg-gray-50 px-3 py-2 text-sm text-gray-900 dark:border-gray-700 dark:bg-gray-800 dark:text-white"
          />
          <button type="submit" className="brand-button">
            <i className="fas fa-filter"></i>
            {t('Фильтр')}
          </button>
        </div>
        <div className="xl:col-span-5">
          <a href="/activity-logs" className="text-xs font-medium text-gray-500 hover:text-gray-800 dark:text-gray-400 dark:hover:text-white">
            {t('Сбросить фильтры')}
          </a>
        </div>
      </form>

      <div className="overflow-x-auto rounded-xl border border-gray-200 bg-white dark:border-gray-800 dark:bg-gray-900">
        <table className="w-full min-w-[850px] text-left text-sm">
          <thead className="border-b border-gray-200 bg-gray-50 text-xs uppercase text-gray-500 dark:border-gray-800 dark:bg-gray-950 dark:text-gray-400">
            <tr>
              <th className="px-4 py-3 font-semibold">{t('Когда')}</th>
              <th className="px-4 py-3 font-semibold">{t('Пользователь')}</th>
              <th className="px-4 py-3 font-semibold">{t('Событие')}</th>
              <th className="px-4 py-3 font-semibold">{t('Описание')}</th>
              <th className="px-4 py-3 font-semibold">IP</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-gray-100 dark:divide-gray-800">
            {data.items.length === 0 && (
              <tr>
                <td colSpan={5} className="px-4 py-12 text-center text-sm text-gray-500 dark:text-gray-400">
                  {t('По выбранным фильтрам записей нет.')}
                </td>
              </tr>
            )}
            {data.items.map((log) => (
              <tr key={log.id}>
                <td className="whitespace-nowrap px-4 py-3 text-gray-600 dark:text-gray-400">{formatDate(log.created_at, tz, 'd.m.Y H:i:s')}</td>
                <td className="px-4 py-3 text-gray-800 dark:text-gray-200">{log.user_name ?? t('Система')}</td>
                <td className="px-4 py-3">
                  <span className="inline-flex rounded-full bg-gray-100 px-2 py-1 text-xs font-medium text-gray-700 dark:bg-gray-800 dark:text-gray-300">{labels.get(log.event) ?? log.event}</span>
                </td>
                <td className="px-4 py-3 text-gray-700 dark:text-gray-300">
                  {log.description}
                  {log.details.length > 0 && (
                    <div className="mt-1 flex flex-wrap gap-x-3 gap-y-1 text-xs text-gray-500 dark:text-gray-400">
                      {log.details.map((d) => (
                        <span key={d.key}>
                          {d.key}: {d.value}
                        </span>
                      ))}
                    </div>
                  )}
                </td>
                <td className="px-4 py-3 text-xs text-gray-500 dark:text-gray-400">{log.ip_address ?? '—'}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <div className="mt-4">
        <SimplePagination prevHref={prevHref} nextHref={nextHref} />
      </div>
    </div>
  );
}
