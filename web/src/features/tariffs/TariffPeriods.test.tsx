import { fireEvent, render, screen } from '@testing-library/react';
import { useState } from 'react';
import { describe, expect, it } from 'vitest';
import { TariffPeriods } from './TariffPeriods';
import type { WeeklyPeriod } from './api';

function Harness({ initial }: { initial: WeeklyPeriod[] }) {
  const [periods, setPeriods] = useState(initial);
  return (
    <>
      <TariffPeriods field="sale_periods" periods={periods} onChange={setPeriods} />
      <output data-testid="value">{JSON.stringify(periods)}</output>
    </>
  );
}

describe('периоды тарифа (tariffs/periods.blade.php)', () => {
  it('«+» добавляет строку на день позже, корзина не удаляет последнюю строку', () => {
    render(<Harness initial={[{ start_day: 'fri', start_time: '22:00', end_day: 'sat', end_time: '08:00' }]} />);
    fireEvent.click(screen.getAllByLabelText('Добавить период')[0]!);
    expect(JSON.parse(screen.getByTestId('value').textContent ?? '')).toEqual([
      { start_day: 'fri', start_time: '22:00', end_day: 'sat', end_time: '08:00' },
      { start_day: 'sat', start_time: '22:00', end_day: 'sun', end_time: '08:00' },
    ]);
    expect(screen.getAllByRole('combobox')[0]).toHaveAttribute('name', 'sale_periods[0][start_day]');
    fireEvent.click(screen.getAllByLabelText('Удалить период')[0]!);
    fireEvent.click(screen.getAllByLabelText('Удалить период')[0]!);
    expect(JSON.parse(screen.getByTestId('value').textContent ?? '')).toHaveLength(1);
  });
});
