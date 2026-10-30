import { redirect } from '@tanstack/react-router';
import { can, isStaff, type SessionStore, type Me } from './session';
import { rememberIntended } from './intended';
import { ForbiddenError } from '../pages/errors';

/*
 * Гарды маршрутов (beforeLoad) — аналоги middleware Laravel:
 * - requireGuest  — `guest`: вошедшего на /dashboard;
 * - requireAuth   — `auth`: гостя на /login (адрес запоминается, как url.intended);
 * - requireClub   — `current-club`: нужен выбранный клуб и роль сотрудника (иначе 403 с текстом старого кода);
 * - requirePermission — `club-area`/`can`: право из me.permissions (ClubPermissions::allows), иначе 403.
 * Проверку всё равно делает сервер; гард лишь не показывает страницу, которую сервер не отдаст.
 */

export interface GuardContext {
  readonly session: SessionStore;
}

interface BeforeLoadArgs {
  readonly context: GuardContext;
  readonly location: { readonly href: string };
}

export const MSG_NO_CLUBS = 'У аккаунта нет доступа ни к одному клубу.';
/** RequireClubArea: abort_unless(…, 403, __('Нет права на это действие. …')). */
export const MSG_NO_RIGHT = 'Нет права на это действие. Его даёт владелец клуба в «Настройки → Права».';

export async function requireGuest({ context }: BeforeLoadArgs): Promise<void> {
  const s = await context.session.ensureLoaded();
  if (s.status === 'authenticated') throw redirect({ to: '/dashboard' });
}

export async function requireAuth({ context, location }: BeforeLoadArgs): Promise<Me> {
  const s = await context.session.ensureLoaded();
  if (s.status !== 'authenticated' || s.me === null) {
    rememberIntended(location.href);
    throw redirect({ to: '/login' });
  }
  return s.me;
}

/** Вход + выбранный клуб (SetCurrentClub: без клуба — 403 «У аккаунта нет доступа ни к одному клубу.»). */
export async function requireClub(args: BeforeLoadArgs): Promise<Me> {
  const me = await requireAuth(args);
  if (me.club == null) throw new ForbiddenError(MSG_NO_CLUBS);
  return me;
}

/** Право «раздел.действие» (beforeLoad маршрута раздела): `beforeLoad: requirePermission('cash.view')`. */
export function requirePermission(right: string, message: string = MSG_NO_RIGHT) {
  return async (args: BeforeLoadArgs): Promise<void> => {
    const me = await requireClub(args);
    if (!can(me, right)) throw new ForbiddenError(message);
  };
}

/** Одна из ролей (club-role:a,b — владелец проходит всегда; RequireClubRole: abort(403) без текста). */
export function requireRole(...roles: string[]) {
  return async (args: BeforeLoadArgs): Promise<void> => {
    const me = await requireClub(args);
    if (me.role !== 'owner' && !roles.includes(me.role ?? '')) throw new ForbiddenError();
  };
}

/** Персонал клуба (club-role:owner,admin,manager,cashier,operator). */
export const requireStaff = requireRole('admin', 'manager', 'cashier', 'operator');

export { isStaff };
