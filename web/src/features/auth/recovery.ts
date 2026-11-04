import { queryOptions } from '@tanstack/react-query';
import { createApiClient, unwrap } from '../../api/http';
import type { paths } from '../../api/gen/recovery';

/*
 * Восстановление пароля кодом из Telegram (api/recovery.yaml; PasswordRecoveryController, Этап 4). Телефон между
 * шагами (session('password_recovery_phone') в старом) — sessionStorage.
 */

const KEY = 'zafiro.recoveryPhone';

const recoveryApi = createApiClient<paths>();

/** PasswordRecovery::available(). */
export const recoveryQuery = () =>
  queryOptions({
    queryKey: ['recovery'],
    queryFn: () => unwrap(recoveryApi.GET('/api/forgot-password')),
    staleTime: 0,
  });

/** POST /forgot-password (send): код в Telegram. */
export const sendRecoveryCode = (phone: string) =>
  unwrap(recoveryApi.POST('/api/forgot-password', { body: { phone } }));

/** POST /reset-password (update): код и новый пароль. */
export const resetPassword = (body: { phone: string; code: string; password: string; password_confirmation: string }) =>
  unwrap(recoveryApi.POST('/api/reset-password', { body }));

export function recoveryPhone(): string | null {
  try {
    return sessionStorage.getItem(KEY);
  } catch {
    return null;
  }
}

export function setRecoveryPhone(phone: string | null): void {
  try {
    if (phone === null) sessionStorage.removeItem(KEY);
    else sessionStorage.setItem(KEY, phone);
  } catch {
    // хранилище недоступно — шаг «Новый пароль» вернёт на ввод номера
  }
}
