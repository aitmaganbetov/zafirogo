import { fireEvent, screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { session } from '../../auth/session';
import { apiError, mockApi, renderPage, signIn } from '../../test/pages';
import { BillingPage } from './BillingPage';
import { safePayUrl, type Billing, type Invoice } from './api';

/*
 * Экран «Подписка» (billing/index.blade.php, Zafiro 77ae201, 4821225, 5c457c1): цена «за ПК в месяц», счёт и оплата
 * по ссылке Kaspi, форма «Продлить подписку». Суммы приходят с сервера — во фронте счёт не считается.
 */

const invoice = (extra: Partial<Invoice> = {}): Invoice => ({
  id: 42, number: 'ZF-00042', plan_name: 'Базовый', months: 3, computers: 3, price_per_computer_tiyn: 100000, discount_percent: 10,
  amount_tiyn: 810000, status: 'pending', created_at: '2026-11-15T20:00:00Z', period_ends_at: null, ...extra,
});

const billing = (extra: Partial<Billing> = {}): Billing => ({
  subscription: {
    plan_name: 'Базовый', saas_plan_id: 2, status: 'active', state: 'active', trial_ends_at: null,
    current_period_ends_at: '2026-12-01T20:00:00Z', monthly_price_tiyn: 4000000, monthly_total_tiyn: 12000000,
    discount_percent: 20, discount_until: '2026-11-25',
  },
  can_pay: true, computers: 3, discount_percent: 20, months: [1, 3, 6, 12],
  plans: [
    { id: 1, name: 'Старт', price_monthly_tiyn: 100000, amounts: [{ months: 1, amount_tiyn: 240000 }, { months: 3, amount_tiyn: 720000 }, { months: 6, amount_tiyn: 1440000 }, { months: 12, amount_tiyn: 2880000 }] },
    { id: 2, name: 'Pro', price_monthly_tiyn: 5000000, amounts: [{ months: 1, amount_tiyn: 12000000 }, { months: 3, amount_tiyn: 36000000 }, { months: 6, amount_tiyn: 72000000 }, { months: 12, amount_tiyn: 144000000 }] },
  ],
  kaspi: { url: 'https://pay.kaspi.kz/pay/zafiro', recipient: 'ИП Zafiro' },
  pending: null, invoices: [], ...extra,
});

describe('Подписка', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
    session.clear();
  });

  it('цена за ПК в месяц со скидкой и даты по часам клуба', async () => {
    signIn('owner', ['billing.view', 'billing.pay']);
    mockApi({ 'GET /api/billing': billing() });
    renderPage(<BillingPage />);
    expect(await screen.findByText('Стоимость в месяц')).toBeInTheDocument();
    const price = screen.getByText('Стоимость в месяц').nextElementSibling;
    expect(price).toHaveTextContent('40 000 ₸ за ПК · 120 000 ₸ за 3 ПК со скидкой 20% до 25.11.2026');
    // 01.12 20:00 UTC — в Алматы уже 02.12.
    expect(screen.getByText('Оплачено до').nextElementSibling).toHaveTextContent('02.12.2026');
  });

  it('форма «Продлить подписку»: тариф подписки выбран, сумма по тарифу и сроку — с сервера', async () => {
    signIn('owner', ['billing.view', 'billing.pay']);
    const api = mockApi({ 'GET /api/billing': billing(), 'POST /api/billing/invoices': { message: 'Счёт выставлен. Оплатите его через Kaspi.', invoice: invoice() } });
    renderPage(<BillingPage />);
    const select = (await screen.findByLabelText('Тариф')) as HTMLSelectElement;
    expect(select.value).toBe('2');
    expect(screen.getByRole('option', { name: 'Pro — 50 000 ₸ за ПК в месяц' })).toBeInTheDocument();
    expect(screen.getByText('ПК в клубе: 3. Сумма считается за все ПК клуба. Ваша скидка: 20%.')).toBeInTheDocument();
    const total = () => document.querySelector('[data-billing-total]')?.textContent;
    expect(total()).toBe('120 000');
    fireEvent.click(screen.getAllByRole('radio')[2] as HTMLElement); // 6 месяцев
    expect(total()).toBe('720 000');
    fireEvent.change(select, { target: { value: '1' } });
    expect(total()).toBe('14 400');

    fireEvent.click(screen.getByRole('button', { name: 'Выставить счёт' }));
    await waitFor(() => expect(api.of('POST', '/api/billing/invoices')).toHaveLength(1));
    expect(api.of('POST', '/api/billing/invoices')[0]?.body).toEqual({ saas_plan_id: 1, months: 6 });
    expect(await screen.findByRole('status')).toHaveTextContent('Счёт выставлен. Оплатите его через Kaspi.');
  });

  it('неоплаченный счёт: сумма, шаги оплаты, получатель, ссылка Kaspi, отмена', async () => {
    signIn('owner', ['billing.view', 'billing.pay']);
    const pending = invoice();
    const api = mockApi({
      'GET /api/billing': billing({ pending, invoices: [pending, invoice({ id: 41, number: 'ZF-00041', status: 'paid', months: 1, amount_tiyn: 270000, period_ends_at: '2026-12-15T07:00:00Z' })] }),
      'POST /api/billing/invoices/42/cancel': { message: 'Счёт отменён.' },
    });
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    renderPage(<BillingPage />);
    expect(await screen.findByText('Счёт ZF-00042 ожидает оплаты')).toBeInTheDocument();
    expect(screen.getByText('Переведите 8 100 ₸.')).toBeInTheDocument();
    expect(screen.getByText('В комментарии к платежу укажите номер счёта ZF-00042.')).toBeInTheDocument();
    expect(screen.getByText('Получатель: ИП Zafiro')).toBeInTheDocument();
    expect(screen.getByText(/Базовый, .*, 3 ПК × 1 000 ₸, скидка 10%/)).toBeInTheDocument();
    const link = screen.getByRole('link', { name: 'Оплатить в Kaspi' });
    expect(link).toHaveAttribute('href', 'https://pay.kaspi.kz/pay/zafiro');
    expect(link).toHaveAttribute('target', '_blank');
    expect(link).toHaveAttribute('rel', 'noopener noreferrer');
    expect(screen.queryByRole('button', { name: 'Выставить счёт' })).not.toBeInTheDocument();
    // Таблица счетов: дата по часам клуба (15.11 20:00 UTC — 16.11 в Алматы), оплаченный — со сроком.
    expect(screen.getByRole('row', { name: /ZF-00042 16\.11\.2026 .* 8 100 ₸ Ожидает оплаты/ })).toBeInTheDocument();
    expect(screen.getByRole('row', { name: /ZF-00041 .* 2 700 ₸ Оплачен · до 15\.12\.2026/ })).toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: 'Отменить счёт' }));
    await waitFor(() => expect(api.of('POST', '/api/billing/invoices/42/cancel')).toHaveLength(1));
    expect(await screen.findByRole('status')).toHaveTextContent('Счёт отменён.');
  });

  it('без права billing.pay — только сообщение; счёт виден, но отменить нельзя', async () => {
    signIn('manager', ['billing.view']);
    mockApi({ 'GET /api/billing': billing({ can_pay: false }) });
    const view = renderPage(<BillingPage />);
    expect(await screen.findByText('Продлить подписку может владелец клуба.')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Выставить счёт' })).not.toBeInTheDocument();
    view.unmount();

    mockApi({ 'GET /api/billing': billing({ can_pay: false, pending: invoice() }) });
    renderPage(<BillingPage />);
    expect(await screen.findByRole('link', { name: 'Оплатить в Kaspi' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Отменить счёт' })).not.toBeInTheDocument();
  });

  it('оплата не настроена: нет Kaspi или тарифов с ценой', async () => {
    signIn('owner', ['billing.view', 'billing.pay']);
    mockApi({ 'GET /api/billing': billing({ kaspi: null }) });
    const view = renderPage(<BillingPage />);
    expect(await screen.findByText('Оплата пока не настроена. Напишите в поддержку Zafiro.')).toBeInTheDocument();
    view.unmount();
    mockApi({ 'GET /api/billing': billing({ plans: [] }) });
    renderPage(<BillingPage />);
    expect(await screen.findByText('Оплата пока не настроена. Напишите в поддержку Zafiro.')).toBeInTheDocument();
  });

  // Ревью 22.11.2026: ссылка не https → кнопки нет, значит и шага «по кнопке ниже» быть не должно.
  it('счёт при негодной ссылке Kaspi: шагов оплаты нет, «Оплата пока не настроена»', async () => {
    signIn('owner', ['billing.view', 'billing.pay']);
    mockApi({ 'GET /api/billing': billing({ pending: invoice(), kaspi: { url: 'http://pay.kaspi.kz/pay/zafiro', recipient: 'ИП Zafiro' } }) });
    renderPage(<BillingPage />);
    expect(await screen.findByText('Счёт ZF-00042 ожидает оплаты')).toBeInTheDocument();
    expect(screen.getByText('Оплата пока не настроена. Напишите в поддержку Zafiro.')).toBeInTheDocument();
    expect(screen.queryByText('Откройте оплату в Kaspi по кнопке ниже.')).not.toBeInTheDocument();
    expect(screen.queryByText(/Переведите/)).not.toBeInTheDocument();
    expect(screen.queryByRole('link', { name: 'Оплатить в Kaspi' })).not.toBeInTheDocument();
    // Счёт по-прежнему можно отменить.
    expect(screen.getByRole('button', { name: 'Отменить счёт' })).toBeInTheDocument();
  });

  it('ошибка сервера показывается, как $errors->first()', async () => {
    signIn('owner', ['billing.view', 'billing.pay']);
    mockApi({
      'GET /api/billing': billing(),
      'POST /api/billing/invoices': () => apiError(422, { message: 'x', errors: { invoice: ['У клуба уже есть неоплаченный счёт: оплатите или отмените его.'] } }),
    });
    renderPage(<BillingPage />);
    fireEvent.click(await screen.findByRole('button', { name: 'Выставить счёт' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('У клуба уже есть неоплаченный счёт: оплатите или отмените его.');
  });

  it('ссылка на оплату — только https', () => {
    expect(safePayUrl('https://pay.kaspi.kz/pay/zafiro')).toBe('https://pay.kaspi.kz/pay/zafiro');
    for (const bad of ['http://pay.kaspi.kz/pay', 'javascript:alert(1)', '//pay.kaspi.kz', 'https://a b', '']) expect(safePayUrl(bad)).toBeNull();
  });
});
