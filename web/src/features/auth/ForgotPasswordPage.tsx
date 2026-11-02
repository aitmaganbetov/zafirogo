import { useQuery } from '@tanstack/react-query';
import { useNavigate } from '@tanstack/react-router';
import { useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { PhoneLocalInput } from '../../components/PhoneLocalInput';
import { AuthLayout } from '../../layouts/AuthLayout';
import { setFlash } from '../../lib/flash';
import { phoneDigits } from '../../lib/phone';
import { recoveryQuery, sendRecoveryCode, setRecoveryPhone } from './recovery';
import { serverErrors } from './serverErrors';

/*
 * Забыли пароль — перенос resources/views/auth/forgot-password.blade.php (PasswordRecoveryController::show/send).
 * Код уходит в Telegram; номер запоминается для шага «Новый пароль», сообщение — flash status.
 */
export function ForgotPasswordPage() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const { data } = useQuery(recoveryQuery());
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const onSubmit = async (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    const phone = phoneDigits(String(new FormData(e.currentTarget).get('phone') ?? ''));
    setBusy(true);
    try {
      const r = await sendRecoveryCode(phone);
      setError(null);
      setRecoveryPhone(r.phone);
      setFlash('status', r.message);
      await navigate({ to: '/reset-password' });
    } catch (err) {
      setError(serverErrors(err).first);
    } finally {
      setBusy(false);
    }
  };

  return (
    <AuthLayout title={t('Восстановление пароля')}>
      <div className="auth-card-head">
        <h2>{t('Восстановление пароля')}</h2>
        <p>{t('Пришлём код в Telegram на номер вашего аккаунта.')}</p>
      </div>
      {error && <div className="auth-errors">{t(error)}</div>}
      {data?.available === true && (
        <form action="/forgot-password" method="POST" className="auth-form" onSubmit={onSubmit}>
          <div className="auth-field">
            <label htmlFor="phone">{t('Номер телефона')}</label>
            <div className="auth-input-wrap">
              <i className="fas fa-phone"></i>
              <span className="auth-phone-prefix">+7</span>
              <PhoneLocalInput id="phone" name="phone" />
            </div>
          </div>
          <button className="auth-submit" disabled={busy}>
            {t('Получить код в Telegram')}
          </button>
        </form>
      )}
      {data?.available === false && (
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
