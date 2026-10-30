import { queryOptions } from '@tanstack/react-query';
import { createApiClient, idempotencyKey, unwrap } from '../../api/http';
import type { components, paths } from '../../api/gen/clubmap';
import type { paths as reservationPaths } from '../../api/gen/reservations';
import type { paths as sessionPaths } from '../../api/gen/sessions';

/*
 * Карта клуба (api/clubmap.yaml; бронь — api/reservations.yaml, гостевая сессия — api/sessions.yaml).
 * Laravel: ClubZoneController, ClubComputerController, ComputerReservationController::storeMany(),
 * GuestSessionController. Ключи Query — ['clubmap', …].
 */

export const clubmapApi = createApiClient<paths>();
const reservationsApi = createApiClient<reservationPaths>();
const sessionsApi = createApiClient<sessionPaths>();

type S = components['schemas'];
export type ClubMapPage = S['ClubMapPage'];
export type Zone = S['Zone'];
export type ComputerCard = S['ComputerCard'];
export type ComputerState = S['ComputerState'];
export type MapTariff = S['MapTariff'];
export type ZoneInput = S['ZoneInput'];
export type ControlResult = S['ControlResult'];

export const clubMapKey = ['clubmap', 'page'] as const;

export const clubMapQuery = () =>
  queryOptions({
    queryKey: clubMapKey,
    queryFn: () => unwrap(clubmapApi.GET('/api/club-map')),
  });

/** Состояние нескольких ПК (досылка после события computer.state). */
export const fetchStates = (ids: readonly number[]) =>
  unwrap(clubmapApi.GET('/api/club-map/state', { params: { query: { ids: ids.join(',') } } }));

export const createZone = (body: ZoneInput) => unwrap(clubmapApi.POST('/api/club-map', { body }));
export const updateZone = (id: number, body: ZoneInput) => unwrap(clubmapApi.PUT('/api/club-map/{zoneId}', { params: { path: { zoneId: id } }, body }));
export const deleteZone = (id: number) => unwrap(clubmapApi.DELETE('/api/club-map/{zoneId}', { params: { path: { zoneId: id } } }));
export const saveZoneLayout = (id: number, body: S['ZoneLayout']) =>
  unwrap(clubmapApi.PATCH('/api/club-map/{zoneId}/layout', { params: { path: { zoneId: id } }, body }));
export const addComputer = (zoneId: number) =>
  unwrap(clubmapApi.POST('/api/club-map/{zoneId}/computers', { params: { path: { zoneId } } }));
export const updateComputer = (id: number, body: S['ComputerInput']) =>
  unwrap(clubmapApi.PATCH('/api/club-computers/{computerId}', { params: { path: { computerId: id } }, body }));
export const deleteComputer = (id: number) =>
  unwrap(clubmapApi.DELETE('/api/club-computers/{computerId}', { params: { path: { computerId: id } } }));
export const saveComputerLayout = (id: number, body: S['ComputerLayout']) =>
  unwrap(clubmapApi.PATCH('/api/club-computers/{computerId}/layout', { params: { path: { computerId: id } }, body }));
export const controlComputers = (ids: number[], action: string) =>
  unwrap(clubmapApi.POST('/api/club-computers/control', { body: { computer_ids: ids, action } }));
export const endSessions = (ids: number[]) => unwrap(clubmapApi.POST('/api/club-computers/end-sessions', { body: { computer_ids: ids } }));
export const notifyComputers = (ids: number[], message: string) =>
  unwrap(clubmapApi.POST('/api/club-computers/notify', { body: { computer_ids: ids, message } }));

/** reservations.customers: клиенты клуба по имени или телефону. */
export async function searchCustomers(q: string, signal: AbortSignal) {
  const r = await unwrap(reservationsApi.GET('/api/reservations/customers', { params: { query: { q } }, signal }));
  return r.data;
}

export interface BookingInput {
  readonly computer_ids: number[];
  readonly user_id: number;
  readonly starts_at: string;
  readonly duration_minutes: number;
  readonly note: string | null;
}

/** reservations.store-many: бронь выбранных ПК одному клиенту. */
export const bookComputers = (body: BookingInput) => unwrap(reservationsApi.POST('/api/reservations/many', { body }));

/** club-computers.guest-session: время гостю без аккаунта с оплатой в открытую смену (денежный POST). */
export const sellGuestSession = (body: { computer_ids: number[]; tariff_id?: number; minutes?: number; payment_method: string }, key: string = idempotencyKey()) =>
  unwrap(sessionsApi.POST('/api/club-computers/guest-session', { params: { header: { 'Idempotency-Key': key } }, body }));
