import { createContext, useContext, useEffect } from 'react';
import type { AdminSession } from './types';

/** Название приложения в <title>: config('app.name') старого проекта. */
export const APP_NAME = 'Zafiro';

/**
 * Заглушка до появления API сессии (Этап 1): пустой пользователь-владелец без клубов.
 * Приложение передаёт настоящие данные через <AdminSessionContext.Provider value={...}>.
 */
export const STUB_ADMIN_SESSION: AdminSession = {
  user: { name: '' },
  membership: { clubId: 0, clubName: '', role: 'owner', permissions: {} },
  availableClubs: [],
  superadminViewing: null,
  clubTime: { timezone: Intl.DateTimeFormat().resolvedOptions().timeZone, nowMs: Date.now() },
  badges: { chat: 0, orders: 0 },
  notifications: null,
};

export const AdminSessionContext = createContext<AdminSession>(STUB_ADMIN_SESSION);

export function useAdminSession(): AdminSession {
  return useContext(AdminSessionContext);
}

/** Заголовок страницы в верхней панели (@section('page-title') в Blade). */
export const PageTitleContext = createContext<(title: string | null) => void>(() => undefined);

/**
 * Страница задаёт свой заголовок: он показывается в верхней панели и в <title>
 * («Заголовок · Zafiro», как layouts/admin.blade.php). Передавайте уже переведённый текст.
 */
export function useAdminPageTitle(title: string): void {
  const setTitle = useContext(PageTitleContext);
  useEffect(() => {
    setTitle(title);
    return () => { setTitle(null); };
  }, [setTitle, title]);
}
