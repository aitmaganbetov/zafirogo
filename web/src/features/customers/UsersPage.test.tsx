import { fireEvent, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { session } from '../../auth/session';
import { mockApi, renderPage, signIn, stubDialog } from '../../test/pages';
import { UsersPage } from './UsersPage';

/* FE-11: «Пользователи» — балансы из тиынов, пополнение/списание баланса владельцем, новый клиент с балансом. */

vi.mock('@tanstack/react-router', async (original) => ({
  ...(await original<typeof import('@tanstack/react-router')>()),
  useNavigate: () => vi.fn(async () => undefined),
}));

const customer = {
  user_id: 9, name: 'Айгерим', phone: '+77011234567', email: null, role: 'client', discount_effective: '5.00', discount_cumulative: false,
  account_locked: false, discount: '5.00', total_spent_tiyn: 123456789, balance_tiyn: 150050, bonus_balance_tiyn: 5,
};
const page = { items: [customer], next_after_id: null, prev_before_id: null, total: 1 };
const saved = { customer, message: 'Клиент сохранён.' };
const OWNER = ['customers.view', 'customers.create', 'customers.update', 'customer_money.update'];

describe('пользователи: деньги', () => {
  beforeEach(() => stubDialog());
  afterEach(() => {
    vi.unstubAllGlobals();
    session.clear();
  });

  const row = () => document.querySelector('[data-customer-row]') as HTMLElement;
  const input = (id: string) => document.getElementById(id) as HTMLInputElement;
  const submit = (selector: string) => fireEvent.submit(document.querySelector(selector) as HTMLFormElement);

  it('балансы и «потрачено» — из целых тиынов, с копейками', async () => {
    signIn('owner', OWNER);
    mockApi({ 'GET /api/customers': page });
    renderPage(<UsersPage search={{}} />);
    await screen.findByText('Айгерим');
    const cells = [...row().querySelectorAll('td')].map((td) => td.textContent);
    expect(cells.slice(2, 6)).toEqual(['5.00%', '1 234 567.89', '1 500.50', '0.05']);
  });

  it('владелец пополняет бонусный баланс: «250.75» → 25 075 тиынов, с ключом идемпотентности', async () => {
    signIn('owner', OWNER);
    const api = mockApi({ 'GET /api/customers': page, 'PATCH /api/customers/9': saved });
    renderPage(<UsersPage search={{}} />);
    await screen.findByText('Айгерим');
    fireEvent.click(row().querySelector('[data-edit-user]') as HTMLElement);
    fireEvent.change(input('edit-user-top-up'), { target: { value: '250.75' } });
    fireEvent.change(input('edit-user-top-up-balance'), { target: { value: 'bonus_balance' } });
    fireEvent.change(input('edit-user-balance-action'), { target: { value: 'write_off' } });
    submit('[data-edit-form]');
    expect(await screen.findByText('Клиент сохранён.')).toBeInTheDocument();
    const [patch] = api.of('PATCH', '/api/customers/9');
    expect(patch?.body).toEqual({ name: 'Айгерим', email: null, discount: '5.00', top_up_amount_tiyn: 25075, top_up_balance: 'bonus_balance', balance_action: 'write_off' });
    expect(patch?.headers.get('Idempotency-Key')).toMatch(/^[0-9a-f-]{36}$/);
  });

  it('без суммы — обычная правка: денежных полей и ключа идемпотентности в запросе нет', async () => {
    signIn('owner', OWNER);
    const api = mockApi({ 'GET /api/customers': page, 'PATCH /api/customers/9': saved });
    renderPage(<UsersPage search={{}} />);
    await screen.findByText('Айгерим');
    fireEvent.click(row().querySelector('[data-edit-user]') as HTMLElement);
    fireEvent.change(input('edit-user-name'), { target: { value: 'Айгерим К.' } });
    submit('[data-edit-form]');
    await waitFor(() => expect(api.of('PATCH', '/api/customers/9')).toHaveLength(1));
    const [patch] = api.of('PATCH', '/api/customers/9');
    expect(patch?.body).toEqual({ name: 'Айгерим К.', email: null, discount: '5.00' });
    expect(patch?.headers.get('Idempotency-Key')).toBeNull();
  });

  it('сумма с тремя знаками — ошибка в форме, запрос не уходит (Q-002)', async () => {
    signIn('owner', OWNER);
    const api = mockApi({ 'GET /api/customers': page, 'PATCH /api/customers/9': saved });
    renderPage(<UsersPage search={{}} />);
    await screen.findByText('Айгерим');
    fireEvent.click(row().querySelector('[data-edit-user]') as HTMLElement);
    fireEvent.change(input('edit-user-top-up'), { target: { value: '10.005' } });
    submit('[data-edit-form]');
    await waitFor(() => expect(document.querySelector('#user-edit-dialog .bg-red-50, dialog[open] [class*="red"]')).not.toBeNull());
    expect(api.of('PATCH', '/api/customers/9')).toHaveLength(0);
  });

  it('админ без роли владельца не видит пополнения без кассы', async () => {
    signIn('admin', OWNER);
    mockApi({ 'GET /api/customers': page });
    renderPage(<UsersPage search={{}} />);
    await screen.findByText('Айгерим');
    fireEvent.click(row().querySelector('[data-edit-user]') as HTMLElement);
    expect(input('edit-user-top-up')).toBeNull();
    expect(input('edit-user-discount')).not.toBeNull();
  });

  it('новый клиент: стартовые балансы в тиынах; без права на деньги — без денежных полей', async () => {
    signIn('owner', OWNER);
    const api = mockApi({ 'GET /api/customers': page, 'POST /api/customers': saved });
    const view = renderPage(<UsersPage search={{}} />);
    await screen.findByText('Айгерим');
    fireEvent.click(document.getElementById('open-user-dialog') as HTMLElement);
    fireEvent.change(input('new-user-name'), { target: { value: 'Данияр' } });
    fireEvent.change(input('new-user-phone'), { target: { value: '7001234567' } });
    const form = document.querySelector('form[action="/users"]') as HTMLFormElement;
    const byName = (name: string) => form.querySelector(`[name="${name}"]`) as HTMLInputElement;
    fireEvent.change(byName('password'), { target: { value: 'secret-123' } });
    fireEvent.change(byName('password_confirmation'), { target: { value: 'secret-123' } });
    fireEvent.change(byName('balance'), { target: { value: '1000.5' } });
    fireEvent.change(byName('bonus_balance'), { target: { value: '0.07' } });
    fireEvent.submit(form);
    await waitFor(() => expect(api.of('POST', '/api/customers')).toHaveLength(1));
    const [post] = api.of('POST', '/api/customers');
    expect(post?.body).toMatchObject({ name: 'Данияр', phone: '7001234567', balance_tiyn: 100050, bonus_balance_tiyn: 7 });
    expect(post?.headers.get('Idempotency-Key')).toMatch(/^[0-9a-f-]{36}$/);
    view.unmount();

    signIn('cashier', ['customers.view', 'customers.create']);
    const api2 = mockApi({ 'GET /api/customers': page, 'POST /api/customers': saved });
    renderPage(<UsersPage search={{}} />);
    await screen.findByText('Айгерим');
    fireEvent.click(document.getElementById('open-user-dialog') as HTMLElement);
    const form2 = document.querySelector('form[action="/users"]') as HTMLFormElement;
    expect(form2.querySelector('[name="balance"]')).toBeNull();
    fireEvent.change(input('new-user-name'), { target: { value: 'Данияр' } });
    fireEvent.change(input('new-user-phone'), { target: { value: '7001234567' } });
    fireEvent.change(form2.querySelector('[name="password"]') as HTMLInputElement, { target: { value: 'secret-123' } });
    fireEvent.change(form2.querySelector('[name="password_confirmation"]') as HTMLInputElement, { target: { value: 'secret-123' } });
    fireEvent.submit(form2);
    await waitFor(() => expect(api2.of('POST', '/api/customers')).toHaveLength(1));
    const body = api2.of('POST', '/api/customers')[0]?.body as Record<string, unknown>;
    expect(Object.keys(body).sort()).toEqual(['email', 'name', 'password', 'password_confirmation', 'phone']);
  });
});
