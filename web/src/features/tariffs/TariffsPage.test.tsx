import { fireEvent, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { session } from '../../auth/session';
import { mockApi, renderPage, signIn, stubDialog } from '../../test/pages';
import { tariffsPageQuery } from './api';
import { TariffsPage } from './TariffsPage';

/* FE-11: «Зоны и тарифы» — цены из тиынов (час и минута зоны, тариф), стоимость в запросе — целые тиыны. */

vi.mock('@tanstack/react-router', async (original) => ({
  ...(await original<typeof import('@tanstack/react-router')>()),
  useNavigate: () => vi.fn(async () => undefined),
}));

const always = [{ start_day: 'mon', start_time: '00:00', end_day: 'sun', end_time: '23:59' }];
const tariff = {
  id: 4, name: 'Ночь', duration_minutes: 480, price_tiyn: 250050, currency: 'KZT', is_active: true, payment_method: 'main_or_bonus',
  discount_enabled: true, usage_limit_enabled: false, quantity_limit_enabled: false, sold: 0, zone_ids: [1], zone_names: ['VIP'],
  sale_periods: always, validity_periods: always, on_sale: true, in_validity_period: true,
};
const page = {
  zones: [
    { id: 1, name: 'VIP', price_per_hour_tiyn: 100000 },
    { id: 2, name: 'Общий зал', price_per_hour_tiyn: 50050 },
    { id: 3, name: 'Лаунж', price_per_hour_tiyn: null },
  ],
  tariffs: [tariff],
  currency: 'KZT',
  can_delete: true,
};
const RIGHTS = ['tariffs.view', 'tariffs.create', 'tariffs.update', 'tariffs.delete'];

describe('тарифы: деньги', () => {
  beforeEach(() => {
    stubDialog();
    signIn('owner', RIGHTS);
  });
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
    session.clear();
  });

  const field = (id: string) => document.getElementById(id) as HTMLInputElement;
  const form = () => document.querySelector('#tariff-dialog form') as HTMLFormElement;

  it('цена часа, цена минуты (округление до тиына, половина вверх) и цена тарифа — из целых тиынов', async () => {
    mockApi({ 'GET /api/tariffs/page': page });
    renderPage(<TariffsPage />);
    await screen.findByText('Ночь');
    expect(screen.getByText('1 000,00 KZT')).toBeInTheDocument(); // VIP, час
    expect(screen.getByText('16,67 KZT')).toBeInTheDocument(); // 100 000 / 60 = 1 666,67 тиына → 1 667
    expect(screen.getByText('500,50 KZT')).toBeInTheDocument();
    expect(screen.getByText('8,34 KZT')).toBeInTheDocument(); // 50 050 / 60 = 834,17 → 834
    expect(screen.getByText('Не продаётся поминутно')).toBeInTheDocument();
    expect(screen.getByText('2 500,50 KZT')).toBeInTheDocument(); // тариф
  });

  it('новый тариф: «1500.5» → price_tiyn 150 050', async () => {
    const api = mockApi({ 'GET /api/tariffs/page': page, 'POST /api/tariffs': { message: 'Тариф создан.', tariff } });
    renderPage(<TariffsPage />);
    await screen.findByText('Ночь');
    fireEvent.click(document.getElementById('open-tariff-dialog') as HTMLElement);
    fireEvent.change(field('tariff-name'), { target: { value: ' Утро ' } });
    fireEvent.change(field('tariff-duration'), { target: { value: '3' } });
    fireEvent.change(field('tariff-price'), { target: { value: '1500.5' } });
    fireEvent.submit(form());
    await waitFor(() => expect(api.of('POST', '/api/tariffs')).toHaveLength(1));
    expect(api.of('POST', '/api/tariffs')[0]?.body).toMatchObject({
      name: 'Утро', duration_value: 3, duration_unit: 'hours', price_tiyn: 150050, payment_method: 'main_balance_only', is_active: true,
    });
  });

  it('изменение: поле «Стоимость» заполняется из тиынов без потери копеек; сохранение — PATCH с целыми тиынами', async () => {
    const api = mockApi({ 'GET /api/tariffs/page': page, 'PATCH /api/tariffs/4': { message: 'Тариф сохранён.', tariff } });
    // Форма изменения открывается заполненной: данные страницы к первому рендеру уже загрузил loader маршрута.
    renderPage(<TariffsPage editId={4} />, (qc) => qc.setQueryData(tariffsPageQuery().queryKey, page as never));
    await screen.findByText('Ночь');
    expect(field('tariff-price').value).toBe('2500.50');
    fireEvent.change(field('tariff-price'), { target: { value: '2600' } });
    fireEvent.submit(form());
    await waitFor(() => expect(api.of('PATCH', '/api/tariffs/4')).toHaveLength(1));
    expect(api.of('PATCH', '/api/tariffs/4')[0]?.body).toMatchObject({ name: 'Ночь', price_tiyn: 260000, duration_value: 8, duration_unit: 'hours', zone_ids: [1] });
  });

  it('стоимость с тремя знаками — ошибка формы, запрос не уходит (Q-002)', async () => {
    const api = mockApi({ 'GET /api/tariffs/page': page, 'POST /api/tariffs': { message: 'Тариф создан.', tariff } });
    renderPage(<TariffsPage />);
    await screen.findByText('Ночь');
    fireEvent.click(document.getElementById('open-tariff-dialog') as HTMLElement);
    fireEvent.change(field('tariff-name'), { target: { value: 'Утро' } });
    fireEvent.change(field('tariff-price'), { target: { value: '10.005' } });
    fireEvent.submit(form());
    await Promise.resolve();
    expect(api.of('POST', '/api/tariffs')).toHaveLength(0);
    expect(document.querySelector('#tariff-dialog [class*="red"]')).not.toBeNull();
  });

  it('удаление — после подтверждения', async () => {
    const api = mockApi({ 'GET /api/tariffs/page': page, 'DELETE /api/tariffs/4': { message: 'Тариф удалён.' } });
    const confirm = vi.spyOn(window, 'confirm').mockReturnValueOnce(false).mockReturnValueOnce(true);
    renderPage(<TariffsPage />);
    await screen.findByText('Ночь');
    fireEvent.click(screen.getByRole('button', { name: 'Удалить' }));
    expect(api.of('DELETE', '/api/tariffs/4')).toHaveLength(0);
    fireEvent.click(screen.getByRole('button', { name: 'Удалить' }));
    expect(confirm).toHaveBeenCalledTimes(2);
    expect(await screen.findByText('Тариф удалён.')).toBeInTheDocument();
    expect(api.of('DELETE', '/api/tariffs/4')).toHaveLength(1);
  });
});
