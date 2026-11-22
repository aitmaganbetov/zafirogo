import { useEffect, useRef, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';

/*
 * Окно свежего кода TOTP действующего суперадмина перед необратимым действием кабинета (аудит A-10,
 * docs/open-questions/audit-ui.md AU-14): вместо window.confirm — тот же вопрос и поле кода; сервер без кода
 * действие не выполнит. Одно окно на весь кабинет («Пользователи», «Реселлеры»): <dialog> на классах карточек и
 * кнопок кабинета, своего CSS нет.
 */
export interface SecondFactorDialogProps {
  /** Вопрос действия; null — окно закрыто. */
  readonly question: string | null;
  /** Ошибка кода под полем (неверный, уже использованный, не введён). */
  readonly error: string | null;
  readonly busy: boolean;
  readonly onSubmit: (code: string) => void;
  readonly onClose: () => void;
}

export function SecondFactorDialog({ question, error, busy, onSubmit, onClose }: SecondFactorDialogProps) {
  const { t } = useTranslation();
  const ref = useRef<HTMLDialogElement>(null);
  const open = question !== null;
  useEffect(() => {
    const dialog = ref.current;
    if (!dialog) return;
    if (open && !dialog.open) dialog.showModal();
    if (!open && dialog.open) dialog.close();
  }, [open]);

  const submit = (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    onSubmit(String(new FormData(e.currentTarget).get('code') ?? '').trim());
  };

  return (
    <dialog
      ref={ref}
      aria-labelledby="second-factor-confirm-title"
      className="m-auto w-[calc(100%-2rem)] max-w-md rounded-xl border border-gray-200 bg-white p-5 text-gray-900 dark:border-gray-700 dark:bg-gray-900 dark:text-gray-100"
      onClose={onClose}
      onClick={(e) => {
        if (e.target === ref.current) onClose();
      }}
    >
      {open && (
        <form method="POST" onSubmit={submit}>
          <h3 id="second-factor-confirm-title" className="font-semibold text-gray-900 dark:text-white">
            {t('Подтверждение кодом')}
          </h3>
          <p className="mt-2 text-sm text-gray-600 dark:text-gray-400">{question}</p>
          <label className="mt-4 block">
            <span className="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-300">{t('Код из приложения')}</span>
            <input
              type="text"
              name="code"
              required
              autoFocus
              maxLength={7}
              inputMode="numeric"
              autoComplete="one-time-code"
              placeholder="123456"
              aria-invalid={error != null}
              aria-describedby="second-factor-confirm-hint"
              className="w-full rounded-lg border border-gray-300 bg-gray-50 px-3 py-2 text-gray-900 dark:border-gray-700 dark:bg-gray-800 dark:text-gray-100"
            />
          </label>
          {error && (
            <p role="alert" className="mt-1 text-sm text-red-600">
              {error}
            </p>
          )}
          <p id="second-factor-confirm-hint" className="mt-2 text-sm text-gray-500">
            {t('Введите новый код из приложения-аутентификатора. Код, с которым вы вошли или уже подтвердили действие, не подойдёт.')}
          </p>
          <div className="mt-4 flex flex-wrap justify-end gap-2">
            <button type="button" className="rounded-lg border border-gray-300 px-4 py-2 text-sm" onClick={onClose}>
              {t('Отмена')}
            </button>
            <button type="submit" disabled={busy} className="rounded-lg border border-violet-200 px-4 py-2 text-sm text-violet-700">
              {t('Подтвердить')}
            </button>
          </div>
        </form>
      )}
    </dialog>
  );
}
