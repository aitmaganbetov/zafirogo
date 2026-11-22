import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { isApiError } from '../../api/http';
import { Pagination } from '../../components/Pagination';
import { useAdminPageTitle } from '../../layouts/adminSession';
import { formatMoney } from '../../lib/money';
import { firstError } from '../../api/errors';
import { cancelInvoice, invoicesQuery, payInvoice, qs, setInvoiceKaspi } from './api';
import { useSuperadminFlash } from './flash';
import { saHref } from './host';
import { SecondFactorDialog } from './SecondFactorDialog';

/*
 * Оплаты — перенос resources/views/superadmin/invoices/index.blade.php (Superadmin\InvoiceController): счета клубов
 * за подписку, которые суперадмин отмечает оплаченными, когда приходит платёж Kaspi, и ссылка на оплату.
 *
 * Отличие от Blade (решение владельца, ревью 22.11.2026): «Оплата получена» и сохранение ссылки Kaspi требуют свежего
 * кода второго фактора — вопрос и поле кода в одном окне (SecondFactorDialog, как выплата реселлеру), не window.confirm.
 */

type Pending =
  | { readonly kind: 'pay'; readonly id: number; readonly number: string }
  | { readonly kind: 'kaspi'; readonly url: string | null; readonly recipient: string | null };

const MONTHS_KEY = '{{count}} месяц|{{count}} месяца|{{count}} месяцев';

/** $money: number_format((float) $value, 0, '.', ' '). */
const money = (tiyn: number) => formatMoney(tiyn, 0, '.', ' ');

