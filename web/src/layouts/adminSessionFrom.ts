import type { Me } from '../auth/session';
import { SECTIONS } from './permissions';
import type { AdminSession, ClubRole, Membership, PermissionOverrides } from './types';

/** Права из me.permissions в виде «выбора владельца» для permissions.allows(): всё известное — да/нет. */
export function permissionsOf(me: Me): PermissionOverrides {
  const out: Record<string, boolean> = {};
  for (const [section, actions] of Object.entries(SECTIONS)) {
    for (const action of Object.keys(actions)) out[`${section}.${action}`] = false;
  }
  for (const right of me.permissions) out[right] = true;
  return out;
}

export function adminSessionFrom(me: Me, path: string): Omit<AdminSession, 'badges' | 'notifications'> {
  const role = (me.role ?? 'client') as ClubRole;
  const membership: Membership = {
    clubId: me.club?.id ?? 0,
    clubName: me.club?.name ?? '',
    role,
    permissions: permissionsOf(me),
  };
  return {
    user: { name: me.user.name },
    membership,
    availableClubs: me.clubs.map((c) => ({ clubId: c.club_id, clubName: c.club_name, role: c.role as ClubRole, permissions: {} })),
    superadminViewing: me.read_only && me.club ? { name: me.club.name } : null,
    clubTime: {
      timezone: me.club?.timezone ?? Intl.DateTimeFormat().resolvedOptions().timeZone,
      nowMs: Date.parse(me.server_time),
    },
    currentPath: path,
  };
}

