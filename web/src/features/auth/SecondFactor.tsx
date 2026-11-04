import { useEffect, useState, type FormEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { isApiError } from '../../api/http';
import { session, type AuthSession, type Me, type SecondFactorChallenge, type TotpSetup } from '../../auth/session';
import { serverErrors } from './serverErrors';

/*
 * Второй фактор входа суперадмина (Этап 7, новая функция; в старом Laravel нет — docs/open-questions/stage7-totp.md).
 * Шаг формы входа после пароля, в карточке auth.blade.php теми же классами: настройка (ключ и otpauth:// — текстом,
 * QR-библиотеки в проекте нет), код из приложения или код восстановления, показ кодов восстановления один раз.
 * Полувход живёт ~5 минут на сервере; истёк — назад к паролю.
 */

/** Полувход истёк, использован или попытки исчерпаны (410). */
export const MSG_CHALLENGE_GONE = 'Время на ввод кода истекло. Войдите ещё раз.';

interface Props {
  readonly challenge: SecondFactorChallenge;
  /** Сессия принята: перейти в кабинет. */
  readonly onDone: (me: Me) => void;
  /** Назад к паролю (с текстом ошибки или без). */
  readonly onRestart: (error: string | null) => void;
}

export function SecondFactorStep({ challenge, onDone, onRestart }: Props) {
  const { t } = useTranslation();
  const [setup, setSetup] = useState<TotpSetup | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [codes, setCodes] = useState<{ readonly session: AuthSession; readonly list: readonly string[] } | null>(null);

  // Принудительная настройка: ключ запрашивается сразу (каждый запрос выдаёт новый, пока TOTP не включён).
  useEffect(() => {
    if (!challenge.setup_required) return;
    let alive = true;
    session.secondFactorSetup(challenge.challenge).then(
      (s) => {
        if (alive) setSetup(s);
      },
      (e: unknown) => {
        if (!alive) return;
        if (isApiError(e) && e.kind === 'gone') onRestart(MSG_CHALLENGE_GONE);
        else setError(serverErrors(e).first);
      },
    );
    return () => {
      alive = false;
    };
  }, [challenge, onRestart]);

  const onSubmit = async (e: FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    const form = e.currentTarget;
    const code = String(new FormData(form).get('code') ?? '').trim();
    setBusy(true);
    try {
      const r = await session.secondFactorVerify(challenge.challenge, code);
      if (r.recovery_codes.length > 0) {
        setCodes({ session: r.session, list: r.recovery_codes });
        return;
      }
      session.accept(r.session);
      onDone(r.session.me);
    } catch (err) {
      if (isApiError(err) && err.kind === 'gone') {
        onRestart(MSG_CHALLENGE_GONE);
        return;
      }
      setError(serverErrors(err).first);
      form.reset();
    } finally {
      setBusy(false);
    }
  };

  if (codes) {
    return (
      <>
        <div className="auth-card-head">
          <h2>{t('Коды восстановления')}</h2>
          <p>{t('Сохраните коды в надёжном месте: каждый подходит для входа один раз, если телефона с приложением нет под рукой. Больше они не будут показаны.')}</p>
        </div>
        <div className="auth-status" data-recovery-codes>
          {codes.list.map((c) => (
            <div key={c}>
              <code>{c}</code>
            </div>
          ))}
        </div>
        <button
          type="button"
          className="auth-submit"
          onClick={() => {
            session.accept(codes.session);
            onDone(codes.session.me);
          }}
        >
          {t('Я сохранил коды — продолжить')}
        </button>
      </>
    );
  }

  const setupRequired = challenge.setup_required;
  return (
    <>
      <div className="auth-card-head">
        <h2>{setupRequired ? t('Настройка двухфакторной аутентификации') : t('Двухфакторная аутентификация')}</h2>
        <p>
          {setupRequired
            ? t('Вход суперадмина защищён вторым фактором. Добавьте ключ в приложение-аутентификатор (Google Authenticator, Aegis, 1Password и др.) и введите код из него.')
            : t('Введите 6-значный код из приложения-аутентификатора или код восстановления.')}
        </p>
      </div>
      {error && <div className="auth-errors">{t(error)}</div>}
      <form action="/login" method="POST" className="auth-form" onSubmit={onSubmit}>
        {setupRequired && (
          <>
            <div className="auth-field">
              <label htmlFor="totp-secret">{t('Ключ для приложения')}</label>
              <div className="auth-input-wrap">
                <i className="fas fa-key"></i>
                <input type="text" id="totp-secret" readOnly value={setup?.secret ?? ''} autoComplete="off" spellCheck={false} />
              </div>
            </div>
            {setup && (
              <div className="auth-row">
                <a href={setup.otpauth_uri}>{t('Открыть в приложении-аутентификаторе')}</a>
              </div>
            )}
          </>
        )}
        <div className="auth-field">
          <label htmlFor="code">{setupRequired ? t('Код из приложения') : t('Код')}</label>
          <div className="auth-input-wrap">
            <i className="fas fa-shield-halved"></i>
            <input
              type="text"
              id="code"
              name="code"
              required
              autoFocus
              maxLength={setupRequired ? 7 : 24}
              inputMode={setupRequired ? 'numeric' : 'text'}
              autoComplete="one-time-code"
              placeholder="123456"
            />
          </div>
        </div>
        <button className="auth-submit" disabled={busy || (setupRequired && setup === null)}>
          {setupRequired ? t('Включить и войти') : t('Подтвердить')}
        </button>
      </form>
      <p className="auth-footer">
        <button type="button" className="auth-link-button" onClick={() => onRestart(null)}>
          {t('Войти заново')}
        </button>
      </p>
    </>
  );
}
