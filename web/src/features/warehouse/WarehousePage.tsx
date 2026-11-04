import { useQueryClient, useSuspenseQuery } from '@tanstack/react-query';
import { useCallback, useEffect, useRef, useState, type FormEvent, type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import { useSession } from '../../auth/session';
import { Pagination } from '../../components/Pagination';
import { useAdminPageTitle } from '../../layouts/adminSession';
import { formatDate } from '../../lib/datetime';
import { formatMoney, parseMoney, PARSE_MONEY_MESSAGES } from '../../lib/money';
import { useInvalidateOn } from '../../realtime/hooks';
import { createCategory, createMovement, createProduct, deleteProduct, overviewQuery, productsPageQuery, type Product } from './api';
import { allErrors } from './errors';
import { ProductPicker } from './ProductPicker';

/*
 * Склад (каталог) — перенос resources/views/warehouse.blade.php (WarehouseController): категории, новый товар,
 * движение товара, удаление товара (warehouse.delete), таблица товаров (paginate(50)), последние 10 движений.
 * Отличие (решение Q-070): товар в формах движения и удаления выбирается поиском, а не списком всех товаров.
 */

/** Маркер списания продажей (StockMovement note «Продажа POS») — не переводится, сравнивается. */
const POS_SALE_NOTE = 'Продажа POS';
const FIELD = 'brand-focus w-full rounded-lg border border-gray-300 bg-gray-50 px-4 py-2 text-gray-900 focus:outline-none dark:border-gray-700 dark:bg-gray-800 dark:text-gray-100';
const LABEL = 'mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300';
const TH = 'px-3 py-3 text-left font-semibold text-gray-700 dark:text-gray-300';
const TH_R = 'px-3 py-3 text-right font-semibold text-gray-700 dark:text-gray-300';

type ModalId = 'category-modal' | 'product-modal' | null;

export function WarehousePage({ page }: { page: number }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const { me } = useSession();
  const tz = me?.club?.timezone ?? 'UTC';
  useAdminPageTitle(t('Склад'));
  const { data: overview } = useSuspenseQuery(overviewQuery());
  const { data: stock } = useSuspenseQuery(productsPageQuery(page));
  useInvalidateOn(['stock.*', 'product.*'], ['warehouse']);
  const [status, setStatus] = useState<string | null>(null);
  const [errors, setErrors] = useState<string[]>([]);
  const [modal, setModal] = useState<ModalId>(null);
  const categories = overview.categories;

  const done = async (message: string) => {
    setErrors([]);
    setStatus(message);
    await qc.invalidateQueries({ queryKey: ['warehouse'] });
  };

  useEffect(() => {
    document.body.style.overflow = modal ? 'hidden' : '';
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setModal(null);
    };
    document.addEventListener('keydown', onKey);
    return () => {
      document.removeEventListener('keydown', onKey);
      document.body.style.overflow = '';
    };
  }, [modal]);

  return (
    <div className="p-6 lg:p-8">
      {status && (
        <div className="mb-4 rounded-lg border border-green-200 bg-green-50 px-4 py-3 text-sm text-green-700 dark:border-green-800 dark:bg-green-900/20 dark:text-green-300">
          {t(status)}
        </div>
      )}
      {errors.length > 0 && (
        <div className="mb-4 rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700 dark:border-red-800 dark:bg-red-900/20 dark:text-red-300">
          <ul className="list-disc space-y-1 ps-5">
            {errors.map((e, i) => (
              <li key={i}>{t(e)}</li>
            ))}
          </ul>
        </div>
      )}

      <div className="warehouse-catalog mb-6">
        <div className="warehouse-catalog-inner">
          <div>
            <h3 className="warehouse-catalog-title">{t('Управление каталогом')}</h3>
            <p className="warehouse-catalog-meta">{t('Категорий: {{categories}} · Товаров: {{products}}', { categories: categories.length, products: overview.products_total })}</p>
          </div>
          <div className="warehouse-catalog-actions">
            <button type="button" className="warehouse-action category" onClick={() => setModal('category-modal')}>
              <i className="fas fa-folder-plus"></i>
              {t('Новая категория')}
            </button>
            <button type="button" className="warehouse-action product" disabled={categories.length === 0} onClick={() => setModal('product-modal')}>
              <i className="fas fa-box-open"></i>
              {t('Новый товар')}
            </button>
          </div>
        </div>
        {categories.length > 0 && (
          <div className="warehouse-category-list">
            {categories.map((c) => (
              <span key={c.id} className="warehouse-category-pill">
                {c.name} · {c.products_count}
              </span>
            ))}
          </div>
        )}
      </div>

      <CategoryModal open={modal === 'category-modal'} onClose={() => setModal(null)} onDone={(m) => { setModal(null); void done(m); }} onErrors={setErrors} />
      <ProductModal open={modal === 'product-modal'} categories={categories} onClose={() => setModal(null)} onDone={(m) => { setModal(null); void done(m); }} onErrors={setErrors} />

      <MovementCard onDone={done} onErrors={setErrors} />

      {me?.permissions.includes('warehouse.delete') && overview.products_total > 0 && <DeleteCard onDone={done} onErrors={setErrors} />}

      <div className="animate-slide rounded-xl border border-gray-200 bg-white p-6 dark:border-gray-800 dark:bg-gray-900">
        <h3 className="mb-2 text-2xl font-bold text-gray-900 dark:text-white">{t('Склад')}</h3>
        <p className="mb-6 text-gray-600 dark:text-gray-400">{t('Товаров на складе: {{count}}', { count: stock.total })}</p>

        <div className="overflow-x-auto">
          <table className="w-full text-sm">
            <thead className="border-b border-gray-200 dark:border-gray-800">
              <tr>
                <th className={TH}>{t('Изображение')}</th>
                <th className={TH}>SKU</th>
                <th className={TH}>{t('Товар')}</th>
                <th className={TH}>{t('Категория')}</th>
                <th className={TH_R}>{t('Цена')}</th>
                <th className={TH_R}>{t('Остаток')}</th>
              </tr>
            </thead>
            <tbody>
              {stock.items.map((p) => (
                <tr key={p.id} className="border-b border-gray-100 transition last:border-0 hover:bg-gray-50 dark:border-gray-800 dark:hover:bg-gray-800/50">
                  <td className="px-3 py-3">
                    <img src="/images/product-placeholder.svg" alt={p.name} width="56" height="56" className="h-14 w-14 rounded-lg object-cover" />
                  </td>
                  <td className="px-3 py-4 font-mono text-gray-500 dark:text-gray-400">{p.sku}</td>
                  <td className="px-3 py-4 font-medium text-gray-900 dark:text-gray-100">{p.name}</td>
                  <td className="px-3 py-4 text-gray-600 dark:text-gray-400">{p.category}</td>
                  <td className="px-3 py-4 text-right font-semibold text-gray-900 dark:text-gray-100">{formatMoney(p.price_tiyn, 0)} KZT</td>
                  <td className={'px-3 py-4 text-right font-semibold ' + (p.quantity <= 10 ? 'text-red-600 dark:text-red-400' : 'text-green-700 dark:text-green-400')}>{p.quantity}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        {stock.total > stock.per_page && (
          <div className="mt-4">
            <Pagination page={page} perPage={stock.per_page} total={stock.total} count={stock.items.length} hrefFor={(n) => `/warehouse?page=${n}`} />
          </div>
        )}
      </div>

      <div className="mt-6 animate-slide rounded-xl border border-gray-200 bg-white p-6 dark:border-gray-800 dark:bg-gray-900">
        <h3 className="mb-4 text-xl font-bold text-gray-900 dark:text-white">{t('Последние операции')}</h3>

        <div className="overflow-x-auto">
          <table className="w-full text-sm">
            <thead className="border-b border-gray-200 dark:border-gray-800">
              <tr>
                <th className={TH}>{t('Дата')}</th>
                <th className={TH}>{t('Товар')}</th>
                <th className={TH}>{t('Операция')}</th>
                <th className={TH_R}>{t('Количество')}</th>
                <th className={TH_R}>{t('Остаток')}</th>
                <th className={TH}>{t('Комментарий')}</th>
              </tr>
            </thead>
            <tbody>
              {overview.movements.length === 0 ? (
                <tr>
                  <td colSpan={6} className="px-3 py-8 text-center text-gray-500 dark:text-gray-400">
                    {t('Операций пока нет.')}
                  </td>
                </tr>
              ) : (
                overview.movements.map((m) => {
                  const isSale = m.type === 'write_off' && m.note === POS_SALE_NOTE;
                  const color = m.type === 'receipt' ? 'text-green-700 dark:text-green-400' : isSale ? 'text-blue-600 dark:text-blue-400' : 'text-red-600 dark:text-red-400';
                  return (
                    <tr key={m.id} className="border-b border-gray-100 last:border-0 dark:border-gray-800">
                      <td className="px-3 py-4 text-gray-500 dark:text-gray-400">{m.created_at ? formatDate(m.created_at, tz, 'd.m.Y H:i') : ''}</td>
                      <td className="px-3 py-4 font-medium text-gray-900 dark:text-gray-100">{m.product_name}</td>
                      <td className={'px-3 py-4 font-semibold ' + color}>{m.type === 'receipt' ? t('Оприходование') : isSale ? t('Продажа') : t('Списание')}</td>
                      <td className="px-3 py-4 text-right font-semibold text-gray-900 dark:text-gray-100">{m.quantity}</td>
                      <td className="px-3 py-4 text-right text-gray-700 dark:text-gray-300">{m.balance_after}</td>
                      <td className="px-3 py-4 text-gray-500 dark:text-gray-400">{m.note || '—'}</td>
                    </tr>
                  );
                })
              )}
            </tbody>
          </table>
        </div>
      </div>
    </div>
  );
}

interface FormProps {
  onDone: (message: string) => void | Promise<void>;
  onErrors: (errors: string[]) => void;
}

/** Модалка склада: открытие — класс flex вместо hidden, закрытие — по кнопке, фону и Escape; фокус на первое поле. */
function Modal({ id, open, onClose, children }: { id: string; open: boolean; onClose: () => void; children: ReactNode }) {
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!open) return;
    const h = setTimeout(() => ref.current?.querySelector<HTMLElement>('input:not([type="hidden"]), select')?.focus(), 50);
    return () => clearTimeout(h);
  }, [open]);
  return (
    <div
      id={id}
      ref={ref}
      className={'warehouse-modal fixed inset-0 items-center justify-center bg-gray-950/60 p-4 backdrop-blur-sm ' + (open ? 'flex' : 'hidden')}
      onClick={(e) => {
        if (e.target === e.currentTarget) onClose();
      }}
    >
      {children}
    </div>
  );
}

