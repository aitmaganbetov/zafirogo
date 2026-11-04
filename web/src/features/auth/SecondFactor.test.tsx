import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { ApiError } from '../../api/http';
import { session, type AuthSession, type Me } from '../../auth/session';
import { MSG_CHALLENGE_GONE, SecondFactorStep } from './SecondFactor';

const me: Me = {
  user: { id: 1, name: 'Супер Админ', phone: '+77010000001', is_superadmin: true },
  club: null,
  role: null,
  permissions: [],
  read_only: false,
  clubs: [],
  server_time: '2026-10-15T07:00:00Z',
};
const auth: AuthSession = { access_token: 'a', token_type: 'Bearer', expires_in: 600, csrf_token: 'c', me };

afterEach(() => vi.restoreAllMocks());

function submitCode(code: string) {
  fireEvent.change(screen.getByLabelText(/Код/), { target: { value: code } });
  fireEvent.submit(document.querySelector('form') as HTMLFormElement);
}

describe('SecondFactorStep', () => {
  it('настройка: ключ и otpauth текстом, код → коды восстановления один раз → сессия по кнопке', async () => {
    vi.spyOn(session, 'secondFactorSetup').mockResolvedValue({ secret: 'JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP', otpauth_uri: 'otpauth://totp/Zafiro:%2B77010000001?secret=JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP' });
    const verify = vi.spyOn(session, 'secondFactorVerify').mockResolvedValue({ session: auth, recovery_codes: ['aaaa-bbbb-cccc-dddd', 'eeee-ffff-gggg-hhhh'] });
    const accept = vi.spyOn(session, 'accept').mockImplementation(() => undefined);
    const onDone = vi.fn();
    render(<SecondFactorStep challenge={{ challenge: 'ch1', setup_required: true, expires_in: 300 }} onDone={onDone} onRestart={vi.fn()} />);

    expect(screen.getByRole('heading')).toHaveTextContent('Настройка двухфакторной аутентификации');
    await waitFor(() => expect(screen.getByLabelText('Ключ для приложения')).toHaveValue('JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP'));
    expect(screen.getByRole('link', { name: 'Открыть в приложении-аутентификаторе' })).toHaveAttribute('href', expect.stringMatching(/^otpauth:\/\/totp\//));

    submitCode(' 123456 ');
    await waitFor(() => expect(document.querySelector('[data-recovery-codes]')).toHaveTextContent('aaaa-bbbb-cccc-dddd'));
    expect(verify).toHaveBeenCalledWith('ch1', '123456');
    // Сессия в SPA — только после того, как коды показаны и подтверждены.
    expect(accept).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole('button', { name: 'Я сохранил коды — продолжить' }));
    expect(accept).toHaveBeenCalledWith(auth);
    expect(onDone).toHaveBeenCalledWith(me);
  });

  it('вход: неверный код — ошибка в карточке; верный — сразу сессия', async () => {
    const setup = vi.spyOn(session, 'secondFactorSetup');
    const verify = vi
      .spyOn(session, 'secondFactorVerify')
      .mockRejectedValueOnce(new ApiError(422, { error: 'validation_failed', message: 'Неверный код.', errors: { code: ['Неверный код.'] } }))
      .mockResolvedValueOnce({ session: auth, recovery_codes: [] });
    const accept = vi.spyOn(session, 'accept').mockImplementation(() => undefined);
    const onDone = vi.fn();
    render(<SecondFactorStep challenge={{ challenge: 'ch2', setup_required: false, expires_in: 300 }} onDone={onDone} onRestart={vi.fn()} />);

    expect(screen.getByRole('heading')).toHaveTextContent('Двухфакторная аутентификация');
    expect(screen.queryByLabelText('Ключ для приложения')).toBeNull();
    expect(setup).not.toHaveBeenCalled();

    submitCode('000000');
    await waitFor(() => expect(document.querySelector('.auth-errors')).toHaveTextContent('Неверный код.'));
    expect(onDone).not.toHaveBeenCalled();

    submitCode('abcd-efgh-jkmn-pqrs'); // код восстановления — тем же полем
    await waitFor(() => expect(onDone).toHaveBeenCalledWith(me));
    expect(verify).toHaveBeenLastCalledWith('ch2', 'abcd-efgh-jkmn-pqrs');
    expect(accept).toHaveBeenCalledWith(auth);
  });

  it('полувход истёк (410) или «Войти заново» — назад к паролю', async () => {
    vi.spyOn(session, 'secondFactorVerify').mockRejectedValue(new ApiError(410, { error: 'challenge_gone', message: MSG_CHALLENGE_GONE }));
    const onRestart = vi.fn();
    render(<SecondFactorStep challenge={{ challenge: 'ch3', setup_required: false, expires_in: 300 }} onDone={vi.fn()} onRestart={onRestart} />);
    submitCode('123456');
    await waitFor(() => expect(onRestart).toHaveBeenCalledWith(MSG_CHALLENGE_GONE));
    fireEvent.click(screen.getByRole('button', { name: 'Войти заново' }));
    expect(onRestart).toHaveBeenLastCalledWith(null);
  });
});
