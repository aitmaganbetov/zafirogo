import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import type { ReactNode } from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { session } from '../../auth/session';
import { apiError, mockApi, renderPage, signIn, stubDialog } from '../../test/pages';
import { FlashContext } from './flash';
import { ResellersPage } from './ResellersPage';

/* «Реселлеры» в кабинете суперадмина (superadmin/resellers/index.blade.php). */

const navigate = vi.fn(async (_: unknown) => undefined);
vi.mock('@tanstack/react-router', async (original) => ({
  ...(await original<typeof import('@tanstack/react-router')>()),
  useNavigate: () => navigate,
}));

const SUPER = { user: { id: 1, name: 'Главный', phone: '+77010000001', is_superadmin: true }, club: null };
const arman = {
  id: 3, code: 'arman-games', name: 'Arman', user_name: 'Арман Сериков', user_phone: '+77001234567', commission_bp: 1250, discount_percent: 20,
  is_active: true, earned_tiyn: 48000, paid_out_tiyn: 30000, balance_tiyn: 18000, clubs: [{ id: 5, name: 'Cyber Arena', attached: '20.11.2026' }],
};
const stopped = { ...arman, id: 2, code: 'stopped', name: 'Stopped', is_active: false, commission_bp: 1000, earned_tiyn: 0, paid_out_tiyn: 5000, balance_tiyn: -5000, clubs: [] };
const board = { host: 'resseler.zafiro.kz', items: [arman, stopped], page: 1, per_page: 30, total: 2 };

