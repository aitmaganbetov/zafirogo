import { Navigate } from '@tanstack/react-router';
import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useAdminPageTitle } from '../../layouts/adminSession';
import { forgetRevealedPassword, peekRevealedPassword } from './api';

/*
 * Пароль сброшен — перенос resources/views/users-password.blade.php (ответ UserController::resetPassword(),
 * no-store): пароль показывается один раз. Он живёт только в памяти вкладки до этой страницы; после перезагрузки
 * или повторного входа на адрес — к списку клиентов.
 */
export function UserPasswordPage({ userId }: { userId: number }) {
  const { t } = useTranslation();
  useAdminPageTitle(t('Новый пароль клиента'));
  const [shown] = useState(() => peekRevealedPassword(userId));
  useEffect(() => forgetRevealedPassword(), []);
  if (!shown) return <Navigate to="/users" replace />;

  return (
    <div className="p-6 lg:p-8">
      <div className="max-w-xl rounded-xl border border-gray-200 bg-white p-6 dark:border-gray-800 dark:bg-gray-900">
        <h3 className="mb-2 text-xl font-bold text-gray-900 dark:text-white">{t('Пароль сброшен: {{name}}', { name: shown.name })}</h3>
        <p className="mb-3 text-sm text-gray-700 dark:text-gray-300">
          {t('Передайте клиенту новый пароль. Он показывается только сейчас; клиент вышел из аккаунта на всех ПК.')}
        </p>
        <p className="mb-3 select-all rounded-lg bg-gray-100 px-4 py-3 text-center font-mono text-2xl tracking-widest text-gray-900 dark:bg-gray-800 dark:text-white" data-new-password>
          {shown.password}
        </p>
        <p className="text-xs text-gray-500 dark:text-gray-400">
          {t('В следующий раз клиент может восстановить пароль сам: «Забыли пароль?» на экране входа, код придёт в Telegram.')}
        </p>
        <a href="/users" className="brand-button mt-5 inline-flex rounded-lg px-4 py-2 text-sm">
          {t('К списку клиентов')}
        </a>
      </div>
    </div>
  );
}
