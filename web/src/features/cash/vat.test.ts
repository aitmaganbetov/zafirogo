import { describe, expect, it } from 'vitest';
// Векторы общие с сервером (server/internal/cash/vat_test.go): расчёт в браузере совпадает с серверной формулой.
import vectors from '../../../../server/internal/cash/testdata/vat.json';
import { vatIncludedTiyn } from './vat';

describe('vatIncludedTiyn (Q-019)', () => {
  it('совпадает с сервером на общих векторах', () => {
    expect(vectors.length).toBeGreaterThan(5);
    for (const v of vectors.filter((x) => Number.isSafeInteger(x.total_tiyn * 5))) {
      expect(vatIncludedTiyn(v.total_tiyn), String(v.total_tiyn)).toBe(v.vat_tiyn);
    }
  });
  it('как старый Math.round(total * 5 / 105) на целых тенге', () => {
    for (const tenge of [0, 1, 10, 21, 450, 1050, 2400, 9999, 123456]) {
      expect(vatIncludedTiyn(tenge * 100)).toBe(Math.round((tenge * 5) / 105) * 100);
    }
  });
});
