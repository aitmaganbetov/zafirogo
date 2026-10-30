import { fireEvent, render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { LineChartView, LogRows, SubscriptionBadgeView } from './components';
import { useSuperadminFlash } from './flash';
import { SuperadminLayout } from './SuperadminLayout';

function FlashButton() {
  const flash = useSuperadminFlash();
  return (
    <button type="button" onClick={() => flash.status('Тариф создан.')}>
      go
    </button>
  );
}

describe('кабинет суперадмина: каркас и общие части', () => {
  it('меню: активный пункт по пути, «Панель своего клуба» только при членстве, flash в layout', () => {
    const { rerender } = render(
      <SuperadminLayout userName="Супер Админ" hasClub={false} path="/superadmin/clubs/5" onLogout={() => undefined}>
        <FlashButton />
      </SuperadminLayout>,
    );
    expect(screen.getByText('Клубы').closest('a')).toHaveClass('sidebar-link active');
    expect(screen.getByText('Сервер').closest('a')).toHaveAttribute('href', '/superadmin');
    expect(screen.queryByText('Панель своего клуба')).toBeNull();
    fireEvent.click(screen.getByText('go'));
    expect(screen.getByRole('status')).toHaveTextContent('Тариф создан.');
    // Другая страница — сообщение уходит.
    rerender(
      <SuperadminLayout userName="Супер Админ" hasClub path="/superadmin/plans" onLogout={() => undefined}>
        <FlashButton />
      </SuperadminLayout>,
    );
    expect(screen.queryByRole('status')).toBeNull();
    expect(screen.getByText('Панель своего клуба')).toBeInTheDocument();
  });

  it('строки журнала: «Система» только в журнале платформы, клуб — ссылка', () => {
    const row = { id: 1, club_id: 7, club_name: 'Arena', user_name: '', system: true, event: 'x', event_label: null, description: 'd', ip_address: '1.2.3.4', at_utc: '15.10.2026 07:00' };
    const { rerender } = render(<LogRows logs={[row]} platform />);
    expect(screen.getByText('Система')).toBeInTheDocument();
    expect(screen.getByText('Arena').closest('a')).toHaveAttribute('href', '/superadmin/clubs/7');
    rerender(<LogRows logs={[]} platform={false} />);
    expect(screen.getByText('Записей нет.')).toBeInTheDocument();
  });

  it('подписка и график', () => {
    render(<SubscriptionBadgeView badge={{ label: 'Истекла', active: false }} />);
    expect(screen.getByText('Истекла')).toHaveClass('bg-red-100');
    const { container } = render(
      <LineChartView
        title="Память"
        unit="%"
        chart={{ max_label: '100', from_label: '14.10 12:00', to_label: '15.10 12:00', lines: [{ label: 'a', translate: false, color: '#7c3aed', segments: ['0,1 600,2', '0,3'] }] }}
      />,
    );
    expect(container.querySelectorAll('polyline')).toHaveLength(2);
    expect(screen.getByText('15.10 12:00')).toBeInTheDocument();
  });
});