export function InvoicesPage({ page }: { readonly page?: number }) {
  const { t } = useTranslation();
  const flash = useSuperadminFlash();
  const queryClient = useQueryClient();
  const { data } = useQuery(invoicesQuery(page));
  useAdminPageTitle(t('Оплаты'));

  const done = (r: { message: string }) => {
    flash.status(r.message);
    return queryClient.invalidateQueries({ queryKey: ['superadmin'] });
  };
  const fail = (e: unknown) => flash.error(firstError(e));
  const cancel = useMutation({ mutationFn: cancelInvoice, onSuccess: done, onError: fail });
  const [pending, setPending] = useState<Pending | null>(null);
  const [codeError, setCodeError] = useState<string | null>(null);
  const confirmed = useMutation({
    mutationFn: ({ action, code }: { action: Pending; code: string }) =>
      action.kind === 'pay' ? payInvoice(action.id, code) : setInvoiceKaspi({ url: action.url, recipient: action.recipient, code }),
    onSuccess: (r) => {
      setPending(null);
      return done(r);
    },
    onError: (e) => {
      // Ошибка кода (неверный, уже использованный, не введён) — под полем, окно остаётся открытым.
      const code = isApiError(e) ? e.field('code') : undefined;
      if (code) {
        setCodeError(code);
        return;
      }
      setPending(null);
      fail(e);
    },
  });
  const ask = (action: Pending) => {
    setCodeError(null);
    setPending(action);
  };

  if (!data) return null;

  const submitPay = (id: number, number: string) => (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    ask({ kind: 'pay', id, number });
  };
  const submitCancel = (id: number) => (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    if (window.confirm(t('Отменить счёт?'))) cancel.mutate(id);
  };
  const submitKaspi = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    const f = new FormData(e.currentTarget);
    const text = (name: string) => {
      const v = f.get(name);
      return typeof v === 'string' && v.trim() !== '' ? v.trim() : null;
    };
    ask({ kind: 'kaspi', url: text('url'), recipient: text('recipient') });
  };

  return (
    <>
      <p className="mb-4 text-sm text-gray-600 dark:text-gray-400">{t('Клуб выставляет счёт на странице «Подписка» и платит по ссылке Kaspi, указав номер счёта в комментарии. Когда деньги пришли, нажмите «Оплата получена» — подписка клуба продлится на оплаченный срок.')}</p>

      <section className="mb-8" aria-labelledby="pending-title">
        <h3 id="pending-title" className="mb-3 font-semibold text-gray-900 dark:text-white">{t('Ожидают оплаты')}</h3>
        {data.pending.map((invoice) => (
          <div key={invoice.id} className="mb-3 flex flex-wrap items-center gap-4 rounded-xl border border-amber-300 bg-white p-4 dark:border-amber-700 dark:bg-gray-900">
            {/* basis-64 (аудит 22.11.2026, UI-3): при нехватке места цена и кнопки переносятся под счёт, а не сжимают
                колонку счёта до букв в строке (kk, 375 px); в Blade — min-w-0 flex-1. */}
            <div className="min-w-0 flex-1 basis-64">
              <p className="font-semibold text-gray-900 dark:text-white">{invoice.number} · <a href={saHref(`/clubs/${invoice.club_id}`)} className="text-violet-700 hover:underline dark:text-violet-300">{invoice.club_name}</a></p>
              <p className="text-sm text-gray-600 dark:text-gray-400">{invoice.plan_name}, {t(MONTHS_KEY, { count: invoice.months })}, {invoice.computers} {t('ПК')} × {money(invoice.price_per_computer_tiyn)} ₸{invoice.discount_percent > 0 && <>, {t('скидка {{percent}}%', { percent: invoice.discount_percent })}</>} · {invoice.created}</p>
            </div>
            <p className="text-xl font-bold text-gray-900 dark:text-white">{money(invoice.amount_tiyn)} ₸</p>
            <form method="POST" onSubmit={submitPay(invoice.id, invoice.number)}>
              <button type="submit" disabled={confirmed.isPending} className="brand-button rounded-lg px-4 py-2 text-sm">{t('Оплата получена')}</button>
            </form>
            <form method="POST" onSubmit={submitCancel(invoice.id)}>
              <button type="submit" disabled={cancel.isPending} className="rounded-lg border border-gray-300 px-4 py-2 text-sm dark:border-gray-700">{t('Отменить')}</button>
            </form>
          </div>
        ))}
        {data.pending.length === 0 && <p className="text-sm text-gray-500">{t('Неоплаченных счетов нет.')}</p>}
      </section>

      {/* key: после сохранения поля берут новые значения (как перезагрузка страницы). */}
      <form key={`${data.kaspi?.url ?? ''}|${data.kaspi?.recipient ?? ''}`} method="POST" className="mb-8 rounded-xl border border-gray-200 bg-white p-4 dark:border-gray-700 dark:bg-gray-900" onSubmit={submitKaspi}>
        <h3 className="mb-1 font-semibold text-gray-900 dark:text-white">{t('Оплата через Kaspi')}</h3>
        <p className="mb-3 text-sm text-gray-600 dark:text-gray-400">{t('Ссылка на оплату из Kaspi Pay (Kaspi Business → «Удалённая оплата» или ссылка на QR). Пока ссылки нет, клубы не могут выставить счёт.')}</p>
        <div className="grid gap-3 sm:grid-cols-2">
          <label className="block text-sm"><span className="mb-1 block text-gray-600 dark:text-gray-300">{t('Ссылка на оплату')}</span><input type="url" name="url" defaultValue={data.kaspi?.url ?? ''} placeholder="https://pay.kaspi.kz/pay/…" className="w-full rounded-lg border border-gray-300 bg-gray-50 px-3 py-2 dark:border-gray-700 dark:bg-gray-800" /></label>
          <label className="block text-sm"><span className="mb-1 block text-gray-600 dark:text-gray-300">{t('Получатель (видят клубы)')}</span><input type="text" name="recipient" maxLength={200} defaultValue={data.kaspi?.recipient ?? ''} className="w-full rounded-lg border border-gray-300 bg-gray-50 px-3 py-2 dark:border-gray-700 dark:bg-gray-800" /></label>
        </div>
        <button type="submit" disabled={confirmed.isPending} className="mt-3 rounded-lg border border-gray-300 px-4 py-2 text-sm dark:border-gray-700">{t('Сохранить')}</button>
      </form>

      <section aria-labelledby="closed-title">
        <h3 id="closed-title" className="mb-3 font-semibold text-gray-900 dark:text-white">{t('История')}</h3>
        {data.closed.length === 0 ? (
          <p className="text-sm text-gray-500">{t('Пока пусто.')}</p>
        ) : (
          <>
            <div className="overflow-x-auto rounded-xl border border-gray-200 bg-white dark:border-gray-700 dark:bg-gray-900">
              <table className="w-full text-left text-sm">
                <thead className="bg-gray-50 text-gray-500 dark:bg-gray-800 dark:text-gray-400"><tr><th className="px-4 py-2 font-medium">{t('Счёт')}</th><th className="px-4 py-2 font-medium">{t('Клуб')}</th><th className="px-4 py-2 font-medium">{t('Срок')}</th><th className="px-4 py-2 font-medium">{t('Сумма')}</th><th className="px-4 py-2 font-medium">{t('Статус')}</th></tr></thead>
                <tbody className="divide-y divide-gray-100 text-gray-800 dark:divide-gray-800 dark:text-gray-200">
                  {data.closed.map((invoice) => (
                    <tr key={invoice.id}>
                      <td className="px-4 py-2">{invoice.number}<span className="block text-xs text-gray-500">{invoice.created}</span></td>
                      <td className="px-4 py-2"><a href={saHref(`/clubs/${invoice.club_id}`)} className="text-violet-700 hover:underline dark:text-violet-300">{invoice.club_name}</a></td>
                      <td className="px-4 py-2">{invoice.plan_name}, {t(MONTHS_KEY, { count: invoice.months })}</td>
                      <td className="px-4 py-2">{money(invoice.amount_tiyn)} ₸</td>
                      <td className="px-4 py-2">{invoice.status === 'paid' ? t('Оплачен {{date}}', { date: invoice.paid ?? '' }) : t('Отменён')}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
            <div className="mt-4">
              <Pagination page={data.page} perPage={data.per_page} total={data.total} count={data.closed.length} hrefFor={(p) => saHref('/invoices' + qs({ page: p > 1 ? p : undefined }))} />
            </div>
          </>
        )}
      </section>

      <SecondFactorDialog
        question={
          pending === null
            ? null
            : pending.kind === 'pay'
              ? t('Деньги по счёту {{number}} пришли? Подписка клуба будет продлена.', { number: pending.number })
              : t('Изменить настройки оплаты через Kaspi? По этой ссылке платят все клубы.')
        }
        error={codeError}
        busy={confirmed.isPending}
        onClose={() => setPending(null)}
        onSubmit={(code) => {
          if (!pending) return;
          setCodeError(null);
          confirmed.mutate({ action: pending, code });
        }}
      />
    </>
  );
}
