import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { session } from '../../auth/session';
import { apiError, mockApi, renderPage, signIn } from '../../test/pages';
import type { CashReceipt } from './api';
import { CashRegisterPage } from './CashRegisterPage';
import { RefundButton } from './RefundButton';

/* FE-11: касса — корзина, оплата, пополнение баланса, возврат. Суммы — целые тиыны; на сервер уходят целые. */

const navigate = vi.fn(async () => undefined);
vi.mock('@tanstack/react-router', async (original) => ({
  ...(await original<typeof import('@tanstack/react-router')>()),
  useNavigate: () => navigate,
}));

const shift = {
  shift: { id: 3, status: 'open', opening_balance_tiyn: 500000, opened_at: '2026-10-15T03:00:00Z', z_report_printed: false },
  totals: { opening_tiyn: 500000, income_tiyn: 734550, expense_tiyn: 0, balance_tiyn: 1234550 },
};
const catalog = {
  products_total: 2,
  categories: [{ name: 'Напитки', count: 2 }],
  services: [{ id: 3, name: 'Распечатка', price_tiyn: 5050, unit: 'лист' }],
};
const products = {
  items: [
    { id: 1, name: 'Coca-Cola 0,5 л', category: 'Напитки', price_tiyn: 45000, quantity: 2, image_url: '/images/cola.png' },
    { id: 2, name: 'Вода 0,5 л', category: 'Напитки', price_tiyn: 20033, quantity: 0, image_url: '/images/water.png' },
  ],
  next_cursor: null,
};
const sale = { message: 'Продажа проведена.', transaction: { id: 77, shift_id: 3, type: 'income', amount_tiyn: 95050, description: 'Продажа POS', created_at: '2026-10-15T07:00:00Z' } };

function routes(extra: Record<string, unknown> = {}) {
  return mockApi({
    'GET /api/cash/shift': shift,
    'GET /api/cash/catalog': catalog,
    'GET /api/cash/products': products,
    'GET /api/cash/customers': [{ id: 9, name: 'Айгерим', phone: '+77011234567', balance_tiyn: 150050, bonus_balance_tiyn: 2000 }],
    ...extra,
  });
}

const text = (id: string) => document.getElementById(id)?.textContent;
const addButton = (name: string) => screen.getByRole('button', { name: `Добавить в заказ: ${name}` });

