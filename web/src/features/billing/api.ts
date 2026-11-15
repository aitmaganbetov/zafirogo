import { queryOptions } from '@tanstack/react-query';
import { createApiClient, isApiError, unwrap } from '../../api/http';
import type { components, paths } from '../../api/gen/billing';

/*
 * Экран «Подписка» и счета за подписку (api/billing.yaml, BillingController). Ключи Query — ['billing'].
 * Суммы приходят с сервера целыми тиынами, в том числе «К оплате» по срокам: во фронте сумма счёта не считается.
 */

export const billingApi = createApiClient<paths>();
type S = components['schemas'];
export type Billing = S['Billing'];
export type BillingSubscription = S['BillingSubscription'];
export type Invoice = S['Invoice'];
export type PayablePlan = S['PayablePlan'];

export const billingQuery = () =>
  queryOptions({
    queryKey: ['billing'],
    queryFn: ({ signal }) => unwrap(billingApi.GET('/api/billing', { signal })),
  });

export const createInvoice = (planId: number, months: number) =>
  unwrap(billingApi.POST('/api/billing/invoices', { body: { saas_plan_id: planId, months } }));

export const cancelInvoice = (invoiceId: number) =>
  unwrap(billingApi.POST('/api/billing/invoices/{invoiceId}/cancel', { params: { path: { invoiceId } } }));

/** $errors->first(): первая ошибка ответа (сервер уже перевёл текст на язык клуба). */
export function firstError(e: unknown): string {
  if (isApiError(e)) {
    const first = Object.values(e.fieldErrors)[0]?.[0];
    return first ?? e.message;
  }
  return e instanceof Error ? e.message : String(e);
}

/** Ссылку на оплату открываем, только если это https (сервер проверяет её при сохранении — правило url:https). */
export function safePayUrl(url: string): string | null {
  return /^https:\/\/[^\s]+$/i.test(url) ? url : null;
}
