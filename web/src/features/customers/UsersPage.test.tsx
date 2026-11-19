import { fireEvent, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { session } from '../../auth/session';
import { mockApi, renderPage, signIn, stubDialog } from '../../test/pages';
import { UsersPage } from './UsersPage';

/* FE-11: «Пользователи» — балансы из тиынов, пополнение/списание баланса владельцем, новый клиент с балансом. */

const navigate = vi.hoisted(() => vi.fn(async (_to: unknown) => undefined));
vi.mock('@tanstack/react-router', async (original) => ({
  ...(await original<typeof import('@tanstack/react-router')>()),
  useNavigate: () => navigate,
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

/* Старый Zafiro 35c36a0: поиск по имени, телефону в любом формате или email (UsersTest «finds customers by name…»). */
describe('пользователи: поиск', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    session.clear();
    navigate.mockClear();
  });

  const field = () => screen.getByRole('searchbox', { name: 'Поиск пользователей' }) as HTMLInputElement;

  it('запрос уходит в ?q=, пагинация сохраняет поиск', async () => {
    signIn('owner', OWNER);
    const api = mockApi({ 'GET /api/customers': { ...page, next_after_id: 9 } });
    renderPage(<UsersPage search={{ q: '8 700 123-45-67' }} />);
    await screen.findByText('Айгерим');
    expect(api.of('GET', '/api/customers')[0]?.search.get('q')).toBe('8 700 123-45-67');
    expect(field().value).toBe('8 700 123-45-67');
    expect(field().maxLength).toBe(100);
    expect(screen.getByRole('link', { name: 'Сбросить' })).toHaveAttribute('href', '/users');
    expect(document.querySelector('a[rel="next"]')).toHaveAttribute('href', '/users?q=8+700+123-45-67&after=9');
  });

  it('форма: trim и переход на /users?q=…; пустой запрос — без q', () => {
    signIn('owner', OWNER);
    mockApi({ 'GET /api/customers': page });
    renderPage(<UsersPage search={{}} />);
    expect(screen.queryByRole('link', { name: 'Сбросить' })).toBeNull();
    fireEvent.change(field(), { target: { value: '  dana@  ' } });
    fireEvent.click(screen.getByRole('button', { name: 'Найти' }));
    expect(navigate).toHaveBeenLastCalledWith({ href: '/users?q=dana%40' });
    fireEvent.change(field(), { target: { value: '   ' } });
    fireEvent.click(screen.getByRole('button', { name: 'Найти' }));
    expect(navigate).toHaveBeenLastCalledWith({ href: '/users' });
  });

  it('ничего не нашли — текст с запросом; без поиска — «Пользователей пока нет»', async () => {
    signIn('owner', OWNER);
    mockApi({ 'GET /api/customers': { ...page, items: [], total: 0 } });
    const view = renderPage(<UsersPage search={{ q: 'нет такого' }} />);
    expect(await screen.findByText('Никого не нашли по запросу «нет такого»')).toBeInTheDocument();
    view.unmount();
    renderPage(<UsersPage search={{}} />);
    expect(await screen.findByText('Пользователей пока нет')).toBeInTheDocument();
  });
});
