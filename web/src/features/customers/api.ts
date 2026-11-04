import { queryOptions } from '@tanstack/react-query';
import { createApiClient, unwrap } from '../../api/http';
import type { components, paths } from '../../api/gen/customers';

/*
 * Клиенты клуба (api/customers.yaml): список с keyset-пагинацией и поиском, создание, изменение (с пополнением/
 * списанием владельцем без кассы), сброс пароля. Laravel: UserController. Ключи Query — ['customers', …].
 */

export const customersApi = createApiClient<paths>();

type S = components['schemas'];
export type Customer = S['Customer'];
export type CustomerPage = S['CustomerPage'];
export type CustomerCreateRequest = S['CustomerCreateRequest'];
export type CustomerUpdateRequest = S['CustomerUpdateRequest'];
export type CustomerSaved = S['CustomerSaved'];
export type CustomerPasswordReset = S['CustomerPasswordReset'];

/** UserController::index() paginate(25). */
export const CUSTOMERS_PER_PAGE = 25;

/** Параметры страницы списка (?after=…, ?before=…, ?q=…). */
export interface CustomersSearch {
  readonly after?: number;
  readonly before?: number;
  readonly q?: string;
}

const positive = (v: unknown): number | undefined => {
  const n = typeof v === 'number' ? v : typeof v === 'string' && /^\d+$/.test(v) ? Number(v) : NaN;
  return Number.isSafeInteger(n) && n > 0 ? n : undefined;
};

export function parseCustomersSearch(s: Record<string, unknown>): CustomersSearch {
  const q = typeof s.q === 'string' && s.q.trim() !== '' ? s.q.slice(0, 100) : undefined;
  const after = positive(s.after);
  return { after, before: after === undefined ? positive(s.before) : undefined, q };
}

/** URL страницы списка с сохранением поиска (withQueryString()). */
export function customersHref(p: CustomersSearch): string {
  const params = new URLSearchParams();
  if (p.q) params.set('q', p.q);
  if (p.after) params.set('after', String(p.after));
  if (p.before) params.set('before', String(p.before));
  const qs = params.toString();
  return qs ? `/users?${qs}` : '/users';
}

export const customersQuery = (p: CustomersSearch) =>
  queryOptions({
    queryKey: ['customers', 'list', p.q ?? '', p.after ?? 0, p.before ?? 0],
    queryFn: () =>
      unwrap(
        customersApi.GET('/api/customers', {
          params: { query: { q: p.q, after_id: p.after, before_id: p.before, limit: CUSTOMERS_PER_PAGE } },
        }),
      ),
  });

export function createCustomer(body: CustomerCreateRequest, key: string): Promise<CustomerSaved> {
  return unwrap(customersApi.POST('/api/customers', { params: { header: { 'Idempotency-Key': key } }, body }));
}

export function updateCustomer(userId: number, body: CustomerUpdateRequest, key: string | null): Promise<CustomerSaved> {
  return unwrap(
    customersApi.PATCH('/api/customers/{userId}', {
      params: { path: { userId }, header: key ? { 'Idempotency-Key': key } : {} },
      body,
    }),
  );
}

export function resetCustomerPassword(userId: number): Promise<CustomerPasswordReset> {
  return unwrap(customersApi.POST('/api/customers/{userId}/password', { params: { path: { userId } } }));
}

/*
 * Новый пароль клиента показывается один раз (users-password.blade.php, ответ no-store): держим его только в памяти
 * вкладки до показа страницы — не в URL, history.state или sessionStorage. Перезагрузка страницы — пароля нет.
 */
let revealed: CustomerPasswordReset | null = null;

export function revealPassword(r: CustomerPasswordReset): void {
  revealed = r;
}

/** Пароль для показа (страница читает его при монтировании и сразу забывает — forgetRevealedPassword). */
export function peekRevealedPassword(userId: number): CustomerPasswordReset | null {
  return revealed !== null && revealed.user_id === userId ? revealed : null;
}

export function forgetRevealedPassword(): void {
  revealed = null;
}
