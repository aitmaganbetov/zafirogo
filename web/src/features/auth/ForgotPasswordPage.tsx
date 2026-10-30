import { useTranslation } from 'react-i18next';
import { PhoneLocalInput } from '../../components/PhoneLocalInput';
import { AuthLayout } from '../../layouts/AuthLayout';
import { recoveryAvailable } from './recovery';

/*
 * Забыли пароль — перенос resources/views/auth/forgot-password.blade.php. Отправка кода в Telegram — Этап 4;
 * пока шлюз не подключён, показывается ветка @else старого шаблона.
 */
export function ForgotPasswordPage() {
  const { t } = useTranslation();
  const available = recoveryAvailable();
  return (
    <AuthLayout title={t('Восстановление пароля')}>
      <div className="auth-card-head">
        <h2>{t('Восстановление пароля')}</h2>
        <p>{t('Пришлём код в Telegram на номер вашего аккаунта.')}</p>
      </div>
      {available ? (
        <form action="/forgot-password" method="POST" className="auth-form" onSubmit={(e) => e.preventDefault()}>
          <div className="auth-field">
            <label htmlFor="phone">{t('Номер телефона')}</label>
            <div className="auth-input-wrap">
              <i className="fas fa-phone"></i>
              <span className="auth-phone-prefix">+7</span>
              <PhoneLocalInput id="phone" name="phone" />
            </div>
          </div>
          <button className="auth-submit">{t('Получить код в Telegram')}</button>
        </form>
      ) : (
        <div className="auth-errors">
          {t('Восстановление пароля через Telegram пока не подключено. Обратитесь к администратору клуба.')}
        </div>
      )}
      <p className="auth-footer">
        <a href="/login">{t('Вернуться ко входу')}</a>
      </p>
    </AuthLayout>
  );
}