describe('касса POS', () => {
  beforeEach(() => {
    navigate.mockClear();
    signIn('cashier', ['cash.view', 'cash.create']);
  });
  afterEach(() => {
    vi.unstubAllGlobals();
    session.clear();
  });

  it('остаток кассы и цены — из целых тиынов; корзина считает сумму и НДС без float', async () => {
    routes();
    renderPage(<CashRegisterPage />);
    expect(await screen.findByText('12 345,50 KZT')).toBeInTheDocument(); // «В кассе: …» — 1 234 550 тиынов
    await screen.findByText('Coca-Cola 0,5 л');
    expect(screen.getByText('450 ₸')).toBeInTheDocument();
    expect(screen.getByText('200 ₸')).toBeInTheDocument(); // 200,33 ₸ на витрине — до целых (number_format 0)
    expect(addButton('Вода 0,5 л')).toBeDisabled(); // нет в наличии

    fireEvent.click(addButton('Coca-Cola 0,5 л'));
    fireEvent.click(addButton('Coca-Cola 0,5 л'));
    fireEvent.click(addButton('Coca-Cola 0,5 л')); // остаток 2 — третью не добавить
    fireEvent.click(addButton('Распечатка'));
    const cart = document.getElementById('cart') as HTMLElement;
    expect(within(cart).getByText('900 ₸')).toBeInTheDocument();
    expect(within(cart).getByText('50,5 ₸')).toBeInTheDocument();
    // 2 × 450,00 + 50,50 = 950,50; НДС 5 % включён — целые тенге, как в старом (Q-019): round(950,50 × 5 / 105) = 45.
    expect(text('subtotal')).toBe('950,5 ₸');
    expect(text('total')).toBe('950,5 ₸');
    expect(text('tax')).toBe('45 ₸');

    // «−» у товара: 1 × 450,00 + 50,50.
    fireEvent.click(cart.querySelector('[data-change="-1"]') as HTMLElement);
    expect(text('total')).toBe('500,5 ₸');
  });

  it('оплата: позиции с количеством и ключ идемпотентности; после продажи — чек', async () => {
    const api = routes({ 'POST /api/cash/sales': sale });
    renderPage(<CashRegisterPage />);
    await screen.findByText('Coca-Cola 0,5 л');
    expect(screen.getByRole('button', { name: 'Оформить заказ · Оплатить' })).toBeDisabled();
    fireEvent.click(addButton('Coca-Cola 0,5 л'));
    fireEvent.click(addButton('Coca-Cola 0,5 л'));
    fireEvent.click(addButton('Распечатка'));
    fireEvent.click(screen.getByRole('button', { name: 'Оформить заказ · Оплатить' }));
    await waitFor(() => expect(navigate).toHaveBeenCalled());
    const [post] = api.of('POST', '/api/cash/sales');
    // Суммы в запросе нет: цену считает сервер по своим данным; уходит только состав заказа.
    expect(post?.body).toEqual({ items: [{ product_id: 1, quantity: 2 }, { service_id: 3, quantity: 1 }] });
    expect(post?.headers.get('Idempotency-Key')).toMatch(/^[0-9a-f-]{36}$/);
    expect(api.of('POST', '/api/cash/sales')).toHaveLength(1);
    expect(navigate).toHaveBeenCalledWith({ to: '/cash-register/receipts/$id', params: { id: '77' } });
  });

  it('ошибка продажи показывается, корзина остаётся, повторная оплата возможна', async () => {
    let fail = true;
    const api = routes({
      'POST /api/cash/sales': () => (fail ? apiError(422, { error: 'validation_failed', message: 'Недостаточно товара на складе.', errors: { items: ['Недостаточно товара на складе.'] } }) : sale),
    });
    renderPage(<CashRegisterPage />);
    await screen.findByText('Coca-Cola 0,5 л');
    fireEvent.click(addButton('Coca-Cola 0,5 л'));
    const pay = screen.getByRole('button', { name: 'Оформить заказ · Оплатить' });
    fireEvent.click(pay);
    expect(await screen.findByText('Недостаточно товара на складе.')).toBeInTheDocument();
    expect(text('total')).toBe('450 ₸');
    await waitFor(() => expect(pay).toBeEnabled());
    fail = false;
    fireEvent.click(pay);
    await waitFor(() => expect(navigate).toHaveBeenCalled());
    const posts = api.of('POST', '/api/cash/sales');
    expect(posts).toHaveLength(2);
    // Новая попытка кассира — новая операция: свой ключ идемпотентности.
    expect(posts[0]?.headers.get('Idempotency-Key')).not.toBe(posts[1]?.headers.get('Idempotency-Key'));
  });

  it('пополнение баланса: клиент из списка, сумма «1500.50» → 150 050 тиынов', async () => {
    const api = routes({ 'POST /api/cash/balance-top-ups': { message: 'Баланс пополнен.', transaction: sale.transaction } });
    renderPage(<CashRegisterPage />);
    await screen.findByText('Coca-Cola 0,5 л');
    fireEvent.click(screen.getByRole('tab', { name: 'Пополнение баланса' }));
    const search = screen.getByRole('combobox', { name: 'Пользователь' });
    fireEvent.focus(search);
    fireEvent.change(search, { target: { value: 'Айг' } });
    const option = await screen.findByRole('option', { name: /Айгерим/ });
    // Балансы клиента в списке — из тиынов, до целых тенге.
    expect(option.textContent).toContain('Основной 1 501 ₸');
    expect(option.textContent).toContain('Бонусный 20 ₸');
    fireEvent.click(option);
    fireEvent.change(screen.getByPlaceholderText('Введите сумму'), { target: { value: '1500.50' } });
    fireEvent.change(screen.getByRole('combobox', { name: 'Зачислить на' }), { target: { value: 'bonus_balance' } });
    fireEvent.change(screen.getByRole('combobox', { name: 'Способ оплаты' }), { target: { value: 'card' } });
    fireEvent.submit(document.querySelector('.pos-balance-form') as HTMLFormElement);
    expect(await screen.findByText('Баланс пополнен.')).toBeInTheDocument();
    const [post] = api.of('POST', '/api/cash/balance-top-ups');
    expect(post?.body).toEqual({ user_id: 9, amount_tiyn: 150050, balance_type: 'bonus_balance', payment_method: 'card' });
    expect(post?.headers.get('Idempotency-Key')).toMatch(/^[0-9a-f-]{36}$/);
  });

  it('пополнение: больше двух знаков после запятой — ошибка формы, запрос не уходит (Q-002)', async () => {
    const api = routes({ 'POST /api/cash/balance-top-ups': { message: 'Баланс пополнен.', transaction: sale.transaction } });
    renderPage(<CashRegisterPage />);
    await screen.findByText('Coca-Cola 0,5 л');
    fireEvent.click(screen.getByRole('tab', { name: 'Пополнение баланса' }));
    const search = screen.getByRole('combobox', { name: 'Пользователь' });
    fireEvent.focus(search);
    fireEvent.click(await screen.findByRole('option', { name: /Айгерим/ }));
    fireEvent.change(screen.getByPlaceholderText('Введите сумму'), { target: { value: '10.005' } });
    fireEvent.submit(document.querySelector('.pos-balance-form') as HTMLFormElement);
    await waitFor(() => expect(document.querySelector('.bg-red-50')).not.toBeNull());
    expect(api.of('POST', '/api/cash/balance-top-ups')).toHaveLength(0);
  });

  it('без выбранного клиента пополнение не отправляется', async () => {
    const api = routes();
    renderPage(<CashRegisterPage />);
    await screen.findByText('Coca-Cola 0,5 л');
    fireEvent.click(screen.getByRole('tab', { name: 'Пополнение баланса' }));
    fireEvent.change(screen.getByPlaceholderText('Введите сумму'), { target: { value: '100' } });
    fireEvent.submit(document.querySelector('.pos-balance-form') as HTMLFormElement);
    await Promise.resolve();
    expect(api.of('POST', '/api/cash/balance-top-ups')).toHaveLength(0);
  });
});

