import { useSuspenseQuery } from '@tanstack/react-query';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useSession } from '../../auth/session';
import { Pagination } from '../../components/Pagination';
import { useAdminPageTitle } from '../../layouts/adminSession';
import { formatDate } from '../../lib/datetime';
import { formatMoney } from '../../lib/money';
import { useInvalidateOn } from '../../realtime/hooks';
import { operationsQuery } from './api';
import { RefundButton } from './RefundButton';

/** Маркер описания продажи POS (App\Models\CashTransaction::POS_SALE): показывается переведённым. */
export const POS_SALE = 'Продажа POS';

/*
 * Операции смены — перенос cash-register/operations.blade.php (CashRegisterController::operations()): приходы открытой
 * смены, новые сверху, paginate(25); возврат — RefundButton.
 */
export function OperationsPage({ page }: { page: number }) {
  const { t } = useTranslation();
  const { me } = useSession();
  const tz = me?.club?.timezone ?? 'UTC';
  const { data } = useSuspenseQuery(operationsQuery(page));
  const [status, setStatus] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  useInvalidateOn(['cash.*'], ['cash', 'operations']);
  useAdminPageTitle(t('Операции смены'));
  const canRefund = me?.permissions.includes('cash.delete') ?? false;

  return (
    <div className="mx-auto max-w-5xl space-y-4 p-4 md:p-6">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="text-xl font-semibold text-gray-900 dark:text-white">{t('Операции смены #{{id}}', { id: data.shift_id })}</h1>
          <p className="text-sm text-gray-500 dark:text-gray-400">
            {t('Приход открытой смены. Возврат продажи вернёт товар на склад, возврат пополнения спишет сумму с баланса клиента; деньги выдаются из кассы.')}
          </p>
        </div>
        <a href="/cash-register" className="text-sm font-medium text-gray-700 hover:underline dark:text-gray-300">
          {t('← Вернуться в кассу')}
        </a>
      </div>
      {status && (
        <div className="rounded-lg bg-green-50 p-3 text-sm text-green-700" role="status">
          {t(status)}
        </div>
      )}
      {error && (
        <div className="rounded-lg bg-red-50 p-3 text-sm text-red-700" role="alert">
          {t(error)}
        </div>
      )}
      {!canRefund && (
        <p className="rounded-lg bg-gray-50 p-3 text-sm text-gray-600 dark:bg-gray-800 dark:text-gray-300">
          {t('Нет права на возвраты. Его даёт владелец клуба в «Настройки → Права».')}
        </p>
      )}

      <div className="overflow-x-auto rounded-2xl border border-gray-200 bg-white dark:border-gray-800 dark:bg-gray-900">
        <table className="w-full text-left text-sm text-gray-700 dark:text-gray-300">
          <thead className="bg-gray-50 text-xs uppercase text-gray-500 dark:bg-gray-800 dark:text-gray-400">
            <tr>
              <th className="px-5 py-3">{t('Чек')}</th>
              <th className="px-5 py-3">{t('Время')}</th>
              <th className="px-5 py-3">{t('Операция')}</th>
              <th className="px-5 py-3 text-right">{t('Сумма')}</th>
              <th className="px-5 py-3"></th>
            </tr>
          </thead>
          <tbody className="divide-y divide-gray-100 dark:divide-gray-800">
            {data.items.length === 0 ? (
              <tr>
                <td colSpan={5} className="px-5 py-10 text-center text-gray-500 dark:text-gray-400">
                  {t('В этой смене ещё нет прихода.')}
                </td>
              </tr>
            ) : (
              data.items.map((op) => {
                const tx = op.transaction;
                return (
                  <tr key={tx.id}>
                    <td className="whitespace-nowrap px-5 py-3">
                      <a href={`/cash-register/receipts/${tx.id}`} className="font-medium text-green-700 hover:underline dark:text-green-400">
                        №{tx.id}
                      </a>
                    </td>
                    <td className="whitespace-nowrap px-5 py-3">{formatDate(tx.created_at, tz, 'H:i')}</td>
                    <td className="px-5 py-3">
                      {tx.description === POS_SALE ? t('Продажа товаров') : tx.description}
                      {tx.items && <span className="block text-xs text-gray-500 dark:text-gray-400">{tx.items.map((i) => `${i.name} × ${i.quantity}`).join(', ')}</span>}
                    </td>
                    <td className="whitespace-nowrap px-5 py-3 text-right tabular-nums">{formatMoney(tx.amount_tiyn, 2)} KZT</td>
                    <td className="whitespace-nowrap px-5 py-3 text-right">
                      <RefundButton receipt={op} onStatus={setStatus} onError={setError} />
                    </td>
                  </tr>
                );
              })
            )}
          </tbody>
        </table>
      </div>
      <Pagination page={page} perPage={data.per_page} total={data.total} count={data.items.length} hrefFor={(p) => `/cash-register/operations?page=${p}`} />
    </div>
  );
}
