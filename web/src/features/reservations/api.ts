import { queryOptions } from '@tanstack/react-query';
import { createApiClient, unwrap } from '../../api/http';
import type { components, paths } from '../../api/gen/reservations';
import { intParam } from '../../lib/searchParams';

/*
 * Брони ПК (api/reservations.yaml). Laravel: ComputerReservationController, ComputerReservations.
 * Ключи Query — ['reservations', …]; изменения других сотрудников — событие reservation.changed (WebSocket).
 */

export const reservationsApi = createApiClient<paths>();

type S = components['schemas'];
export type Reservation = S['Reservation'];
export type ReservationInput = S['ReservationInput'];
export type ReservationCustomer = S['ReservationCustomer'];

export interface ReservationsSearch {
  readonly date?: string;
  readonly page?: number;
}

export function parseReservationsSearch(s: Record<string, unknown>): ReservationsSearch {
  return {
    date: typeof s.date === 'string' && s.date !== '' ? s.date : undefined,
    page: intParam(s.page, 2),
  };
}

export const reservationsQuery = (p: ReservationsSearch) =>
  queryOptions({
    queryKey: ['reservations', p.date ?? '', p.page ?? 1],
    queryFn: ({ signal }) => unwrap(reservationsApi.GET('/api/reservations', { params: { query: { date: p.date, page: p.page } }, signal })),
  });

export const createReservation = (body: ReservationInput) => unwrap(reservationsApi.POST('/api/reservations', { body }));
export const cancelReservation = (id: number) =>
  unwrap(reservationsApi.POST('/api/reservations/{reservationId}/cancel', { params: { path: { reservationId: id } } }));
export const arriveReservation = (id: number) =>
  unwrap(reservationsApi.POST('/api/reservations/{reservationId}/arrive', { params: { path: { reservationId: id } } }));

/** reservations.customers: пользователи клуба по имени или телефону (не больше 10). */
export async function searchCustomers(q: string, signal: AbortSignal): Promise<ReservationCustomer[]> {
  const r = await unwrap(reservationsApi.GET('/api/reservations/customers', { params: { query: { q } }, signal }));
  return r.data;
}

