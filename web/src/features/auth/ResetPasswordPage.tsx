import { useNavigate } from '@tanstack/react-router';
import { useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { AuthLayout } from '../../layouts/AuthLayout';
import { setFlash, useFlash } from '../../lib/flash';
import { recoveryPhone, resetPassword, setRecoveryPhone } from './recovery';
import { serverErrors } from './serverErrors';

/*
 * Новый пароль по коду — перенос resources/views/auth/reset-password.blade.php (PasswordRecoveryController::edit/update).
 * Без телефона маршрут уводит на /forgot-password; после смены — вход с flash «Пароль изменён…».
 */
export function ResetPasswordPage() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const status = useFlash('status');
  const phone = recoveryPhone() ?? '';
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const onSubmit = async (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    const form = e.currentTarget;
    const f = new FormData(form);
    setBusy(true);
    try {
      const r = await resetPassword({
        phone,
        code: String(f.get('code') ?? ''),
        password: String(f.get('password') ?? ''),
        password_confirmation: String(f.get('password_confirmation') ?? ''),
      });
      setRecoveryPhone(null);
      setFlash('status', r.message);
      await navigate({ to: '/login' });
    } catch (err) {
      setError(serverErrors(err).first);
      form.reset();
    } finally {
      setBusy(false);
    }
  };

  return (
    <AuthLayout title={t('Восстановление пароля')}>
      <div className="auth-card-head">
        <h2>{t('Новый пароль')}</h2>
        <p>{t('Номер: {{phone}}', { phone })}</p>
      </div>
      {status && !error && <div className="auth-status">{t(status)}</div>}
      {error && <div className="auth-errors">{t(error)}</div>}
      <form action="/reset-password" method="POST" className="auth-form" onSubmit={onSubmit}>
        <div className="auth-field">
          <label htmlFor="code">{t('Код из Telegram')}</label>
          <div className="auth-input-wrap">
            <i className="fab fa-telegram"></i>
            <input type="text" id="code" name="code" required inputMode="numeric" pattern="[0-9]{6}" maxLength={6} autoComplete="one-time-code" placeholder="123456" />
          </div>
        </div>
        <div className="auth-field">
          <label htmlFor="password">{t('Новый пароль')}</label>
          <div className="auth-input-wrap">
            <i className="fas fa-lock"></i>
            <input type="password" id="password" name="password" required minLength={6} autoComplete="new-password" placeholder={t('Не менее 6 символов')} />
          </div>
        </div>
        <div className="auth-field">
          <label htmlFor="password_confirmation">{t('Подтверждение пароля')}</label>
          <div className="auth-input-wrap">
            <i className="fas fa-shield-halved"></i>
            <input type="password" id="password_confirmation" name="password_confirmation" required minLength={6} autoComplete="new-password" />
          </div>
        </div>
        <button className="auth-submit" disabled={busy}>
          {t('Сменить пароль')}
        </button>
      </form>
      <p className="auth-footer">
        <a href="/forgot-password">{t('Запросить новый код')}</a>
      </p>
    </AuthLayout>
  );
}
