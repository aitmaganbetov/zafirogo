import { useQueryClient, useSuspenseQuery } from '@tanstack/react-query';
import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useSession } from '../../auth/session';
import { Pagination } from '../../components/Pagination';
import { useAdminPageTitle } from '../../layouts/adminSession';
import { formatDate } from '../../lib/datetime';
import { formatMoney } from '../../lib/money';
import { useInvalidateOn } from '../../realtime/hooks';
import { serverErrors } from '../auth/serverErrors';
import { cancelOrder, markDone, shopOrdersQuery, type OrderStatusFilter, type ShopOrder } from './api';

/*
 * Заказы из агента — перенос resources/views/shop-orders/index.blade.php (ShopOrderController::index()).
 * Опрос /shop-orders/state раз в 15 с с перезагрузкой страницы заменён событиями shop_order.created/updated
 * (WebSocket, решение Q-062): список перечитывается сам.
 */

const STATUSES: readonly OrderStatusFilter[] = ['new', 'done', 'cancelled', 'all'];
const BADGE: Record<string, string> = {
  new: 'bg-amber-100 text-amber-800 dark:bg-amber-900/40 dark:text-amber-200',
  done: 'bg-green-100 text-green-800 dark:bg-green-900/40 dark:text-green-200',
  cancelled: 'bg-gray-100 text-gray-700 dark:bg-gray-800 dark:text-gray-300',
};

export function ShopOrdersPage({ status, page }: { status: OrderStatusFilter; page: number }) {
  const { t } = useTranslation();
  useAdminPageTitle(t('Заказы'));
  const { data } = useSuspenseQuery(shopOrdersQuery(status, page));
  useInvalidateOn(['shop_order.*'], ['shop-orders']);
  const [flash, setFlash] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const labels: Record<OrderStatusFilter, string> = { new: t('Новые'), done: t('Выданные'), cancelled: t('Отменённые'), all: t('Все') };

  return (
    <div className="p-6 lg:p-8" data-orders>
      <header className="mb-6 flex flex-wrap items-end justify-between gap-4">
        <div>
          <h3 className="mb-2 text-2xl font-bold text-gray-900 dark:text-white">{t('Заказы')}</h3>
          <p className="text-sm text-gray-600 dark:text-gray-400">
            {t('Товары, которые клиенты заказали в агенте и оплатили с баланса. Отнесите заказ к ПК и отметьте «Выдан».')}
          </p>
        </div>
        <nav className="flex flex-wrap gap-2" aria-label={t('Статус заказов')}>
          {STATUSES.map((key) => (
            <a
              key={key}
              href={`/shop-orders?status=${key}`}
              className={'rounded-lg border px-3 py-2 text-sm ' + (status === key ? 'border-transparent brand-button' : 'border-gray-300 text-gray-700 dark:border-gray-700 dark:text-gray-300')}
              aria-current={status === key ? 'page' : undefined}
            >
              {labels[key]}
              {key === 'new' && data.new_count > 0 && (
                <>
                  {' '}
                  <span className="ml-1 rounded-full bg-red-600 px-2 text-xs font-bold text-white">{data.new_count}</span>
                </>
              )}
            </a>
          ))}
        </nav>
      </header>

      {flash && (
        <div className="mb-4 rounded-lg border border-green-200 bg-green-50 px-4 py-3 text-sm text-green-700 dark:border-green-800 dark:bg-green-900/20 dark:text-green-300">
          {flash}
        </div>
      )}
      {error && (
        <div className="mb-4 rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700 dark:border-red-800 dark:bg-red-900/20 dark:text-red-300" role="alert">
          {t(error)}
        </div>
      )}

      <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
        {data.items.length === 0 ? (
          <p className="rounded-xl border border-dashed border-gray-300 p-8 text-center text-sm text-gray-500 md:col-span-2 xl:col-span-3 dark:border-gray-700">
            {status === 'new' ? t('Новых заказов нет.') : t('Заказов нет.')}
          </p>
        ) : (
          data.items.map((o) => <OrderCard key={o.id} order={o} onStatus={setFlash} onError={setError} />)
        )}
      </div>

      <div className="mt-6">
        <Pagination page={page} perPage={data.per_page} total={data.total} count={data.items.length} hrefFor={(p) => `/shop-orders?status=${status}&page=${p}`} />
      </div>
    </div>
  );
}

