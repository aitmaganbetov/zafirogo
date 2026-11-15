import { afterEach, describe, expect, it } from 'vitest';
import { allowedOnResellerHost, publicPath, resellerHref, resellerRewrite, toInternal } from './host';

const setHost = (on: boolean) => {
  if (on) document.documentElement.setAttribute('data-reseller-host', '');
  else document.documentElement.removeAttribute('data-reseller-host');
};

describe('домен кабинета реселлера (UQ-21)', () => {
  afterEach(() => setHost(false));

  it('без домена кабинет — под /reseller, адреса не переписываются', () => {
    expect(resellerHref()).toBe('/reseller');
    expect(resellerHref('?clubs_page=2')).toBe('/reseller?clubs_page=2');
    expect(toInternal('/')).toBe('/');
    expect(resellerRewrite.input({ url: new URL('http://x/') })).toBeUndefined();
    expect(resellerRewrite.output({ url: new URL('http://x/reseller') })).toBeUndefined();
  });

  it('на хосте реселлера кабинет — с корня', () => {
    setHost(true);
    expect(resellerHref()).toBe('/');
    expect(resellerHref('?earnings_page=3')).toBe('/?earnings_page=3');
    expect(toInternal('/')).toBe('/reseller');
    expect(toInternal('/login')).toBe('/login');
    expect(publicPath('/reseller')).toBe('/');
    expect(publicPath('/login')).toBe('/login');
    expect(resellerRewrite.input({ url: new URL('http://r.test/?clubs_page=2') })?.toString()).toBe('http://r.test/reseller?clubs_page=2');
    expect(resellerRewrite.output({ url: new URL('http://r.test/reseller?clubs_page=2') })?.toString()).toBe('http://r.test/?clubs_page=2');
  });

  // RestrictResellerHost: reseller.*, auth.show-login, auth.password.* — остальное уходит в кабинет (GS-561).
  it('на хосте реселлера — только кабинет, вход и восстановление пароля', () => {
    for (const p of ['/reseller', '/login', '/forgot-password', '/reset-password']) expect(allowedOnResellerHost(p)).toBe(true);
    for (const p of ['/', '/register', '/dashboard', '/pricing', '/product/features', '/superadmin', '/settings/club', '/resellers', '/invitations/abc'])
      expect(allowedOnResellerHost(p)).toBe(false);
  });
});
