import { describe, expect, it } from 'vitest';
import { customersHref, parseCustomersSearch } from './api';
import { buildCreateBody, buildUpdateBody, type CreateForm, type EditForm } from './forms';

const create: CreateForm = {
  name: 'Клиент', phone: '700 123 45 67', email: '', password: 'secret', password_confirmation: 'secret',
  discount: '5', balance: '1500.50', bonus_balance: '',
};
const edit: EditForm = { name: 'Клиент', email: ' a@b.kz ', discount: '10.5', top_up_amount: '', top_up_balance: 'balance', balance_action: 'top_up' };

describe('формы «Пользователи»', () => {
  it('новый клиент: суммы в тиынах, пустой баланс — 0, телефон — цифры', () => {
    const r = buildCreateBody(create, true);
    expect(r).toEqual({
      ok: true,
      body: {
        name: 'Клиент', phone: '7001234567', email: null, password: 'secret', password_confirmation: 'secret',
        discount: '5', balance_tiyn: 150050, bonus_balance_tiyn: 0,
      },
    });
  });

  it('новый клиент без права customer_money: скидки и балансов в теле нет', () => {
    const r = buildCreateBody({ ...create, balance: '999' }, false);
    expect(r.ok && 'balance_tiyn' in r.body).toBe(false);
    expect(r.ok && 'discount' in r.body).toBe(false);
  });

  it('сумма с тремя знаками — ошибка формы, без округления (Q-002)', () => {
    expect(buildCreateBody({ ...create, balance: '1.005' }, true)).toEqual({ ok: false, error: 'The balance field must have 0-2 decimal places.' });
    expect(buildUpdateBody({ ...edit, top_up_amount: '0.001' }, { canSetMoney: true, canTopUp: true })).toEqual({
      ok: false, error: 'The top up amount field must have 0-2 decimal places.',
    });
  });

  it('изменение: пополнение только владельцу и только с суммой', () => {
    expect(buildUpdateBody(edit, { canSetMoney: true, canTopUp: true })).toEqual({
      ok: true, body: { name: 'Клиент', email: 'a@b.kz', discount: '10.5' },
    });
    expect(buildUpdateBody({ ...edit, top_up_amount: '50', balance_action: 'write_off' }, { canSetMoney: false, canTopUp: true })).toEqual({
      ok: true, body: { name: 'Клиент', email: 'a@b.kz', top_up_amount_tiyn: 5000, top_up_balance: 'balance', balance_action: 'write_off' },
    });
    expect(buildUpdateBody({ ...edit, top_up_amount: '50' }, { canSetMoney: false, canTopUp: false })).toEqual({
      ok: true, body: { name: 'Клиент', email: 'a@b.kz' },
    });
  });

  it('адрес страницы списка: keyset и поиск', () => {
    expect(parseCustomersSearch({ after: '25', before: '3', q: ' ' })).toEqual({ after: 25, before: undefined, q: undefined });
    expect(parseCustomersSearch({ before: 7, q: 'Ким' })).toEqual({ after: undefined, before: 7, q: 'Ким' });
    expect(parseCustomersSearch({ after: '-1', before: 'x' })).toEqual({ after: undefined, before: undefined, q: undefined });
    expect(customersHref({ q: 'Ким', after: 25 })).toBe('/users?q=%D0%9A%D0%B8%D0%BC&after=25');
    expect(customersHref({})).toBe('/users');
  });
});
