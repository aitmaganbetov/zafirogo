import { act, render } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import i18n from '../i18n';
import { ClubClock } from '../layouts/ClubClock';
import { intlLocale } from './intlLocale';

/*
 * UI-8 (аудит 22.11.2026): в Chromium нет данных CLDR для kk — Intl отдаёт корневой формат («M10 15, Thu»).
 * Здесь Node с полным ICU, поэтому Chromium подменяется: для kk* DateTimeFormat пишет месяц как «M10».
 */

const Real = Intl.DateTimeFormat;

function chromiumWithoutKk() {
  vi.spyOn(Intl, 'DateTimeFormat').mockImplementation(function (locale?: string | string[], options?: Intl.DateTimeFormatOptions) {
    const l = Array.isArray(locale) ? locale[0] : locale;
    const real = new Real('en-US', options);
    if (!l?.startsWith('kk')) return new Real(locale, options);
    const root = (d?: Date | number) => real.formatToParts(d).map((p) => (p.type === 'month' ? { ...p, value: `M${new Real('en-US', { month: 'numeric', timeZone: options?.timeZone }).format(d)}` } : p));
    return {
      format: (d?: Date | number) => root(d).map((p) => p.value).join(''),
      formatToParts: root,
      resolvedOptions: () => ({ ...real.resolvedOptions(), locale: 'kk' }),
    } as unknown as Intl.DateTimeFormat;
  } as unknown as typeof Intl.DateTimeFormat);
}

afterEach(async () => {
  vi.restoreAllMocks();
  await i18n.changeLanguage('ru');
});

describe('intlLocale', () => {
  it('язык с данными CLDR — как есть; пусто или неверный код — ru', () => {
    expect(intlLocale('en')).toBe('en');
    expect(intlLocale('ru')).toBe('ru');
    expect(intlLocale('kk')).toBe('kk'); // Node: полный ICU
    expect(intlLocale('')).toBe('ru');
    expect(intlLocale(undefined)).toBe('ru');
    expect(intlLocale('not a locale!')).toBe('ru');
  });

  it('kk без данных (Chromium) — ru', () => {
    chromiumWithoutKk();
    expect(intlLocale('kk-KZ')).toBe('ru');
  });

  it('часы клуба на kk в Chromium — дата по-русски, не «M10 15, Thu»', async () => {
    chromiumWithoutKk();
    await act(async () => { await i18n.changeLanguage('kk-x-chromium'); });
    const { container } = render(<ClubClock clubTime={{ timezone: 'Asia/Almaty', nowMs: Date.UTC(2026, 9, 15, 7, 0, 0) }} />);
    const date = container.querySelector('[data-clock-date]')?.textContent ?? '';
    expect(date).not.toMatch(/M\d+/);
    expect(date).toBe('чт, 15 окт.');
  });
});
