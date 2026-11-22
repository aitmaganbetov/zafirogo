import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { session } from '../../auth/session';
import { mockApi, renderPage, signIn } from '../../test/pages';
import { ClubSettingsPage } from './ClubSettingsPage';
import type { ClubSettingsPage as ClubSettings } from './api';

vi.mock('@tanstack/react-router', async (original) => ({
  ...(await original<typeof import('@tanstack/react-router')>()),
  useLocation: () => ({ pathname: '/settings/club' }),
}));

/*
 * «Тема оформления на ПК» в карточке «Агент на ПК» (settings/club.blade.php, коммит старого e38b764;
 * tests/Feature/ShellThemeTest.php): по умолчанию отмечена стандартная, выбранная тема уходит в секцию club.
 */

const themes: ClubSettings['shell_themes'] = [
  { key: 'standard', accent: '#8b5cf6', accent_2: '#5eb7ff', bg_1: '#071018' },
  { key: 'blue', accent: '#2563eb', accent_2: '#38bdf8', bg_1: '#071226' },
  { key: 'green', accent: '#16a34a', accent_2: '#4ade80', bg_1: '#061a0e' },
  { key: 'red', accent: '#dc2626', accent_2: '#fb7185', bg_1: '#1f0709' },
  { key: 'orange', accent: '#ea580c', accent_2: '#fbbf24', bg_1: '#1f1005' },
  { key: 'teal', accent: '#0d9488', accent_2: '#2dd4bf', bg_1: '#051c1c' },
  { key: 'pink', accent: '#db2777', accent_2: '#f472b6', bg_1: '#1f0716' },
];

const page = (theme: string, shellThemes: ClubSettings['shell_themes'] = themes): ClubSettings => ({
  club_name: 'Zafiro Arena',
  settings: {
    club: {
      session_idle_timeout: 15, pc_action_after_session_end: 'none', pc_restart_after_session_end: 0.5,
      session_auto_terminate_when_pc_unavailable: 5, booking_interval: 30, max_cashiers_on_shift: 0, max_operators_on_shift: 0,
      agent_idle_logout_minutes: 5, agent_shop_enabled: true, agent_auto_update: true, app_booking_enabled: false, app_booking_cancel: true,
      agent_shell_theme: theme,
    },
    finances: {
      currency: 'KZT', loyalty_enabled: false, loyalty_levels: [], topup_bonus_enabled: false, topup_bonus_levels: [], discount_scope: 'all',
      bonus_max_percent: 100, allow_mixed_payment: false, allow_whole_balance: false, auto_billing: false, auto_billing_pause_minutes: 5,
    },
    guests: { show_qr_code_on_login: false, allow_guest_self_transfer: false, allow_adding_friends: false, allow_balance_transfer_to_friends: false, guest_mode_enabled: true },
  },
  timezone: 'Asia/Almaty', timezones_kazakhstan: [], timezones_all: [], club_time: '16.11.2026 12:00', agent_pin_set_at: null,
  agent_staff_minutes: 15, can_manage_agent_pin: true, logo_url: null, signin_image_url: null, city: null, address: null, cities: [],
  shell_themes: shellThemes,
});

describe('Настройка клуба: тема оформления на ПК', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    session.clear();
  });

  const fieldset = async () => (await screen.findByRole('group', { name: 'Тема оформления на ПК' })) as HTMLElement;

  it('семь тем со свотчами, по умолчанию — стандартная; выбранная уходит с секцией club', async () => {
    signIn('owner', ['settings.view', 'settings.update']);
    const api = mockApi({ 'GET /api/settings': page('standard'), 'PUT /api/settings/sections/club': { message: 'Настройки сохранены' } });
    renderPage(<ClubSettingsPage />);
    const group = await fieldset();
    expect(within(group).getByText('Цвета экрана гостя и экрана входа на ПК клуба. Агент применит тему в течение минуты.')).toBeInTheDocument();
    const radios = within(group).getAllByRole('radio') as HTMLInputElement[];
    expect(radios.map((r) => r.value)).toEqual(['standard', 'blue', 'green', 'red', 'orange', 'teal', 'pink']);
    expect(radios.filter((r) => r.checked).map((r) => r.value)).toEqual(['standard']);
    const red = within(group).getByLabelText('Красная') as HTMLInputElement;
    // Свотч: фон — bg_1, полоски — accent и accent_2 (ShellThemes::swatch()).
    const swatch = red.nextElementSibling as HTMLElement;
    expect(swatch).toHaveAttribute('aria-hidden', 'true');
    expect(swatch.style.background).toBe('rgb(31, 7, 9)');
    expect((swatch.children[0] as HTMLElement).style.background).toBe('rgb(220, 38, 38)');
    expect(within(group).getByLabelText('Стандартная (фиолетовая)')).toBeChecked();

    fireEvent.click(red);
    fireEvent.click(screen.getAllByRole('button', { name: 'Сохранить' })[0] as HTMLElement);
    await waitFor(() => expect(api.of('PUT', '/api/settings/sections/club')).toHaveLength(1));
    expect(api.of('PUT', '/api/settings/sections/club')[0]?.body).toMatchObject({ agent_shell_theme: 'red' });
  });

  // Ревью 22.11.2026: сервер уже знает тему, фронт названия ещё не знает — вариант с ключом, отмечен и сохраняется.
  it('тема сервера без названия во фронте: вариант с ключом, отмечен, уходит в форму', async () => {
    signIn('owner', ['settings.view', 'settings.update']);
    const api = mockApi({
      'GET /api/settings': page('gold', [...themes, { key: 'gold', accent: '#ca8a04', accent_2: '#fde047', bg_1: '#1c1503' }]),
      'PUT /api/settings/sections/club': { message: 'Настройки сохранены' },
    });
    renderPage(<ClubSettingsPage />);
    const group = await fieldset();
    const radios = within(group).getAllByRole('radio') as HTMLInputElement[];
    expect(radios).toHaveLength(8);
    expect(radios.filter((r) => r.checked).map((r) => r.value)).toEqual(['gold']);
    expect(within(group).getByLabelText('gold')).toBeChecked();
    fireEvent.click(screen.getAllByRole('button', { name: 'Сохранить' })[0] as HTMLElement);
    await waitFor(() => expect(api.of('PUT', '/api/settings/sections/club')).toHaveLength(1));
    expect(api.of('PUT', '/api/settings/sections/club')[0]?.body).toMatchObject({ agent_shell_theme: 'gold' });
  });

  it('сохранённая тема отмечена; неизвестная — не отмечена ни одна и в форму не уходит', async () => {
    signIn('owner', ['settings.view', 'settings.update']);
    mockApi({ 'GET /api/settings': page('teal') });
    const view = renderPage(<ClubSettingsPage />);
    expect(within(await fieldset()).getByLabelText('Бирюзовая')).toBeChecked();
    view.unmount();

    const api = mockApi({ 'GET /api/settings': page('purple'), 'PUT /api/settings/sections/club': { message: 'Настройки сохранены' } });
    renderPage(<ClubSettingsPage />);
    const radios = within(await fieldset()).getAllByRole('radio') as HTMLInputElement[];
    expect(radios.some((r) => r.checked)).toBe(false);
    fireEvent.click(screen.getAllByRole('button', { name: 'Сохранить' })[0] as HTMLElement);
    await waitFor(() => expect(api.of('PUT', '/api/settings/sections/club')).toHaveLength(1));
    expect(api.of('PUT', '/api/settings/sections/club')[0]?.body).not.toHaveProperty('agent_shell_theme');
  });
});
