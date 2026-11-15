import { useQuery } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { Pagination } from '../../components/Pagination';
import { useAdminPageTitle } from '../../layouts/adminSession';
import { formatMoney } from '../../lib/money';
import { qs } from '../superadmin/api';
import { cabinetQuery, type CabinetSearch, type ResellerCabinet } from './api';
import { resellerHref } from './host';
import { formatPercent } from './percent';

/*
 * Кабинет реселлера — перенос resources/views/reseller/dashboard.blade.php (ResellerController::dashboard()): код и
 * условия, заработано / выплачено / к выплате, клубы, начисления и выплаты. Суммы считает сервер (целые тиыны).
 */

const CLUBS_KEY = '{{count}} клуб|{{count}} клуба|{{count}} клубов';
const MONTHS_KEY = '{{count}} месяц|{{count}} месяца|{{count}} месяцев';

/** $money: number_format((float) $value, 0, '.', ' ').' ₸'. */
const money = (tiyn: number) => formatMoney(tiyn, 0, '.', ' ') + ' ₸';

type ClubRow = ResellerCabinet['clubs']['items'][number];

export function DashboardPage({ search }: { readonly search: CabinetSearch }) {
  const { t } = useTranslation();
  const { data } = useQuery(cabinetQuery(search));
  useAdminPageTitle(t('Мои клубы и доход'));
  if (!data) return null;

  const subscription = (club: ClubRow) => {
    switch (club.subscription) {
      case 'active':
        return t('{{plan}} до {{date}}', { plan: club.plan_name ?? '—', date: club.subscription_until ?? '' });
      case 'trial':
        return t('Пробный период до {{date}}', { date: club.subscription_until ?? '' });
      default:
        return t('Не оплачена');
    }
  };
  // Каждый список листается своим параметром; второй сохраняется (appends, как withQueryString()).
  const pageHref = (name: 'clubs_page' | 'earnings_page') => (p: number) =>
    resellerHref(qs({ clubs_page: search.clubs_page, earnings_page: search.earnings_page, [name]: p > 1 ? p : undefined }));

  return (
    <>
      <div className="mb-6 grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
        <div className="rounded-xl border border-gray-200 bg-white p-4 dark:border-gray-700 dark:bg-gray-900">
          <div className="text-sm text-gray-500">{t('Ваш код')}</div>
          <div className="mt-1 font-mono text-2xl font-bold text-gray-900 dark:text-white">{data.code}</div>
          <div className="mt-1 text-xs text-gray-500">{t('Клуб вводит его в «Настройки → Настройка клуба → Реселлер».')}</div>
        </div>
        <div className="rounded-xl border border-gray-200 bg-white p-4 dark:border-gray-700 dark:bg-gray-900">
          <div className="text-sm text-gray-500">{t('Ваш процент с оплат')}</div>
          <div className="mt-1 text-2xl font-bold text-gray-900 dark:text-white">{formatPercent(data.commission_bp)}%</div>
          <div className="mt-1 text-xs text-gray-500">{t('Скидка для ваших клубов: {{percent}}%', { percent: data.discount_percent })}</div>
        </div>
        <div className="rounded-xl border border-gray-200 bg-white p-4 dark:border-gray-700 dark:bg-gray-900">
          <div className="text-sm text-gray-500">{t('Заработано всего')}</div>
          <div className="mt-1 text-2xl font-bold text-gray-900 dark:text-white">{money(data.earned_tiyn)}</div>
          <div className="mt-1 text-xs text-gray-500">{t('Выплачено: {{amount}}', { amount: money(data.paid_out_tiyn) })}</div>
        </div>
        <div className="rounded-xl border border-violet-200 bg-violet-50 p-4 dark:border-violet-800 dark:bg-violet-950/40">
          <div className="text-sm text-violet-700 dark:text-violet-300">{t('К выплате')}</div>
          <div className="mt-1 text-2xl font-bold text-violet-900 dark:text-white">{money(data.balance_tiyn)}</div>
        </div>
      </div>
      {!data.is_active && (
        <div className="mb-4 rounded-lg border border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-800" role="status">
          {t('Ваш код приостановлен: новые клубы не могут его ввести, а оплаты идут без скидки и процента. Свяжитесь с Zafiro.')}
        </div>
      )}

      <section className="mb-6 rounded-xl border border-gray-200 bg-white dark:border-gray-700 dark:bg-gray-900" aria-labelledby="reseller-clubs">
        <h3 id="reseller-clubs" className="border-b border-gray-200 px-4 py-3 font-semibold text-gray-900 dark:border-gray-700 dark:text-white">{t(CLUBS_KEY, { count: data.clubs.total })}</h3>
        <div className="overflow-x-auto">
          <table className="w-full text-sm">
            <thead className="text-left text-gray-500"><tr><th className="px-4 py-2">{t('Клуб')}</th><th className="px-4 py-2">{t('Подписка')}</th><th className="px-4 py-2">{t('С вами с')}</th></tr></thead>
            <tbody>
              {data.clubs.items.map((club, i) => (
                <tr key={i} className="border-t border-gray-100 dark:border-gray-800">
                  <td className="px-4 py-2 text-gray-900 dark:text-white">{club.name}{club.city && <div className="text-xs text-gray-500">{t(club.city)}</div>}</td>
                  <td className="px-4 py-2 text-gray-700 dark:text-gray-300">{subscription(club)}</td>
                  <td className="px-4 py-2 text-gray-500">{club.attached ?? '—'}</td>
                </tr>
              ))}
              {data.clubs.items.length === 0 && (
                <tr><td colSpan={3} className="px-4 py-6 text-center text-gray-500">{t('Пока ни один клуб не ввёл ваш код.')}</td></tr>
              )}
            </tbody>
          </table>
        </div>
        <div className="px-4 py-2"><Pagination page={data.clubs.page} perPage={data.clubs.per_page} total={data.clubs.total} count={data.clubs.items.length} hrefFor={pageHref('clubs_page')} /></div>
      </section>

      <section className="mb-6 rounded-xl border border-gray-200 bg-white dark:border-gray-700 dark:bg-gray-900" aria-labelledby="reseller-earnings">
        <h3 id="reseller-earnings" className="border-b border-gray-200 px-4 py-3 font-semibold text-gray-900 dark:border-gray-700 dark:text-white">{t('Начисления')}</h3>
        <div className="overflow-x-auto">
          <table className="w-full text-sm">
            <thead className="text-left text-gray-500"><tr><th className="px-4 py-2">{t('Дата')}</th><th className="px-4 py-2">{t('Клуб')}</th><th className="px-4 py-2">{t('Оплата клуба')}</th><th className="px-4 py-2">{t('Ваш доход')}</th></tr></thead>
            <tbody>
              {data.earnings.items.map((invoice, i) => (
                <tr key={i} className="border-t border-gray-100 dark:border-gray-800">
                  <td className="px-4 py-2 text-gray-500">{invoice.paid}</td>
                  <td className="px-4 py-2 text-gray-900 dark:text-white">{invoice.club_name}</td>
                  <td className="px-4 py-2 text-gray-700 dark:text-gray-300">{money(invoice.amount_tiyn)} · {t(MONTHS_KEY, { count: invoice.months })}</td>
                  <td className="px-4 py-2 font-medium text-gray-900 dark:text-white">{money(invoice.reseller_amount_tiyn)} <span className="text-xs font-normal text-gray-500">({formatPercent(invoice.reseller_bp)}%)</span></td>
                </tr>
              ))}
              {data.earnings.items.length === 0 && (
                <tr><td colSpan={4} className="px-4 py-6 text-center text-gray-500">{t('Начислений пока нет: они появятся, когда ваши клубы оплатят подписку.')}</td></tr>
              )}
            </tbody>
          </table>
        </div>
        <div className="px-4 py-2"><Pagination page={data.earnings.page} perPage={data.earnings.per_page} total={data.earnings.total} count={data.earnings.items.length} hrefFor={pageHref('earnings_page')} /></div>
      </section>

      {data.payouts.length > 0 && (
        <section className="rounded-xl border border-gray-200 bg-white dark:border-gray-700 dark:bg-gray-900" aria-labelledby="reseller-payouts">
          <h3 id="reseller-payouts" className="border-b border-gray-200 px-4 py-3 font-semibold text-gray-900 dark:border-gray-700 dark:text-white">{t('Выплаты')}</h3>
          <ul className="divide-y divide-gray-100 text-sm dark:divide-gray-800">
            {data.payouts.map((payout, i) => (
              <li key={i} className="flex justify-between gap-4 px-4 py-2"><span className="text-gray-500">{payout.date}{payout.note && <> · {payout.note}</>}</span><span className="font-medium text-gray-900 dark:text-white">{money(payout.amount_tiyn)}</span></li>
            ))}
          </ul>
        </section>
      )}
    </>
  );
}
