import { useSyncExternalStore } from 'react';
import {
  createApiClient,
  refreshSession,
  setAccessToken,
  setUnauthenticatedHandler,
  onGrant,
  unwrap,
} from '../api/http';
import type { components, paths } from '../api/gen/auth';

/*
 * Сессия сотрудника в SPA: Me из /api/auth/me (пользователь, клуб, роль, права, клубы для переключения).
 * Laravel: SetCurrentClub (View::share currentClub, currentMembership, availableClubs, superadminViewing).
 * Источник прав для меню и гардов — `me.permissions` (сервер уже учёл роль и выбор владельца).
 */

export type Me = components['schemas']['Me'];
export type AuthSession = components['schemas']['AuthSession'];
export type LoginRequest = components['schemas']['LoginRequest'];
export type RegisterRequest = components['schemas']['RegisterRequest'];

export const authApi = createApiClient<paths>();

export type SessionStatus = 'unknown' | 'anonymous' | 'authenticated';

export interface SessionState {
  readonly status: SessionStatus;
  readonly me: Me | null;
}

type Listener = () => void;

/** Хранилище сессии (одно на приложение; в тестах — своё). */
export class SessionStore {
  private state: SessionState = { status: 'unknown', me: null };
  private readonly listeners = new Set<Listener>();
  private loading: Promise<SessionState> | null = null;

  constructor() {
    setUnauthenticatedHandler(() => this.clear());
    onGrant((grant) => {
      const me = (grant as Partial<AuthSession>).me;
      if (me) this.set({ status: 'authenticated', me });
    });
  }

  readonly subscribe = (fn: Listener): (() => void) => {
    this.listeners.add(fn);
    return () => this.listeners.delete(fn);
  };

  readonly getState = (): SessionState => this.state;

  private set(next: SessionState): void {
    this.state = next;
    for (const fn of this.listeners) fn();
  }

  /** Первая загрузка: refresh по cookie (вкладка открыта заново) → me; без cookie — гость. */
  ensureLoaded(): Promise<SessionState> {
    if (this.state.status !== 'unknown') return Promise.resolve(this.state);
    this.loading ??= refreshSession()
      .then((grant) => {
        if (!grant) this.set({ status: 'anonymous', me: null });
        return this.state;
      })
      .catch(() => {
        this.set({ status: 'anonymous', me: null });
        return this.state;
      })
      .finally(() => {
        this.loading = null;
      });
    return this.loading;
  }

  /** Принять AuthSession (вход, регистрация, принятие приглашения). */
  accept(session: AuthSession): void {
    setAccessToken(session.access_token);
    this.set({ status: 'authenticated', me: session.me });
  }

  /** Обновить Me (смена клуба, принятие приглашения вошедшим, суперадмин-просмотр). */
  setMe(me: Me): void {
    this.set({ status: 'authenticated', me });
  }

  clear(): void {
    setAccessToken(null);
    this.set({ status: 'anonymous', me: null });
  }

  async login(body: LoginRequest): Promise<Me> {
    const s = await unwrap(authApi.POST('/api/auth/login', { body }));
    this.accept(s);
    return s.me;
  }

  async register(body: RegisterRequest): Promise<Me> {
    const s = await unwrap(authApi.POST('/api/auth/register', { body }));
    this.accept(s);
    return s.me;
  }

  async reloadMe(): Promise<Me> {
    const me = await unwrap(authApi.GET('/api/auth/me'));
    this.setMe(me);
    return me;
  }

  async selectClub(clubId: number): Promise<Me> {
    const me = await unwrap(authApi.PUT('/api/auth/club', { body: { club_id: clubId } }));
    this.setMe(me);
    return me;
  }

  async leaveViewedClub(): Promise<Me> {
    const me = await unwrap(authApi.POST('/api/superadmin/leave-club'));
    this.setMe(me);
    return me;
  }

  async logout(): Promise<void> {
    try {
      await authApi.POST('/api/auth/logout', { params: { header: {} } });
    } finally {
      this.clear();
    }
  }
}

export const session = new SessionStore();

export function useSession(store: SessionStore = session): SessionState {
  return useSyncExternalStore(store.subscribe, store.getState, store.getState);
}

/** Есть ли право «раздел.действие» в выбранном клубе (ClubPermissions::allows → me.permissions). */
export function can(me: Me | null, right: string): boolean {
  return me?.club != null && me.permissions.includes(right);
}

/** Роль сотрудника в выбранном клубе. */
export function roleOf(me: Me | null): string | null {
  return me?.club != null ? (me.role ?? null) : null;
}

/** Роли персонала (ClubRoles::STAFF + owner). */
export const STAFF_ROLES = ['owner', 'admin', 'manager', 'cashier', 'operator'] as const;

export function isStaff(me: Me | null): boolean {
  const r = roleOf(me);
  return r !== null && (STAFF_ROLES as readonly string[]).includes(r);
}
