import { describe, expect, it } from 'vitest';
import { categoryIcon, posMoney } from './format';

describe('posMoney', () => {
  it('как Intl ru-RU над тенге', () => {
    expect(posMoney(0)).toBe('0 ₸');
    expect(posMoney(45000)).toBe('450 ₸');
    expect(posMoney(150050)).toBe(new Intl.NumberFormat('ru-RU').format(1500.5) + ' ₸');
    expect(posMoney(123456789)).toBe(new Intl.NumberFormat('ru-RU').format(1234567.89) + ' ₸');
    expect(posMoney(101)).toBe('1,01 ₸');
  });
});

describe('categoryIcon', () => {
  it('по словам названия', () => {
    expect(categoryIcon('Напитки')).toBe('fa-bottle-water');
    expect(categoryIcon('Снеки')).toBe('fa-cookie-bite');
    expect(categoryIcon('Еда')).toBe('fa-burger');
    expect(categoryIcon('Прочее')).toBe('fa-box');
  });
});
