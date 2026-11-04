import { useInfiniteQuery, useQuery, useQueryClient, useSuspenseQuery } from '@tanstack/react-query';
import { useNavigate } from '@tanstack/react-router';
import { useEffect, useRef, useState, type FormEvent } from 'react';
import { useForm } from 'react-hook-form';
import type { TFunction } from 'i18next';
import { useTranslation } from 'react-i18next';
import { useSession } from '../../auth/session';
import { useAdminPageTitle } from '../../layouts/adminSession';
import { setFlash, useFlash } from '../../lib/flash';
import { formatMoney, parseMoney, PARSE_MONEY_MESSAGES } from '../../lib/money';
import { useInvalidateOn } from '../../realtime/hooks';
import { serverErrors } from '../auth/serverErrors';
import {
  catalogQuery,
  createSale,
  createTopUp,
  customersQuery,
  fetchProducts,
  openShift,
  shiftQuery,
  type CashCustomer,
  type CashProduct,
  type CashShift,
  type CashService,
} from './api';
import { categoryIcon, posMoney } from './format';
import { vatIncludedTiyn } from './vat';

/*
 * Касса POS — перенос resources/views/cash-register/index.blade.php (CashRegisterController::index()).
 * Отличия (решение Q-070): товары витрины подгружаются страницами по мере прокрутки, пользователь для пополнения —
 * поиском на сервере (в старом — все товары и все пользователи в HTML). Вид тот же.
 * «НДС» — vatIncludedTiyn (Q-019), итог продажи — с сервера (чек).
 */

const PLACEHOLDER = '/images/product-placeholder.svg';

export function CashRegisterPage() {
  const { t } = useTranslation();
  useAdminPageTitle(t('Касса POS'));
  const { data } = useSuspenseQuery(shiftQuery());
  const flash = useFlash('status');
  const [status, setStatus] = useState<string | null>(flash);
  const [error, setError] = useState<string | null>(null);
  useInvalidateOn(['cash.*'], ['cash', 'shift']);

  return (
    <div className="p-4 lg:p-6">
      {status && <div className="mb-4 rounded-lg bg-green-50 p-4 text-green-700">{t(status)}</div>}
      {error && <div className="mb-4 rounded-lg bg-red-50 p-4 text-red-700">{t(error)}</div>}
      {data.shift == null || data.totals == null ? (
        <OpenShift onStatus={setStatus} onError={setError} />
      ) : (
        <Pos shift={data.shift} balanceTiyn={data.totals.balance_tiyn} onStatus={setStatus} onError={setError} />
      )}
    </div>
  );
}

interface Feedback {
  onStatus: (s: string | null) => void;
  onError: (s: string | null) => void;
}

function moneyError(t: TFunction, input: string, attribute: string): string | null {
  const r = parseMoney(input);
  return r.ok ? null : t(PARSE_MONEY_MESSAGES[r.error], { attribute });
}

/** «Открыть смену» — OpenCashShiftRequest (opening_balance numeric|min:0). */
function OpenShift({ onStatus, onError }: Feedback) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const { me } = useSession();
  const { register, handleSubmit, formState } = useForm<{ opening_balance: string }>({ defaultValues: { opening_balance: '' } });
  const submit = handleSubmit(async (v) => {
    const parsed = parseMoney(v.opening_balance);
    if (!parsed.ok) {
      onError(moneyError(t, v.opening_balance, 'opening balance'));
      return;
    }
    try {
      await openShift(parsed.tiyn);
      onError(null);
      onStatus('Смена открыта');
      await qc.invalidateQueries({ queryKey: ['cash'] });
    } catch (e) {
      onError(serverErrors(e).first);
    }
  });
  return (
    <div className="pos-open">
      <h3>{t('Открыть смену')}</h3>
      <form method="POST" action="/cash-register/shifts" onSubmit={submit}>
        <div>
          <label>{t('Начальный остаток')}</label>
          <input type="number" step="0.01" min="0" required {...register('opening_balance')} />
        </div>
        <button disabled={formState.isSubmitting || me?.read_only === true}>{t('Открыть кассу')}</button>
      </form>
    </div>
  );
}

interface CartItem {
  readonly name: string;
  readonly price: number; // тиыны
  readonly stock: number;
  readonly image: string;
  readonly qty: number;
}

type View = 'sale' | 'balance';

