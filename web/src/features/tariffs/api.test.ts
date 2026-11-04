import { describe, expect, it } from 'vitest';
import { nextPeriod, parseTariffsSearch, perMinuteTiyn, priceInput, splitMinutes } from './api';

describe('тарифы: формулы формы и списка', () => {
  it('splitMinutes — самая крупная целая единица (TariffController::splitMinutes)', () => {
    expect(splitMinutes(2880)).toEqual([2, 'days']);
    expect(splitMinutes(120)).toEqual([2, 'hours']);
    expect(splitMinutes(90)).toEqual([90, 'minutes']);
    expect(splitMinutes(0)).toEqual([0, 'minutes']);
  });

  it('цена минуты — number_format(price / 60, 2): до тиына, половина вверх, без float', () => {
    expect(perMinuteTiyn(60000)).toBe(1000); // 600,00 → 10,00
    expect(perMinuteTiyn(100000)).toBe(1667); // 1000,00 / 60 = 16,666… → 16,67
    expect(perMinuteTiyn(30)).toBe(1); // 0,005 → 0,01
    expect(perMinuteTiyn(29)).toBe(0);
  });

  it('значение поля «Стоимость» — decimal(12,2) строкой', () => {
    expect(priceInput(60000)).toBe('600.00');
    expect(priceInput(5)).toBe('0.05');
  });

  it('новая строка периода — последняя на день позже (вс → пн)', () => {
    expect(nextPeriod({ start_day: 'mon', start_time: '10:00', end_day: 'mon', end_time: '17:00' })).toEqual({
      start_day: 'tue',
      start_time: '10:00',
      end_day: 'tue',
      end_time: '17:00',
    });
    expect(nextPeriod({ start_day: 'sun', start_time: '22:00', end_day: 'mon', end_time: '06:00' }).start_day).toBe('mon');
    expect(nextPeriod(undefined)).toEqual({ start_day: 'mon', start_time: '00:00', end_day: 'sun', end_time: '23:59' });
  });

  it('фильтр зоны из адреса', () => {
    expect(parseTariffsSearch({ zone: '3' })).toEqual({ zone: 3 });
    expect(parseTariffsSearch({ zone: 'x' })).toEqual({ zone: undefined });
  });
});
