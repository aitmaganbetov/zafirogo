import { fireEvent, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { session } from '../../auth/session';
import { apiError, mockApi, renderPage, signIn, stubDialog } from '../../test/pages';
import { AccountsPage } from './AccountsPage';
import { FlashContext } from './flash';

/*
 * Аудит A-10: выдача и снятие прав суперадмина и сброс 2FA — только со свежим кодом TOTP. Окно подтверждения
 * с полем кода: код уходит в теле запроса (application/json — и у DELETE), ошибка кода — под полем, окно открыто.
 */

vi.mock('@tanstack/react-router', async (original) => ({
  ...(await original<typeof import('@tanstack/react-router')>()),
  useNavigate: () => vi.fn(async () => undefined),
}));

const account = (id: number, name: string, extra: Record<string, unknown> = {}) => ({
  id, name, phone: `+7701000000${id}`, is_superadmin: false, blocked_since: null, self: false, second_factor: false, memberships: [], ...extra,
});
const list = {
  items: [
    account(1, 'Главный', { is_superadmin: true, self: true, second_factor: true }),
    account(2, 'Второй', { is_superadmin: true, second_factor: true }),
    account(3, 'Кандидат'),
  ],
  page: 1, per_page: 30, total: 3,
};
const SUPER = { user: { id: 1, name: 'Главный', phone: '+77010000001', is_superadmin: true }, club: null };

describe('кабинет суперадмина: подтверждение кодом', () => {
  const flash = { status: vi.fn(), error: vi.fn() };
  beforeEach(() => {
    stubDialog();
    flash.status.mockReset();
    flash.error.mockReset();
    signIn('', [], SUPER as never);
  });
  afterEach(() => {
    vi.unstubAllGlobals();
    session.clear();
  });

  const render = () =>
    renderPage(
      <FlashContext.Provider value={flash}>
        <AccountsPage search={{}} />
      </FlashContext.Provider>,
    );
  const dialog = () => document.querySelector('dialog') as HTMLDialogElement;
  const code = () => dialog().querySelector('input[name="code"]') as HTMLInputElement;
  const submitCode = (value: string) => {
    fireEvent.change(code(), { target: { value } });
    fireEvent.submit(dialog().querySelector('form') as HTMLFormElement);
  };

  it('выдача прав: окно с вопросом и полем кода, код уходит в теле POST', async () => {
    const confirm = vi.spyOn(window, 'confirm');
    const api = mockApi({ 'GET /api/superadmin/accounts': list, 'POST /api/superadmin/accounts/3/superadmin': { message: 'Права суперадмина выданы.' } });
    render();
    fireEvent.click(await screen.findByRole('button', { name: 'Сделать суперадмином' }));
    expect(confirm).not.toHaveBeenCalled();
    expect(dialog()).toHaveAttribute('open');
    expect(dialog()).toHaveTextContent('Выдать Кандидат права суперадмина? Он получит доступ ко всем клубам платформы.');
    expect(code()).toHaveAttribute('inputmode', 'numeric');
    expect(code()).toHaveAttribute('autocomplete', 'one-time-code');
    expect(code()).toBeRequired();
    expect(api.of('POST', '/api/superadmin/accounts/3/superadmin')).toHaveLength(0);

    submitCode(' 123456 ');
    await waitFor(() => expect(flash.status).toHaveBeenCalledWith('Права суперадмина выданы.'));
    const [post] = api.of('POST', '/api/superadmin/accounts/3/superadmin');
    expect(post?.body).toEqual({ code: '123456' });
    expect(post?.headers.get('Content-Type')).toBe('application/json');
    expect(dialog()).not.toHaveAttribute('open');
  });

  it('снятие прав и сброс 2FA: код в теле DELETE', async () => {
    const api = mockApi({
      'GET /api/superadmin/accounts': list,
      'DELETE /api/superadmin/accounts/2/superadmin': { message: 'Права суперадмина сняты.' },
      'DELETE /api/superadmin/accounts/2/second-factor': { message: 'Двухфакторная аутентификация сброшена.' },
    });
    render();
    fireEvent.click(await screen.findByRole('button', { name: 'Снять права суперадмина' }));
    expect(dialog()).toHaveTextContent('Снять с Второй права суперадмина?');
    submitCode('111222');
    await waitFor(() => expect(flash.status).toHaveBeenCalledWith('Права суперадмина сняты.'));
    const [revoke] = api.of('DELETE', '/api/superadmin/accounts/2/superadmin');
    expect(revoke?.body).toEqual({ code: '111222' });
    expect(revoke?.headers.get('Content-Type')).toBe('application/json');

    fireEvent.click(screen.getByRole('button', { name: 'Сбросить 2FA' }));
    expect(dialog()).toHaveTextContent('Сбросить двухфакторную аутентификацию Второй?');
    expect(code().value).toBe(''); // прошлый код в поле не остаётся
    submitCode('333444');
    await waitFor(() => expect(flash.status).toHaveBeenCalledWith('Двухфакторная аутентификация сброшена.'));
    expect(api.of('DELETE', '/api/superadmin/accounts/2/second-factor')[0]?.body).toEqual({ code: '333444' });
  });

  it('неверный код: ошибка под полем, окно открыто; второй код проходит', async () => {
    let attempt = 0;
    const api = mockApi({
      'GET /api/superadmin/accounts': list,
      'POST /api/superadmin/accounts/3/superadmin': () =>
        ++attempt === 1
          ? apiError(422, { error: 'validation_failed', message: 'Неверный код.', errors: { code: ['Неверный код.'] } })
          : { message: 'Права суперадмина выданы.' },
    });
    render();
    fireEvent.click(await screen.findByRole('button', { name: 'Сделать суперадмином' }));
    submitCode('000000');
    expect(await screen.findByRole('alert')).toHaveTextContent('Неверный код.');
    expect(dialog()).toHaveAttribute('open');
    expect(code()).toHaveAttribute('aria-invalid', 'true');
    expect(flash.error).not.toHaveBeenCalled();
    submitCode('654321');
    await waitFor(() => expect(flash.status).toHaveBeenCalledWith('Права суперадмина выданы.'));
    expect(api.of('POST', '/api/superadmin/accounts/3/superadmin').map((c) => c.body)).toEqual([{ code: '000000' }, { code: '654321' }]);
  });

  it('другая ошибка (лимит попыток) — в сообщение кабинета, окно закрывается; «Отмена» ничего не отправляет', async () => {
    const api = mockApi({
      'GET /api/superadmin/accounts': list,
      'POST /api/superadmin/accounts/3/superadmin': () => apiError(429, { error: 'too_many_requests', message: 'Слишком много попыток.' }),
    });
    render();
    fireEvent.click(await screen.findByRole('button', { name: 'Сделать суперадмином' }));
    fireEvent.click(screen.getByRole('button', { name: 'Отмена' }));
    expect(dialog()).not.toHaveAttribute('open');
    expect(api.of('POST', '/api/superadmin/accounts/3/superadmin')).toHaveLength(0);

    fireEvent.click(screen.getByRole('button', { name: 'Сделать суперадмином' }));
    submitCode('123456');
    await waitFor(() => expect(flash.error).toHaveBeenCalledWith('Слишком много попыток.'));
    expect(dialog()).not.toHaveAttribute('open');
  });

  it('в режиме просмотра клуба кнопки сброса 2FA нет; блокировка — прежний confirm без кода', async () => {
    session.clear();
    signIn('', [], { ...SUPER, read_only: true } as never);
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(true);
    const api = mockApi({ 'GET /api/superadmin/accounts': list, 'POST /api/superadmin/accounts/3/block': { message: 'Пользователь заблокирован.' } });
    render();
    await screen.findByText('Кандидат');
    expect(screen.queryByRole('button', { name: 'Сбросить 2FA' })).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: 'Заблокировать' }));
    expect(confirm).toHaveBeenCalledTimes(1);
    await waitFor(() => expect(flash.status).toHaveBeenCalledWith('Пользователь заблокирован.'));
    expect(api.of('POST', '/api/superadmin/accounts/3/block')[0]?.body).toBeUndefined();
    expect(dialog()).not.toHaveAttribute('open');
  });
});
