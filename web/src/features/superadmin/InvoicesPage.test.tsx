import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import type { ReactNode } from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { session } from '../../auth/session';
import { apiError, mockApi, renderPage, signIn, stubDialog } from '../../test/pages';
import { ClubPage } from './ClubPage';
import { FlashContext } from './flash';
import { InvoicesPage } from './InvoicesPage';

/* «Оплаты» и скидка клуба в кабинете суперадмина (superadmin/invoices/index.blade.php, clubs/show.blade.php). */

vi.mock('@tanstack/react-router', async (original) => ({
  ...(await original<typeof import('@tanstack/react-router')>()),
  useNavigate: () => vi.fn(async () => undefined),
}));

const SUPER = { user: { id: 1, name: 'Главный', phone: '+77010000001', is_superadmin: true }, club: null };
const board = {
  pending: [{ id: 7, number: 'ZF-00007', club_id: 5, club_name: 'Cyber Arena', plan_name: 'Базовый', months: 6, computers: 2, price_per_computer_tiyn: 100000, discount_percent: 10, amount_tiyn: 1080000, created: '15.11.2026 07:00' }],
  closed: [
    { id: 6, number: 'ZF-00006', club_id: 5, club_name: 'Cyber Arena', plan_name: 'Базовый', months: 1, amount_tiyn: 200000, status: 'paid', created: '01.11.2026', paid: '02.11.2026' },
    { id: 5, number: 'ZF-00005', club_id: 4, club_name: 'Old Club', plan_name: 'Pro', months: 3, amount_tiyn: 15000000, status: 'canceled', created: '20.10.2026', paid: null },
  ],
  page: 1, per_page: 30, total: 2,
  kaspi: { url: 'https://pay.kaspi.kz/pay/zafiro', recipient: 'ИП Zafiro' },
};

