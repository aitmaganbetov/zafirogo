import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { session } from '../../auth/session';
import { apiError, mockApi, renderPage, signIn } from '../../test/pages';
import { ClubSettingsPage } from './ClubSettingsPage';
import { ProfilePage } from './ProfilePage';
import type { ClubProfilePage, ClubSettingsPage as ClubSettings } from './api';

// Каркас настроек отмечает открытый раздел по адресу страницы (useLocation роутера).
let pathname = '/settings/profile';
vi.mock('@tanstack/react-router', async (original) => ({
  ...(await original<typeof import('@tanstack/react-router')>()),
  useLocation: () => ({ pathname }),
}));

/*
 * Настройки → Профиль клуба (settings/profile.blade.php) и карточка «Город и адрес» на «Настройка клуба»
 * (settings/club.blade.php) — тема В, docs/upstream-2026-11.md; сценарии GS-566, GS-570.
 */

const profile = (extra: Partial<ClubProfilePage> = {}): ClubProfilePage => ({
  profile: { description: 'Лучший клуб', phone: '+7 700 000 00 00', hours: 'Круглосуточно', instagram: 'asia.game' },
  photos: [{ id: 11, url: '/club-photos/1/aaaa.png' }, { id: 12, url: '/club-photos/1/bbbb.jpg' }],
  photo_limit: 10,
  zones: [
    { id: 5, name: 'VIP', description: 'Тихая комната', computers_count: 2, price_per_hour: 80050, devices: { monitor: 'ASUS 240 Гц' }, detected: { cpu: 'Intel Core i5-11400F', ram: '16 ГБ' } },
    { id: 6, name: 'Main', description: null, computers_count: 0, price_per_hour: null, devices: {}, detected: {} },
  ],
  device_fields: [
    { key: 'cpu', label: 'Процессор' }, { key: 'gpu', label: 'Видеокарта' }, { key: 'ram', label: 'Оперативная память' },
    { key: 'monitor', label: 'Монитор' }, { key: 'keyboard', label: 'Клавиатура' }, { key: 'mouse', label: 'Мышь' },
    { key: 'headset', label: 'Гарнитура' }, { key: 'chair', label: 'Кресло' },
  ],
  ...extra,
});

const settings = (extra: Partial<ClubSettings> = {}): ClubSettings => ({
  club_name: 'Zafiro Arena',
  settings: {
    club: {
      session_idle_timeout: 15, pc_action_after_session_end: 'none', pc_restart_after_session_end: 0.5,
      session_auto_terminate_when_pc_unavailable: 5, booking_interval: 30, max_cashiers_on_shift: 0, max_operators_on_shift: 0,
      agent_idle_logout_minutes: 0, agent_shop_enabled: true, agent_auto_update: true, agent_shell_theme: 'standard',
    },
    finances: {
      currency: 'KZT', loyalty_enabled: false, loyalty_levels: [], topup_bonus_enabled: false, topup_bonus_levels: [], discount_scope: 'all',
      bonus_max_percent: 100, allow_mixed_payment: true, allow_whole_balance: false, auto_billing: false, auto_billing_pause_minutes: 0,
    },
    guests: { show_qr_code_on_login: false, allow_guest_self_transfer: false, allow_adding_friends: false, allow_balance_transfer_to_friends: false, guest_mode_enabled: true },
  },
  timezone: 'Asia/Almaty', timezones_kazakhstan: [{ id: 'Asia/Almaty', label: '(UTC+05:00) Asia/Almaty' }], timezones_all: [],
  club_time: '15.10.2026 12:00', agent_pin_set_at: null, agent_staff_minutes: 15, can_manage_agent_pin: true, logo_url: null, signin_image_url: null,
  city: null, address: null,
  cities: [{ key: 'aktau', name: 'Актау' }, { key: 'aktobe', name: 'Актобе' }, { key: 'almaty', name: 'Алматы' }],
  shell_themes: [],
  ...extra,
} as ClubSettings);

