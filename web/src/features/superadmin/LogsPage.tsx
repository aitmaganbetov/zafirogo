import { useQuery } from '@tanstack/react-query';
import { useNavigate } from '@tanstack/react-router';
import type { FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { Pagination } from '../../components/Pagination';
import { useAdminPageTitle } from '../../layouts/adminSession';
import { logsQuery, qs, type LogsSearch } from './api';
import { LogRows } from './components';
import { saHref } from './host';

/* Журнал — перенос resources/views/superadmin/logs/index.blade.php (Superadmin\LogController). */

export function LogsPage({ search }: { readonly search: LogsSearch }) {
  const { t } = useTranslation();
  useAdminPageTitle(t('Журнал'));
  const navigate = useNavigate();
  const { data } = useQuery(logsQuery(search));
  if (!data) return null;
  const platform = data.tab === 'platform';

  const submit = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    const f = new FormData(e.currentTarget);
    void navigate({ href: saHref('/logs' + qs({ tab: platform ? 'platform' : 'clubs', club: data.club?.id, event: String(f.get('event') ?? '') })) });
  };

  return (
    <>
      <nav className="mb-4 flex gap-2" aria-label={t('Журналы')}>
        <a
          href={saHref('/logs' + qs({ club: search.club }))}
          className={`rounded-lg px-4 py-2 text-sm ${platform ? 'bg-violet-700 text-white' : 'border border-gray-300'}`}
          aria-current={platform ? 'page' : undefined}
        >
          {t('Действия суперадминов')}
        </a>
        <a
          href={saHref('/logs' + qs({ tab: 'clubs', club: search.club }))}
          className={`rounded-lg px-4 py-2 text-sm ${!platform ? 'bg-violet-700 text-white' : 'border border-gray-300'}`}
          aria-current={!platform ? 'page' : undefined}
        >
          {t('Журналы клубов')}
        </a>
      </nav>

      <form method="GET" className="mb-4 flex flex-wrap items-end gap-3 rounded-xl border border-gray-200 bg-white p-4 dark:border-gray-700 dark:bg-gray-900" onSubmit={submit}>
        <label>
          <span className="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-300">{t('Событие')}</span>
          <select name="event" defaultValue={search.event ?? ''} className="rounded-lg border border-gray-300 bg-gray-50 px-3 py-2">
            <option value="">{t('Все')}</option>
            {data.events.map((ev) => (
              <option key={ev.event} value={ev.event}>
                {t(ev.label)}
              </option>
            ))}
          </select>
        </label>
        <button type="submit" className="rounded-lg border border-gray-300 px-4 py-2 text-sm">
          {t('Показать')}
        </button>
        {data.club && (
          <a href={saHref('/logs' + qs({ tab: platform ? 'platform' : 'clubs' }))} className="text-sm text-violet-700 hover:underline">
            {t('Клуб «{{club}}» · показать все клубы', { club: data.club.name })}
          </a>
        )}
      </form>

      <div className="rounded-xl border border-gray-200 bg-white p-2 dark:border-gray-700 dark:bg-gray-900">
        <LogRows logs={data.items} platform={platform} />
      </div>
      <div className="mt-4">
        <Pagination
          page={data.page}
          perPage={data.per_page}
          total={data.total}
          count={data.items.length}
          hrefFor={(p) => saHref('/logs' + qs({ tab: search.tab, club: search.club, event: search.event, page: p > 1 ? p : undefined }))}
        />
      </div>
    </>
  );
}
