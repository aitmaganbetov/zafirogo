import { useQueryClient, useSuspenseQuery } from '@tanstack/react-query';
import { useNavigate } from '@tanstack/react-router';
import { useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { isApiError } from '../../api/http';
import { session } from '../../auth/session';
import { AuthLayout } from '../../layouts/AuthLayout';
import { approvePcLogin, pcLoginQuery } from './api';

/*
 * Подтвердить вход на ПК — перенос resources/views/pc-login/show.blade.php (layout auth). GET/POST /api/pc-login/{code}
 * (QrLoginController::show/approve). Вход уже не открыт — тот же экран «QR-код устарел» (в старом — редирект назад на
 * страницу кода); подтверждено — /pc-login/done. «Выйти из аккаунта» — POST /logout (на лендинг, redirect('/')).
 */
export function PcLoginPage({ code }: { code: string }) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { data: login } = useSuspenseQuery(pcLoginQuery(code));
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const approve = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    try {
      const res = await approvePcLogin(code);
      if (res.approved) {
        await navigate({ to: '/pc-login/done' });
        return;
      }
      setError(null);
      await queryClient.invalidateQueries({ queryKey: ['pclogin', code] });
    } catch (err) {
      // $errors->first(): первая ошибка (поле code) или общий текст (429 и т. п.).
      if (isApiError(err)) setError(err.field('code') ?? err.message);
      else setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };

  const logout = (e: FormEvent) => {
    e.preventDefault();
    void session.logout().then(() => {
      queryClient.clear();
      void navigate({ to: '/' });
    });
  };

  return (
    <AuthLayout title={t('Вход на ПК')}>
      {!login.open ? (
        <div className="auth-card-head">
          <h2>{t('QR-код устарел')}</h2>
          <p>{t('Код уже использован или прошло больше 5 минут. Обновите QR-код на экране ПК и отсканируйте снова.')}</p>
        </div>
      ) : (
        <>
          <div className="auth-card-head">
            <h2>{t('Вход на ПК')}</h2>
            <p>{t('Клуб {{club}} · ПК {{computer}}', { club: login.club_name ?? '', computer: login.computer_name ?? '' })}</p>
          </div>
          {error && <div className="auth-errors">{t(error)}</div>}
          <p className="auth-footer">{t('Вы войдёте на этом ПК как {{name}}.', { name: login.user_name })}</p>
          <form action={`/pc-login/${code}`} method="POST" className="auth-form" onSubmit={approve}>
            <button className="auth-submit" disabled={busy}>{t('Войти на ПК')}</button>
          </form>
          <form action="/logout" method="POST" className="auth-form" onSubmit={logout}>
            <p className="auth-footer">{t('Это не вы?')} <button type="submit" className="auth-link-button">{t('Выйти из аккаунта')}</button></p>
          </form>
        </>
      )}
    </AuthLayout>
  );
}

/** pc-login/done.blade.php — статичный текст. */
export function PcLoginDonePage() {
  const { t } = useTranslation();
  return (
    <AuthLayout title={t('Вход на ПК')}>
      <div className="auth-card-head">
        <h2>{t('Готово')}</h2>
        <p>{t('Вход подтверждён. Через несколько секунд ПК откроется под вашей учётной записью.')}</p>
      </div>
    </AuthLayout>
  );
}
