import { screen, within } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { session } from '../../auth/session';
import { mockApi, renderPage, signIn } from '../../test/pages';
import { DashboardPage } from './DashboardPage';

/* Кабинет реселлера (reseller/dashboard.blade.php): суммы до тиына приходят с сервера, на экране — целые тенге. */

const cabinet = {
  code: 'arman-games', commission_bp: 1250, discount_percent: 20, is_active: true,
  earned_tiyn: 48000, paid_out_tiyn: 30000, balance_tiyn: 18000,
  clubs: {
    items: [
      { name: 'Cyber Arena', city: 'Алматы', subscription: 'active', plan_name: 'Базовый', subscription_until: '20.02.2027', attached: '20.11.2026' },
      { name: 'Trial Club', subscription: 'trial', subscription_until: '25.11.2026', attached: '19.11.2026' },
      { name: 'Old Club', subscription: 'unpaid' },
    ],
    page: 1, per_page: 30, total: 3,
  },
  earnings: { items: [{ paid: '20.11.2026', club_name: 'Cyber Arena', months: 3, amount_tiyn: 480000, reseller_amount_tiyn: 48000, reseller_bp: 1000 }], page: 1, per_page: 20, total: 1 },
  payouts: [{ date: '20.11.2026', note: 'Kaspi', amount_tiyn: 30000 }, { date: '18.11.2026', amount_tiyn: 12345 }],
};

describe('кабинет реселлера', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    document.documentElement.removeAttribute('data-reseller-host');
    session.clear();
  });
  const show = (data: unknown, search = {}) => {
    signIn('', [], { user: { id: 9, name: 'Arman', phone: '+77001234567', is_superadmin: false }, club: null } as never);
    const api = mockApi({ 'GET /api/reseller/dashboard': data });
    renderPage(<DashboardPage search={search} />);
    return api;
  };

  // GS-563: «480 ₸», «180 ₸», имя клуба.
  it('код, условия, заработано, выплачено и к выплате', async () => {
    show(cabinet);
    expect(await screen.findByText('arman-games')).toBeInTheDocument();
    expect(screen.getByText('12.5%')).toBeInTheDocument();
    expect(screen.getByText('Скидка для ваших клубов: 20%')).toBeInTheDocument();
    expect(within(screen.getByText('Заработано всего').parentElement as HTMLElement).getByText('480 ₸')).toBeInTheDocument();
    expect(screen.getByText('Выплачено: 300 ₸')).toBeInTheDocument();
    expect(within(screen.getByText('К выплате').parentElement as HTMLElement).getByText('180 ₸')).toBeInTheDocument();
    expect(screen.queryByText(/Ваш код приостановлен/)).not.toBeInTheDocument();
  });

  it('клубы с подпиской, начисления и выплаты', async () => {
    show(cabinet);
    expect(await screen.findByRole('row', { name: /Cyber Arena\s*Алматы Базовый до 20\.02\.2027 20\.11\.2026/ })).toBeInTheDocument();
    expect(screen.getByRole('row', { name: /Trial Club Пробный период до 25\.11\.2026 19\.11\.2026/ })).toBeInTheDocument();
    expect(screen.getByRole('row', { name: /Old Club Не оплачена —/ })).toBeInTheDocument();
    expect(screen.getByRole('row', { name: /20\.11\.2026 Cyber Arena 4 800 ₸ · .* 480 ₸ \(10%\)/ })).toBeInTheDocument();
    const payouts = within(screen.getByRole('region', { name: 'Выплаты' })).getAllByRole('listitem');
    expect(payouts[0]).toHaveTextContent('20.11.2026 · Kaspi300 ₸');
    // 123,45 ₸ на экране — целые тенге (number_format(…, 0)).
    expect(payouts[1]).toHaveTextContent('18.11.2026123 ₸');
  });

  it('пустой кабинет, приостановленный код и отрицательный остаток', async () => {
    show({ ...cabinet, is_active: false, earned_tiyn: 18000, paid_out_tiyn: 50001, balance_tiyn: -32001, clubs: { items: [], page: 1, per_page: 30, total: 0 }, earnings: { items: [], page: 1, per_page: 20, total: 0 }, payouts: [] });
    expect(await screen.findByText('Пока ни один клуб не ввёл ваш код.')).toBeInTheDocument();
    expect(screen.getByText('Начислений пока нет: они появятся, когда ваши клубы оплатят подписку.')).toBeInTheDocument();
    expect(screen.getByRole('status')).toHaveTextContent('Ваш код приостановлен');
    // GAP-053: выплачено больше заработанного — «к выплате» отрицательное.
    expect(within(screen.getByText('К выплате').parentElement as HTMLElement).getByText('-320 ₸')).toBeInTheDocument();
    expect(screen.queryByRole('region', { name: 'Выплаты' })).not.toBeInTheDocument();
  });

  it('страницы списков — свои параметры адреса; на хосте реселлера — с корня', async () => {
    const many = { ...cabinet, clubs: { ...cabinet.clubs, page: 2, total: 65 }, earnings: { ...cabinet.earnings, page: 1, total: 45 } };
    const api = show(many, { clubs_page: 2 });
    await screen.findByText('arman-games');
    expect(api.of('GET', '/api/reseller/dashboard')[0]?.search.get('clubs_page')).toBe('2');
    const hrefs = () => screen.getAllByRole('link').map((a) => a.getAttribute('href'));
    expect(hrefs()).toContain('/reseller?clubs_page=3');
    expect(hrefs()).toContain('/reseller'); // первая страница клубов — без параметра
    expect(hrefs()).toContain('/reseller?clubs_page=2&earnings_page=2'); // вторая страница начислений сохраняет страницу клубов
  });
});