describe('кабинет суперадмина: реселлеры', () => {
  const flash = { status: vi.fn(), error: vi.fn() };
  beforeEach(() => {
    flash.status.mockReset();
    flash.error.mockReset();
    navigate.mockClear();
    stubDialog();
    signIn('', [], SUPER as never);
  });
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
    session.clear();
  });
  const render = (ui: ReactNode) => renderPage(<FlashContext.Provider value={flash}>{ui}</FlashContext.Provider>);
  const card = (code: string) => screen.getByText(code).closest('article') as HTMLElement;

  // GS-563: суперадмин видит «к выплате 180 ₸».
  it('карточка реселлера: итоги, условия, клубы', async () => {
    mockApi({ 'GET /api/superadmin/resellers': board });
    render(<ResellersPage search={{}} />);
    expect(await screen.findByText(/они входят в свой кабинет на resseler\.zafiro\.kz по телефону и паролю/)).toBeInTheDocument();
    const a = within(card('arman-games'));
    expect(a.getByText('Заработано 480 ₸, выплачено 300 ₸, к выплате 180 ₸')).toBeInTheDocument();
    expect(a.getByText(/Арман Сериков · \+77001234567 · /)).toBeInTheDocument();
    expect((a.getByLabelText('Процент реселлера') as HTMLInputElement).value).toBe('12.5');
    expect((a.getByLabelText('Скидка клубам, %') as HTMLInputElement).value).toBe('20');
    expect((a.getByLabelText('Код действует') as HTMLInputElement).checked).toBe(true);
    // В поле выплаты подставлен остаток; у реселлера без остатка — пусто.
    expect((a.getByLabelText('Выплата, ₸') as HTMLInputElement).value).toBe('180');
    expect(a.getByRole('link', { name: 'Cyber Arena' })).toHaveAttribute('href', '/superadmin/clubs/5');
    const s = within(card('stopped'));
    expect(s.getByText('Заработано 0 ₸, выплачено 50 ₸, к выплате -50 ₸')).toBeInTheDocument();
    expect((s.getByLabelText('Выплата, ₸') as HTMLInputElement).value).toBe('');
    expect((s.getByLabelText('Код действует') as HTMLInputElement).checked).toBe(false);
    expect(s.queryByText('Клубы реселлера')).not.toBeInTheDocument();
  });

  // GS-560: процент уходит базисными пунктами, пустой пароль — null (существующий аккаунт сохраняет свой).
  it('новый реселлер: процент — базисные пункты, пароль необязателен', async () => {
    const api = mockApi({ 'GET /api/superadmin/resellers': board, 'POST /api/superadmin/resellers': { message: 'Реселлер partner создан.' } });
    render(<ResellersPage search={{}} />);
    const form = (await screen.findByText('Новый реселлер')).closest('form') as HTMLFormElement;
    const f = within(form);
    fireEvent.change(f.getByLabelText('Имя или компания'), { target: { value: ' Club Partner ' } });
    fireEvent.change(f.getByLabelText('Телефон'), { target: { value: '8 700 555-66-77' } });
    fireEvent.change(f.getByLabelText(/Код реселлера/), { target: { value: 'Partner' } });
    fireEvent.change(f.getByLabelText('Процент реселлера'), { target: { value: '12.35' } });
    fireEvent.change(f.getByLabelText('Скидка клубам, %'), { target: { value: '15' } });
    fireEvent.click(f.getByRole('button', { name: 'Создать реселлера' }));
    await waitFor(() => expect(flash.status).toHaveBeenCalledWith('Реселлер partner создан.'));
    expect(api.of('POST', '/api/superadmin/resellers')[0]?.body).toEqual({ name: 'Club Partner', phone: '8 700 555-66-77', password: null, code: 'Partner', commission_bp: 1235, discount_percent: 15 });
  });

  it('процент с тремя знаками отклоняется до запроса; ошибка сервера — во flash', async () => {
    const api = mockApi({
      'GET /api/superadmin/resellers': board,
      'PATCH /api/superadmin/resellers/3': () => apiError(422, { error: 'validation_failed', message: 'x', errors: { discount_percent: ['The discount percent field must not be greater than 100.'] } }),
    });
    render(<ResellersPage search={{}} />);
    await screen.findByText('arman-games');
    const a = within(card('arman-games'));
    // Браузер не отправит поле с шагом 0.01 и тремя знаками; разбор — второй рубеж (submit мимо проверки поля).
    fireEvent.change(a.getByLabelText('Процент реселлера'), { target: { value: '12.345' } });
    fireEvent.submit(a.getByLabelText('Процент реселлера').closest('form') as HTMLFormElement);
    expect(flash.error).toHaveBeenCalledWith('The commission percent field must have 0-2 decimal places.');
    expect(api.of('PATCH', '/api/superadmin/resellers/3')).toHaveLength(0);
    // GS-564: 12.5 % → 1250, код остановлен.
    fireEvent.change(a.getByLabelText('Процент реселлера'), { target: { value: '12.5' } });
    fireEvent.click(a.getByLabelText('Код действует'));
    fireEvent.click(a.getByRole('button', { name: 'Сохранить' }));
    await waitFor(() => expect(flash.error).toHaveBeenCalledWith('The discount percent field must not be greater than 100.'));
    expect(api.of('PATCH', '/api/superadmin/resellers/3')[0]?.body).toEqual({ commission_bp: 1250, discount_percent: 20, is_active: false });
  });

  it('выплата: окно кода, сумма в тиынах, один ключ идемпотентности на форму', async () => {
    let attempt = 0;
    const api = mockApi({
      'GET /api/superadmin/resellers': board,
      'POST /api/superadmin/resellers/3/payouts': () =>
        ++attempt === 1 ? apiError(422, { error: 'validation_failed', message: 'Неверный код.', errors: { code: ['Неверный код.'] } }) : { message: 'Выплата реселлеру arman-games записана.' },
    });
    render(<ResellersPage search={{}} />);
    await screen.findByText('arman-games');
    const a = within(card('arman-games'));
    // Больше двух знаков — ошибка формы, не округление (Q-002, UQ-7).
    fireEvent.change(a.getByLabelText('Выплата, ₸'), { target: { value: '300.505' } });
    fireEvent.submit(a.getByLabelText('Выплата, ₸').closest('form') as HTMLFormElement);
    expect(flash.error).toHaveBeenCalledWith('The amount field must have 0-2 decimal places.');
    expect(screen.queryByText('Подтверждение кодом')).not.toBeInTheDocument();

    fireEvent.change(a.getByLabelText('Выплата, ₸'), { target: { value: '300.5' } });
    fireEvent.change(a.getByLabelText('Комментарий'), { target: { value: ' Kaspi ' } });
    fireEvent.click(a.getByRole('button', { name: 'Записать выплату' }));
    const dialog = within(screen.getByRole('dialog'));
    expect(dialog.getByText('Выплата реселлеру arman-games: 300.50 ₸')).toBeInTheDocument();
    expect(api.of('POST', '/api/superadmin/resellers/3/payouts')).toHaveLength(0);
    fireEvent.change(dialog.getByLabelText('Код из приложения'), { target: { value: '111111' } });
    fireEvent.click(dialog.getByRole('button', { name: 'Подтвердить' }));
    // Неверный код — под полем, окно открыто.
    expect(await dialog.findByRole('alert')).toHaveTextContent('Неверный код.');
    fireEvent.change(dialog.getByLabelText('Код из приложения'), { target: { value: '222222' } });
    fireEvent.click(dialog.getByRole('button', { name: 'Подтвердить' }));
    await waitFor(() => expect(flash.status).toHaveBeenCalledWith('Выплата реселлеру arman-games записана.'));
    const calls = api.of('POST', '/api/superadmin/resellers/3/payouts');
    expect(calls).toHaveLength(2);
    expect(calls[1]?.body).toEqual({ amount_tiyn: 30050, note: 'Kaspi', code: '222222' });
    const key = calls[0]?.headers.get('Idempotency-Key');
    expect(key).toBeTruthy();
    expect(calls[1]?.headers.get('Idempotency-Key')).toBe(key);
  });

  // Ревью 22.11.2026: потерянный ответ (обрыв, 5xx) не должен приводить ко второй выплате — ключ живёт до успеха.
  it('выплата: ответ потерян → повтор с тем же ключом, и из окна, и после его закрытия; после успеха ключ новый', async () => {
    let attempt = 0;
    const api = mockApi({
      'GET /api/superadmin/resellers': board,
      'POST /api/superadmin/resellers/3/payouts': () => {
        attempt++;
        if (attempt === 1) throw new TypeError('Failed to fetch');
        if (attempt === 2) return apiError(502, { error: 'bad_gateway', message: '' });
        if (attempt === 3) return apiError(403, { error: 'forbidden', message: 'Недостаточно прав.' });
        return { message: 'Выплата реселлеру arman-games записана.' };
      },
    });
    render(<ResellersPage search={{}} />);
    await screen.findByText('arman-games');
    const a = within(card('arman-games'));
    const calls = () => api.of('POST', '/api/superadmin/resellers/3/payouts');
    const lists = () => api.of('GET', '/api/superadmin/resellers').length;
    const confirm = (code: string) => {
      const dialog = within(screen.getByRole('dialog'));
      fireEvent.change(dialog.getByLabelText('Код из приложения'), { target: { value: code } });
      fireEvent.click(dialog.getByRole('button', { name: 'Подтвердить' }));
      return dialog;
    };

    fireEvent.click(a.getByRole('button', { name: 'Записать выплату' }));
    // 1: сети нет — окно остаётся, ошибка в окне, список перечитан (выплата могла записаться).
    const before = lists();
    let dialog = confirm('111111');
    expect(await dialog.findByRole('alert')).toHaveTextContent('Нет связи с сервером. Повторите попытку.');
    await waitFor(() => expect(lists()).toBeGreaterThan(before));
    // 2: 502 — то же окно, тот же ключ.
    dialog = confirm('222222');
    await waitFor(() => expect(calls()).toHaveLength(2));
    await waitFor(() => expect(dialog.getByRole('button', { name: 'Подтвердить' })).toBeEnabled());
    expect(screen.getByRole('dialog')).toHaveAttribute('open');
    // 3: отказ (403) закрывает окно, но ключ выплаты остаётся.
    confirm('333333');
    await waitFor(() => expect(flash.error).toHaveBeenCalledWith('Недостаточно прав.'));
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
    // 4: та же форма отправлена заново — тот же ключ.
    fireEvent.click(a.getByRole('button', { name: 'Записать выплату' }));
    confirm('444444');
    await waitFor(() => expect(flash.status).toHaveBeenCalledWith('Выплата реселлеру arman-games записана.'));
    const keys = calls().map((c) => c.headers.get('Idempotency-Key'));
    expect(keys).toHaveLength(4);
    expect(keys[0]).toBeTruthy();
    expect(new Set(keys).size).toBe(1);

    // После успеха — новая выплата, новый ключ; другая сумма — тоже новый ключ.
    fireEvent.change(a.getByLabelText('Выплата, ₸'), { target: { value: '180' } });
    fireEvent.click(a.getByRole('button', { name: 'Записать выплату' }));
    confirm('555555');
    await waitFor(() => expect(calls()).toHaveLength(5));
    expect(calls()[4]?.headers.get('Idempotency-Key')).not.toBe(keys[0]);
  });

  it('выплата: изменилась сумма — ключ новый', async () => {
    const api = mockApi({
      'GET /api/superadmin/resellers': board,
      'POST /api/superadmin/resellers/3/payouts': () => apiError(503, { error: 'unavailable', message: '' }),
    });
    render(<ResellersPage search={{}} />);
    await screen.findByText('arman-games');
    const a = within(card('arman-games'));
    const calls = () => api.of('POST', '/api/superadmin/resellers/3/payouts');
    for (const [n, amount] of [[1, '100'], [2, '100'], [3, '150']] as const) {
      fireEvent.change(a.getByLabelText('Выплата, ₸'), { target: { value: amount } });
      fireEvent.click(a.getByRole('button', { name: 'Записать выплату' }));
      const dialog = within(screen.getByRole('dialog'));
      fireEvent.change(dialog.getByLabelText('Код из приложения'), { target: { value: '111111' } });
      fireEvent.click(dialog.getByRole('button', { name: 'Подтвердить' }));
      await waitFor(() => expect(calls()).toHaveLength(n));
      await dialog.findByRole('alert');
      fireEvent.click(dialog.getByRole('button', { name: 'Отмена' }));
    }
    const keys = calls().map((c) => c.headers.get('Idempotency-Key'));
    expect(keys[1]).toBe(keys[0]);
    expect(keys[2]).not.toBe(keys[0]);
  });

  it('удаление — с кодом; отвязка клуба — после подтверждения', async () => {
    const api = mockApi({
      'GET /api/superadmin/resellers': board,
      'DELETE /api/superadmin/resellers/3': { message: 'Реселлер arman-games удалён.' },
      'DELETE /api/superadmin/resellers/3/clubs/5': { message: 'Клуб Cyber Arena отвязан от реселлера.' },
    });
    const confirm = vi.spyOn(window, 'confirm').mockReturnValueOnce(false).mockReturnValue(true);
    render(<ResellersPage search={{}} />);
    await screen.findByText('arman-games');
    const a = within(card('arman-games'));
    fireEvent.click(a.getByRole('button', { name: 'Отвязать' }));
    expect(confirm).toHaveBeenCalledWith('Отвязать клуб от реселлера? Новые счета пойдут без его скидки и процента.');
    expect(api.of('DELETE', '/api/superadmin/resellers/3/clubs/5')).toHaveLength(0);
    fireEvent.click(a.getByRole('button', { name: 'Отвязать' }));
    await waitFor(() => expect(flash.status).toHaveBeenCalledWith('Клуб Cyber Arena отвязан от реселлера.'));

    fireEvent.click(a.getByRole('button', { name: 'Удалить' }));
    const dialog = within(screen.getByRole('dialog'));
    expect(dialog.getByText('Удалить реселлера arman-games? Аккаунт останется, клубы отвяжутся, выплаты удалятся.')).toBeInTheDocument();
    expect(api.of('DELETE', '/api/superadmin/resellers/3')).toHaveLength(0);
    fireEvent.change(dialog.getByLabelText('Код из приложения'), { target: { value: '123456' } });
    fireEvent.click(dialog.getByRole('button', { name: 'Подтвердить' }));
    await waitFor(() => expect(flash.status).toHaveBeenCalledWith('Реселлер arman-games удалён.'));
    expect(api.of('DELETE', '/api/superadmin/resellers/3')[0]?.body).toEqual({ code: '123456' });
  });

  it('поиск и пустой список', async () => {
    const api = mockApi({ 'GET /api/superadmin/resellers': { ...board, items: [], total: 0 } });
    render(<ResellersPage search={{ q: 'arm' }} />);
    expect(await screen.findByText('Реселлеров пока нет.')).toBeInTheDocument();
    expect(api.of('GET', '/api/superadmin/resellers')[0]?.search.get('q')).toBe('arm');
    const q = screen.getByPlaceholderText('Код или имя') as HTMLInputElement;
    expect(q.value).toBe('arm');
    fireEvent.change(q, { target: { value: 'partner' } });
    fireEvent.click(screen.getByRole('button', { name: 'Найти' }));
    expect(navigate).toHaveBeenCalledWith({ href: '/superadmin/resellers?q=partner' });
  });
});
