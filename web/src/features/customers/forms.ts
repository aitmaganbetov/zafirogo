import { PARSE_MONEY_MESSAGES, parseMoney } from '../../lib/money';
import { phoneDigits } from '../../lib/phone';
import type { CustomerCreateRequest, CustomerUpdateRequest } from './api';

/*
 * Формы экрана «Пользователи» → тела запросов API. Суммы — строки поля ввода → целые тиыны (parseMoney, решение
 * Q-002: > 2 знаков — ошибка формы, без округления). Остальное проверяет сервер (все ошибки сразу, как FormRequest).
 */

export interface CreateForm {
  name: string;
  phone: string;
  email: string;
  password: string;
  password_confirmation: string;
  discount: string;
  balance: string;
  bonus_balance: string;
}

export interface EditForm {
  name: string;
  email: string;
  discount: string;
  top_up_amount: string;
  top_up_balance: string;
  balance_action: string;
}

export type Built<T> = { readonly ok: true; readonly body: T } | { readonly ok: false; readonly error: string };

/** Ошибка суммы — текст валидатора Laravel с именем поля (как :attribute — поле с пробелами). */
function moneyError(field: string, input: string): string | null {
  const r = parseMoney(input);
  if (r.ok) return null;
  return PARSE_MONEY_MESSAGES[r.error].replace('{{attribute}}', field.replace(/_/g, ' '));
}

/** Сумма поля «необязательно»: пусто — 0 (old('balance', 0)); ошибка — текст для формы. */
function optionalTiyn(field: string, input: string): { readonly tiyn: number } | { readonly error: string } {
  if (input.trim() === '') return { tiyn: 0 };
  const r = parseMoney(input);
  return r.ok ? { tiyn: r.tiyn } : { error: moneyError(field, input) ?? '' };
}

const emptyToNull = (s: string) => (s.trim() === '' ? null : s.trim());

/** «Новый клиент» → POST /api/customers. Скидка и балансы — только с правом customer_money.update (поля формы). */
export function buildCreateBody(v: CreateForm, canSetMoney: boolean): Built<CustomerCreateRequest> {
  const body: CustomerCreateRequest = {
    name: v.name, phone: phoneDigits(v.phone), email: emptyToNull(v.email), password: v.password, password_confirmation: v.password_confirmation,
  };
  if (canSetMoney) {
    // Порядок проверки — как порядок полей формы: первой показывается ошибка баланса, затем бонусов.
    const balance = optionalTiyn('balance', v.balance);
    if ('error' in balance) return { ok: false, error: balance.error };
    const bonus = optionalTiyn('bonus_balance', v.bonus_balance);
    if ('error' in bonus) return { ok: false, error: bonus.error };
    body.discount = emptyToNull(v.discount);
    body.balance_tiyn = balance.tiyn;
    body.bonus_balance_tiyn = bonus.tiyn;
  }
  return { ok: true, body };
}

/**
 * «Изменить клиента» → PATCH /api/customers/{id}. Скидка — только с правом customer_money.update; пополнение/списание —
 * только владельцу, и только если введена сумма (пусто — без операции).
 */
export function buildUpdateBody(v: EditForm, opts: { canSetMoney: boolean; canTopUp: boolean }): Built<CustomerUpdateRequest> {
  const body: CustomerUpdateRequest = { name: v.name, email: v.email.trim() === '' ? null : v.email.trim() };
  if (opts.canSetMoney && v.discount.trim() !== '') body.discount = v.discount.trim();
  if (opts.canTopUp && v.top_up_amount.trim() !== '') {
    const r = parseMoney(v.top_up_amount);
    if (!r.ok) return { ok: false, error: moneyError('top_up_amount', v.top_up_amount) ?? '' };
    body.top_up_amount_tiyn = r.tiyn;
    body.top_up_balance = v.top_up_balance;
    body.balance_action = v.balance_action;
  }
  return { ok: true, body };
}
