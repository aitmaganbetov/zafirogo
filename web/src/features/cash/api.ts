import { queryOptions } from '@tanstack/react-query';
import { createApiClient, idempotencyKey, unwrap } from '../../api/http';
import type { components, paths } from '../../api/gen/cash';

/*
 * Касса (api/cash.yaml): смена и итоги, витрина POS страницами, поиск клиента, операции смены, чек, X/Z-отчёты.
 * Laravel: CashRegisterController. Ключи Query — ['cash', …]; денежные POST — с Idempotency-Key.
 */

export const cashApi = createApiClient<paths>();

type S = components['schemas'];
export type CashShiftState = S['CashShiftState'];
export type CashShift = S['CashShift'];
export type CashCatalog = S['CashCatalog'];
export type CashProduct = S['CashProduct'];
export type CashService = S['CashService'];
export type CashCustomer = S['CashCustomer'];
export type CashReceipt = S['CashReceipt'];
export type CashTransaction = S['CashTransaction'];
export type CashReport = S['CashReport'];
export type CashOperationPage = S['CashOperationPage'];

/** Товаров витрины на одну подгрузку (решение Q-070: вместо всех товаров в HTML). */
export const POS_PAGE_SIZE = 60;

export const shiftQuery = () =>
  queryOptions({ queryKey: ['cash', 'shift'], queryFn: () => unwrap(cashApi.GET('/api/cash/shift')) });

export const catalogQuery = () =>
  queryOptions({ queryKey: ['cash', 'catalog'], queryFn: () => unwrap(cashApi.GET('/api/cash/catalog')) });

export function fetchProducts(category: string, cursor: string | null) {
  return unwrap(
    cashApi.GET('/api/cash/products', {
      params: { query: { category: category || undefined, cursor: cursor ?? undefined, limit: POS_PAGE_SIZE } },
    }),
  );
}

export const customersQuery = (q: string) =>
  queryOptions({
    queryKey: ['cash', 'customers', q],
    queryFn: ({ signal }) => unwrap(cashApi.GET('/api/cash/customers', { params: { query: { q: q || undefined, limit: 20 } }, signal })),
    staleTime: 10_000,
  });

export const operationsQuery = (page: number) =>
  queryOptions({
    queryKey: ['cash', 'operations', page],
    queryFn: () => unwrap(cashApi.GET('/api/cash/operations', { params: { query: { page } } })),
  });

export const receiptQuery = (id: number) =>
  queryOptions({
    queryKey: ['cash', 'receipt', id],
    queryFn: () => unwrap(cashApi.GET('/api/cash/receipts/{transactionId}', { params: { path: { transactionId: id } } })),
  });

export const xReportQuery = () =>
  queryOptions({ queryKey: ['cash', 'report', 'x'], queryFn: () => unwrap(cashApi.GET('/api/cash/reports/x')) });

export const zReportQuery = (shiftId: number) =>
  queryOptions({
    queryKey: ['cash', 'report', 'z', shiftId],
    queryFn: () => unwrap(cashApi.GET('/api/cash/shifts/{shiftId}/z-report', { params: { path: { shiftId } } })),
  });

const key = (k?: string) => ({ header: { 'Idempotency-Key': k ?? idempotencyKey() } });

export const openShift = (openingTiyn: number, k?: string) =>
  unwrap(cashApi.POST('/api/cash/shifts', { params: key(k), body: { opening_balance_tiyn: openingTiyn } }));

export const createSale = (items: S['SaleItem'][], k?: string) =>
  unwrap(cashApi.POST('/api/cash/sales', { params: key(k), body: { items } }));

export const createTopUp = (body: S['TopUpRequest'], k?: string) =>
  unwrap(cashApi.POST('/api/cash/balance-top-ups', { params: key(k), body }));

export const refundReceipt = (id: number, k?: string) =>
  unwrap(cashApi.POST('/api/cash/receipts/{transactionId}/refund', { params: { ...key(k), path: { transactionId: id } } }));

export const issueZNonce = (shiftId: number) =>
  unwrap(cashApi.POST('/api/cash/shifts/{shiftId}/z-report/nonce', { params: { path: { shiftId } } }));

export const markZPrinted = (shiftId: number, nonce: string) =>
  unwrap(cashApi.POST('/api/cash/shifts/{shiftId}/z-report/printed', { params: { path: { shiftId } }, body: { nonce } }));

export const closeShift = (shiftId: number, k?: string) =>
  unwrap(cashApi.POST('/api/cash/shifts/{shiftId}/close', { params: { ...key(k), path: { shiftId } } }));