describe('возврат по чеку', () => {
  const receipt = {
    transaction: { ...sale.transaction, amount_tiyn: 150050 },
    refund: null,
    can_refund: true,
    refundable: true,
    shift_open: true,
  } as unknown as CashReceipt;

  beforeEach(() => signIn('cashier', ['cash.view', 'cash.delete']));
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
    session.clear();
  });

  it('подтверждение показывает сумму из тиынов; возврат — POST с ключом идемпотентности', async () => {
    const api = mockApi({ 'POST /api/cash/receipts/77/refund': { message: 'Возврат выполнен.', transaction: sale.transaction } });
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(true);
    const onStatus = vi.fn();
    renderPage(<RefundButton receipt={receipt} onStatus={onStatus} onError={vi.fn()} />);
    fireEvent.click(screen.getByRole('button', { name: 'Возврат' }));
    expect(confirm).toHaveBeenCalledWith('Вернуть 1 500,50 KZT по чеку №77? Деньги выдаются из кассы.');
    await waitFor(() => expect(onStatus).toHaveBeenCalledWith('Возврат выполнен.'));
    const [post] = api.of('POST', '/api/cash/receipts/77/refund');
    expect(post?.body).toBeUndefined();
    expect(post?.headers.get('Idempotency-Key')).toMatch(/^[0-9a-f-]{36}$/);
  });

  it('отказ в подтверждении — запроса нет; уже возвращённый чек — без кнопки', () => {
    const api = mockApi({});
    vi.spyOn(window, 'confirm').mockReturnValue(false);
    const view = renderPage(<RefundButton receipt={receipt} onStatus={vi.fn()} onError={vi.fn()} />);
    fireEvent.click(screen.getByRole('button', { name: 'Возврат' }));
    expect(api.calls).toHaveLength(0);
    view.unmount();
    renderPage(<RefundButton receipt={{ ...receipt, refund: { id: 80 } } as unknown as CashReceipt} onStatus={vi.fn()} onError={vi.fn()} />);
    expect(screen.queryByRole('button', { name: 'Возврат' })).toBeNull();
    expect(screen.getByText('Возвращено')).toBeInTheDocument();
  });
});
