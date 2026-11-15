import { fireEvent, screen, waitFor } from '@testing-library/react';
import type { ReactNode } from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { session } from '../../auth/session';
import { apiError, mockApi, renderPage, signIn } from '../../test/pages';
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
    signIn('', [], SUPER as never);
  });
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
    session.clear();
  });
  const render = (ui: ReactNode) => renderPage(<FlashContext.Provider value={flash}>{ui}</FlashContext.Provider>);

  it('счёт, ожидающий оплаты: «Оплата получена» после подтверждения', async () => {
    const api = mockApi({ 'GET /api/superadmin/invoices': board, 'POST /api/superadmin/invoices/7/pay': { message: 'Счёт ZF-00007 оплачен, подписка продлена до 25.05.2027.' } });
    const confirm = vi.spyOn(window, 'confirm').mockReturnValueOnce(false).mockReturnValue(true);
    render(<InvoicesPage />);
    expect(await screen.findByText('10 800 ₸')).toBeInTheDocument();
    expect(screen.getAllByRole('link', { name: 'Cyber Arena' })[0]).toHaveAttribute('href', '/superadmin/clubs/5');
    expect(screen.getByText(/Базовый, .*, 2 ПК × 1 000 ₸, скидка 10% · 15\.11\.2026 07:00/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Оплата получена' }));
    expect(confirm).toHaveBeenCalledWith('Деньги по счёту ZF-00007 пришли? Подписка клуба будет продлена.');
    expect(api.of('POST', '/api/superadmin/invoices/7/pay')).toHaveLength(0);
    fireEvent.click(screen.getByRole('button', { name: 'Оплата получена' }));
    await waitFor(() => expect(flash.status).toHaveBeenCalledWith('Счёт ZF-00007 оплачен, подписка продлена до 25.05.2027.'));
    expect(api.of('POST', '/api/superadmin/invoices/7/pay')).toHaveLength(1);
  });

  it('отмена счёта, история и настройка Kaspi', async () => {
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
    await waitFor(() => expect(flash.status).toHaveBeenCalledWith('Настройки оплаты сохранены.'));
    // Пустая ссылка выключает оплату.
    expect(api.of('POST', '/api/superadmin/invoices/kaspi')[0]?.body).toEqual({ url: null, recipient: 'ИП Zafiro' });
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
    await waitFor(() => expect(flash.status).toHaveBeenCalledWith('Скидка снята.'));
    expect(api.of('POST', '/api/superadmin/clubs/5/subscription/discount')[0]?.body).toEqual({ discount_percent: 0, discount_until: null, discount_note: 'Партнёр' });
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
    await waitFor(() => expect(flash.error).toHaveBeenCalledWith(message));
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