describe('Профиль клуба', () => {
  beforeEach(() => {
    pathname = '/settings/profile';
  });
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
    session.clear();
  });

  it('о клубе: значения профиля, «@» у Instagram, сохранение формы', async () => {
    signIn('owner', ['settings.view', 'settings.update']);
    const api = mockApi({ 'GET /api/settings/profile': profile(), 'PUT /api/settings/profile': { message: 'Описание клуба сохранено.' } });
    renderPage(<ProfilePage />);
    const about = (await screen.findByRole('heading', { name: 'О клубе' })).closest('section') as HTMLElement;
    expect(about.id).toBe('club-about');
    expect(within(about).getByText('Описание').nextElementSibling).toHaveValue('Лучший клуб');
    const instagram = within(about).getByPlaceholderText('@club') as HTMLInputElement;
    expect(instagram.value).toBe('@asia.game');
    expect(instagram.maxLength).toBe(64);
    fireEvent.change(within(about).getByPlaceholderText('+7 700 000 00 00'), { target: { value: '+7 701 111 22 33' } });
    fireEvent.click(within(about).getByRole('button', { name: 'Сохранить' }));
    await waitFor(() => expect(api.of('PUT', '/api/settings/profile')).toHaveLength(1));
    expect(api.of('PUT', '/api/settings/profile')[0]?.body).toEqual({ description: 'Лучший клуб', phone: '+7 701 111 22 33', hours: 'Круглосуточно', instagram: '@asia.game' });
    expect(await screen.findByText('Описание клуба сохранено.')).toBeInTheDocument();
  });

  it('о клубе: ошибка поля показывается над формой', async () => {
    signIn('owner', ['settings.view', 'settings.update']);
    mockApi({
      'GET /api/settings/profile': profile(),
      'PUT /api/settings/profile': () => apiError(422, { error: 'validation_failed', message: 'x', errors: { phone: ['Телефон — только цифры, пробелы, скобки, «+» и «-».'] } }),
    });
    renderPage(<ProfilePage />);
    const about = (await screen.findByRole('heading', { name: 'О клубе' })).closest('section') as HTMLElement;
    fireEvent.click(within(about).getByRole('button', { name: 'Сохранить' }));
    expect(await within(about).findByRole('alert')).toHaveTextContent('Телефон — только цифры, пробелы, скобки, «+» и «-».');
  });

  it('фото: первое — обложка, «Сделать обложкой», удаление с подтверждением, загрузка формой photos[]', async () => {
    signIn('owner', ['settings.view', 'settings.update']);
    const api = mockApi({
      'GET /api/settings/profile': profile(),
      'POST /api/settings/profile/photos/12/cover': { message: 'Обложка клуба изменена.' },
      'DELETE /api/settings/profile/photos/11': { message: 'Фото удалено.' },
      'POST /api/settings/profile/photos': { message: 'Фото добавлены.' },
    });
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false);
    renderPage(<ProfilePage />);
    const card = (await screen.findByRole('heading', { name: 'Фотографии' })).closest('section') as HTMLElement;
    expect(within(card).getByText('До 10 фото зала, зон и входа: JPG, PNG или WebP, до 3 МБ. Первое фото — обложка клуба в списке.')).toBeInTheDocument();
    const items = within(card).getAllByRole('listitem');
    expect(items).toHaveLength(2);
    expect(within(items[0] as HTMLElement).getByText('Обложка')).toBeInTheDocument();
    expect(within(items[0] as HTMLElement).queryByRole('button', { name: 'Сделать обложкой' })).toBeNull();
    expect(within(items[0] as HTMLElement).getByAltText('Фото клуба')).toHaveAttribute('src', '/club-photos/1/aaaa.png');

    fireEvent.click(within(items[1] as HTMLElement).getByRole('button', { name: 'Сделать обложкой' }));
    await waitFor(() => expect(api.of('POST', '/api/settings/profile/photos/12/cover')).toHaveLength(1));

    // «Удалить фото?» — отказ ничего не удаляет.
    fireEvent.click(within(items[0] as HTMLElement).getByRole('button', { name: 'Удалить' }));
    expect(confirm).toHaveBeenCalledWith('Удалить фото?');
    expect(api.of('DELETE', '/api/settings/profile/photos/11')).toHaveLength(0);
    confirm.mockReturnValue(true);
    fireEvent.click(within(items[0] as HTMLElement).getByRole('button', { name: 'Удалить' }));
    await waitFor(() => expect(api.of('DELETE', '/api/settings/profile/photos/11')).toHaveLength(1));

    const file = card.querySelector('input[type=file]') as HTMLInputElement;
    expect(file.name).toBe('photos[]');
    expect(file.multiple).toBe(true);
    expect(file.accept).toBe('image/jpeg,image/png,image/webp');
    expect(within(card).getByLabelText('Фотографии')).toBe(file); // UI-6: имя поля — заголовок карточки
    fireEvent.submit(file.closest('form') as HTMLFormElement);
    await waitFor(() => expect(api.of('POST', '/api/settings/profile/photos')).toHaveLength(1));
  });

  it('фото: при 10 фото формы загрузки нет; ошибка файла — над списком', async () => {
    signIn('owner', ['settings.view', 'settings.update']);
    mockApi({ 'GET /api/settings/profile': profile({ photos: Array.from({ length: 10 }, (_, i) => ({ id: i + 1, url: `/club-photos/1/${i}.png` })) }) });
    const { unmount } = renderPage(<ProfilePage />);
    const card = (await screen.findByRole('heading', { name: 'Фотографии' })).closest('section') as HTMLElement;
    expect(card.querySelector('input[type=file]')).toBeNull();
    unmount();

    mockApi({
      'GET /api/settings/profile': profile({ photos: [] }),
      'POST /api/settings/profile/photos': () => apiError(422, { error: 'validation_failed', message: 'x', errors: { 'photos.1': ['Фото — не больше 3 МБ.'] } }),
    });
    renderPage(<ProfilePage />);
    const empty = (await screen.findByRole('heading', { name: 'Фотографии' })).closest('section') as HTMLElement;
    expect(within(empty).queryByRole('list')).toBeNull();
    fireEvent.submit(empty.querySelector('form') as HTMLFormElement);
    expect(await within(empty).findByRole('alert')).toHaveTextContent('Фото — не больше 3 МБ.');
  });

  it('зоны: число ПК и цена, подсказки из данных агентов, сохранение оборудования', async () => {
    signIn('owner', ['settings.view', 'settings.update']);
    const api = mockApi({ 'GET /api/settings/profile': profile(), 'PUT /api/settings/profile/zones/5': { message: 'Зона VIP сохранена.' } });
    renderPage(<ProfilePage />);
    const vip = (await screen.findByRole('heading', { name: 'VIP' })).closest('form') as HTMLFormElement;
    expect(vip.id).toBe('zone-5');
    // number_format((float) 800.50, 0, '.', ' ') = 801.
    expect(within(vip).getByText('2 ПК · 801 ₸/час')).toBeInTheDocument();
    const cpu = within(vip).getByText('Процессор').nextElementSibling as HTMLInputElement;
    expect(cpu.name).toBe('devices[cpu]');
    expect(cpu.placeholder).toBe('Intel Core i5-11400F');
    expect(cpu.value).toBe('');
    expect((within(vip).getByText('Оперативная память').nextElementSibling as HTMLInputElement).placeholder).toBe('16 ГБ');
    expect(within(vip).getByText('Монитор').nextElementSibling).toHaveValue('ASUS 240 Гц');
    expect(within(vip).getByText('Описание зоны').nextElementSibling).toHaveValue('Тихая комната');
    const main = screen.getByRole('heading', { name: 'Main' }).closest('form') as HTMLFormElement;
    expect(within(main).getByText('0 ПК · 0 ₸/час')).toBeInTheDocument();

    fireEvent.change(within(vip).getByText('Мышь').nextElementSibling as HTMLInputElement, { target: { value: 'Logitech G102' } });
    fireEvent.click(within(vip).getByRole('button', { name: 'Сохранить зону' }));
    await waitFor(() => expect(api.of('PUT', '/api/settings/profile/zones/5')).toHaveLength(1));
    expect(api.of('PUT', '/api/settings/profile/zones/5')[0]?.body).toEqual({
      description: 'Тихая комната',
      devices: { cpu: '', gpu: '', ram: '', monitor: 'ASUS 240 Гц', keyboard: '', mouse: 'Logitech G102', headset: '', chair: '' },
    });
    expect(await screen.findByText('Зона VIP сохранена.')).toBeInTheDocument();
  });

  it('меню настроек: пункт «Профиль клуба» после «Настройка клуба», под ним — якоря страницы', async () => {
    signIn('owner', ['settings.view']);
    mockApi({ 'GET /api/settings/profile': profile() });
    renderPage(<ProfilePage />);
    const link = await screen.findByRole('link', { name: 'Профиль клуба' });
    expect(link).toHaveAttribute('href', '/settings/profile');
    expect(link.className).toContain('brand-tab');
    expect(link.querySelector('i')?.className).toBe('fas fa-mobile-screen w-4');
    expect(link.previousElementSibling).toHaveTextContent('Настройка клуба');
    const anchors = link.nextElementSibling as HTMLElement;
    expect(anchors).toHaveAttribute('data-settings-anchors');
    expect([...anchors.querySelectorAll('a')].map((a) => [a.getAttribute('href'), a.textContent])).toEqual([
      ['#club-about', 'О клубе'], ['#club-photos', 'Фотографии'], ['#club-zones', 'Зоны и оборудование'],
    ]);
  });

  it('зон нет — подсказка создать их на карте клуба', async () => {
    signIn('owner', ['settings.view']);
    mockApi({ 'GET /api/settings/profile': profile({ zones: [] }) });
    renderPage(<ProfilePage />);
    expect(await screen.findByText('Зон пока нет: создайте их на карте клуба.')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Карта клуба' })).toHaveAttribute('href', '/club-map');
  });

  it('в режиме просмотра суперадмином формы ничего не отправляют', async () => {
    signIn('owner', ['settings.view', 'settings.update'], { read_only: true });
    const api = mockApi({ 'GET /api/settings/profile': profile() });
    renderPage(<ProfilePage />);
    const about = (await screen.findByRole('heading', { name: 'О клубе' })).closest('section') as HTMLElement;
    fireEvent.click(within(about).getByRole('button', { name: 'Сохранить' }));
    fireEvent.click(screen.getAllByRole('button', { name: 'Сохранить зону' })[0] as HTMLElement);
    expect(api.calls.filter((c) => c.method !== 'GET')).toHaveLength(0);
  });
});

