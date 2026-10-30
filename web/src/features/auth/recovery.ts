/*
 * Восстановление пароля через Telegram Gateway переносится на Этапе 4 (DECISIONS.md, «Telegram»). До этого
 * PasswordRecovery::available() = false: страница показывает «не подключено», как старый шаблон при выключенном
 * шлюзе. Телефон между шагами (session('password_recovery_phone') в старом) — sessionStorage.
 */

const KEY = 'zafiro.recoveryPhone';

/** PasswordRecovery::available(). */
export function recoveryAvailable(): boolean {
  return false;
}

export function recoveryPhone(): string | null {
  try {
    return sessionStorage.getItem(KEY);
  } catch {
    return null;
  }
}
