import { screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { session } from '../../auth/session';
import { mockApi, renderPage, signIn } from '../../test/pages';
import { dashboardFixture } from './dashboard.fixture';
import { DashboardPage } from './DashboardPage';

/*
 * Аудит AC-3: строка «Сверка балансов с журналом» в списке проверок экрана «Сервер» появляется, только когда
 * фоновая сверка нашла расхождения или остановилась; в норме её нет (вид экрана — как в Blade).
 */

vi.mock('@tanstack/react-router', async (original) => ({
  ...(await original<typeof import('@tanstack/react-router')>()),
  useNavigate: () => vi.fn(async () => undefined),
}));

const LABEL = 'Сверка балансов с журналом';

describe('экран «Сервер»: сверка балансов с журналом операций', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    session.clear();
  });

  const show = async (ledger_reconcile: typeof dashboardFixture.ledger_reconcile) => {
    signIn('', [], { user: { id: 1, name: 'Супер Админ', phone: '+77010000001', is_superadmin: true }, club: null } as never);
    mockApi({ 'GET /api/superadmin/dashboard': { ...dashboardFixture, ledger_reconcile } });
    renderPage(<DashboardPage search={{}} />);
    await screen.findByText('Последний бэкап');
  };

  it('сверка прошла, расхождений нет — строки нет', async () => {
    await show({ open_findings: 0, stale: false, at_human: '5 минут назад' });
    expect(screen.queryByText(LABEL)).toBeNull();
  });

  it('сверка ещё не проходила (сервер только запущен) — строки нет', async () => {
    await show({ open_findings: 0, stale: false, at_human: null });
    expect(screen.queryByText(LABEL)).toBeNull();
  });

  it('есть расхождения — красная строка с числом', async () => {
    await show({ open_findings: 3, stale: false, at_human: '5 минут назад' });
    const value = screen.getByText(LABEL).nextElementSibling;
    expect(value).toHaveTextContent('Расхождений: 3');
    expect(value).toHaveClass('text-red-600');
  });

  it('сверка остановилась — красная строка с давностью последней', async () => {
    await show({ open_findings: 0, stale: true, at_human: '5 часов назад' });
    const value = screen.getByText(LABEL).nextElementSibling;
    expect(value).toHaveTextContent('Не выполняется, последняя — 5 часов назад');
    expect(value).toHaveClass('text-red-600');
  });

  it('расхождения важнее давности', async () => {
    await show({ open_findings: 1, stale: true, at_human: '5 часов назад' });
    expect(screen.getByText(LABEL).nextElementSibling).toHaveTextContent('Расхождений: 1');
  });
});

/*
 * Аудит CODE-3: журнал ошибок, не подключённый к экрану (log_errors: null), не выдаётся за «ошибок нет»; сроки
 * сертификатов — как в Blade: дни (меньше 14 — красным), «Не удалось проверить», «Ещё не проверялся».
 */
describe('экран «Сервер»: журнал ошибок и сертификаты', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    session.clear();
  });

  const show = async (patch: Partial<typeof dashboardFixture>) => {
    signIn('', [], { user: { id: 1, name: 'Супер Админ', phone: '+77010000001', is_superadmin: true }, club: null } as never);
    mockApi({ 'GET /api/superadmin/dashboard': { ...dashboardFixture, ...patch } });
    renderPage(<DashboardPage search={{}} />);
    await screen.findByText('Последний бэкап');
  };

  it('журнал не подключён — честная надпись, а не «ошибок нет»', async () => {
    await show({ log_errors: null });
    expect(screen.getByText(/Журнал ошибок к этому экрану не подключён/)).toBeInTheDocument();
    expect(screen.queryByText('Ошибок в логе нет.')).toBeNull();
  });

  it('журнал подключён и пуст — «Ошибок в логе нет.»', async () => {
    await show({ log_errors: [] });
    expect(screen.getByText('Ошибок в логе нет.')).toBeInTheDocument();
  });

  it('сроки сертификатов', async () => {
    await show({
      certificate_hosts: [
        { host: 'zafiro.kz', days: 40, checked: true },
        { host: 'admin.zafiro.kz', days: 3, checked: true },
        { host: 'wiki.zafiro.kz', days: null, checked: true },
        { host: 'new.zafiro.kz', days: null, checked: false },
      ],
    });
    const value = (host: string) => screen.getByText(`SSL ${host}`).nextElementSibling;
    expect(value('zafiro.kz')).not.toHaveClass('text-red-600');
    expect(value('admin.zafiro.kz')).toHaveClass('text-red-600');
    expect(value('wiki.zafiro.kz')).toHaveTextContent('Не удалось проверить');
    expect(value('new.zafiro.kz')).toHaveTextContent('Ещё не проверялся');
  });
});