function OrderCard({ order: o, onStatus, onError }: { order: ShopOrder; onStatus: (s: string | null) => void; onError: (s: string | null) => void }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const { me } = useSession();
  const tz = me?.club?.timezone ?? 'UTC';
  const [busy, setBusy] = useState(false);
  const badgeText: Record<string, string> = { new: t('Новый'), done: t('Выдан'), cancelled: t('Отменён') };
  const money = (tiyn: number) => formatMoney(tiyn, 2);
  const can = (right: string) => me?.permissions.includes(right) ?? false;
  const act = async (fn: () => Promise<unknown>, message: string) => {
    setBusy(true);
    try {
      await fn();
      onError(null);
      onStatus(message);
      await qc.invalidateQueries({ queryKey: ['shop-orders'] });
      await qc.invalidateQueries({ queryKey: ['layout', 'badges'] });
    } catch (e) {
      onError(serverErrors(e).first);
    } finally {
      setBusy(false);
    }
  };
  return (
    <article className="flex flex-col rounded-xl border border-gray-200 bg-white p-4 dark:border-gray-800 dark:bg-gray-900">
      <div className="mb-3 flex items-start justify-between gap-3">
        <div className="min-w-0">
          <p className="text-lg font-bold text-gray-900 dark:text-white">{t('ПК {{computer}}', { computer: o.computer_name ?? '—' })}</p>
          <p className="truncate text-sm text-gray-600 dark:text-gray-400">
            {t('Заказ №{{id}}', { id: o.id })} · {o.created_at ? formatDate(o.created_at, tz, 'd.m H:i') : ''} · {o.customer_name ?? '—'}
          </p>
        </div>
        <span className={'shrink-0 rounded-full px-2 py-1 text-xs font-semibold ' + (BADGE[o.status] ?? '')}>{badgeText[o.status] ?? o.status}</span>
      </div>
      <ul className="mb-3 flex-1 divide-y divide-gray-100 text-sm dark:divide-gray-800">
        {o.items.map((line, i) => (
          <li key={i} className="flex justify-between gap-3 py-1.5 text-gray-800 dark:text-gray-200">
            <span className="min-w-0">
              {line.name || '—'} <span className="font-semibold">× {line.quantity}</span>
            </span>
            <span className="shrink-0 text-gray-600 dark:text-gray-400">{money(line.total_tiyn)}</span>
          </li>
        ))}
      </ul>
      <p className="mb-3 flex justify-between text-sm font-semibold text-gray-900 dark:text-white">
        <span>{t('Итого, оплачено с баланса')}</span>
        <span>{money(o.amount_tiyn)}</span>
      </p>
      {o.status === 'new' ? (
        <div className="flex flex-wrap gap-2">
          {can('orders.update') && (
            <form method="POST" action={`/shop-orders/${o.id}/done`} className="flex-1" onSubmit={(e) => { e.preventDefault(); void act(() => markDone(o.id), t('Заказ №{{id}} выдан.', { id: o.id })); }}>
              <button type="submit" className="brand-button w-full rounded-lg px-4 py-2 text-sm" disabled={busy || me?.read_only === true}>
                <i className="fas fa-check"></i>
                {t('Выдан')}
              </button>
            </form>
          )}
          {can('orders.delete') && (
            <form
              method="POST"
              action={`/shop-orders/${o.id}/cancel`}
              data-confirm={t('Отменить заказ №{{id}}? Деньги вернутся на счёт клиента, товар — на склад.', { id: o.id })}
              onSubmit={(e) => {
                e.preventDefault();
                if (!window.confirm(t('Отменить заказ №{{id}}? Деньги вернутся на счёт клиента, товар — на склад.', { id: o.id }))) return;
                void act(() => cancelOrder(o.id), t('Заказ №{{id}} отменён, деньги возвращены на счёт клиента.', { id: o.id }));
              }}
            >
              <button type="submit" className="rounded-lg border border-red-300 px-4 py-2 text-sm text-red-700 dark:border-red-800 dark:text-red-300" disabled={busy || me?.read_only === true}>
                {t('Отменить')}
              </button>
            </form>
          )}
        </div>
      ) : (
        <p className="text-xs text-gray-500">
          {badgeText[o.status] ?? ''}: {o.handler_name ?? '—'}, {o.handled_at ? formatDate(o.handled_at, tz, 'd.m H:i') : ''}
        </p>
      )}
    </article>
  );
}
