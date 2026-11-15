import { isRedirect } from '@tanstack/react-router';
import { afterEach, describe, expect, it } from 'vitest';
import { publicSiteGuard } from './guard';

// RestrictSuperadminHost: на хосте кабинета суперадмина страницы сайта уводят в кабинет.
describe('publicSiteGuard', () => {
  afterEach(() => document.documentElement.removeAttribute('data-superadmin-host'));

  it('обычный хост — страница открывается', () => {
    expect(() => publicSiteGuard()).not.toThrow();
  });

  it('хост кабинета — переход в кабинет', () => {
    document.documentElement.setAttribute('data-superadmin-host', '');
    let thrown: unknown;
    try {
      publicSiteGuard();
    } catch (e) {
      thrown = e;
    }
    expect(isRedirect(thrown)).toBe(true);
    expect((thrown as { options: { href: string } }).options.href).toBe('/superadmin');
  });
});
