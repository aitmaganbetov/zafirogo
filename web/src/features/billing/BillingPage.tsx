import { useMutation, useQueryClient, useSuspenseQuery } from '@tanstack/react-query';
import { useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { useSession } from '../../auth/session';
import { useAdminPageTitle } from '../../layouts/adminSession';
import { formatDate } from '../../lib/datetime';
import { formatMoney } from '../../lib/money';
import { billingQuery, cancelInvoice, createInvoice, firstError, safePayUrl, type BillingSubscription, type Invoice } from './api';

/*
 * Подписка — перенос resources/views/billing/index.blade.php (BillingController::show/store/cancel): подписка клуба,
 * неоплаченный счёт с оплатой по ссылке Kaspi, форма «Продлить подписку», последние счета. Даты — в часовом поясе
 * клуба (модели ClubSubscription и SubscriptionInvoice — UsesClubTimezone). «К оплате» по срокам приходит с сервера:
 * скрипт старого шаблона считал сумму во float, здесь расчёта во фронте нет.
 */

const STATE_LABEL: Record<BillingSubscription['state'], string> = {
  expired: 'Истекла',
  inactive: 'Не активна',
  trialing: 'Пробный период',
  active: 'Активна',
};

// $statuses шаблона.
const INVOICE_STATUS: Record<Invoice['status'], string> = {
  pending: 'Ожидает оплаты',
  paid: 'Оплачен',
  canceled: 'Отменён',
};

const MONTHS_KEY = '{{count}} месяц|{{count}} месяца|{{count}} месяцев';

/** $money: number_format((float) $value, 0, '.', ' '). */
const money = (tiyn: number) => formatMoney(tiyn, 0, '.', ' ');

/** discount_until (Y-m-d) → d.m.Y. */
function dayLabel(date: string): string {
  const [y, m, d] = date.split('-');
  return `${d}.${m}.${y}`;
}

export function BillingPage() {
  const { t } = useTranslation();
  const { me } = useSession();
  const queryClient = useQueryClient();
  const { data } = useSuspenseQuery(billingQuery());
  useAdminPageTitle(t('Подписка'));
  const tz = me?.club?.timezone ?? 'UTC';
  const subscription = data.subscription;
  const pending = data.pending;
  const kaspi = data.kaspi;
  const payUrl = kaspi ? safePayUrl(kaspi.url) : null;

  const [success, setSuccess] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  // @selected($plan->id === $subscription?->saas_plan_id), @checked($loop->first)
  const [planChoice, setPlanChoice] = useState<number | null>(null);
  const [monthsChoice, setMonthsChoice] = useState<number | null>(null);
  const plan = data.plans.find((p) => p.id === planChoice) ?? data.plans.find((p) => p.id === subscription?.saas_plan_id) ?? data.plans[0];
  const months = monthsChoice ?? data.months[0];
  const total = plan?.amounts.find((a) => a.months === months)?.amount_tiyn;

  const done = (r: { message: string }) => {
    setError(null);
    setSuccess(r.message);
    return queryClient.invalidateQueries({ queryKey: ['billing'] });
  };
  const fail = (e: unknown) => {
    setSuccess(null);
    setError(firstError(e));
  };
  const open = useMutation({ mutationFn: (v: { plan: number; months: number }) => createInvoice(v.plan, v.months), onSuccess: done, onError: fail });
  const cancel = useMutation({ mutationFn: (id: number) => cancelInvoice(id), onSuccess: done, onError: fail });

  const submitOpen = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    if (plan && months !== undefined) open.mutate({ plan: plan.id, months });
  };
  const submitCancel = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    if (pending && window.confirm(t('Отменить счёт?'))) cancel.mutate(pending.id);
  };

  return (
    <div className="max-w-3xl p-6 lg:p-8">
      <h3 className="mb-6 text-xl font-semibold text-gray-900 dark:text-white">{t('Подписка клуба «{{club}}»', { club: me?.club?.name ?? '' })}</h3>
      {success && <div className="mb-4 rounded-lg border border-green-200 bg-green-50 px-4 py-3 text-sm text-green-700" role="status">{success}</div>}
      {error && <div className="mb-4 rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700" role="alert">{error}</div>}
      {subscription ? (
        <dl className="grid grid-cols-1 gap-4 sm:grid-cols-2">
          {/* __($subscription->plan->name): названия планов платформы переводятся в старом шаблоне. */}
          <div><dt className="text-sm text-gray-500">{t('План')}</dt><dd className="mt-1 font-medium text-gray-900 dark:text-white">{t(subscription.plan_name)}</dd></div>
          <div><dt className="text-sm text-gray-500">{t('Статус')}</dt><dd className="mt-1 font-medium text-gray-900 dark:text-white">{t(STATE_LABEL[subscription.state])}</dd></div>
          {subscription.status === 'trialing' && subscription.trial_ends_at && (
            <div><dt className="text-sm text-gray-500">{t('Пробный период до')}</dt><dd className="mt-1 font-medium text-gray-900 dark:text-white">{formatDate(subscription.trial_ends_at, tz, 'd.m.Y H:i')}</dd></div>
          )}
          {subscription.monthly_price_tiyn != null && subscription.monthly_total_tiyn != null && (
            <div><dt className="text-sm text-gray-500">{t('Стоимость в месяц')}</dt><dd className="mt-1 font-medium text-gray-900 dark:text-white">{t('{{price}} ₸ за ПК', { price: money(subscription.monthly_price_tiyn) })} · {t('{{amount}} ₸ за {{count}} ПК', { amount: money(subscription.monthly_total_tiyn), count: data.computers })}{subscription.discount_percent > 0 && <> <span className="text-sm font-normal text-gray-500">{subscription.discount_until ? t('со скидкой {{percent}}% до {{date}}', { percent: subscription.discount_percent, date: dayLabel(subscription.discount_until) }) : t('со скидкой {{percent}}%', { percent: subscription.discount_percent })}</span></>}</dd></div>
          )}
          {subscription.current_period_ends_at && (
            <div><dt className="text-sm text-gray-500">{t('Оплачено до')}</dt><dd className="mt-1 font-medium text-gray-900 dark:text-white">{formatDate(subscription.current_period_ends_at, tz, 'd.m.Y')}</dd></div>
          )}
        </dl>
      ) : (
        <p className="text-sm text-gray-600 dark:text-gray-300">{t('Для клуба ещё не оформлена подписка.')}</p>
      )}

      {pending ? (
        <section className="mt-8 rounded-xl border border-amber-300 bg-amber-50 p-5 dark:border-amber-700 dark:bg-amber-950/40" aria-labelledby="pending-title">
          <h4 id="pending-title" className="font-semibold text-gray-900 dark:text-white">{t('Счёт {{number}} ожидает оплаты', { number: pending.number })}</h4>
          <p className="mt-2 text-3xl font-bold text-gray-900 dark:text-white">{money(pending.amount_tiyn)} ₸</p>
          <p className="mt-1 text-sm text-gray-600 dark:text-gray-300">{t('{{plan}}, {{months}}, {{computers}} ПК × {{price}} ₸', { plan: pending.plan_name, months: t(MONTHS_KEY, { count: pending.months }), computers: pending.computers, price: money(pending.price_per_computer_tiyn) })}{pending.discount_percent > 0 && <>, {t('скидка {{percent}}%', { percent: pending.discount_percent })}</>}</p>
          {kaspi && (
            <>
              <ol className="mt-4 list-decimal space-y-1 pl-5 text-sm text-gray-700 dark:text-gray-200">
                <li>{t('Откройте оплату в Kaspi по кнопке ниже.')}</li>
                <li>{t('Переведите {{amount}} ₸.', { amount: money(pending.amount_tiyn) })}</li>
                <li>{t('В комментарии к платежу укажите номер счёта {{number}}.', { number: pending.number })}</li>
              </ol>
              {kaspi.recipient && <p className="mt-3 text-sm text-gray-600 dark:text-gray-300">{t('Получатель: {{recipient}}', { recipient: kaspi.recipient })}</p>}
              {payUrl && (
                <a href={payUrl} target="_blank" rel="noopener noreferrer" className="mt-4 inline-flex items-center gap-2 rounded-lg bg-[#f14635] px-5 py-2.5 font-semibold text-white hover:bg-[#d93a2b]">{t('Оплатить в Kaspi')}<i className="fas fa-arrow-up-right-from-square text-sm" aria-hidden="true"></i></a>
              )}
            </>
          )}
          <p className="mt-4 text-sm text-gray-600 dark:text-gray-300">{t('Подписка продлится, как только мы увидим оплату — обычно в течение рабочего дня.')}</p>
          {data.can_pay && (
            <form method="POST" className="mt-3" onSubmit={submitCancel}>
              <button type="submit" disabled={cancel.isPending} className="text-sm font-medium text-gray-600 underline hover:text-gray-900 dark:text-gray-300 dark:hover:text-white">{t('Отменить счёт')}</button>
            </form>
          )}
        </section>
      ) : !data.can_pay ? (
        <p className="mt-8 text-sm text-gray-500">{t('Продлить подписку может владелец клуба.')}</p>
      ) : !plan || !kaspi ? (
        <p className="mt-8 text-sm text-gray-500">{t('Оплата пока не настроена. Напишите в поддержку Zafiro.')}</p>
      ) : (
        <section className="mt-8 rounded-xl border border-gray-200 bg-white p-5 dark:border-gray-700 dark:bg-gray-900" aria-labelledby="pay-title">
          <h4 id="pay-title" className="font-semibold text-gray-900 dark:text-white">{t('Продлить подписку')}</h4>
          <form method="POST" className="mt-4 space-y-4" onSubmit={submitOpen}>
            <div>
              <label htmlFor="saas_plan_id" className="mb-1 block text-sm text-gray-600 dark:text-gray-300">{t('Тариф')}</label>
              <select id="saas_plan_id" name="saas_plan_id" value={plan.id} onChange={(e) => { setPlanChoice(Number(e.target.value)); }} className="w-full rounded-lg border border-gray-300 bg-gray-50 px-3 py-2 text-gray-900 dark:border-gray-700 dark:bg-gray-800 dark:text-gray-100">
                {data.plans.map((p) => (
                  <option key={p.id} value={p.id}>{p.name} — {t('{{price}} ₸ за ПК в месяц', { price: money(p.price_monthly_tiyn) })}</option>
                ))}
              </select>
            </div>
            <fieldset>
              <legend className="mb-1 text-sm text-gray-600 dark:text-gray-300">{t('Срок')}</legend>
              <div className="flex flex-wrap gap-2">
                {data.months.map((m) => (
                  <label key={m} className="cursor-pointer"><input type="radio" name="months" value={m} className="peer sr-only" checked={m === months} onChange={() => { setMonthsChoice(m); }} /><span className="block rounded-lg border border-gray-300 px-4 py-2 text-sm text-gray-700 peer-checked:border-[#ff0169] peer-checked:bg-[#ff0169]/10 peer-checked:text-[#d90058] peer-focus-visible:ring-2 peer-focus-visible:ring-[#ff0169] dark:border-gray-700 dark:text-gray-200">{t(MONTHS_KEY, { count: m })}</span></label>
                ))}
              </div>
            </fieldset>
            <p className="text-sm text-gray-600 dark:text-gray-300">{t('ПК в клубе: {{count}}. Сумма считается за все ПК клуба.', { count: data.computers })}{data.discount_percent > 0 && <> {t('Ваша скидка: {{percent}}%.', { percent: data.discount_percent })}</>}</p>
            <p className="text-lg font-semibold text-gray-900 dark:text-white">{t('К оплате')}: <span data-billing-total>{total != null ? money(total) : ''}</span> ₸</p>
            <button type="submit" disabled={open.isPending} className="brand-button rounded-lg px-5 py-2 text-sm">{t('Выставить счёт')}</button>
          </form>
        </section>
      )}

      {data.invoices.length > 0 && (
        <section className="mt-10" aria-labelledby="invoices-title">
          <h4 id="invoices-title" className="mb-3 font-semibold text-gray-900 dark:text-white">{t('Счета')}</h4>
          <div className="overflow-x-auto rounded-xl border border-gray-200 dark:border-gray-700">
            <table className="w-full text-left text-sm">
              <thead className="bg-gray-50 text-gray-500 dark:bg-gray-800 dark:text-gray-400"><tr><th className="px-4 py-2 font-medium">{t('Счёт')}</th><th className="px-4 py-2 font-medium">{t('Дата')}</th><th className="px-4 py-2 font-medium">{t('Срок')}</th><th className="px-4 py-2 font-medium">{t('Сумма')}</th><th className="px-4 py-2 font-medium">{t('Статус')}</th></tr></thead>
              <tbody className="divide-y divide-gray-100 text-gray-800 dark:divide-gray-800 dark:text-gray-200">
                {data.invoices.map((invoice) => (
                  <tr key={invoice.id}><td className="px-4 py-2">{invoice.number}</td><td className="px-4 py-2">{formatDate(invoice.created_at, tz, 'd.m.Y')}</td><td className="px-4 py-2">{t(MONTHS_KEY, { count: invoice.months })}</td><td className="px-4 py-2">{money(invoice.amount_tiyn)} ₸</td><td className="px-4 py-2">{t(INVOICE_STATUS[invoice.status])}{invoice.period_ends_at && <> · {t('до {{date}}', { date: formatDate(invoice.period_ends_at, tz, 'd.m.Y') })}</>}</td></tr>
                ))}
              </tbody>
            </table>
          </div>
        </section>
      )}
    </div>
  );
}