function CategoryModal({ open, onClose, onDone, onErrors }: { open: boolean; onClose: () => void } & FormProps) {
  const { t } = useTranslation();
  const [name, setName] = useState('');
  const [busy, setBusy] = useState(false);
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    try {
      await createCategory(name);
      setName('');
      await onDone('Категория создана.');
    } catch (err) {
      onErrors(allErrors(err));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Modal id="category-modal" open={open} onClose={onClose}>
      <div className="warehouse-dialog category" role="dialog" aria-modal="true" aria-labelledby="category-modal-title">
        <div className="warehouse-dialog-head">
          <div className="warehouse-dialog-heading">
            <span className="warehouse-dialog-icon purple">
              <i className="fas fa-folder-plus"></i>
            </span>
            <div>
              <h3 id="category-modal-title" className="warehouse-dialog-title">
                {t('Новая категория')}
              </h3>
              <p className="warehouse-dialog-subtitle">{t('Создайте группу для товаров склада')}</p>
            </div>
          </div>
          <button type="button" className="warehouse-modal-close" onClick={onClose}>
            <i className="fas fa-times"></i>
          </button>
        </div>
        <form action="/warehouse/categories" method="POST" className="warehouse-form" onSubmit={(e) => void submit(e)}>
          <input type="hidden" name="form_type" value="category" />
          <div className="warehouse-field">
            <label>{t('Название категории')}</label>
            <input type="text" name="name" value={name} onChange={(e) => setName(e.target.value)} maxLength={100} required placeholder={t('Например: Напитки')} />
          </div>
          <div className="warehouse-form-actions">
            <button type="button" className="warehouse-button cancel" onClick={onClose}>
              {t('Отмена')}
            </button>
            <button className="warehouse-button save purple" disabled={busy}>
              {t('Создать категорию')}
            </button>
          </div>
        </form>
      </div>
    </Modal>
  );
}

