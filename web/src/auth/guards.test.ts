import { isRedirect } from '@tanstack/react-router';
import { describe, expect, it } from 'vitest';
import { ForbiddenError } from '../pages/errors';
import { MSG_NO_CLUBS, MSG_NO_RIGHT, requireAuth, requireClub, requireGuest, requirePermission, requireRole } from './guards';
import { peekIntended } from './intended';
import type { Me, SessionStore } from './session';

function store(me: Me | null): SessionStore {
  const state = { status: me ? ('authenticated' as const) : ('anonymous' as const), me };
  return { ensureLoaded: async () => state } as unknown as SessionStore;
}

function me(over: Partial<Me> = {}): Me {
  return {
    user: { id: 1, name: 'Иван', phone: '+77010000000', is_superadmin: false },
    club: { id: 7, name: 'Клуб', timezone: 'Asia/Almaty', locale: 'ru' },
    role: 'cashier',
    permissions: ['cash.view', 'cash.create'],
    read_only: false,
    clubs: [],
    server_time: '2026-10-30T12:00:00Z',
    ...over,
  };
}

const args = (m: Me | null, href = '/cash-register?x=1') => ({ context: { session: store(m) }, location: { href } });

async function thrown(p: Promise<unknown>): Promise<unknown> {
  try {
    await p;
  } catch (e) {
    return e;
  }
  return null;
}

describe('гарды маршрутов', () => {
  it('гость → /login, адрес запоминается (url.intended)', async () => {
    const e = await thrown(requireAuth(args(null)));
    expect(isRedirect(e)).toBe(true);
    expect(peekIntended()).toBe('/cash-register?x=1');
  });

  it('вошедший на страницу гостя → /dashboard', async () => {
    expect(isRedirect(await thrown(requireGuest(args(me()))))).toBe(true);
    expect(await thrown(requireGuest(args(null)))).toBeNull();
  });

  it('без клуба — 403 с текстом SetCurrentClub', async () => {
    const e = await thrown(requireClub(args(me({ club: null }))));
    expect(e).toBeInstanceOf(ForbiddenError);
    expect((e as Error).message).toBe(MSG_NO_CLUBS);
  });

  it('право из me.permissions', async () => {
    expect(await thrown(requirePermission('cash.view')(args(me())))).toBeNull();
    const e = await thrown(requirePermission('warehouse.view')(args(me())));
    expect((e as Error).message).toBe(MSG_NO_RIGHT);
  });

  it('роль: владелец проходит всегда, остальные — по списку', async () => {
    expect(await thrown(requireRole('admin')(args(me({ role: 'owner' }))))).toBeNull();
    expect(await thrown(requireRole('admin')(args(me())))).toBeInstanceOf(ForbiddenError);
  });
});
