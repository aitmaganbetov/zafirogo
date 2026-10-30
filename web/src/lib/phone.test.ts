import { describe, expect, it } from 'vitest';
import { formatPhone, initialPhone, phoneDigits, reformatPhone } from './phone';

describe('маска телефона (resources/js/app.js)', () => {
  it('formatPhone', () => {
    expect(formatPhone('7011234567')).toBe('701 123-45-67');
    expect(formatPhone('701')).toBe('701');
    expect(formatPhone('7011')).toBe('701 1');
  });
  it('old(phone): последние 10 цифр', () => {
    expect(initialPhone('+77011234567')).toBe('701 123-45-67');
  });
  it('курсор после вставленной цифры', () => {
    expect(reformatPhone('7011', 4)).toEqual({ value: '701 1', cursor: 5 });
    expect(reformatPhone('701 123-4', 9)).toEqual({ value: '701 123-4', cursor: 9 });
  });
  it('перед отправкой — только цифры', () => {
    expect(phoneDigits('701 123-45-67')).toBe('7011234567');
  });
});
