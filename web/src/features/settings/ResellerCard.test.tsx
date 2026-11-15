import { fireEvent, screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { ApiError } from '../../api/http';
import { session } from '../../auth/session';
import { mockApi, renderPage, signIn } from '../../test/pages';
import { ResellerCard } from './ResellerCard';

/* Карточка «Реселлер» настроек клуба (settings/club.blade.php, #reseller; GS-562). */

describe('настройки клуба: карточка «Реселлер»', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    session.clear();
  });
  const show = (routes: Record<string, unknown>, readOnly = false) => {
    signIn('owner', ['settings.view', 'settings.update']);
    const api = mockApi(routes);
    const onDone = vi.fn();
    const onError = vi.fn();
    const view = renderPage(<ResellerCard readOnly={readOnly} onDone={onDone} onError={onError} error={null} />);
    return { api, onDone, onError, ...view };
  };

  it('клуб без реселлера: форма кода, код уходит как введён', async () => {
    const { api, onDone } = show({ 'GET /api/settings/reseller': { reseller: null }, 'POST /api/settings/reseller': { message: 'Клуб привязан к реселлеру Arman.' } });
    expect(await screen.findByText(/Код вводится один раз\./)).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText('Код реселлера'), { target: { value: ' Arman-Games ' } });
    fireEvent.click(screen.getByRole('button', { name: 'Привязать' }));
    await waitFor(() => expect(onDone).toHaveBeenCalledWith('Клуб привязан к реселлеру Arman.'));
    // Регистр и пробелы приводит сервер (Reseller::normalizeCode()).
    expect(api.of('POST', '/api/settings/reseller')[0]?.body).toEqual({ reseller_code: ' Arman-Games ' });
  });

  it('привязанный клуб: реселлер и скидка, формы нет', async () => {
    show({ 'GET /api/settings/reseller': { reseller: { name: 'Arman', code: 'arman-games', discount_percent: 15 } } });
    expect(await screen.findByText(/Клуб подключён через реселлера Arman \(код arman-games\)\. Скидка на подписку: 15%\./)).toBeInTheDocument();
    expect(screen.getByText('Сменить реселлера можно через поддержку Zafiro.')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Привязать' })).not.toBeInTheDocument();
  });

  it('реселлер без скидки (или остановлен): текста о скидке нет', async () => {
    show({ 'GET /api/settings/reseller': { reseller: { name: 'Arman', code: 'arman-games', discount_percent: 0 } } });
    expect(await screen.findByText('Клуб подключён через реселлера Arman (код arman-games).')).toBeInTheDocument();
    expect(screen.queryByText(/Скидка на подписку/)).not.toBeInTheDocument();
  });

  it('режим просмотра форму не отправляет', async () => {
    const { api, onError } = show({ 'GET /api/settings/reseller': { reseller: null }, 'POST /api/settings/reseller': { message: 'нельзя' } }, true);
    await screen.findByLabelText('Код реселлера');
    fireEvent.change(screen.getByLabelText('Код реселлера'), { target: { value: 'nobody' } });
    fireEvent.click(screen.getByRole('button', { name: 'Привязать' }));
    expect(api.of('POST', '/api/settings/reseller')).toHaveLength(0);
    expect(onError).not.toHaveBeenCalled();
  });

  it('текст ошибки поля reseller_code показывается над формой', async () => {
    signIn('owner', ['settings.update']);
    mockApi({ 'GET /api/settings/reseller': { reseller: null } });
    const e = new ApiError(422, { error: 'validation_failed', message: 'x', errors: { reseller_code: ['Реселлер с таким кодом не найден.'] } });
    renderPage(<ResellerCard readOnly={false} onDone={vi.fn()} onError={vi.fn()} error={e} />);
    expect(await screen.findByRole('alert')).toHaveTextContent('Реселлер с таким кодом не найден.');
  });
});
