import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useNavigate } from '@tanstack/react-router';
import type { FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { unwrap } from '../../api/http';
import { authApi, session } from '../../auth/session';
import { Pagination } from '../../components/Pagination';
import { useAdminPageTitle } from '../../layouts/adminSession';
import { changeClubPlan, clubQuery, extendSubscription, firstError, suspendSubscription } from './api';
import { roleLabel } from './roles';
import { LogRows, SubscriptionBadgeView } from './components';
import { saHref } from './host';
import { useSuperadminFlash } from './flash';

/*
 * Клуб — перенос resources/views/superadmin/clubs/show.blade.php (ClubController::show/extend/suspend/changePlan/view).
 */

const CARD = 'rounded-xl border border-gray-200 bg-white p-5 dark:border-gray-700 dark:bg-gray-900';
const INPUT = 'rounded-lg border border-gray-300 bg-gray-50 px-3 py-2 text-gray-900 dark:border-gray-700 dark:bg-gray-800 dark:text-gray-100';
const WEEKDAYS: readonly [number, string][] = [
  [1, 'Пн'], [2, 'Вт'], [3, 'Ср'], [4, 'Чт'], [5, 'Пт'], [6, 'Сб'], [7, 'Вс'],
];
const HOURS = Array.from({ length: 24 }, (_, h) => h);

export function ClubPage({ clubId, page }: { readonly clubId: number; readonly page?: number }) {
  const { t } = useTranslation();
  const flash = useSuperadminFlash();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { data } = useQuery(clubQuery(clubId, page));
  useAdminPageTitle(data?.name ?? '');

  const done = (r: { message: string }) => {
    flash.status(r.message);
    return queryClient.invalidateQueries({ queryKey: ['superadmin'] });
  };
  const fail = (e: unknown) => flash.error(firstError(e));
  const extend = useMutation({ mutationFn: (b: { days: number; saas_plan_id?: number }) => extendSubscription(clubId, b), onSuccess: done, onError: fail });
  const plan = useMutation({ mutationFn: (id: number) => changeClubPlan(clubId, id), onSuccess: done, onError: fail });
  const suspend = useMutation({ mutationFn: () => suspendSubscription(clubId), onSuccess: done, onError: fail });
  const view = useMutation({
    // Открыть панель клуба только для просмотра; дальше — панель клуба (dashboard).
    mutationFn: async () => {
      await unwrap(authApi.POST('/api/superadmin/clubs/{clubId}/view', { params: { path: { clubId } } }));
      await session.reloadMe();
    },
    onSuccess: () => {
      queryClient.clear();
      void navigate({ to: '/dashboard' });
    },
    onError: fail,
  });

  if (!data) return null;
  const sub = data.subscription;
  const heat = new Map(data.heatmap.map((c) => [`${c.weekday}-${c.hour}`, c]));

  const submitExtend = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    const f = new FormData(e.currentTarget);
    const planId = f.get('saas_plan_id');
    extend.mutate({ days: Number(f.get('days')), ...(planId ? { saas_plan_id: Number(planId) } : {}) });
  };
  const submitPlan = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    plan.mutate(Number(new FormData(e.currentTarget).get('saas_plan_id')));
  };
  const submitSuspend = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    if (window.confirm(t('Приостановить подписку клуба {{club}}? Клуб потеряет доступ к панели и агентам.', { club: data.name }))) suspend.mutate();
  };
  const submitView = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    view.mutate();
  };

  return (
    <>
      <div className="mb-4 flex flex-wrap items-center justify-between gap-3">
        <a href={saHref('/clubs')} className="text-sm text-gray-600 hover:underline">
          {t('← Клубы')}
        </a>
        <form method="POST" onSubmit={submitView}>
          <button type="submit" className="rounded-lg bg-violet-700 px-4 py-2 text-sm font-medium text-white hover:bg-violet-800">
            <i className="fas fa-eye mr-1" aria-hidden="true"></i>
            {t('Открыть панель клуба (просмотр)')}
          </button>
        </form>
      </div>

      <div className="grid grid-cols-1 gap-4 xl:grid-cols-3">
        <section className={CARD} aria-labelledby="club-info-title">
          <h3 id="club-info-title" className="mb-3 font-semibold text-gray-900 dark:text-white">
            {t('Клуб')}
          </h3>
          <dl className="space-y-2 text-sm">
            <div className="flex justify-between gap-3">
              <dt className="text-gray-500">{t('Адрес')}</dt>
              <dd className="font-mono">{data.slug}</dd>
            </div>
            <div className="flex justify-between gap-3">
              <dt className="text-gray-500">{t('Создан')}</dt>
              <dd className="tabular-nums">{data.created}</dd>
            </div>
            <div className="flex justify-between gap-3">
              <dt className="text-gray-500">{t('Часовой пояс')}</dt>
              <dd>{data.timezone}</dd>
            </div>
            <div className="flex justify-between gap-3">
              <dt className="text-gray-500">{t('ПК в сети')}</dt>
              <dd className="tabular-nums">
                {data.computers.online} / {data.computers.total}
              </dd>
            </div>
          </dl>
        </section>

        <section className={`${CARD} xl:col-span-2`} aria-labelledby="subscription-title">
          <div className="mb-3 flex flex-wrap items-center gap-3">
            <h3 id="subscription-title" className="font-semibold text-gray-900 dark:text-white">
              {t('Подписка')}
            </h3>
            <SubscriptionBadgeView badge={data.badge} />
          </div>
          <p className="mb-4 text-sm text-gray-600 dark:text-gray-400">
            {t('Тариф: {{plan}}', { plan: sub?.plan_name ?? '—' })}
            {sub?.paid_until && <> · {t('оплачено до {{date}}', { date: sub.paid_until })}</>}
            {sub?.trial_until && <> · {t('пробный период до {{date}}', { date: sub.trial_until })}</>}
          </p>
          <div className="grid grid-cols-1 gap-4 lg:grid-cols-3">
            <form method="POST" className="space-y-2 rounded-lg bg-gray-50 p-4 dark:bg-gray-800" onSubmit={submitExtend}>
              <h4 className="text-sm font-medium text-gray-900 dark:text-white">{t('Продлить')}</h4>
              <label className="block text-sm">
                <span className="mb-1 block text-gray-600">{t('Дней')}</span>
                <input type="number" name="days" min="1" max="3650" defaultValue="30" required className={`w-full ${INPUT}`} />
              </label>
              {sub === null && (
                <label className="block text-sm">
                  <span className="mb-1 block text-gray-600">{t('Тариф')}</span>
                  <select name="saas_plan_id" className={`w-full ${INPUT}`}>
                    {data.plans
                      .filter((p) => p.is_active)
                      .map((p) => (
                        <option key={p.id} value={p.id}>
                          {p.name}
                        </option>
                      ))}
                  </select>
                </label>
              )}
              <button type="submit" className="brand-button w-full rounded-lg px-4 py-2 text-sm" disabled={extend.isPending}>
                {t('Продлить подписку')}
              </button>
            </form>
            {sub && (
              <form method="POST" className="space-y-2 rounded-lg bg-gray-50 p-4 dark:bg-gray-800" onSubmit={submitPlan}>
                <h4 className="text-sm font-medium text-gray-900 dark:text-white">{t('Сменить тариф')}</h4>
                <select name="saas_plan_id" className={`w-full ${INPUT}`} aria-label={t('Тариф')} defaultValue={sub.saas_plan_id}>
                  {data.plans.map((p) => (
                    <option key={p.id} value={p.id}>
                      {p.name}
                      {!p.is_active && ` (${t('выключен')})`}
                    </option>
                  ))}
                </select>
                <button type="submit" className="w-full rounded-lg border border-gray-300 px-4 py-2 text-sm" disabled={plan.isPending}>
                  {t('Сохранить тариф')}
                </button>
              </form>
            )}
            {sub?.active && (
              <form method="POST" className="space-y-2 rounded-lg bg-red-50 p-4 dark:bg-red-950" onSubmit={submitSuspend}>
                <h4 className="text-sm font-medium text-red-900 dark:text-red-200">{t('Приостановить')}</h4>
                <p className="text-xs text-red-800 dark:text-red-300">{t('Клуб потеряет доступ к панели и агентам ПК, пока подписку не продлят.')}</p>
                <button type="submit" className="w-full rounded-lg bg-red-600 px-4 py-2 text-sm text-white hover:bg-red-700" disabled={suspend.isPending}>
                  {t('Приостановить подписку')}
                </button>
              </form>
            )}
          </div>
        </section>
      </div>

      {/* Тепловая карта: средняя доля занятых ПК по дням недели и часам (время клуба) за 4 недели. */}
      <section className={`${CARD} mt-6`} aria-labelledby="heatmap-title">
        <h3 id="heatmap-title" className="mb-1 font-semibold text-gray-900 dark:text-white">
          {t('Загрузка по часам')}
        </h3>
        <p className="mb-3 text-sm text-gray-600 dark:text-gray-400">{t('Средняя доля занятых ПК за последние 4 недели, время клуба.')}</p>
        {data.heatmap.length === 0 ? (
          <p className="text-sm text-gray-500">{t('Данных пока нет: снимки загрузки сохраняются каждые 5 минут.')}</p>
        ) : (
          <div className="overflow-x-auto">
            <table className="border-separate text-xs" style={{ borderSpacing: '2px' }}>
              <thead>
                <tr>
                  <th></th>
                  {HOURS.map((h) => (
                    <th key={h} className="w-7 font-normal tabular-nums text-gray-500">
                      {h}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {WEEKDAYS.map(([day, label]) => (
                  <tr key={day}>
                    <th className="pr-2 text-left font-normal text-gray-500">{t(label)}</th>
                    {HOURS.map((h) => {
                      const cell = heat.get(`${day}-${h}`);
                      return (
                        <td
                          key={h}
                          className={`h-6 w-7 rounded ${cell === undefined ? 'bg-gray-50 dark:bg-gray-800' : ''}`}
                          style={cell ? { backgroundColor: `rgb(124 58 237 / ${Math.max(0.06, cell.busy / 100)})` } : undefined}
                          title={
                            cell
                              ? t('{{day}}, {{hour}}:00 — занято {{busy}}%, в сети в среднем {{online}} ПК', { day: t(label), hour: h, busy: cell.busy, online: cell.online })
                              : undefined
                          }
                        >
                          <span className="sr-only">{cell ? `${cell.busy}%` : '—'}</span>
                        </td>
                      );
                    })}
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>

      <section className={`${CARD} mt-6`} aria-labelledby="members-title">
        <h3 id="members-title" className="mb-3 font-semibold text-gray-900 dark:text-white">
          {t('Участники клуба')}
        </h3>
        <div className="overflow-x-auto">
          <table className="w-full text-sm">
            <thead>
              <tr className="text-left text-gray-500">
                <th className="px-3 py-2">{t('Имя')}</th>
                <th className="px-3 py-2">{t('Телефон')}</th>
                <th className="px-3 py-2">{t('Роль')}</th>
                <th className="px-3 py-2">{t('Статус')}</th>
              </tr>
            </thead>
            <tbody>
              {data.members.map((m) => (
                <tr key={m.id} className="border-t border-gray-100 dark:border-gray-800">
                  <td className="px-3 py-2 text-gray-900 dark:text-white">{m.name || '—'}</td>
                  <td className="px-3 py-2 font-mono text-gray-700 dark:text-gray-300">{m.phone}</td>
                  <td className="px-3 py-2">{roleLabel(t, m.role)}</td>
                  <td className="px-3 py-2">
                    {m.blocked ? <span className="rounded-full bg-red-100 px-2 py-1 text-xs text-red-800">{t('Заблокирован')}</span> : <span className="text-gray-500">{t('Активен')}</span>}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        <div className="mt-3">
          <Pagination page={data.page} perPage={data.per_page} total={data.total} count={data.members.length} hrefFor={(p) => saHref(`/clubs/${clubId}${p > 1 ? `?page=${p}` : ''}`)} />
        </div>
      </section>

      <div className="mt-6 grid grid-cols-1 gap-4 2xl:grid-cols-2">
        <section className={CARD} aria-labelledby="club-activity-title">
          <div className="mb-3 flex items-center justify-between">
            <h3 id="club-activity-title" className="font-semibold text-gray-900 dark:text-white">
              {t('Журнал клуба')}
            </h3>
            <a href={saHref(`/logs?tab=clubs&club=${clubId}`)} className="text-sm text-violet-700 hover:underline">
              {t('Весь журнал')}
            </a>
          </div>
          <LogRows logs={data.activity} platform={false} />
        </section>
        <section className={CARD} aria-labelledby="club-platform-title">
          <div className="mb-3 flex items-center justify-between">
            <h3 id="club-platform-title" className="font-semibold text-gray-900 dark:text-white">
              {t('Действия суперадминов')}
            </h3>
            <a href={saHref(`/logs?club=${clubId}`)} className="text-sm text-violet-700 hover:underline">
              {t('Весь журнал')}
            </a>
          </div>
          <LogRows logs={data.platform_logs} platform />
        </section>
      </div>
    </>
  );
}
