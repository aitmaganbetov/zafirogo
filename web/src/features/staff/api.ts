import { queryOptions } from '@tanstack/react-query';
import { createApiClient, unwrap } from '../../api/http';
import type { components, paths } from '../../api/gen/staff';

/*
 * Работники, приглашения и права сотрудников (api/staff.yaml). Laravel: WorkerController,
 * ClubInvitationController::store(), PermissionController. Ключи Query — ['staff', …].
 */

export const staffApi = createApiClient<paths>();

type S = components['schemas'];
export type Worker = S['Worker'];
export type WorkerPage = S['WorkerPage'];
export type StaffPermissions = S['StaffPermissions'];
export type PermissionStaffMember = S['PermissionStaffMember'];

/** ?cursor=… (вперёд) / ?before=… (назад) — keyset вместо ?page= (paginate(50)). */
export interface WorkersSearch {
  readonly cursor?: string;
  readonly before?: string;
}

const str = (v: unknown): string | undefined => (typeof v === 'string' && v !== '' && v.length <= 512 ? v : undefined);

export function parseWorkersSearch(s: Record<string, unknown>): WorkersSearch {
  const cursor = str(s.cursor);
  return { cursor, before: cursor === undefined ? str(s.before) : undefined };
}

export function workersHref(p: WorkersSearch): string {
  const params = new URLSearchParams();
  if (p.cursor) params.set('cursor', p.cursor);
  if (p.before) params.set('before', p.before);
  const qs = params.toString();
  return qs ? `/workers?${qs}` : '/workers';
}

export const workersQuery = (p: WorkersSearch) =>
  queryOptions({
    queryKey: ['staff', 'workers', p.cursor ?? '', p.before ?? ''],
    queryFn: () => unwrap(staffApi.GET('/api/workers', { params: { query: { cursor: p.cursor, before: p.before } } })),
  });

export const permissionStaffQuery = () =>
  queryOptions({ queryKey: ['staff', 'permissions'], queryFn: () => unwrap(staffApi.GET('/api/settings/permissions')) });

export const staffPermissionsQuery = (membershipId: number) =>
  queryOptions({
    queryKey: ['staff', 'permissions', membershipId],
    queryFn: () => unwrap(staffApi.GET('/api/settings/permissions/{membershipId}', { params: { path: { membershipId } } })),
  });

export const addWorker = (phone: string, role: string) => unwrap(staffApi.POST('/api/workers', { body: { phone, role } }));

export const changeWorkerRole = (membershipId: number, role: string) =>
  unwrap(staffApi.PATCH('/api/workers/{membershipId}', { params: { path: { membershipId } }, body: { role } }));

export const removeWorker = (membershipId: number) =>
  unwrap(staffApi.DELETE('/api/workers/{membershipId}', { params: { path: { membershipId } } }));

export const createInvitation = (email: string | null, role: string) =>
  unwrap(staffApi.POST('/api/club-invitations', { body: { email, role } }));

export const updateStaffPermissions = (membershipId: number, rights: Record<string, Record<string, boolean>>) =>
  unwrap(staffApi.PUT('/api/settings/permissions/{membershipId}', { params: { path: { membershipId } }, body: { rights } }));

export const resetStaffPermissions = (membershipId: number) =>
  unwrap(staffApi.POST('/api/settings/permissions/{membershipId}/reset', { params: { path: { membershipId } } }));

/** Полный адрес ссылки-приглашения: route('club-invitations.show', $token) — адрес сайта + путь из ответа. */
export function invitationUrl(path: string, origin: string = location.origin): string {
  return origin + path;
}
