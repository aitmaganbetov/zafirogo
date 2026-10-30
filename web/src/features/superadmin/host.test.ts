import { afterEach, describe, expect, it } from 'vitest';
import { cabinetRewrite, publicPath, saHref, toInternal } from './host';
import { parseClubsSearch, parseDashboardSearch, parseTranslationsSearch, qs } from './api';

const setCabinet = (on: boolean) => {
  if (on) document.documentElement.setAttribute('data-superadmin-host', '');
  else document.documentElement.removeAttribute('data-superadmin-host');
};

describe('домен кабинета суперадмина (Q-069)', () => {
  afterEach(() => setCabinet(false));

  it('без домена кабинет — под /superadmin, адреса не переписываются', () => {
    expect(saHref('/')).toBe('/superadmin');
    expect(saHref('/clubs/5')).toBe('/superadmin/clubs/5');
    expect(saHref('/?period=7d')).toBe('/superadmin?period=7d');
    expect(toInternal('/clubs')).toBe('/clubs');
    expect(cabinetRewrite.input({ url: new URL('http://x/clubs') })).toBeUndefined();
  });

  it('на хосте кабинета — с корня; панель клуба не трогается', () => {
    setCabinet(true);
    expect(saHref('/clubs/5')).toBe('/clubs/5');
    expect(toInternal('/')).toBe('/superadmin');
    expect(toInternal('/languages/3/translations')).toBe('/superadmin/languages/3/translations');
    expect(toInternal('/dashboard')).toBe('/dashboard');
    expect(toInternal('/login')).toBe('/login');
    expect(publicPath('/superadmin')).toBe('/');
    expect(publicPath('/superadmin/plans')).toBe('/plans');
    const out = cabinetRewrite.output({ url: new URL('http://admin.test/superadmin/clubs?page=2') });
    expect(out?.toString()).toBe('http://admin.test/clubs?page=2');
    const input = cabinetRewrite.input({ url: new URL('http://admin.test/accounts?filter=blocked') });
    expect(input?.toString()).toBe('http://admin.test/superadmin/accounts?filter=blocked');
  });
});

describe('параметры адреса', () => {
  it('qs пропускает пустое, повторяет массивы', () => {
    expect(qs({ a: '', b: undefined, c: 0, d: ['1', '2'], e: true, f: false })).toBe('?c=0&d=1&d=2&e=1');
    expect(qs({})).toBe('');
  });

  it('разбор фильтров', () => {
    expect(parseDashboardSearch({ period: '7d', clubs: ['3', 'x', 5] })).toEqual({ period: '7d', clubs: ['3', '5'] });
    expect(parseDashboardSearch({ period: 'year', clubs: '4' })).toEqual({ period: undefined, clubs: ['4'] });
    expect(parseClubsSearch({ subscription: 'nope', direction: 'desc', page: '1' })).toEqual({
      search: undefined, subscription: undefined, sort: undefined, direction: 'desc', page: undefined,
    });
    expect(parseTranslationsSearch({ missing: '1', page: 3 })).toEqual({ search: undefined, missing: true, page: 3 });
  });
});