function Pos({ shift, balanceTiyn, onStatus, onError }: { shift: CashShift; balanceTiyn: number } & Feedback) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const { me } = useSession();
  const readOnly = me?.read_only === true;
  const { data: catalog } = useQuery(catalogQuery());
  const [category, setCategory] = useState('all');
  const productsCategory = category === 'all' || category === '__services' ? '' : category;
  const products = useInfiniteQuery({
    queryKey: ['cash', 'products', productsCategory],
    queryFn: ({ pageParam }) => fetchProducts(productsCategory, pageParam),
    initialPageParam: null as string | null,
    getNextPageParam: (last) => last.next_cursor ?? null,
    enabled: category !== '__services',
  });
  useInvalidateOn(['stock.*', 'product.*'], ['cash', 'products']);
  useInvalidateOn(['stock.*', 'product.*'], ['cash', 'catalog']);
  const items: readonly CashProduct[] = category === '__services' ? [] : (products.data?.pages.flatMap((p) => p.items) ?? []);
  const services: readonly CashService[] = category === 'all' || category === '__services' ? (catalog?.services ?? []) : [];

  // Подгрузка следующей страницы витрины, когда низ списка виден.
  const sentinel = useRef<HTMLDivElement>(null);
  const { hasNextPage, isFetchingNextPage, fetchNextPage } = products;
  useEffect(() => {
    const el = sentinel.current;
    if (!el || !hasNextPage || typeof IntersectionObserver === 'undefined') return;
    const io = new IntersectionObserver((entries) => {
      if (entries.some((e) => e.isIntersecting) && !isFetchingNextPage) void fetchNextPage();
    });
    io.observe(el);
    return () => io.disconnect();
  }, [hasNextPage, isFetchingNextPage, fetchNextPage]);

  const [cart, setCart] = useState<ReadonlyMap<string, CartItem>>(new Map());
  const [view, setView] = useState<View>('sale');
  const [mode, setMode] = useState(0);
  const [payment, setPayment] = useState(0);
  const [selling, setSelling] = useState(false);

  const add = (key: string, base: Omit<CartItem, 'qty'>) =>
    setCart((old) => {
      const next = new Map(old);
      const cur = next.get(key) ?? { ...base, qty: 0 };
      next.set(key, { ...cur, qty: Math.min(cur.qty + 1, cur.stock) });
      return next;
    });
  const change = (key: string, delta: number) =>
    setCart((old) => {
      const cur = old.get(key);
      if (!cur) return old;
      const next = new Map(old);
      const qty = cur.qty + delta;
      if (qty < 1) next.delete(key);
      else next.set(key, { ...cur, qty: Math.min(qty, cur.stock) });
      return next;
    });

  let total = 0;
  for (const it of cart.values()) total += it.price * it.qty;

  const checkout = async (e: FormEvent) => {
    e.preventDefault();
    if (cart.size === 0 || selling) return;
    setSelling(true);
    try {
      // Ключи корзины: "p12" — товар, "s3" — услуга.
      const lines = [...cart.entries()].map(([key, it]) =>
        key.startsWith('s') ? { service_id: Number(key.slice(1)), quantity: it.qty } : { product_id: Number(key.slice(1)), quantity: it.qty },
      );
      const res = await createSale(lines);
      setFlash('status', res.message);
      await navigate({ to: '/cash-register/receipts/$id', params: { id: String(res.transaction.id) } });
    } catch (err) {
      onError(serverErrors(err).first);
      setSelling(false);
    }
  };

  const categories = catalog?.categories ?? [];
  const productsTotal = catalog?.products_total ?? 0;
  const plural = (count: number) => t('{{count}} товар|{{count}} товара|{{count}} товаров', { count });

  return (
    <div className="pos-page">
      <div className="pos-top">
        <div className="pos-status">
          {t('Смена #{{id}}', { id: shift.id })} · {t('В кассе:')} <strong>{formatMoney(balanceTiyn, 2)} KZT</strong>
        </div>
        <div className="pos-actions">
          <a href="/cash-register/operations">{t('Операции смены')}</a>
          <a href="/cash-register/reports/x">{t('X-отчёт')}</a>
          <a className="danger" href={`/cash-register/shifts/${shift.id}/z-report`}>
            {t('Закрыть смену · Z-отчёт')}
          </a>
        </div>
      </div>
      <div className="pos-layout">
        <main className="pos-main">
          <div className="pos-categories">
            <button className={'pos-category' + (category === 'all' ? ' active' : '')} data-category="all" title={plural(productsTotal)} onClick={() => setCategory('all')}>
              <i className="fas fa-border-all pos-category-icon" aria-hidden="true"></i>
              <strong>{t('Все')}</strong>
              <small>{productsTotal}</small>
            </button>
            {categories.map((c) => (
              <button key={c.name} className={'pos-category' + (category === c.name ? ' active' : '')} data-category={c.name} title={plural(c.count)} onClick={() => setCategory(c.name)}>
                <i className={`fas ${categoryIcon(c.name)} pos-category-icon`} aria-hidden="true"></i>
                <strong>{c.name}</strong>
                <small>{c.count}</small>
              </button>
            ))}
            {(catalog?.services.length ?? 0) > 0 && (
              <button className={'pos-category' + (category === '__services' ? ' active' : '')} data-category="__services" title={t('Услуги клуба: PlayStation, распечатка…')} onClick={() => setCategory('__services')}>
                <i className="fas fa-bell-concierge pos-category-icon" aria-hidden="true"></i>
                <strong>{t('Услуги')}</strong>
                <small>{catalog?.services.length}</small>
              </button>
            )}
          </div>
          <div className="pos-products">
            {items.map((p) => {
              const key = `p${p.id}`;
              return (
                <article key={key} className={'pos-product' + (cart.has(key) ? ' selected' : '')} data-cart-key={key} data-category={p.category}>
                  <img src={p.image_url} alt={p.name} loading="lazy" />
                  <h4>{p.name}</h4>
                  <p className="pos-product-meta">{p.category || t('Без категории')}</p>
                  <div className="pos-product-price">
                    <span>{formatMoney(p.price_tiyn, 0)} ₸</span>
                    <span className={'pos-stock ' + (p.quantity < 5 ? 'low' : '')}>
                      {p.quantity > 0 ? t('В наличии: {{count}} шт.', { count: p.quantity }) : t('Нет в наличии · 0 шт.')}
                    </span>
                  </div>
                  <button
                    type="button"
                    className="pos-add add-product"
                    aria-label={t('Добавить в заказ: {{product}}', { product: p.name })}
                    disabled={p.quantity < 1}
                    onClick={() => add(key, { name: p.name, price: p.price_tiyn, stock: p.quantity, image: p.image_url })}
                  >
                    <i className="fas fa-plus" aria-hidden="true"></i> {t('В заказ')}
                  </button>
                </article>
              );
            })}
            {services.map((s) => {
              const key = `s${s.id}`;
              return (
                <article key={key} className={'pos-product pos-service' + (cart.has(key) ? ' selected' : '')} data-cart-key={key} data-category="__services">
                  <div className="pos-service-icon" aria-hidden="true">
                    <i className="fas fa-bell-concierge"></i>
                  </div>
                  <h4>{s.name}</h4>
                  <p className="pos-product-meta">
                    {t('Услуга')}
                    {s.unit ? ` · ${s.unit}` : ''}
                  </p>
                  <div className="pos-product-price">
                    <span>{formatMoney(s.price_tiyn, 0)} ₸</span>
                  </div>
                  <button
                    type="button"
                    className="pos-add add-product"
                    aria-label={t('Добавить в заказ: {{product}}', { product: s.name })}
                    onClick={() => add(key, { name: s.name, price: s.price_tiyn, stock: 9999, image: '' })}
                  >
                    <i className="fas fa-plus" aria-hidden="true"></i> {t('В заказ')}
                  </button>
                </article>
              );
            })}
          </div>
          <div ref={sentinel} aria-hidden="true" />
        </main>
        <aside className="pos-order">
          <div className="pos-view-tabs" role="tablist" aria-label={t('Раздел кассы')}>
            <button type="button" className={'pos-view-tab ' + (view === 'sale' ? 'active' : '')} data-pos-view="sale" role="tab" aria-selected={view === 'sale'} onClick={() => setView('sale')}>
              {t('Продажа товаров')}
            </button>
            <button type="button" className={'pos-view-tab ' + (view === 'balance' ? 'active' : '')} data-pos-view="balance" role="tab" aria-selected={view === 'balance'} onClick={() => setView('balance')}>
              {t('Пополнение баланса')}
            </button>
          </div>
          <div id="sale-view" hidden={view !== 'sale'}>
            <div className="pos-order-head">
              <h3>{t('Новый заказ')}</h3>
              <div className="pos-modes">
                {[t('В зале'), t('С собой'), t('Доставка')].map((label, i) => (
                  <button key={i} type="button" className={'pos-mode' + (mode === i ? ' active' : '')} onClick={() => setMode(i)}>
                    {label}
                  </button>
                ))}
              </div>
            </div>
            <div id="cart" className="pos-cart" data-cart>
              {cart.size === 0 ? (
                <div className="pos-empty">{t('Добавьте товары в заказ')}</div>
              ) : (
                [...cart.entries()].map(([key, it]) => (
                  <div key={key} className="pos-cart-item">
                    <img src={it.image || PLACEHOLDER} alt="" />
                    <div>
                      <div className="pos-cart-name">{it.name}</div>
                      <div className="pos-cart-sub">{t('Без модификаторов')}</div>
                      <div className="pos-cart-line">
                        <span className="pos-cart-price">{posMoney(it.price * it.qty)}</span>
                        <div className="pos-qty">
                          <button type="button" data-change="-1" onClick={() => change(key, -1)}>
                            −
                          </button>
                          <span>{it.qty}</span>
                          <button type="button" data-change="1" onClick={() => change(key, 1)}>
                            +
                          </button>
                        </div>
                      </div>
                    </div>
                  </div>
                ))
              )}
            </div>
            <div className="pos-summary">
              <div className="pos-summary-row">
                <span>{t('Подытог')}</span>
                <span id="subtotal">{posMoney(total)}</span>
              </div>
              <div className="pos-summary-row">
                <span>{t('НДС 5% (включён)')}</span>
                <span id="tax">{posMoney(vatIncludedTiyn(total))}</span>
              </div>
              <div className="pos-summary-total">
                <span>{t('Итого')}</span>
                <span id="total">{posMoney(total)}</span>
              </div>
            </div>
            <div className="pos-payments">
              {[
                ['▣', t('Наличные')],
                ['▤', t('Карта')],
                ['▦', t('QR-код')],
              ].map(([icon, label], i) => (
                <button key={i} type="button" className={'pos-payment' + (payment === i ? ' active' : '')} onClick={() => setPayment(i)}>
                  <span>{icon}</span>
                  {label}
                </button>
              ))}
            </div>
            <form id="sale-form" method="POST" action="/cash-register/sales" onSubmit={(e) => void checkout(e)}>
              <div id="sale-items"></div>
              <button id="checkout" disabled={cart.size === 0 || selling || readOnly} className="pos-checkout">
                {t('Оформить заказ · Оплатить')}
              </button>
            </form>
          </div>
          <TopUp hidden={view !== 'balance'} readOnly={readOnly} onStatus={onStatus} onError={onError} />
        </aside>
      </div>
    </div>
  );
}

