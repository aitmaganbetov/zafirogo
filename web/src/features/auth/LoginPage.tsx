import { useNavigate } from '@tanstack/react-router';
import { useCallback, useState } from 'react';
import { useForm } from 'react-hook-form';
import { useTranslation } from 'react-i18next';
import { peekIntended, takeIntended } from '../../auth/intended';
import { session, type Me, type SecondFactorChallenge } from '../../auth/session';
import { isCabinetHost } from '../superadmin/host';
import { PhoneLocalInput } from '../../components/PhoneLocalInput';
import { AuthLayout } from '../../layouts/AuthLayout';
import { useFlash } from '../../lib/flash';
import { phoneDigits } from '../../lib/phone';
import { SecondFactorStep } from './SecondFactor';
import { NO_ERRORS, serverErrors, type FormServerErrors } from './serverErrors';

/*
 * Вход — перенос resources/views/auth/login.blade.php. POST /api/auth/login (AuthController::login()).
 * После входа — redirect()->intended('/dashboard'); суперадмин без клуба — в кабинет (/superadmin).
 * Суперадмин (Этап 7): после пароля — второй фактор (SecondFactorStep) в той же карточке, сессия — после кода.
 */

/** Вход ради подтверждения входа на ПК по QR: клиентам разрешён (в старом — url.intended начинается с /pc-login/). */
function isPcLogin(): boolean {
  return (peekIntended() ?? '').startsWith('/pc-login/');
}

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
  const [challenge, setChallenge] = useState<SecondFactorChallenge | null>(null);
  const { register, handleSubmit, resetField, formState } = useForm<LoginForm>({
    defaultValues: { phone: '', password: '', remember: false },
  });

  const enter = useCallback(
    async (me: Me) => {
      // Хост кабинета (Q-069) и суперадмин без клуба — в кабинет.
      const fallback = isCabinetHost() || (me.club == null && me.user.is_superadmin) ? '/superadmin' : '/dashboard';
      await navigate({ href: takeIntended(fallback) });
    },
    [navigate],
  );
  const restart = useCallback(
    (error: string | null) => {
      setChallenge(null);
      setErrors(error ? { first: error, fields: {} } : NO_ERRORS);
      resetField('password');
    },
    [resetField],
  );

  const onSubmit = handleSubmit(async (v) => {
    try {
      const r = await session.login({ phone: phoneDigits(v.phone).slice(0, 10), password: v.password, remember: v.remember, pc_login: isPcLogin() });
      setErrors(NO_ERRORS);
      if (r.kind === 'second_factor') {
        setChallenge(r.challenge);
        return;
      }
      await enter(r.me);
    } catch (e) {
      setErrors(serverErrors(e));
      resetField('password'); // back()->onlyInput('phone')
    }
  });

  if (challenge) {
    return (
      <AuthLayout title={t('Вход')}>
        <SecondFactorStep challenge={challenge} onDone={(me) => void enter(me)} onRestart={restart} />
      </AuthLayout>
    );
  }

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
