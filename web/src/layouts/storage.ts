/*
 * localStorage с защитой от исключений (приватный режим, запрет cookies) — как try/catch
 * в скриптах старого layout. Ключи сохранены прежними, чтобы настройки браузера пережили переход.
 */

export const STORAGE_KEYS = {
  theme: 'zafiro.theme',
  sidebarCollapsed: 'zafiro.sidebarCollapsed',
  notificationsHeard: 'zafiro.notificationsHeard',
  notificationSound: 'zafiro.notificationSound',
  installDismissedAt: 'zafiro.installDismissedAt',
  locale: 'zafiro.locale',
} as const;

export function readStorage(key: string): string | null {
  try {
    return window.localStorage.getItem(key);
  } catch {
    return null;
  }
}

export function writeStorage(key: string, value: string): void {
  try {
    window.localStorage.setItem(key, value);
  } catch {
    // Хранилище недоступно: настройка живёт до перезагрузки.
  }
}

/** Мобильная раскладка меню: тот же брейкпоинт, что в старом CSS/JS. */
export const MOBILE_QUERY = '(max-width: 768px)';

/** Применить сохранённую тему до первого рендера (идемпотентно). */
export function applyStoredTheme(): boolean {
  const dark = readStorage(STORAGE_KEYS.theme) === 'dark';
  if (dark) document.documentElement.classList.add('dark');
  return document.documentElement.classList.contains('dark');
}
