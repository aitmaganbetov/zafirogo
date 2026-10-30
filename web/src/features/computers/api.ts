import { queryOptions } from '@tanstack/react-query';
import { createApiClient, unwrap } from '../../api/http';
import type { components, paths } from '../../api/gen/computers';

/*
 * Список ПК и подключение ПК (api/computers.yaml). Laravel: ClubComputerListController; коды регистрации, ключи
 * образа и ожидающие ПК — сервис agent/enroll (Q-037). Ключи Query — ['computers', …].
 */

export const computersApi = createApiClient<paths>();
type S = components['schemas'];
export type ComputerList = S['ComputerList'];
export type ListedComputer = S['ListedComputer'];
export type Enrollment = S['Enrollment'];

export interface ComputersSearch {
  readonly page?: number;
}

export function parseComputersSearch(s: Record<string, unknown>): ComputersSearch {
  const n = typeof s.page === 'number' ? s.page : typeof s.page === 'string' ? Number(s.page) : NaN;
  return { page: Number.isSafeInteger(n) && n > 1 ? n : undefined };
}

export const computersQuery = (page?: number) =>
  queryOptions({
    queryKey: ['computers', 'list', page ?? 1],
    queryFn: () => unwrap(computersApi.GET('/api/club-computers', { params: { query: { page } } })),
  });

export const enrollmentQuery = () =>
  queryOptions({
    queryKey: ['computers', 'enrollment'],
    queryFn: () => unwrap(computersApi.GET('/api/agent-enrollment')),
  });

export const createCode = (zoneId: number | null) => unwrap(computersApi.POST('/api/agent-enrollment/codes', { body: { zone_id: zoneId } }));
export const cancelCode = (id: number) =>
  unwrap(computersApi.DELETE('/api/agent-enrollment/codes/{codeId}', { params: { path: { codeId: id } } }));
export const createImageKey = (body: S['ImageKeyInput']) => unwrap(computersApi.POST('/api/agent-enrollment/image-keys', { body }));
export const revokeImageKey = (id: number) =>
  unwrap(computersApi.DELETE('/api/agent-enrollment/image-keys/{keyId}', { params: { path: { keyId: id } } }));
export const approvePending = (id: number, zoneId: number | null) =>
  unwrap(computersApi.POST('/api/agent-enrollment/pending/{pendingId}/approve', { params: { path: { pendingId: id } }, body: { zone_id: zoneId } }));
export const rejectPending = (id: number) =>
  unwrap(computersApi.POST('/api/agent-enrollment/pending/{pendingId}/reject', { params: { path: { pendingId: id } } }));

/** «Перерегистрировать ПК»: сертификаты карточки отозваны, код — для неё (показывается один раз). */
export const reregisterComputer = (id: number) =>
  unwrap(computersApi.POST('/api/agent-enrollment/computers/{computerId}/reregister', { params: { path: { computerId: id } } }));
/** «Отключить ПК»: все сертификаты карточки отозваны. */
export const revokeComputer = (id: number) =>
  unwrap(computersApi.POST('/api/agent-enrollment/computers/{computerId}/revoke', { params: { path: { computerId: id } } }));

/** number_format($mb / 1024, 1, ',', ' ') . ' GB' — объём памяти (показ, не деньги). */
export function formatRam(mb: number): string {
  const tenths = Math.round((mb * 10) / 1024);
  const whole = String(Math.floor(tenths / 10)).replace(/\B(?=(\d{3})+(?!\d))/g, ' ');
  return `${whole},${tenths % 10} GB`;
}

/** Кто может выдавать коды и подтверждать ПК (enroll.RegisterRoles = ClubComputerPolicy::register). */
export const REGISTER_ROLES = ['owner', 'admin', 'manager', 'operator'];
