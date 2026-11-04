import { describe, expect, it } from 'vitest';
import { formatMoney, parseMoney, tiynToInput } from './money';

describe('formatMoney = number_format($x, d, dec, sep) для тиынов', () => {
  it.each([
    [210000, 2, '2 100,00'],
    [12345, 2, '123,45'],
    [5, 2, '0,05'],
    [-100000000, 2, '-1 000 000,00'],
    [0, 2, '0,00'],
    [123456789, 2, '1 234 567,89'],
    // 0 знаков: округление половины от нуля, как PHP round()
    [12350, 0, '124'],
    [12349, 0, '123'],
    [-12350, 0, '-124'],
    [-49, 0, '0'],
    [100000000, 0, '1 000 000'],
  ] as const)('%d, %d → %s', (tiyn, d, want) => {
    expect(formatMoney(tiyn, d)).toBe(want);
  });

  it('другие разделители', () => {
    expect(formatMoney(123456789, 2, '.', ' ')).toBe('1 234 567.89');
  });

  it('не целые тиыны — ошибка', () => {
    expect(() => formatMoney(1.5)).toThrow(RangeError);
  });
});

describe('parseMoney — строго ≤ 2 знаков (Q-002)', () => {
  it.each([
    ['1500', 150000],
    ['1500.5', 150050],
    ['1500,50', 150050],
    ['1 500,05', 150005],
    ['0.01', 1],
    ['007', 700],
    ['0', 0],
    ['1500.', 150000],
  ])('%s → %d', (s, want) => {
    expect(parseMoney(s)).toEqual({ ok: true, tiyn: want });
  });

  it.each([
    ['', 'required'],
    ['  ', 'required'],
    ['1.005', 'decimal'],
    ['10,123', 'decimal'],
    ['abc', 'numeric'],
    ['1e3', 'numeric'],
    ['-5', 'negative'],
    ['12345678901234', 'too_large'],
  ])('%s → %s', (s, err) => {
    expect(parseMoney(s)).toEqual({ ok: false, error: err });
  });

  it('отрицательные — по флагу', () => {
    expect(parseMoney('-5.5', { allowNegative: true })).toEqual({ ok: true, tiyn: -550 });
  });

  it('нет потерь float: 0.29, 1.13, 4.35', () => {
    expect(parseMoney('0.29')).toEqual({ ok: true, tiyn: 29 });
    expect(parseMoney('1.13')).toEqual({ ok: true, tiyn: 113 });
    expect(parseMoney('4.35')).toEqual({ ok: true, tiyn: 435 });
  });

  it('tiynToInput', () => {
    expect(tiynToInput(150050)).toBe('1500.50');
    expect(tiynToInput(150000)).toBe('1500');
    expect(tiynToInput(-5)).toBe('-0.05');
  });
});