function ProductModal({ open, categories, onClose, onDone, onErrors }: { open: boolean; categories: readonly { id: number; name: string }[]; onClose: () => void } & FormProps) {
  const { t } = useTranslation();
  const empty = { sku: '', name: '', category: '', price: '', quantity: '0' };
  const [v, setV] = useState(empty);
  const [busy, setBusy] = useState(false);
  const set = (k: keyof typeof empty) => (e: { target: { value: string } }) => setV((o) => ({ ...o, [k]: e.target.value }));
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    const price = parseMoney(v.price);
    if (!price.ok) {
      onErrors([t(PARSE_MONEY_MESSAGES[price.error], { attribute: 'price' })]);
      return;
    }
    setBusy(true);
    try {
      await createProduct({ sku: v.sku, name: v.name, product_category_id: Number(v.category), price_tiyn: price.tiyn, quantity: Number(v.quantity) });
      setV(empty);
      await onDone('Товар создан.');
    } catch (err) {
      onErrors(allErrors(err));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Modal id="product-modal" open={open} onClose={onClose}>
      <div className="warehouse-dialog product" role="dialog" aria-modal="true" aria-labelledby="product-modal-title">
        <div className="warehouse-dialog-head">
          <div className="warehouse-dialog-heading">
            <span className="warehouse-dialog-icon">
              <i className="fas fa-box-open"></i>
            </span>
            <div>
              <h3 id="product-modal-title" className="warehouse-dialog-title">
                {t('Новый товар')}
              </h3>
              <p className="warehouse-dialog-subtitle">{t('Заполните данные и начальный остаток')}</p>
            </div>
          </div>
          <button type="button" className="warehouse-modal-close" onClick={onClose}>
            <i className="fas fa-times"></i>
          </button>
        </div>
        <form action="/warehouse/products" method="POST" className="warehouse-form" onSubmit={(e) => void submit(e)}>
          <input type="hidden" name="form_type" value="product" />
          <div className="warehouse-form-grid">
            <div className="warehouse-field">
              <label>{t('SKU / артикул')}</label>
              <input type="text" name="sku" value={v.sku} onChange={set('sku')} maxLength={100} required placeholder={t('Например: DRK-001')} />
            </div>
            <div className="warehouse-field">
              <label>{t('Название товара')}</label>
              <input type="text" name="name" value={v.name} onChange={set('name')} maxLength={150} required placeholder={t('Введите название')} />
            </div>
            <div className="warehouse-field">
              <label>{t('Категория')}</label>
              <select name="product_category_id" value={v.category} onChange={set('category')} required>
                <option value="">{t('Выберите категорию')}</option>
                {categories.map((c) => (
                  <option key={c.id} value={c.id}>
                    {c.name}
                  </option>
                ))}
              </select>
            </div>
            <div className="warehouse-field">
              <label>{t('Цена, KZT')}</label>
              <input type="number" name="price" value={v.price} onChange={set('price')} min="0" step="0.01" required placeholder="0.00" />
            </div>
            <div className="warehouse-field">
              <label>{t('Начальный остаток')}</label>
              <input type="number" name="quantity" value={v.quantity} onChange={set('quantity')} min="0" required placeholder="0" />
            </div>
          </div>
          <div className="warehouse-form-actions">
            <button type="button" className="warehouse-button cancel" onClick={onClose}>
              {t('Отмена')}
            </button>
            <button className="warehouse-button save" disabled={busy}>
              {t('Создать товар')}
            </button>
          </div>
        </form>
      </div>
    </Modal>
  );
}

