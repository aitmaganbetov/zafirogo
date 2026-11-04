import { z } from 'zod';
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

/** Сумма поля «необязательно»: пусто — 0 (old('balance', 0)). */
const optionalTiyn = (field: string) =>
  z.string().transform((s, ctx) => {
    if (s.trim() === '') return 0;
    const r = parseMoney(s);
    if (!r.ok) {
      ctx.addIssue({ code: 'custom', message: moneyError(field, s) ?? '' });
      return z.NEVER;
    }
    return r.tiyn;
  });

const emptyToNull = z.string().transform((s) => (s.trim() === '' ? null : s.trim()));

const createSchema = (canSetMoney: boolean) =>
  z.object({
    name: z.string(),
    phone: z.string().transform((s) => phoneDigits(s)),
    email: emptyToNull,
    password: z.string(),
    password_confirmation: z.string(),
    discount: canSetMoney ? emptyToNull : z.string().transform(() => null),
    balance: canSetMoney ? optionalTiyn('balance') : z.string().transform(() => 0),
    bonus_balance: canSetMoney ? optionalTiyn('bonus_balance') : z.string().transform(() => 0),
  });

function firstIssue(e: z.ZodError): string {
  return e.issues[0]?.message ?? '';
}

/** «Новый клиент» → POST /api/customers. Скидка и балансы — только с правом customer_money.update (поля формы). */
export function buildCreateBody(v: CreateForm, canSetMoney: boolean): Built<CustomerCreateRequest> {
  const r = createSchema(canSetMoney).safeParse(v);
  if (!r.success) return { ok: false, error: firstIssue(r.error) };
  const d = r.data;
  const body: CustomerCreateRequest = {
    name: d.name, phone: d.phone, email: d.email, password: d.password, password_confirmation: d.password_confirmation,
  };
  if (canSetMoney) {
    body.discount = d.discount;
    body.balance_tiyn = d.balance;
    body.bonus_balance_tiyn = d.bonus_balance;
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