interface TopUpForm {
  amount: string;
  balance_type: 'balance' | 'bonus_balance';
  payment_method: 'cash' | 'card' | 'qr';
}

/** Поиск с задержкой 250 мс (как поиск клиента в бронях). */
function useDebounced(value: string, ms = 250): string {
  const [v, setV] = useState(value);
  useEffect(() => {
    const id = setTimeout(() => setV(value), ms);
    return () => clearTimeout(id);
  }, [value, ms]);
  return v;
}

/** «Пополнение баланса» — StoreBalanceTopUpRequest (user_id, amount, balance_type, payment_method). */
function TopUp({ hidden, readOnly, onStatus, onError }: { hidden: boolean; readOnly: boolean } & Feedback) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [search, setSearch] = useState('');
  const [user, setUser] = useState<CashCustomer | null>(null);
  const [open, setOpen] = useState(false);
  const query = useDebounced(search.trim());
  const users = useQuery({ ...customersQuery(user ? '' : query), enabled: open });
  const picker = useRef<HTMLDivElement>(null);
  const searchInput = useRef<HTMLInputElement | null>(null);
  const { register, handleSubmit, reset, formState } = useForm<TopUpForm>({
    defaultValues: { amount: '', balance_type: 'balance', payment_method: 'cash' },
  });

  useEffect(() => {
    const onDoc = (e: MouseEvent) => {
      if (picker.current && !picker.current.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener('click', onDoc);
    return () => document.removeEventListener('click', onDoc);
  }, []);

  const submit = handleSubmit(async (v) => {
    if (!user) return;
    const parsed = parseMoney(v.amount);
    if (!parsed.ok) {
      onError(moneyError(t, v.amount, 'amount'));
      return;
    }
    try {
      const res = await createTopUp({ user_id: user.id, amount_tiyn: parsed.tiyn, balance_type: v.balance_type, payment_method: v.payment_method });
      onError(null);
      onStatus(res.message);
      reset();
      setUser(null);
      setSearch('');
      await qc.invalidateQueries({ queryKey: ['cash'] });
    } catch (e) {
      onError(serverErrors(e).first);
    }
  });

  const list = users.data ?? [];
    return (
    <section id="balance-view" className="pos-balance-view" hidden={hidden}>
      <div className="pos-order-head">
        <h3>{t('Пополнение баланса')}</h3>
      </div>
      <form method="POST" action="/cash-register/balance-top-ups" className="pos-balance-form"
        onSubmit={(e) => {
          if (!user) {
            // Выбор только из списка (старый JS: setCustomValidity + reportValidity).
            e.preventDefault();
            const input = e.currentTarget.querySelector<HTMLInputElement>('[data-user-search]');
            input?.setCustomValidity(t('Выберите пользователя из списка.'));
            input?.reportValidity();
            return;
          }
          void submit(e);
        }}
      >
        <input type="hidden" name="cash_view" value="balance" />
        <div className="pos-user-picker" data-user-picker ref={picker}>
          <label htmlFor="top-up-user-search">{t('Пользователь')}</label>
          <input id="top-up-user-id" type="hidden" name="user_id" value={user?.id ?? ''} data-user-id />
          <input
            id="top-up-user-search"
            ref={searchInput}
            type="search"
            role="combobox"
            aria-autocomplete="list"
            aria-controls="top-up-user-options"
            aria-expanded={open}
            autoComplete="off"
            required
            value={search}
            placeholder={t('Поиск по имени или телефону')}
            className="pos-balance-input"
            data-user-search
            onFocus={() => setOpen(true)}
            onChange={(e) => {
              setUser(null);
              setSearch(e.target.value);
              e.target.setCustomValidity('');
              setOpen(true);
            }}
          />
          <div id="top-up-user-options" className="pos-user-options" role="listbox" hidden={!open} data-user-options>
            {list.map((u) => (
              <button
                key={u.id}
                type="button"
                className="pos-user-option"
                role="option"
                aria-selected={user?.id === u.id}
                data-user-option
                onClick={() => {
                  setUser(u);
                  setSearch(`${u.name} · ${u.phone}`);
                  searchInput.current?.setCustomValidity('');
                  setOpen(false);
                }}
              >
                <span>
                  <strong>{u.name}</strong>
                  <small>
                    {u.phone} · {t('Основной')} {formatMoney(u.balance_tiyn, 0)} ₸ · {t('Бонусный')} {formatMoney(u.bonus_balance_tiyn, 0)} ₸
                  </small>
                </span>
              </button>
            ))}
            <p className="pos-user-no-results" data-user-no-results hidden={list.length > 0 || !users.isSuccess}>
              {t('Пользователь не найден')}
            </p>
          </div>
        </div>
        <label>
          {t('Сумма, ₸')}
          <input type="number" min="1" max="9999999999.99" step="0.01" required className="pos-balance-input" placeholder={t('Введите сумму')} {...register('amount')} />
        </label>
        <label>
          {t('Зачислить на')}
          <select required className="pos-balance-input" {...register('balance_type')}>
            <option value="balance">{t('Основной баланс')}</option>
            <option value="bonus_balance">{t('Бонусный баланс')}</option>
          </select>
        </label>
        <label>
          {t('Способ оплаты')}
          <select required className="pos-balance-input" {...register('payment_method')}>
            <option value="cash">{t('Наличные')}</option>
            <option value="card">{t('Карта')}</option>
            <option value="qr">{t('QR-код')}</option>
          </select>
        </label>
        <button type="submit" className="pos-checkout" disabled={formState.isSubmitting || readOnly}>
          {t('Пополнить баланс')}
        </button>
      </form>
    </section>
  );
}
