import { useQuery } from '@tanstack/react-query';
import { useNavigate } from '@tanstack/react-router';
import type { FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { Pagination } from '../../components/Pagination';
import { useAdminPageTitle } from '../../layouts/adminSession';
import { clubsQuery, qs, type ClubsSearch } from './api';
import { SubscriptionBadgeView } from './components';
import { saHref } from './host';

/*
 * Клубы — перенос resources/views/superadmin/clubs/index.blade.php (ClubController::index(), ClubAttention,
 * sort-link.blade.php, subscription-badge.blade.php). Сортировка, фильтр и страницы — параметры адреса.
 */

// SORTS: ключ → направление по умолчанию.
const DEFAULT_DIRECTION: Record<string, 'asc' | 'desc'> = {
  name: 'asc', online: 'desc', busy: 'desc', agent_requests: 'desc', api_requests: 'desc',
  last_login: 'desc', sessions: 'desc', webhooks: 'desc', ends: 'asc',
};

const ICONS: Record<string, string> = { offline: 'fa-plug-circle-xmark', webhooks: 'fa-bolt', subscription: 'fa-hourglass-half' };

export function ClubsPage({ search }: { readonly search: ClubsSearch }) {
  const { t } = useTranslation();
  useAdminPageTitle(t('Клубы'));
  const navigate = useNavigate();
  const { data } = useQuery(clubsQuery(search));
  if (!data) return null;

  const href = (p: Partial<ClubsSearch>) => saHref('/clubs' + qs({ ...search, ...p }));
  // sort-link.blade.php: повторный клик меняет направление.
  const sortLink = (key: string, label: string) => {
    const next = data.sort === key ? (data.direction === 'asc' ? 'desc' : 'asc') : DEFAULT_DIRECTION[key];
    return (
      <a href={href({ sort: key, direction: next, page: undefined })} className="hover:text-violet-700">
        {label}
        {data.sort === key && ` ${data.direction === 'asc' ? '↑' : '↓'}`}
      </a>
    );
  };
  const avg = t('Среднее за {{minutes}} минут', { minutes: data.load_minutes });

  const submit = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    const f = new FormData(e.currentTarget);
    const sub = String(f.get('subscription') ?? '');
    void navigate({
      href: saHref(
        '/clubs' +
          qs({
            search: String(f.get('search') ?? ''),
            subscription: sub,
            sort: data.sort !== 'name' ? data.sort : undefined,
            direction: data.sort !== 'name' ? data.direction : undefined,
          }),
      ),
    });
  };

  return (
    <>
      <p className="mb-4 text-sm text-gray-600 dark:text-gray-400">
        {t('Всего клубов: {{total}} · с активной подпиской: {{active}}, из них на пробном периоде: {{trial}}', {
          total: data.totals.clubs,
          active: data.totals.active,
          trial: data.totals.trial,
        })}
      </p>

      {data.issues.length > 0 && (
        <section className="mb-4 rounded-xl border border-amber-300 bg-amber-50 p-4 dark:border-amber-800 dark:bg-amber-950" aria-labelledby="attention-title">
          <h3 id="attention-title" className="mb-2 font-semibold text-amber-900 dark:text-amber-200">
            <i className="fas fa-triangle-exclamation mr-1" aria-hidden="true"></i>
            {t('Требуют внимания')}
          </h3>
          <ul className="space-y-1 text-sm">
            {data.issues.map((issue, i) => (
              <li key={i} className="text-amber-900 dark:text-amber-100">
                <i className={`fas ${ICONS[issue.kind]} mr-1 w-4 text-amber-700`} aria-hidden="true"></i>
                <a href={saHref(`/clubs/${issue.club_id}`)} className="font-medium hover:underline">
                  {issue.club}
                </a>{' '}
                — {issue.text}
              </li>
            ))}
          </ul>
        </section>
      )}

      <form method="GET" className="mb-4 flex flex-wrap items-end gap-3 rounded-xl border border-gray-200 bg-white p-4 dark:border-gray-700 dark:bg-gray-900" onSubmit={submit}>
        <label className="min-w-56 flex-1">
          <span className="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-300">{t('Поиск')}</span>
          <input type="search" name="search" defaultValue={search.search ?? ''} placeholder={t('Название или адрес клуба')} className="w-full rounded-lg border border-gray-300 bg-gray-50 px-3 py-2" />
        </label>
        <label>
          <span className="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-300">{t('Подписка')}</span>
          <select name="subscription" defaultValue={search.subscription ?? ''} className="rounded-lg border border-gray-300 bg-gray-50 px-3 py-2">
            <option value="">{t('Все')}</option>
            <option value="active">{t('Активна')}</option>
            <option value="inactive">{t('Неактивна')}</option>
          </select>
        </label>
        <button type="submit" className="rounded-lg border border-gray-300 px-4 py-2 text-sm">
          {t('Показать')}
        </button>
      </form>

      <div className="overflow-x-auto rounded-xl border border-gray-200 bg-white dark:border-gray-700 dark:bg-gray-900">
        <table className="w-full text-sm">
          <thead className="border-b border-gray-200 dark:border-gray-700">
            <tr className="text-left text-gray-600">
              <th className="px-4 py-3">{sortLink('name', t('Клуб'))}</th>
              <th className="px-4 py-3 text-right">{sortLink('online', t('В сети / всего'))}</th>
              <th className="px-4 py-3 text-right">{sortLink('busy', t('Занято'))}</th>
              <th className="px-4 py-3 text-right" title={avg}>
                {sortLink('agent_requests', t('Агенты, запр./мин'))}
              </th>
              <th className="px-4 py-3 text-right" title={avg}>
                {sortLink('api_requests', t('API, запр./мин'))}
              </th>
              <th className="px-4 py-3">{sortLink('last_login', t('Вход сотрудника'))}</th>
              <th className="px-4 py-3 text-right">{sortLink('sessions', t('Сессий за сутки'))}</th>
              <th className="px-4 py-3 text-right" title={data.latest_agent ? t('Старше версии {{version}}', { version: data.latest_agent }) : t('Новая версия не объявлена')}>
                {t('Старый агент')}
              </th>
              <th className="px-4 py-3 text-right">{sortLink('webhooks', t('Ошибки вебхуков'))}</th>
              <th className="px-4 py-3">{sortLink('ends', t('Подписка'))}</th>
            </tr>
          </thead>
          <tbody>
            {data.items.map((club) => (
              <tr key={club.id} className="border-t border-gray-100 tabular-nums dark:border-gray-800">
                <td className="px-4 py-3">
                  <a href={saHref(`/clubs/${club.id}`)} className="font-medium text-violet-700 hover:underline">
                    {club.name}
                  </a>
                  <div className="text-xs text-gray-500">
                    {club.owner_name ?? '—'} · <span className="font-mono">{club.slug}</span>
                  </div>
                </td>
                <td className="px-4 py-3 text-right text-gray-700 dark:text-gray-300">
                  {club.online_count} / {club.computers_count}
                </td>
                <td className="px-4 py-3 text-right text-gray-700 dark:text-gray-300">
                  {club.busy_count}
                  {club.busy_percent != null && (
                    <>
                      {' '}
                      <span className="text-xs text-gray-500">({club.busy_percent}%)</span>
                    </>
                  )}
                </td>
                <td className="px-4 py-3 text-right text-gray-700 dark:text-gray-300">{club.agent_per_minute}</td>
                <td className="px-4 py-3 text-right text-gray-700 dark:text-gray-300">{club.api_per_minute}</td>
                <td className="px-4 py-3 text-gray-700 dark:text-gray-300" data-relative-time>
                  {club.last_login_human ?? '—'}
                </td>
                <td className="px-4 py-3 text-right text-gray-700 dark:text-gray-300">{club.sessions_today}</td>
                <td className={`px-4 py-3 text-right ${(club.outdated_agents ?? 0) > 0 ? 'text-amber-700' : 'text-gray-500'}`}>
                  {data.latest_agent ? club.outdated_agents : '—'}
                </td>
                <td className={`px-4 py-3 text-right ${club.webhook_failures > 0 ? 'text-red-600' : 'text-gray-500'}`}>{club.webhook_failures}</td>
                <td className="px-4 py-3">
                  <SubscriptionBadgeView badge={club.badge} />
                  {club.days_left != null && (
                    <div className={`mt-1 text-xs ${club.days_left <= 3 ? 'text-red-600' : 'text-gray-500'}`}>
                      {t('ещё {{count}} день|ещё {{count}} дня|ещё {{count}} дней', { count: club.days_left })}
                    </div>
                  )}
                </td>
              </tr>
            ))}
            {data.items.length === 0 && (
              <tr>
                <td colSpan={10} className="px-4 py-8 text-center text-gray-500">
                  {t('Клубы не найдены.')}
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>
      <div className="mt-4">
        <Pagination page={data.page} perPage={data.per_page} total={data.total} count={data.items.length} hrefFor={(p) => href({ page: p > 1 ? p : undefined })} />
      </div>
    </>
  );
}
