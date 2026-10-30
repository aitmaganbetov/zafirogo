import { useNavigate } from '@tanstack/react-router';
import { useState } from 'react';
import { useForm } from 'react-hook-form';
import { useTranslation } from 'react-i18next';
import { takeIntended } from '../../auth/intended';
import { session } from '../../auth/session';
import { PhoneLocalInput } from '../../components/PhoneLocalInput';
import { AuthLayout } from '../../layouts/AuthLayout';
import { useFlash } from '../../lib/flash';
import { phoneDigits } from '../../lib/phone';
import { NO_ERRORS, serverErrors, type FormServerErrors } from './serverErrors';

/*
 * Вход — перенос resources/views/auth/login.blade.php. POST /api/auth/login (AuthController::login()).
 * После входа — redirect()->intended('/dashboard'); суперадмин без клуба — в кабинет (/superadmin).
 */

interface LoginForm {
  phone: string;
  password: string;
  remember: boolean;
}

export function LoginPage() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const status = useFlash('status');
  const [errors, setErrors] = useState<FormServerErrors>(NO_ERRORS);
  const { register, handleSubmit, resetField, formState } = useForm<LoginForm>({
    defaultValues: { phone: '', password: '', remember: false },
  });

  const onSubmit = handleSubmit(async (v) => {
    try {
      const me = await session.login({ phone: phoneDigits(v.phone).slice(0, 10), password: v.password, remember: v.remember, pc_login: false });
      setErrors(NO_ERRORS);
      const fallback = me.club == null && me.user.is_superadmin ? '/superadmin' : '/dashboard';
      await navigate({ href: takeIntended(fallback) });
    } catch (e) {
      setErrors(serverErrors(e));
      resetField('password'); // back()->onlyInput('phone')
    }
  });

  return (
    <AuthLayout title={t('Вход')}>
      <div className="auth-card-head">
        <h2>{t('Добро пожаловать')}</h2>
        <p>{t('Войдите, чтобы продолжить работу в Zafiro')}</p>
      </div>
      {status && <div className="auth-status">{t(status)}</div>}
      {errors.first && <div className="auth-errors">{t(errors.first)}</div>}
      <form action="/login" method="POST" className="auth-form" onSubmit={onSubmit} noValidate={false}>
        <div className="auth-field">
          <label htmlFor="phone">{t('Номер телефона')}</label>
          <div className="auth-input-wrap">
            <i className="fas fa-phone"></i>
            <span className="auth-phone-prefix">+7</span>
            <PhoneLocalInput id="phone" {...register('phone')} />
          </div>
          {errors.fields.phone && <p className="auth-error-text">{t(errors.fields.phone)}</p>}
        </div>
        <div className="auth-field">
          <label htmlFor="password">{t('Пароль')}</label>
          <div className="auth-input-wrap">
            <i className="fas fa-lock"></i>
            <input type="password" id="password" required placeholder={t('Введите пароль')} {...register('password')} />
          </div>
        </div>
        <div className="auth-row">
          <label className="auth-check">
            <input type="checkbox" {...register('remember')} /> {t('Запомнить меня')}
          </label>
          <a href="/forgot-password">{t('Забыли пароль?')}</a>
        </div>
        <button className="auth-submit" disabled={formState.isSubmitting}>
          {t('Войти в систему')}
        </button>
      </form>
      <p className="auth-footer">
        {t('Нет аккаунта?')} <a href="/register">{t('Зарегистрироваться')}</a>
      </p>
    </AuthLayout>
  );
}
