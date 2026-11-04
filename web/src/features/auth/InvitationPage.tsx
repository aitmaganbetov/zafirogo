import { useSuspenseQuery } from '@tanstack/react-query';
import { useNavigate } from '@tanstack/react-router';
import { useState, type FormEvent } from 'react';
import { useForm } from 'react-hook-form';
import { useTranslation } from 'react-i18next';
import { unwrap } from '../../api/http';
import { authApi, session, useSession, type AuthSession, type Me } from '../../auth/session';
import { AuthLayout } from '../../layouts/AuthLayout';
import { setFlash } from '../../lib/flash';
import { invitationQuery } from './invitation';
import { NO_ERRORS, serverErrors, type FormServerErrors } from './serverErrors';

/*
 * Приглашение в клуб — перенос resources/views/invitations/accept.blade.php. POST /api/invitations/{token}/accept
 * (ClubInvitationController::accept()): вошедшему — кнопка «Принять», гостю — регистрация. Затем /dashboard
 * со статусом «Вы присоединились к клубу.».
 */

interface GuestForm {
  name: string;
  phone: string;
  password: string;
  password_confirmation: string;
}

export function InvitationPage({ token }: { token: string }) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const { status } = useSession();
  const { data: invitation } = useSuspenseQuery(invitationQuery(token));
  const [errors, setErrors] = useState<FormServerErrors>(NO_ERRORS);
  const { register, handleSubmit, formState } = useForm<GuestForm>({
    defaultValues: { name: '', phone: '', password: '', password_confirmation: '' },
  });
  const authed = status === 'authenticated';

  const accept = async (body: Partial<GuestForm>) => {
    try {
      const res = await authApi.POST('/api/invitations/{token}/accept', { params: { path: { token } }, body });
      const out = await unwrap<Me | AuthSession>(Promise.resolve(res));
      if ('access_token' in out) session.accept(out);
      else session.setMe(out);
      setFlash('status', 'Вы присоединились к клубу.');
      await navigate({ to: '/dashboard' });
    } catch (e) {
      setErrors(serverErrors(e));
    }
  };

  const onAuthedSubmit = (e: FormEvent) => {
    e.preventDefault();
    void accept({});
  };
  const onGuestSubmit = handleSubmit((v) => accept(v));

  const lead = invitation.email
    ? t('Вас пригласили в «{{club}}» с ролью {{role}} для {{email}}.', { club: invitation.club_name, role: invitation.role, email: invitation.email })
    : t('Вас пригласили в «{{club}}» с ролью {{role}}.', { club: invitation.club_name, role: invitation.role });

  return (
    <AuthLayout title={t('Приглашение в клуб')}>
      <div className="auth-card-head">
        <h2>{t('Приглашение в клуб')}</h2>
        <p>{lead}</p>
      </div>

      {invitation.already_member ? (
        <div className="auth-errors">{t('Ваш аккаунт уже состоит в этом клубе.')}</div>
      ) : (
        <form action={`/invitations/${token}`} method="POST" className="auth-form" onSubmit={authed ? onAuthedSubmit : onGuestSubmit}>
          {authed ? (
            <button className="auth-submit">{t('Принять приглашение')}</button>
          ) : (
            <>
              <div className="auth-field">
                <label htmlFor="name">{t('Имя')}</label>
                <div className="auth-input-wrap">
                  <i className="fas fa-user"></i>
                  <input id="name" required autoComplete="name" placeholder={t('Ваше имя')} {...register('name')} />
                </div>
              </div>
              <div className="auth-field">
                <label htmlFor="phone">{t('Телефон')}</label>
                <div className="auth-input-wrap">
                  <i className="fas fa-phone"></i>
                  <input id="phone" required type="tel" inputMode="numeric" autoComplete="tel" placeholder="+7 700 123-45-67" {...register('phone')} />
                </div>
              </div>
              <div className="auth-field">
                <label htmlFor="password">{t('Пароль')}</label>
                <div className="auth-input-wrap">
                  <i className="fas fa-lock"></i>
                  <input id="password" required type="password" minLength={6} autoComplete="new-password" placeholder={t('Не менее 6 символов')} {...register('password')} />
                </div>
              </div>
              <div className="auth-field">
                <label htmlFor="password_confirmation">{t('Подтверждение пароля')}</label>
                <div className="auth-input-wrap">
                  <i className="fas fa-shield-halved"></i>
                  <input id="password_confirmation" required type="password" minLength={6} autoComplete="new-password" placeholder={t('Повторите пароль')} {...register('password_confirmation')} />
                </div>
              </div>
              <button className="auth-submit" disabled={formState.isSubmitting}>
                {t('Создать аккаунт и вступить')}
              </button>
            </>
          )}
        </form>
      )}

      {errors.first && <div className="auth-errors">{t(errors.first)}</div>}
    </AuthLayout>
  );
}