describe('кабинет суперадмина: оплаты', () => {
  const flash = { status: vi.fn(), error: vi.fn() };
  beforeEach(() => {
    flash.status.mockReset();
    flash.error.mockReset();
    stubDialog();
    signIn('', [], SUPER as never);
  });
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
    session.clear();
  });
  const render = (ui: ReactNode) => renderPage(<FlashContext.Provider value={flash}>{ui}</FlashContext.Provider>);

  /** Окно свежего кода второго фактора: ввести код и подтвердить. */
  const confirmWith = (code: string) => {
    const dialog = within(screen.getByRole('dialog'));
    fireEvent.change(dialog.getByLabelText('Код из приложения'), { target: { value: code } });
    fireEvent.click(dialog.getByRole('button', { name: 'Подтвердить' }));
    return dialog;
  };

  it('счёт, ожидающий оплаты: «Оплата получена» — вопрос и свежий код в одном окне', async () => {
    let attempt = 0;
    const api = mockApi({
      'GET /api/superadmin/invoices': board,
      'POST /api/superadmin/invoices/7/pay': () =>
        ++attempt === 1
          ? apiError(422, { error: 'validation_failed', message: 'Неверный код.', errors: { code: ['Неверный код.'] } })
          : { message: 'Счёт ZF-00007 оплачен, подписка продлена до 25.05.2027.' },
    });
    const confirm = vi.spyOn(window, 'confirm');
    render(<InvoicesPage />);
    expect(await screen.findByText('10 800 ₸')).toBeInTheDocument();
    expect(screen.getAllByRole('link', { name: 'Cyber Arena' })[0]).toHaveAttribute('href', '/superadmin/clubs/5');
    expect(screen.getByText(/Базовый, .*, 2 ПК × 1 000 ₸, скидка 10% · 15\.11\.2026 07:00/)).toBeInTheDocument();
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();

    // «Отмена» в окне — запрос не уходит.
    fireEvent.click(screen.getByRole('button', { name: 'Оплата получена' }));
    let dialog = within(screen.getByRole('dialog'));
    expect(dialog.getByText('Деньги по счёту ZF-00007 пришли? Подписка клуба будет продлена.')).toBeInTheDocument();
    fireEvent.click(dialog.getByRole('button', { name: 'Отмена' }));
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
    expect(api.of('POST', '/api/superadmin/invoices/7/pay')).toHaveLength(0);

    // Неверный код — под полем, окно открыто; верный — счёт оплачен.
    fireEvent.click(screen.getByRole('button', { name: 'Оплата получена' }));
    dialog = confirmWith('111111');
    expect(await dialog.findByRole('alert')).toHaveTextContent('Неверный код.');
    expect(flash.error).not.toHaveBeenCalled();
    confirmWith('222222');
    await waitFor(() => expect(flash.status).toHaveBeenCalledWith('Счёт ZF-00007 оплачен, подписка продлена до 25.05.2027.'));
    const calls = api.of('POST', '/api/superadmin/invoices/7/pay');
    expect(calls.map((c) => c.body)).toEqual([{ code: '111111' }, { code: '222222' }]);
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
    expect(confirm).not.toHaveBeenCalled();
  });

  it('оплата: ошибка не про код закрывает окно и показывается в layout', async () => {
    mockApi({
      'GET /api/superadmin/invoices': board,
      'POST /api/superadmin/invoices/7/pay': () => apiError(422, { error: 'validation_failed', message: 'Счёт уже закрыт.', errors: { invoice: ['Счёт уже закрыт.'] } }),
    });
    render(<InvoicesPage />);
    fireEvent.click(await screen.findByRole('button', { name: 'Оплата получена' }));
    confirmWith('123456');
    await waitFor(() => expect(flash.error).toHaveBeenCalledWith('Счёт уже закрыт.'));
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
  });

  it('отмена счёта, история и настройка Kaspi со свежим кодом', async () => {
    const api = mockApi({
      'GET /api/superadmin/invoices': board,
      'POST /api/superadmin/invoices/7/cancel': { message: 'Счёт отменён.' },
      'POST /api/superadmin/invoices/kaspi': { message: 'Настройки оплаты сохранены.' },
    });
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    render(<InvoicesPage />);
    expect(await screen.findByRole('row', { name: /ZF-00006\s*01\.11\.2026 Cyber Arena Базовый, .* 2 000 ₸ Оплачен 02\.11\.2026/ })).toBeInTheDocument();
    expect(screen.getByRole('row', { name: /ZF-00005\s*20\.10\.2026 Old Club Pro, .* 150 000 ₸ Отменён/ })).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Отменить' }));
    await waitFor(() => expect(flash.status).toHaveBeenCalledWith('Счёт отменён.'));

    const url = screen.getByLabelText('Ссылка на оплату') as HTMLInputElement;
    expect(url.value).toBe('https://pay.kaspi.kz/pay/zafiro');
    expect((screen.getByLabelText('Получатель (видят клубы)') as HTMLInputElement).value).toBe('ИП Zafiro');
    fireEvent.change(url, { target: { value: '' } });
    fireEvent.click(screen.getByRole('button', { name: 'Сохранить' }));
    // Сначала окно кода: без него запрос не уходит.
    expect(within(screen.getByRole('dialog')).getByText('Изменить настройки оплаты через Kaspi? По этой ссылке платят все клубы.')).toBeInTheDocument();
    expect(api.of('POST', '/api/superadmin/invoices/kaspi')).toHaveLength(0);
    confirmWith('123456');
    await waitFor(() => expect(flash.status).toHaveBeenCalledWith('Настройки оплаты сохранены.'));
    // Пустая ссылка выключает оплату.
    expect(api.of('POST', '/api/superadmin/invoices/kaspi')[0]?.body).toEqual({ url: null, recipient: 'ИП Zafiro', code: '123456' });
  });

  it('пусто: нет счетов и истории', async () => {
    mockApi({ 'GET /api/superadmin/invoices': { ...board, pending: [], closed: [], total: 0, kaspi: null } });
    render(<InvoicesPage />);
    expect(await screen.findByText('Неоплаченных счетов нет.')).toBeInTheDocument();
    expect(screen.getByText('Пока пусто.')).toBeInTheDocument();
  });

  const club = (subscription: Record<string, unknown>) => ({
    id: 5, name: 'Cyber Arena', slug: 'cyber', created: '01.10.2026', timezone: 'Asia/Almaty', today: '2026-11-15', computers: { total: 3, online: 1 },
    subscription: { status: 'active', saas_plan_id: 2, plan_name: 'Pro', paid_until: '15.12.2026', trial_until: null, active: true, plan_price_monthly_tiyn: 5000000, ...subscription },
    badge: { label: 'Активна', active: true }, members: [], page: 1, per_page: 30, total: 0, heatmap: [], plans: [{ id: 2, name: 'Pro', is_active: true }], activity: [], platform_logs: [],
  });

  it('карточка клуба: цена со скидкой и форма «Индивидуальная скидка»', async () => {
    const api = mockApi({
      'GET /api/superadmin/clubs/5': club({ monthly_price_tiyn: 4000000, discount_percent: 20, stored_discount_percent: 20, discount_until: '2026-11-25', discount_until_label: '25.11.2026', discount_note: 'Партнёр' }),
      'POST /api/superadmin/clubs/5/subscription/discount': { message: 'Скидка снята.' },
    });
    render(<ClubPage clubId={5} />);
    expect(await screen.findByText(/к оплате 40 000 ₸ в месяц/)).toBeInTheDocument();
    expect(screen.getByText('(без скидки 50 000 ₸)')).toBeInTheDocument();
    const percent = screen.getByLabelText('Скидка, %') as HTMLInputElement;
    const until = screen.getByLabelText('Действует до (пусто — бессрочно)') as HTMLInputElement;
    expect(percent.value).toBe('20');
    expect(until.value).toBe('2026-11-25');
    expect(until).toHaveAttribute('min', '2026-11-15');
    expect((screen.getByLabelText('Причина') as HTMLInputElement).value).toBe('Партнёр');
    fireEvent.change(percent, { target: { value: '0' } });
    fireEvent.change(until, { target: { value: '' } });
    fireEvent.click(screen.getByRole('button', { name: 'Сохранить скидку' }));
    // Скидка — деньги платформы: сначала окно свежего кода второго фактора.
    expect(within(screen.getByRole('dialog')).getByText('Сохранить скидку 0% для клуба Cyber Arena?')).toBeInTheDocument();
    expect(api.of('POST', '/api/superadmin/clubs/5/subscription/discount')).toHaveLength(0);
    confirmWith('123456');
    await waitFor(() => expect(flash.status).toHaveBeenCalledWith('Скидка снята.'));
    expect(api.of('POST', '/api/superadmin/clubs/5/subscription/discount')[0]?.body).toEqual({ discount_percent: 0, discount_until: null, discount_note: 'Партнёр', code: '123456' });
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
  });

  it('карточка клуба: неверный код скидки — под полем окна, окно открыто', async () => {
    let attempt = 0;
    const api = mockApi({
      'GET /api/superadmin/clubs/5': club({ monthly_price_tiyn: 5000000, discount_percent: 0, stored_discount_percent: 0, discount_until: null, discount_until_label: null, discount_note: null }),
      'POST /api/superadmin/clubs/5/subscription/discount': () =>
        ++attempt === 1 ? apiError(422, { error: 'validation_failed', message: 'Неверный код.', errors: { code: ['Неверный код.'] } }) : { message: 'Скидка сохранена.' },
    });
    render(<ClubPage clubId={5} />);
    fireEvent.change(await screen.findByLabelText('Скидка, %'), { target: { value: '15' } });
    fireEvent.click(screen.getByRole('button', { name: 'Сохранить скидку' }));
    const dialog = confirmWith('111111');
    expect(await dialog.findByRole('alert')).toHaveTextContent('Неверный код.');
    expect(flash.error).not.toHaveBeenCalled();
    confirmWith('222222');
    await waitFor(() => expect(flash.status).toHaveBeenCalledWith('Скидка сохранена.'));
    expect(api.of('POST', '/api/superadmin/clubs/5/subscription/discount').map((c) => c.body)).toEqual([
      { discount_percent: 15, discount_until: null, discount_note: null, code: '111111' },
      { discount_percent: 15, discount_until: null, discount_note: null, code: '222222' },
    ]);
  });

  it('карточка клуба: ошибка поля скидки — под формой и в layout', async () => {
    const message = 'The discount percent field must not be greater than 100.';
    mockApi({
      'GET /api/superadmin/clubs/5': club({ monthly_price_tiyn: 5000000, discount_percent: 0, stored_discount_percent: 0, discount_until: null, discount_until_label: null, discount_note: null }),
      'POST /api/superadmin/clubs/5/subscription/discount': () => apiError(422, { message, errors: { discount_percent: [message] } }),
    });
    render(<ClubPage clubId={5} />);
    fireEvent.change(await screen.findByLabelText('Скидка, %'), { target: { value: '101' } });
    fireEvent.submit(screen.getByRole('button', { name: 'Сохранить скидку' }).closest('form') as HTMLFormElement);
    confirmWith('123456');
    await waitFor(() => expect(flash.error).toHaveBeenCalledWith(message));
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
    expect(screen.getByText(message)).toHaveClass('text-red-600');
  });

  it('карточка клуба: закончившаяся скидка — подсказка и пустая форма', async () => {
    mockApi({
      'GET /api/superadmin/clubs/5': club({ monthly_price_tiyn: 5000000, discount_percent: 0, stored_discount_percent: 20, discount_until: '2026-11-10', discount_until_label: '10.11.2026', discount_note: 'Партнёр' }),
    });
    render(<ClubPage clubId={5} />);
    expect(await screen.findByText('Скидка 20% закончилась 10.11.2026.')).toBeInTheDocument();
    expect(screen.getByText(/к оплате 50 000 ₸ в месяц/)).toBeInTheDocument();
    expect(screen.queryByText(/без скидки/)).not.toBeInTheDocument();
    expect((screen.getByLabelText('Скидка, %') as HTMLInputElement).value).toBe('0');
    expect((screen.getByLabelText('Действует до (пусто — бессрочно)') as HTMLInputElement).value).toBe('');
    expect((screen.getByLabelText('Причина') as HTMLInputElement).value).toBe('');
  });
});
