import { useNavigate } from '@tanstack/react-router';
import { useState } from 'react';
import { useForm } from 'react-hook-form';
import { useTranslation } from 'react-i18next';
import { session } from '../../auth/session';
import { PhoneLocalInput } from '../../components/PhoneLocalInput';
import { AuthLayout } from '../../layouts/AuthLayout';
import { setFlash } from '../../lib/flash';
import { phoneDigits } from '../../lib/phone';
import { NO_ERRORS, serverErrors, type FormServerErrors } from './serverErrors';

/*
 * Регистрация клуба — перенос resources/views/auth/register.blade.php. POST /api/auth/register
 * (AuthController::register()): владелец, клуб, пробная подписка, вход; затем /dashboard с «Добро пожаловать!».
 */

interface RegisterForm {
  name: string;
  club_name: string;
  phone: string;
  password: string;
  password_confirmation: string;
}

export function RegisterPage() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const [errors, setErrors] = useState<FormServerErrors>(NO_ERRORS);
  const { register, handleSubmit, resetField, formState } = useForm<RegisterForm>({
    defaultValues: { name: '', club_name: '', phone: '', password: '', password_confirmation: '' },
  });

  const onSubmit = handleSubmit(async (v) => {
    try {
      await session.register({ ...v, phone: phoneDigits(v.phone).slice(0, 10) });
      setFlash('success', 'Добро пожаловать!');
      await navigate({ to: '/dashboard' });
    } catch (e) {
      setErrors(serverErrors(e));
      resetField('password');
      resetField('password_confirmation');
    }
  });

  const fieldError = (name: keyof RegisterForm) =>
    errors.fields[name] ? <p className="auth-error-text">{t(errors.fields[name])}</p> : null;

  return (
    <AuthLayout title={t('Регистрация')}>
      <div className="auth-card-head">
        <h2>{t('Создать аккаунт')}</h2>
        <p>{t('Заполните данные для регистрации в системе')}</p>
      </div>
      {errors.first && <div className="auth-errors">{t(errors.first)}</div>}
      <form action="/register" method="POST" className="auth-form" onSubmit={onSubmit}>
        <div className="auth-field">
          <label htmlFor="name">{t('Имя')}</label>
          <div className="auth-input-wrap">
            <i className="fas fa-user"></i>
            <input type="text" id="name" required placeholder={t('Ваше имя')} {...register('name')} />
          </div>
          {fieldError('name')}
        </div>
        <div className="auth-field">
          <label htmlFor="club_name">{t('Название клуба')}</label>
          <div className="auth-input-wrap">
            <i className="fas fa-building"></i>
            <input type="text" id="club_name" required maxLength={255} placeholder={t('Например, Zafiro Club')} {...register('club_name')} />
          </div>
          {fieldError('club_name')}
        </div>
        <div className="auth-field">
          <label htmlFor="phone">{t('Номер телефона')}</label>
          <div className="auth-input-wrap">
            <i className="fas fa-phone"></i>
            <span className="auth-phone-prefix">+7</span>
            <PhoneLocalInput id="phone" {...register('phone')} />
          </div>
          {fieldError('phone')}
        </div>
        <div className="auth-field">
          <label htmlFor="password">{t('Пароль')}</label>
          <div className="auth-input-wrap">
            <i className="fas fa-lock"></i>
            <input type="password" id="password" required placeholder={t('Не менее 8 символов')} {...register('password')} />
          </div>
          {fieldError('password')}
        </div>
        <div className="auth-field">
          <label htmlFor="password_confirmation">{t('Подтверждение пароля')}</label>
          <div className="auth-input-wrap">
            <i className="fas fa-shield-halved"></i>
            <input type="password" id="password_confirmation" required placeholder={t('Повторите пароль')} {...register('password_confirmation')} />
          </div>
        </div>
        <button className="auth-submit" disabled={formState.isSubmitting}>
          {t('Создать аккаунт')}
        </button>
      </form>
      <p className="auth-footer">
        {t('Уже есть аккаунт?')} <a href="/login">{t('Войти')}</a>
      </p>
    </AuthLayout>
  );
}