describe('Настройка клуба: город и адрес', () => {
  beforeEach(() => {
    pathname = '/settings/club';
  });
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
    session.clear();
  });

  it('GS-566: карточка «Город и адрес» — список городов, «Не указан», сохранение', async () => {
    signIn('owner', ['settings.view', 'settings.update']);
    const api = mockApi({ 'GET /api/settings': settings(), 'PUT /api/settings/location': { message: 'Город и адрес сохранены.' } });
    renderPage(<ClubSettingsPage />);
    const card = (await screen.findByRole('heading', { name: 'Город и адрес' })).closest('section') as HTMLElement;
    expect(card.id).toBe('location');
    // Якорь карточки — в меню страницы, после «Картинка экрана входа»; за ним — «Реселлер» (тема Д) и «Часовой пояс».
    const anchors = [...document.querySelectorAll('[data-settings-anchors] a')].map((a) => a.getAttribute('href'));
    expect(anchors.slice(anchors.indexOf('#signin-image'), anchors.indexOf('#timezone') + 1)).toEqual(['#signin-image', '#location', '#reseller', '#timezone']);
    const select = within(card).getByText('Город').nextElementSibling as HTMLSelectElement;
    expect([...select.options].map((o) => [o.value, o.text])).toEqual([['', 'Не указан'], ['aktau', 'Актау'], ['aktobe', 'Актобе'], ['almaty', 'Алматы']]);
    expect(select.value).toBe('');
    const address = within(card).getByPlaceholderText('ул. Абая, 10') as HTMLInputElement;
    expect(address.maxLength).toBe(255);
    fireEvent.change(select, { target: { value: 'aktobe' } });
    fireEvent.change(address, { target: { value: 'пр. Абилкайыр хана, 1' } });
    fireEvent.click(within(card).getByRole('button', { name: 'Сохранить' }));
    await waitFor(() => expect(api.of('PUT', '/api/settings/location')).toHaveLength(1));
    expect(api.of('PUT', '/api/settings/location')[0]?.body).toEqual({ city: 'aktobe', address: 'пр. Абилкайыр хана, 1' });
    expect(await screen.findByText('Город и адрес сохранены.')).toBeInTheDocument();
  });

  it('город клуба выбран; ошибка города — в карточке', async () => {
    signIn('owner', ['settings.view', 'settings.update']);
    mockApi({
      'GET /api/settings': settings({ city: 'almaty', address: 'ул. Абая, 1' }),
      'PUT /api/settings/location': () => apiError(422, { error: 'validation_failed', message: 'x', errors: { city: ['Выбранное значение поля «city» некорректно.'] } }),
    });
    renderPage(<ClubSettingsPage />);
    const card = (await screen.findByRole('heading', { name: 'Город и адрес' })).closest('section') as HTMLElement;
    expect(within(card).getByText('Город').nextElementSibling).toHaveValue('almaty');
    expect(within(card).getByPlaceholderText('ул. Абая, 10')).toHaveValue('ул. Абая, 1');
    fireEvent.click(within(card).getByRole('button', { name: 'Сохранить' }));
    expect(await within(card).findByRole('alert')).toHaveTextContent('Выбранное значение поля «city» некорректно.');
  });
});
