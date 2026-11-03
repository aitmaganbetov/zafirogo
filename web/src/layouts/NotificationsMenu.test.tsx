import { render, screen, waitFor } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { NotificationsMenu } from './NotificationsMenu';
import type { NotificationsPayload } from './types';

describe('колокольчик: пункт «Проблемы с ПК» (S7-H-11)', () => {
  it('показывает проблемы ПК как остальные пункты: иконка, заголовок, список проблем, переход на карту к ПК', async () => {
    const payload: NotificationsPayload = {
      unread_count: 1,
      items: [
        {
          id: 'computer-health-7-1730000000000000',
          url: '/club-map?computer=7',
          icon: 'fa-triangle-exclamation',
          title: 'Проблемы с ПК PC-07',
          body: 'Перегрев CPU (92 °C), Мышь не подключена',
          unread: true,
          at: '2026-11-03T10:00:00Z',
          at_human: '1 минуту назад',
        },
      ],
    };
    render(<NotificationsMenu source={{ load: async () => payload, markSeen: async () => undefined }} />);
    const title = await screen.findByText('Проблемы с ПК PC-07');
    const link = title.closest('a');
    expect(link?.getAttribute('href')).toBe('/club-map?computer=7');
    expect(link?.className).toBe('admin-notification unread');
    expect(link?.querySelector('i')?.className).toBe('fas fa-triangle-exclamation');
    expect(screen.getByText('Перегрев CPU (92 °C), Мышь не подключена')).toBeTruthy();
    await waitFor(() => expect(document.querySelector('[data-notifications-dot]')?.hasAttribute('hidden')).toBe(false));
  });
});
