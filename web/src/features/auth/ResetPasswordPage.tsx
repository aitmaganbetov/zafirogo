import { useTranslation } from 'react-i18next';
import { AuthLayout } from '../../layouts/AuthLayout';
import { recoveryPhone } from './recovery';

/*
 * Новый пароль по коду — перенос resources/views/auth/reset-password.blade.php. Смена пароля по коду из
 * Telegram — Этап 4 (без телефона маршрут уводит на /forgot-password, как PasswordRecoveryController::edit()).
 */
export function ResetPasswordPage() {
  const { t } = useTranslation();
  const phone = recoveryPhone() ?? '';
  return (
    <AuthLayout title={t('Восстановление пароля')}>
      <div className="auth-card-head">
        <h2>{t('Новый пароль')}</h2>
        <p>{t('Номер: {{phone}}', { phone })}</p>
      </div>
      <form action="/reset-password" method="POST" className="auth-form" onSubmit={(e) => e.preventDefault()}>
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
        <button className="auth-submit">{t('Сменить пароль')}</button>
      </form>
      <p className="auth-footer">
        <a href="/forgot-password">{t('Запросить новый код')}</a>
      </p>
    </AuthLayout>
  );
}