function MovementCard({ onDone, onErrors }: FormProps) {
  const { t } = useTranslation();
  const [product, setProduct] = useState<Product | null>(null);
  const [type, setType] = useState<'receipt' | 'write_off'>('receipt');
  const [quantity, setQuantity] = useState('1');
  const [note, setNote] = useState('');
  const [busy, setBusy] = useState(false);
  const label = useCallback((p: Product) => t('{{name}} (остаток: {{quantity}})', { name: p.name, quantity: p.quantity }), [t]);
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (!product) return;
    setBusy(true);
    try {
      await createMovement({ product_id: product.id, type, quantity: Number(quantity), note: note || null });
      setQuantity('1');
      setNote('');
      setProduct(null);
      await onDone('Складская операция выполнена');
    } catch (err) {
      onErrors(allErrors(err));
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="mb-6 animate-slide rounded-xl border border-gray-200 bg-white p-6 dark:border-gray-800 dark:bg-gray-900">
      <h3 className="mb-4 text-xl font-bold text-gray-900 dark:text-white">{t('Движение товара')}</h3>

      <form action="/warehouse/stock-movements" method="POST" className="grid grid-cols-1 gap-4 lg:grid-cols-4" onSubmit={(e) => void submit(e)}>
        <div>
          <label htmlFor="product_id" className={LABEL}>
            {t('Товар')}
          </label>
          <ProductPicker id="product_id" className={FIELD} label={label} value={product} onChange={setProduct} />
        </div>

        <div>
          <label htmlFor="type" className={LABEL}>
            {t('Операция')}
          </label>
          <select id="type" name="type" required className={FIELD} value={type} onChange={(e) => setType(e.target.value as 'receipt' | 'write_off')}>
            <option value="receipt">{t('Оприходование')}</option>
            <option value="write_off">{t('Списание')}</option>
          </select>
        </div>

        <div>
          <label htmlFor="quantity" className={LABEL}>
            {t('Количество')}
          </label>
          <input id="quantity" type="number" name="quantity" value={quantity} onChange={(e) => setQuantity(e.target.value)} min="1" required className={FIELD} />
        </div>

        <div>
          <label htmlFor="note" className={LABEL}>
            {t('Комментарий')}
          </label>
          <input id="note" type="text" name="note" value={note} onChange={(e) => setNote(e.target.value)} maxLength={255} className={FIELD} />
        </div>

        <div className="lg:col-span-4 flex justify-end">
          <button type="submit" className="brand-button rounded-lg px-5 py-2 text-sm transition" disabled={busy}>
            {t('Провести операцию')}
          </button>
        </div>
      </form>
    </div>
  );
}

