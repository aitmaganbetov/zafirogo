import { describe, expect, it } from 'vitest';
import { formatPercent, trimHundredths } from './percent';

describe('сотые доли без float', () => {
  it('процент реселлера из базисных пунктов — без хвостовых нулей', () => {
    expect(formatPercent(0)).toBe('0');
    expect(formatPercent(1000)).toBe('10');
    expect(formatPercent(1250)).toBe('12.5');
    expect(formatPercent(1235)).toBe('12.35');
    expect(formatPercent(1005)).toBe('10.05');
    expect(formatPercent(5)).toBe('0.05');
    expect(formatPercent(10000)).toBe('100');
  });

  it('сумма для поля выплаты — round($balance, 2)', () => {
    expect(trimHundredths(18000)).toBe('180');
    expect(trimHundredths(59404)).toBe('594.04');
    expect(trimHundredths(59450)).toBe('594.5');
    expect(() => trimHundredths(-1)).toThrow(RangeError);
    expect(() => trimHundredths(1.5)).toThrow(RangeError);
  });
});
