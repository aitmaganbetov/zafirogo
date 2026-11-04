import type { ClubRole, Membership } from './types';

/*
 * Права панели клуба «раздел.действие» — перенос Zafiro app/Support/ClubPermissions.php
 * (SECTIONS, allows(), byRole(), given()). Используется только для показа пунктов меню;
 * проверку доступа всё равно делает сервер (RequireClubArea).
 */

const STAFF: readonly ClubRole[] = ['admin', 'manager', 'cashier', 'operator'];
const DESK: readonly ClubRole[] = ['admin', 'manager', 'cashier'];
const MANAGERS: readonly ClubRole[] = ['admin', 'manager'];

type Action = 'view' | 'create' | 'update' | 'delete';

/** section => action => роли, у которых право есть по умолчанию (ClubPermissions::SECTIONS). */
export const SECTIONS: Readonly<Record<string, Partial<Record<Action, readonly ClubRole[]>>>> = {
  cash: { view: DESK, create: DESK, delete: MANAGERS },
  customers: { view: STAFF, create: STAFF, update: STAFF },
  customer_money: { update: MANAGERS },
  reservations: { view: STAFF, create: STAFF, update: STAFF, delete: STAFF },
  club_map: { view: STAFF, create: MANAGERS, update: MANAGERS, delete: MANAGERS },
  pc_control: { update: STAFF, delete: ['admin', 'manager', 'operator'] },
  tariffs: { view: MANAGERS, create: MANAGERS, update: MANAGERS, delete: [] },
  warehouse: { view: MANAGERS, create: MANAGERS, update: MANAGERS, delete: [] },
  services: { view: MANAGERS, create: MANAGERS, update: MANAGERS, delete: MANAGERS },
  stock: { view: STAFF, create: STAFF },
  orders: { view: STAFF, update: STAFF, delete: STAFF },
  chat: { view: STAFF, create: ['cashier', 'operator'] },
  todos: { view: STAFF, create: MANAGERS, update: STAFF, delete: MANAGERS },
  news: { view: MANAGERS, create: MANAGERS, update: MANAGERS, delete: MANAGERS },
  activity_logs: { view: MANAGERS },
  settings: { view: MANAGERS, update: MANAGERS },
  workers: { view: ['admin'], create: ['admin'], update: ['admin'], delete: ['admin'] },
  billing: { view: MANAGERS },
};

function split(right: string): [string, string] {
  const dot = right.indexOf('.');
  return dot === -1 ? [right, ''] : [right.slice(0, dot), right.slice(dot + 1)];
}

function rolesFor(right: string): readonly ClubRole[] | undefined {
  const [section, action] = split(right);
  const actions = SECTIONS[section];
  if (actions === undefined) return undefined;
  return (actions as Partial<Record<string, readonly ClubRole[]>>)[action];
}

/** ClubPermissions::exists() */
export function permissionExists(right: string): boolean {
  return rolesFor(right) !== undefined;
}

/** ClubPermissions::allows(): владелец может всё; иначе выбор владельца для сотрудника, иначе роль. */
export function allows(membership: Membership | null, right: string): boolean {
  if (membership === null || !permissionExists(right)) return false;
  if (membership.role === 'owner') return true;
  const given = membership.permissions[right];
  if (typeof given === 'boolean') return given;
  return (rolesFor(right) ?? []).some((role) => role === membership.role);
}