function DeleteCard({ onDone, onErrors }: FormProps) {
  const { t } = useTranslation();
  const [product, setProduct] = useState<Product | null>(null);
  const label = useCallback((p: Product) => `${p.name} · ${p.sku}`, []);
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (!product || !window.confirm(t('Удалить товар «{{product}}»? Он пропадёт из кассы, склада и магазина агента.', { product: product.name }))) return;
    try {
      await deleteProduct(product.id);
      const name = product.name;
      setProduct(null);
      await onDone(t('Товар «{{product}}» удалён.', { product: name }));
    } catch (err) {
      onErrors(allErrors(err));
    }
  };
  return (
    <div className="mb-6 animate-slide rounded-xl border border-gray-200 bg-white p-6 dark:border-gray-800 dark:bg-gray-900">
      <h3 className="mb-1 text-xl font-bold text-gray-900 dark:text-white">{t('Удаление товара')}</h3>
      <p className="mb-4 text-sm text-gray-600 dark:text-gray-400">{t('Товар пропадёт из кассы, склада и магазина агента. История движения и чеки сохранятся.')}</p>
      <form method="POST" data-delete-product className="flex flex-wrap items-end gap-3" onSubmit={(e) => void submit(e)}>
        <div className="min-w-[240px] flex-1">
          <label htmlFor="delete-product" className={LABEL}>
            {t('Товар')}
          </label>
          <ProductPicker id="delete-product" className={FIELD} label={label} value={product} onChange={setProduct} />
        </div>
        <button type="submit" className="rounded-lg border border-red-300 px-5 py-2 text-sm font-medium text-red-600 transition hover:bg-red-50 dark:border-red-800 dark:text-red-400 dark:hover:bg-red-900/20">
          {t('Удалить товар')}
        </button>
      </form>
    </div>
  );
}
