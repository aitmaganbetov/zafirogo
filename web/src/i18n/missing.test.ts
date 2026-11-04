import { beforeAll, describe, expect, it } from 'vitest';
import i18n from './index';
import { chooseForm, ruPluralIndex } from './missing';

describe('chooseForm (MessageSelector::choose, ru)', () => {
  it('формы ru по числу', () => {
    const line = '{{count}} минута|{{count}} минуты|{{count}} минут';
    expect([1, 2, 4, 5, 11, 12, 14, 21, 22, 25, 101, 111, 0].map((n) => chooseForm(line, n))).toEqual([
      '{{count}} минута', '{{count}} минуты', '{{count}} минуты', '{{count}} минут', '{{count}} минут', '{{count}} минут',
      '{{count}} минут', '{{count}} минута', '{{count}} минуты', '{{count}} минут', '{{count}} минута', '{{count}} минут',
      '{{count}} минут',
    ]);
    expect(ruPluralIndex(1.5)).toBe(0);
  });

  it('явные условия и нехватка форм', () => {
    const line = '{0} Нет ПК|[1,4] Несколько ПК|[5,*] Много ПК';
    expect(chooseForm(line, 0)).toBe('Нет ПК');
    expect(chooseForm(line, 3)).toBe('Несколько ПК');
    expect(chooseForm(line, 50)).toBe('Много ПК');
    expect(chooseForm('один|много', 5)).toBe('один'); // формы с номером 2 нет — первая
    expect(chooseForm('без форм', 5)).toBe('без форм');
  });
});

describe('i18next: ключ без перевода', () => {
  beforeAll(async () => {
    await i18n.changeLanguage('kk');
  });

  it('множественное число — одна форма с подстановкой', () => {
    const key = '{{count}} ПК не на связи|{{count}} ПК не на связи (неск.)|{{count}} ПК не на связи (много)';
    expect(i18n.t(key, { count: 1 })).toBe('1 ПК не на связи');
    expect(i18n.t(key, { count: 3 })).toBe('3 ПК не на связи (неск.)');
    expect(i18n.t(key, { count: 7 })).toBe('7 ПК не на связи (много)');
  });

  it('обычный ключ — ключ с параметрами', () => {
    expect(i18n.t('Клуб {{club}} · ПК {{computer}}', { club: 'Arena', computer: 'PC-01' })).toBe('Клуб Arena · ПК PC-01');
    expect(i18n.t('Просто текст.')).toBe('Просто текст.');
  });

  it('переведённый ключ не трогается', () => {
    i18n.addResourceBundle('kk', 'translation', { 'Готово': 'Дайын' }, true, true);
    expect(i18n.t('Готово')).toBe('Дайын');
  });
});
