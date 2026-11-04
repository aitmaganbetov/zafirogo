import { useQueryClient, useSuspenseQuery } from '@tanstack/react-query';
import { useNavigate } from '@tanstack/react-router';
import { useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { useSession } from '../../auth/session';
import { Pagination } from '../../components/Pagination';
import { useAdminPageTitle } from '../../layouts/adminSession';
import { formatMoney } from '../../lib/money';
import { useInvalidateOn } from '../../realtime/hooks';
import { serverErrors } from '../auth/serverErrors';
import { receiveStock, stockQuery, type StockPage as StockPageData } from './api';

/*
 * Остатки (для кассира и оператора) — перенос resources/views/stock/index.blade.php (StockController): поиск по
 * имени или SKU, по категории и имени, paginate(50); «Оприходовать» (stock.create) — StockController::receive().
 */

const INPUT_SM = 'rounded-lg border border-gray-300 bg-gray-50 px-2 py-1.5 text-sm text-gray-900 dark:border-gray-700 dark:bg-gray-800 dark:text-white';

export function StockPage({ search, page }: { search: string; page: number }) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const { me } = useSession();
  useAdminPageTitle(t('Склад'));
  const { data } = useSuspenseQuery(stockQuery(search, page));
  useInvalidateOn(['stock.*', 'product.*'], ['warehouse', 'stock']);
  const [status, setStatus] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [q, setQ] = useState(search);
  const canReceive = me?.permissions.includes('stock.create') ?? false;
  const href = (p: number) => `/stock?${new URLSearchParams({ ...(search ? { search } : {}), page: String(p) }).toString()}`;

  return (
    <div className="p-6 lg:p-8">
      <header className="mb-6 flex flex-wrap items-end justify-between gap-4">
        <div>
          <h3 className="mb-2 text-2xl font-bold text-gray-900 dark:text-white">{t('Склад')}</h3>
          <p className="text-sm text-gray-600 dark:text-gray-400">
            {t('Остатки товаров клуба. Привезли товар — оприходуйте его здесь; списание делает менеджер, продажа — через кассу.')}
          </p>
        </div>
        <form
          method="GET"
          action="/stock"
          className="flex gap-2"
          onSubmit={(e) => {
            e.preventDefault();
            void navigate({ to: '/stock', search: { search: q.trim() || undefined, page: undefined } });
          }}
        >
          <input
            name="search"
            value={q}
            onChange={(e) => setQ(e.target.value)}
            maxLength={100}
            placeholder={t('Название или артикул')}
            aria-label={t('Поиск товара')}
            className="rounded-lg border border-gray-300 bg-gray-50 px-3 py-2 text-sm text-gray-900 dark:border-gray-700 dark:bg-gray-800 dark:text-white"
          />
          <button type="submit" className="brand-button rounded-lg px-4 py-2 text-sm">
            <i className="fas fa-search"></i>
            {t('Найти')}
          </button>
        </form>
      </header>

      {status && (
        <div className="mb-4 rounded-lg border border-green-200 bg-green-50 px-4 py-3 text-sm text-green-700 dark:border-green-800 dark:bg-green-900/20 dark:text-green-300">
          {t(status)}
        </div>
      )}
      {error && (
        <div className="mb-4 rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700 dark:border-red-800 dark:bg-red-900/20 dark:text-red-300" role="alert">
          {t(error)}
        </div>
      )}

      <div className="overflow-x-auto rounded-xl border border-gray-200 bg-white dark:border-gray-800 dark:bg-gray-900">
        <table className="w-full min-w-[900px] text-left text-sm">
          <thead className="bg-gray-50 text-xs uppercase text-gray-500 dark:bg-gray-800 dark:text-gray-400">
            <tr>
              <th className="px-5 py-3 font-semibold">{t('Товар')}</th>
              <th className="px-5 py-3 font-semibold">{t('Категория')}</th>
              <th className="px-5 py-3 text-right font-semibold">{t('Цена')}</th>
              <th className="px-5 py-3 text-right font-semibold">{t('Остаток')}</th>
              <th className="px-5 py-3 font-semibold">{t('Оприходовать')}</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-gray-100 dark:divide-gray-800">
            {data.items.length === 0 ? (
              <tr>
                <td colSpan={5} className="px-5 py-10 text-center text-gray-500 dark:text-gray-400">
                  {t('Товары не найдены.')}
                </td>
              </tr>
            ) : (
              data.items.map((p) => <Row key={p.id} product={p} canReceive={canReceive} readOnly={me?.read_only === true} onStatus={setStatus} onError={setError} />)
            )}
          </tbody>
        </table>
      </div>
      <div className="mt-4">
        <Pagination page={page} perPage={data.per_page} total={data.total} count={data.items.length} hrefFor={href} />
      </div>
    </div>
  );
}

function Row({
  product: p,
  canReceive,
  readOnly,
  onStatus,
  onError,
}: {
  product: StockPageData['items'][number];
  canReceive: boolean;
  readOnly: boolean;
  onStatus: (s: string | null) => void;
  onError: (s: string | null) => void;
}) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [quantity, setQuantity] = useState('');
  const [note, setNote] = useState('');
  const [busy, setBusy] = useState(false);
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (!window.confirm(t('Оприходовать {{product}}?', { product: p.name }) + ' +' + quantity)) return;
    setBusy(true);
    try {
      await receiveStock({ product_id: p.id, quantity: Number(quantity), note: note || null });
      setQuantity('');
      setNote('');
      onError(null);
      onStatus('Товар оприходован.');
      await qc.invalidateQueries({ queryKey: ['warehouse'] });
    } catch (err) {
      onError(serverErrors(err).first);
    } finally {
      setBusy(false);
    }
  };
  const qtyClass = p.quantity <= 0 ? 'text-red-600' : p.quantity < 5 ? 'text-amber-600' : 'text-gray-900 dark:text-white';
  return (
    <tr className="text-gray-700 dark:text-gray-300">
      <td className="px-5 py-3">
        <span className="flex items-center gap-3">
          <img src={p.image_url} alt="" className="h-10 w-10 rounded-lg object-cover" />
          <span>
            <strong className="block text-gray-900 dark:text-white">{p.name}</strong>
            <small className="text-gray-500">{p.sku}</small>
          </span>
        </span>
      </td>
      <td className="px-5 py-3">{p.category}</td>
      <td className="whitespace-nowrap px-5 py-3 text-right tabular-nums">{formatMoney(p.price_tiyn, 0)} ₸</td>
      <td className={'px-5 py-3 text-right font-semibold tabular-nums ' + qtyClass}>{p.quantity}</td>
      <td className="px-5 py-3">
        {canReceive && (
          <form method="POST" action="/stock/receive" className="flex items-center gap-2" onSubmit={(e) => void submit(e)}>
            <input type="hidden" name="product_id" value={p.id} />
            <input
              type="number"
              name="quantity"
              min="1"
              max="100000"
              step="1"
              required
              placeholder={t('шт.')}
              aria-label={t('Сколько пришло: {{product}}', { product: p.name })}
              className={'w-20 ' + INPUT_SM}
              value={quantity}
              onChange={(e) => setQuantity(e.target.value)}
            />
            <input name="note" maxLength={255} placeholder={t('Комментарий')} aria-label={t('Комментарий')} className={'w-36 ' + INPUT_SM} value={note} onChange={(e) => setNote(e.target.value)} />
            <button type="submit" className="brand-button rounded-lg px-3 py-1.5 text-xs" disabled={busy || readOnly}>
              <i className="fas fa-plus"></i>
              {t('Оприходовать')}
            </button>
          </form>
        )}
      </td>
    </tr>
  );
}
