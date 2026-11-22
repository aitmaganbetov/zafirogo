import { describe, expect, it } from 'vitest';
import { intParam, pageParam, strParam } from './searchParams';

describe('параметры адреса', () => {
  it('intParam: целое ≥ min из числа или строки', () => {
    expect(intParam('7')).toBe(7);
    expect(intParam(7)).toBe(7);
    expect(intParam(' 3 ')).toBe(3);
    expect(intParam('1', 2)).toBeUndefined();
    expect(intParam('2', 2)).toBe(2);
    for (const v of [undefined, null, '', '0', '-3', '1.5', 'abc', true, ['5']]) expect(intParam(v)).toBeUndefined();
  });

  it('strParam: непустая строка, число — строкой', () => {
    expect(strParam('ким')).toBe('ким');
    expect(strParam(5)).toBe('5');
    expect(strParam('')).toBeUndefined();
    expect(strParam(undefined)).toBeUndefined();
    expect(strParam(true)).toBeUndefined();
  });

  it('pageParam: 1…100000, иначе 1', () => {
    expect(pageParam('3')).toBe(3);
    expect(pageParam(100000)).toBe(100000);
    for (const v of [undefined, '0', '100001', '2.5', 'x']) expect(pageParam(v)).toBe(1);
  });
});
