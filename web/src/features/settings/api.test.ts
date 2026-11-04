import { describe, expect, it } from 'vitest';
import { formToBody, hundredthsToInput } from './api';

describe('hundredthsToInput', () => {
  it('выводит числа как PHP (int|float)', () => {
    expect(hundredthsToInput(100000)).toBe('1000');
    expect(hundredthsToInput(100050)).toBe('1000.5');
    expect(hundredthsToInput(550)).toBe('5.5');
    expect(hundredthsToInput(1)).toBe('0.01');
    expect(hundredthsToInput(1234)).toBe('12.34');
  });
});

describe('formToBody', () => {
  it('последнее значение повторённого поля, уровни списком, пустые уровни — []', () => {
    const form = document.createElement('form');
    form.innerHTML = `
      <input name="section" value="finances">
      <input type="hidden" name="loyalty_enabled" value="0"><input type="checkbox" name="loyalty_enabled" value="1" checked>
      <input type="hidden" name="auto_billing" value="0"><input type="checkbox" name="auto_billing" value="1">
      <input type="hidden" name="loyalty_levels" value="">
      <input name="loyalty_levels[3][spent]" value="5000"><input name="loyalty_levels[3][percent]" value="10">
      <input name="loyalty_levels[0][spent]" value="1000"><input name="loyalty_levels[0][percent]" value="5">
      <input type="hidden" name="topup_bonus_levels" value="">`;
    expect(formToBody(form)).toEqual({
      section: 'finances',
      loyalty_enabled: '1',
      auto_billing: '0',
      loyalty_levels: [{ spent: '1000', percent: '5' }, { spent: '5000', percent: '10' }],
      topup_bonus_levels: [],
    });
  });
});
