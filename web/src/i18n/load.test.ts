import { afterEach, describe, expect, it, vi } from 'vitest';
import i18n from './index';
import { applyLanguage, clubLanguage, guestLanguage, loadLanguage } from './load';

describe('выбор языка (LocaleResolver)', () => {
  const enabled = ['ru', 'kk', 'en'];
  it('гость: язык браузера среди включённых, иначе en', () => {
    expect(guestLanguage(enabled, ['kk-KZ', 'ru'])).toBe('kk');
    expect(guestLanguage(enabled, ['de-DE'])).toBe('en');
    expect(guestLanguage(['ru', 'kk'], ['de'])).toBe('ru');
  });
  it('клуб: язык клуба, если включён, иначе ru', () => {
    expect(clubLanguage('kk', enabled)).toBe('kk');
    expect(clubLanguage(null, enabled)).toBe('ru');
    expect(clubLanguage('kk', ['ru', 'en'])).toBe('ru');
  });
});

describe('переводы с сервера в формате i18next', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    void i18n.changeLanguage('ru');
  });

  it('подстановки и множественное число', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () =>
        new Response(
          JSON.stringify({
            Касса: 'Касса',
            'Создан товар: {{product}}': 'Тауар құрылды: {{product}}',
            '{{count}} минута|{{count}} минуты|{{count}} минут_one': '{{count}} минута',
            '{{count}} минута|{{count}} минуты|{{count}} минут_few': '{{count}} минуты',
            '{{count}} минута|{{count}} минуты|{{count}} минут_many': '{{count}} минут',
            '{{count}} минута|{{count}} минуты|{{count}} минут_other': '{{count}} минут',
          }),
          { status: 200 },
        ),
      ),
    );
    await loadLanguage('ru', true);
    await applyLanguage('ru');
    const key = '{{count}} минута|{{count}} минуты|{{count}} минут';
    expect(i18n.t(key, { count: 1 })).toBe('1 минута');
    expect(i18n.t(key, { count: 3 })).toBe('3 минуты');
    expect(i18n.t(key, { count: 11 })).toBe('11 минут');
    expect(i18n.t(key, { count: 21 })).toBe('21 минута');
    expect(i18n.t('Создан товар: {{product}}', { product: 'Кола' })).toBe('Тауар құрылды: Кола');
    // нет перевода — ключ (русский текст), точки и двоеточия в ключе не ломают поиск
    expect(i18n.t('Нет перевода: тест. Точка.')).toBe('Нет перевода: тест. Точка.');
    expect(document.documentElement.lang).toBe('ru');
  });
});
